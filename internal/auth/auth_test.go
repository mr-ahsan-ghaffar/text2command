package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"t2c/internal/config"
)

// fakeOpenAI plays OpenAI's sign-in server and acts as the user's browser.
type fakeOpenAI struct {
	t        *testing.T
	key      *rsa.PrivateKey
	srv      *httptest.Server
	scope    string
	aud      string
	refresh  atomic.Int32
	verifier atomic.Value
	nonce    atomic.Value
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOpenAI{t: t, key: key, scope: chatGPTScopes, aud: "oaiapp_test"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/api/accounts/oauth/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	old := ChatGPTIssuer
	ChatGPTIssuer = f.srv.URL
	t.Cleanup(func() { ChatGPTIssuer = old })
	return f
}

func (f *fakeOpenAI) idToken(claims map[string]any) string {
	seg := func(v any) string {
		data, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	head := seg(map[string]string{"alg": "RS256", "kid": "k1"})
	body := seg(claims)
	digest := sha256.Sum256([]byte(head + "." + body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeOpenAI) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.verifier.Load() {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": f.idToken(map[string]any{
				"iss": f.srv.URL, "aud": f.aud, "sub": "user-1", "email": "a@example.com",
				"nonce": f.nonce.Load(), "exp": time.Now().Add(time.Hour).Unix(),
			}),
			"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600, "scope": f.scope,
		})
	case "refresh_token":
		f.refresh.Add(1)
		if r.Form.Get("refresh_token") != "refresh-1" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-2", "refresh_token": "refresh-2", "expires_in": 3600, "scope": chatGPTScopes,
		})
	}
}

// browser finds the login link in what t2c printed, then follows the redirect
// back to t2c's local server the way a browser would after the user approves.
func (f *fakeOpenAI) browser(out *os.File, extra url.Values) {
	go func() {
		link := waitForLink(f.t, out.Name())
		u, err := url.Parse(link)
		if err != nil {
			f.t.Error(err)
			return
		}
		q := u.Query()
		f.verifier.Store(q.Get("code_challenge"))
		f.nonce.Store(q.Get("nonce"))
		cb := url.Values{"code": {"abc"}, "state": {q.Get("state")}, "client_id": {"oaiapp_test"}}
		for k, v := range extra {
			cb[k] = v
		}
		resp, err := http.Get(q.Get("redirect_uri") + "?" + cb.Encode())
		if err != nil {
			f.t.Error(err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
}

var linkPattern = regexp.MustCompile(`https?://\S+authorize\S+`)

func waitForLink(t *testing.T, path string) string {
	for i := 0; i < 200; i++ {
		data, _ := os.ReadFile(path)
		if m := linkPattern.Find(data); m != nil {
			return string(m)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("no login link was printed")
	return ""
}

func setup(t *testing.T) *os.File {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SSH_CONNECTION", "1") // Don't open a real browser during tests.
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	return out
}

func TestLoginChatGPT(t *testing.T) {
	out := setup(t)
	f := newFakeOpenAI(t)
	f.browser(out, nil)
	s, err := LoginChatGPT(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if s.ClientID != "oaiapp_test" || s.Email != "a@example.com" || s.AccessToken != "access-1" {
		t.Errorf("unexpected session: %+v", s)
	}
	data, _ := os.ReadFile(out.Name())
	link := linkPattern.Find(data)
	u, _ := url.Parse(string(link))
	q := u.Query()
	if q.Get("client_id") != dynamicClientID || q.Get("agent_name_hint") != "t2c" {
		t.Errorf("first login should register t2c: %s", link)
	}
	if !strings.HasPrefix(q.Get("ext_agent_host_id"), "urn:uuid:") || q.Get("code_challenge_method") != "S256" {
		t.Errorf("missing required parameters: %s", link)
	}

	// A later login reuses the issued ID and doesn't register again.
	out2, _ := os.CreateTemp(t.TempDir(), "out")
	defer out2.Close()
	f.browser(out2, url.Values{"client_id": {""}})
	if _, err := LoginChatGPT(context.Background(), out2); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(out2.Name())
	u, _ = url.Parse(string(linkPattern.Find(data)))
	if u.Query().Get("client_id") != "oaiapp_test" || u.Query().Has("agent_name_hint") {
		t.Errorf("second login should reuse the client ID: %s", u)
	}
	if u.Query().Get("ext_agent_host_id") != q.Get("ext_agent_host_id") {
		t.Error("the host ID should stay the same on one machine")
	}
}

func TestLoginChatGPTRejections(t *testing.T) {
	t.Run("plan permission declined", func(t *testing.T) {
		out := setup(t)
		f := newFakeOpenAI(t)
		f.scope = "openid profile email"
		f.browser(out, nil)
		if _, err := LoginChatGPT(context.Background(), out); err == nil || !strings.Contains(err.Error(), "Use your ChatGPT plan") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("ID token for another app", func(t *testing.T) {
		out := setup(t)
		f := newFakeOpenAI(t)
		f.aud = "someone-else"
		f.browser(out, nil)
		if _, err := LoginChatGPT(context.Background(), out); err == nil || !strings.Contains(err.Error(), "wrong audience") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("user cancels", func(t *testing.T) {
		out := setup(t)
		f := newFakeOpenAI(t)
		f.browser(out, url.Values{"error": {"access_denied"}})
		if _, err := LoginChatGPT(context.Background(), out); err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("got %v", err)
		}
	})
}

func TestNetworkFailureMessage(t *testing.T) {
	setup(t)
	f := newFakeOpenAI(t)
	f.srv.Close()
	_, err := tokenRequest(context.Background(), url.Values{"grant_type": {"refresh_token"}})
	if err == nil || !strings.Contains(err.Error(), "couldn't reach OpenAI") {
		t.Errorf("got %v", err)
	}
	if strings.Contains(err.Error(), "dial tcp") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("raw network details reached the user: %v", err)
	}
}

func TestVerifyIDToken(t *testing.T) {
	setup(t)
	f := newFakeOpenAI(t)
	good := func() map[string]any {
		return map[string]any{"iss": f.srv.URL, "aud": "app", "sub": "u", "nonce": "n", "exp": time.Now().Add(time.Hour).Unix()}
	}
	ctx := context.Background()
	if _, err := verifyIDToken(ctx, f.idToken(good()), "app", "n"); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	arr := good()
	arr["aud"] = []string{"other", "app"}
	if _, err := verifyIDToken(ctx, f.idToken(arr), "app", "n"); err != nil {
		t.Errorf("audience list rejected: %v", err)
	}
	for name, change := range map[string]func(map[string]any){
		"issuer":   func(c map[string]any) { c["iss"] = "https://evil" },
		"audience": func(c map[string]any) { c["aud"] = "other" },
		"nonce":    func(c map[string]any) { c["nonce"] = "other" },
		"expired":  func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"subject":  func(c map[string]any) { delete(c, "sub") },
	} {
		c := good()
		change(c)
		if _, err := verifyIDToken(ctx, f.idToken(c), "app", "n"); err == nil {
			t.Errorf("token with bad %s was accepted", name)
		}
	}
	// A body swapped after signing must not pass.
	tok := f.idToken(good())
	other := strings.Split(f.idToken(func() map[string]any { c := good(); c["sub"] = "attacker"; return c }()), ".")[1]
	parts := strings.Split(tok, ".")
	parts[1] = other
	if _, err := verifyIDToken(ctx, strings.Join(parts, "."), "app", "n"); err == nil {
		t.Error("token with a changed body was accepted")
	}
}

func TestAccessTokenRefresh(t *testing.T) {
	setup(t)
	f := newFakeOpenAI(t)
	save := func(s *config.ChatGPTSession) {
		if err := config.SaveStore(config.Store{config.ChatGPT: {ChatGPT: s}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	save(&config.ChatGPTSession{ClientID: "c", AccessToken: "fresh", RefreshToken: "refresh-1", ExpiresAt: time.Now().Add(time.Hour)})
	if tok, err := AccessToken(ctx); err != nil || tok != "fresh" {
		t.Fatalf("got %q, %v", tok, err)
	}
	if f.refresh.Load() != 0 {
		t.Error("a fresh token shouldn't be refreshed")
	}

	save(&config.ChatGPTSession{ClientID: "c", AccessToken: "old", RefreshToken: "refresh-1", ExpiresAt: time.Now().Add(-time.Minute)})
	if tok, err := AccessToken(ctx); err != nil || tok != "access-2" {
		t.Fatalf("got %q, %v", tok, err)
	}
	store, _ := config.LoadStore()
	if got := store[config.ChatGPT].ChatGPT.RefreshToken; got != "refresh-2" {
		t.Errorf("the rotated refresh token wasn't saved: %q", got)
	}

	save(&config.ChatGPTSession{ClientID: "c", AccessToken: "old", RefreshToken: "stale", ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := AccessToken(ctx); err == nil || !strings.Contains(err.Error(), "t2c login chatgpt") {
		t.Errorf("expected advice to sign in again, got %v", err)
	}
}

func TestLoginOpenRouter(t *testing.T) {
	out := setup(t)
	var verifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/keys", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		sum := sha256.Sum256([]byte(body["code_verifier"]))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != verifier || body["code"] != "abc" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"key": "sk-or-test"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := OpenRouterBase
	OpenRouterBase = srv.URL
	defer func() { OpenRouterBase = old }()

	go func() {
		pattern := regexp.MustCompile(`https?://\S+/auth\?\S+`)
		for i := 0; i < 200; i++ {
			data, _ := os.ReadFile(out.Name())
			if m := pattern.Find(data); m != nil {
				u, _ := url.Parse(string(m))
				verifier = u.Query().Get("code_challenge")
				cb := u.Query().Get("callback_url")
				// A request to the wrong path must not complete the login.
				parsed, _ := url.Parse(cb)
				parsed.Path = "/callback/wrong"
				if resp, err := http.Get(parsed.String() + "?code=evil"); err == nil {
					resp.Body.Close()
				}
				if resp, err := http.Get(cb + "?code=abc"); err == nil {
					resp.Body.Close()
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	key, err := LoginOpenRouter(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-or-test" {
		t.Errorf("got key %q", key)
	}
}
