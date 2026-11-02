package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"t2c/internal/config"
)

// ChatGPTIssuer is OpenAI's sign-in server. Tests point it at a local server.
var ChatGPTIssuer = "https://auth.openai.com"

const (
	chatGPTPort     = 1455
	chatGPTResource = "https://api.openai.com/v1"
	chatGPTScopes   = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	planScope       = "chatgpt.tokens.use.direct"
	dynamicClientID = "dynamic_agent_client"
	clientIDFile    = "chatgpt-client-id"
)

// LoginChatGPT runs Sign in with ChatGPT in the browser.
func LoginChatGPT(ctx context.Context, out io.Writer) (*config.ChatGPTSession, error) {
	hostID, err := config.HostID()
	if err != nil {
		return nil, err
	}
	savedClient, err := config.ReadFile(clientIDFile)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", chatGPTPort))
	if err != nil {
		// OpenAI allows the port to change. The rest of the address can't.
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	redirect := fmt.Sprintf("http://127.0.0.1:%d/auth/callback", port)

	verifier, challenge, err := newPKCE()
	if err != nil {
		return nil, err
	}
	state, err := randomString()
	if err != nil {
		return nil, err
	}
	nonce, err := randomString()
	if err != nil {
		return nil, err
	}

	q := url.Values{
		"client_id":             {dynamicClientID},
		"ext_agent_host_id":     {hostID},
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"scope":                 {chatGPTScopes},
		"resource":              {chatGPTResource},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if savedClient != "" {
		q.Set("client_id", savedClient)
	} else {
		q.Set("agent_name_hint", "t2c")
	}

	authURL := ChatGPTIssuer + "/api/accounts/authorize?" + q.Encode()
	Show(out, authURL, port)
	params, err := waitForCallback(ctx, ln, "/auth/callback", state)
	if err != nil {
		return nil, err
	}

	clientID := params.Get("client_id")
	switch {
	case savedClient == "" && clientID == "":
		return nil, errors.New("OpenAI didn't finish registering t2c, so please try again")
	case savedClient != "" && clientID != "" && clientID != savedClient:
		return nil, errors.New("OpenAI returned a different app ID than the saved one")
	case savedClient != "":
		clientID = savedClient
	}

	tok, err := tokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {params.Get("code")},
		"code_verifier": {verifier},
		"redirect_uri":  {redirect},
		"resource":      {chatGPTResource},
	})
	if err != nil {
		return nil, err
	}
	claims, err := verifyIDToken(ctx, tok.IDToken, clientID, nonce)
	if err != nil {
		return nil, fmt.Errorf("couldn't verify the sign-in: %w", err)
	}
	scopes := strings.Fields(tok.Scope)
	if !slices.Contains(scopes, planScope) {
		return nil, errors.New(`the "Use your ChatGPT plan" permission wasn't granted. Run t2c login chatgpt again and allow it`)
	}
	if err := config.WriteFile(clientIDFile, []byte(clientID)); err != nil {
		return nil, err
	}
	return &config.ChatGPTSession{
		Email:        claims.Email,
		Subject:      claims.Subject,
		ClientID:     clientID,
		IDToken:      tok.IDToken,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
		Scopes:       scopes,
	}, nil
}

// AccessToken returns a current token for ChatGPT, refreshing it if needed.
func AccessToken(ctx context.Context) (string, error) {
	s, err := currentSession()
	if err != nil {
		return "", err
	}
	if time.Until(s.ExpiresAt) > time.Minute {
		return s.AccessToken, nil
	}

	// Refresh tokens rotate, so only one process may use one at a time.
	unlock, err := config.LockCredentials()
	if err != nil {
		return "", err
	}
	defer unlock()
	if s, err = currentSession(); err != nil {
		return "", err
	}
	if time.Until(s.ExpiresAt) > time.Minute {
		return s.AccessToken, nil
	}

	tok, err := tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {s.ClientID},
		"refresh_token": {s.RefreshToken},
		"resource":      {chatGPTResource},
	})
	var te *tokenError
	if errors.As(err, &te) && te.Code == "invalid_grant" {
		return "", errors.New("your ChatGPT login has expired or moved to another machine. Run: t2c login chatgpt")
	}
	if err != nil {
		return "", err
	}
	scopes := strings.Fields(tok.Scope)
	if tok.Scope != "" && !slices.Contains(scopes, planScope) {
		return "", errors.New("ChatGPT plan use is no longer allowed for t2c. Run: t2c login chatgpt")
	}
	s.AccessToken = tok.AccessToken
	s.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.RefreshToken != "" {
		s.RefreshToken = tok.RefreshToken
	}
	if tok.IDToken != "" {
		s.IDToken = tok.IDToken
	}
	if len(scopes) > 0 {
		s.Scopes = scopes
	}
	store, err := config.LoadStore()
	if err != nil {
		return "", err
	}
	store[config.ChatGPT] = config.Credentials{ChatGPT: s}
	if err := config.SaveStore(store); err != nil {
		return "", err
	}
	return s.AccessToken, nil
}

// RevokeChatGPT ends the session at OpenAI. Failures are ignored by callers
// since the local copy gets deleted regardless.
func RevokeChatGPT(ctx context.Context, s *config.ChatGPTSession) error {
	resp, err := postForm(ctx, ChatGPTIssuer+"/api/accounts/oauth/revoke", url.Values{
		"client_id": {s.ClientID},
		"token":     {s.RefreshToken},
	})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func currentSession() (*config.ChatGPTSession, error) {
	store, err := config.LoadStore()
	if err != nil {
		return nil, err
	}
	s := store[config.ChatGPT].ChatGPT
	if s == nil {
		return nil, errors.New("not signed in to ChatGPT. Run: t2c login chatgpt")
	}
	return s, nil
}

type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type tokenError struct {
	Code   string
	Status int
}

func (e *tokenError) Error() string {
	if e.Code != "" {
		return "OpenAI rejected the sign-in: " + e.Code
	}
	return fmt.Sprintf("OpenAI rejected the sign-in (HTTP %d)", e.Status)
}

func tokenRequest(ctx context.Context, form url.Values) (*tokenResponse, error) {
	resp, err := postForm(ctx, ChatGPTIssuer+"/api/accounts/oauth/token", form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &e)
		return nil, &tokenError{Code: e.Error, Status: resp.StatusCode}
	}
	var t tokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, err
	}
	if t.AccessToken == "" {
		return nil, errors.New("OpenAI's sign-in response had no token")
	}
	return &t, nil
}

func postForm(ctx context.Context, target string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, reachError(ctx, "OpenAI")
	}
	return resp, nil
}

type idClaims struct {
	Issuer  string          `json:"iss"`
	Subject string          `json:"sub"`
	Email   string          `json:"email"`
	Nonce   string          `json:"nonce"`
	Expires int64           `json:"exp"`
	RawAud  json.RawMessage `json:"aud"`
}

func (c idClaims) hasAudience(want string) bool {
	var one string
	if json.Unmarshal(c.RawAud, &one) == nil {
		return one == want
	}
	var many []string
	return json.Unmarshal(c.RawAud, &many) == nil && slices.Contains(many, want)
}

// verifyIDToken checks the ID token's signature against OpenAI's published
// keys, then its issuer, audience, expiry, and nonce.
func verifyIDToken(ctx context.Context, token, clientID, nonce string) (*idClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed ID token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return nil, err
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported signature type %q", header.Alg)
	}
	key, err := fetchKey(ctx, header.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, errors.New("bad signature")
	}

	var c idClaims
	if err := decodeSegment(parts[1], &c); err != nil {
		return nil, err
	}
	switch {
	case c.Issuer != ChatGPTIssuer:
		return nil, errors.New("wrong issuer")
	case !c.hasAudience(clientID):
		return nil, errors.New("wrong audience")
	case time.Now().Unix() >= c.Expires:
		return nil, errors.New("token expired")
	case c.Nonce != nonce:
		return nil, errors.New("wrong nonce")
	case c.Subject == "":
		return nil, errors.New("token has no account ID")
	}
	return &c, nil
}

func decodeSegment(seg string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func fetchKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ChatGPTIssuer+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, reachError(ctx, "OpenAI")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("couldn't fetch signing keys (HTTP %d)", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, err
	}
	for _, k := range set.Keys {
		if k.Kid != kid || k.Kty != "RSA" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, err
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, err
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	}
	return nil, errors.New("signing key not found")
}
