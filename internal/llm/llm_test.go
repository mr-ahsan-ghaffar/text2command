package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"t2c/internal/config"
)

func TestCompatComplete(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ls -la"}}]}`)
	}))
	defer srv.Close()

	c, err := New(config.Config{Provider: config.Custom, Model: "m", BaseURL: srv.URL + "/"}, config.Credentials{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := c.Complete(context.Background(), "sys", "hi")
	if err != nil || reply != "ls -la" {
		t.Fatalf("got %q, %v", reply, err)
	}
	if auth != "Bearer k" || got["model"] != "m" {
		t.Errorf("bad request: auth=%q body=%v", auth, got)
	}
}

func TestErrorsNeverIncludeTheKey(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"nope"}}`)
		}))
		c, _ := New(config.Config{Provider: config.OpenAI, Model: "m", BaseURL: srv.URL}, config.Credentials{APIKey: "secret-key"})
		_, err := c.Complete(context.Background(), "s", "u")
		srv.Close()
		if err == nil {
			t.Errorf("HTTP %d: expected an error", status)
		} else if strings.Contains(err.Error(), "secret-key") {
			t.Errorf("HTTP %d: the error contains the key: %v", status, err)
		}
	}
}

func saveChatGPTLogin(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := config.SaveStore(config.Store{config.ChatGPT: {ChatGPT: &config.ChatGPTSession{
		ClientID: "c", AccessToken: "tok", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour),
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTComplete(t *testing.T) {
	saveChatGPTLogin(t)
	var body map[string]any
	var auth, path string
	stream := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, stream)
	}))
	defer srv.Close()
	c, _ := New(config.Config{Provider: config.ChatGPT, Model: "m", BaseURL: srv.URL}, config.Credentials{})
	ctx := context.Background()

	stream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ls \"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"-la\"}\n\n" +
		"data: {\"type\":\"response.completed\"}\n\n"
	reply, err := c.Complete(ctx, "sys", "hi")
	if err != nil || reply != "ls -la" {
		t.Fatalf("got %q, %v", reply, err)
	}
	if path != "/responses" || auth != "Bearer tok" {
		t.Errorf("bad request: path=%s auth=%s", path, auth)
	}
	if body["store"] != false || body["stream"] != true || body["instructions"] != "sys" {
		t.Errorf("OpenAI requires these fields: %v", body)
	}
	// OpenAI rejects these on this route.
	for _, field := range []string{"temperature", "top_p", "max_output_tokens", "metadata", "user", "truncation"} {
		if _, ok := body[field]; ok {
			t.Errorf("request must not include %s", field)
		}
	}

	stream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ls\"}\n\n"
	if _, err := c.Complete(ctx, "s", "u"); err == nil {
		t.Error("a stream that ends without response.completed must fail")
	}

	stream = `data: {"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}}` + "\n\n"
	if _, err := c.Complete(ctx, "s", "u"); err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Errorf("got %v", err)
	}

	stream = `data: {"type":"response.incomplete"}` + "\n\n"
	if _, err := c.Complete(ctx, "s", "u"); err == nil {
		t.Error("an incomplete reply must fail")
	}
}

func TestChatGPTModels(t *testing.T) {
	saveChatGPTLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"slug":"b","visibility":"list"},{"slug":"hidden","visibility":"hide"},{"slug":"a","visibility":"list"}]}`)
	}))
	defer srv.Close()
	c, _ := New(config.Config{Provider: config.ChatGPT, BaseURL: srv.URL}, config.Credentials{})
	got, err := c.Models(context.Background())
	if err != nil || strings.Join(got, ",") != "b,a" {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestChatGPTHTTPPlanError(t *testing.T) {
	saveChatGPTLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":"subscription_sharing_user_not_eligible","message":"no"}}`)
	}))
	defer srv.Close()
	c, _ := New(config.Config{Provider: config.ChatGPT, Model: "m", BaseURL: srv.URL}, config.Credentials{})
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "can't use its plan") {
		t.Errorf("got %v", err)
	}
}

func anthropicServer(t *testing.T, respond func(body map[string]any, h http.Header) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, respond(body, r.Header))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func message(stop, text string) string {
	return fmt.Sprintf(`{"id":"m","type":"message","role":"assistant","model":"x","stop_reason":%q,"content":[{"type":"thinking","thinking":"","signature":"s"},{"type":"text","text":%q}],"usage":{"input_tokens":1,"output_tokens":1}}`, stop, text)
}

func TestAnthropicRequest(t *testing.T) {
	// Env settings meant for other tools must be ignored.
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	var body map[string]any
	var header http.Header
	srv := anthropicServer(t, func(b map[string]any, h http.Header) string {
		body, header = b, h
		return message("end_turn", "ls -la")
	})
	c, _ := New(config.Config{Provider: config.Anthropic, Model: "claude-opus-5-5", BaseURL: srv.URL}, config.Credentials{APIKey: "key"})
	reply, err := c.Complete(context.Background(), "sys", "hi")
	if err != nil || reply != "ls -la" {
		t.Fatalf("got %q, %v", reply, err)
	}
	if header.Get("X-Api-Key") != "key" || header.Get("Authorization") != "" {
		t.Errorf("wrong credentials sent: %v", header)
	}
	if body["fallbacks"] != "default" || !strings.Contains(header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("fallbacks not enabled: %v %v", body["fallbacks"], header.Get("Anthropic-Beta"))
	}
	if cfg, _ := body["output_config"].(map[string]any); cfg["effort"] != "low" {
		t.Errorf("effort not set: %v", body["output_config"])
	}
	if _, ok := body["thinking"]; ok {
		t.Error("thinking should be left to the model's default")
	}
}

func TestAnthropicHaikuSkipsFallbacks(t *testing.T) {
	var body map[string]any
	var header http.Header
	srv := anthropicServer(t, func(b map[string]any, h http.Header) string {
		body, header = b, h
		return message("end_turn", "ls")
	})
	c, _ := New(config.Config{Provider: config.Anthropic, Model: "claude-haiku-5-5", BaseURL: srv.URL}, config.Credentials{APIKey: "k"})
	if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["fallbacks"]; ok || strings.Contains(header.Get("Anthropic-Beta"), "fallback") {
		t.Errorf("Haiku has no server-side fallback: %v", body)
	}
}

func TestAnthropicRefusal(t *testing.T) {
	srv := anthropicServer(t, func(map[string]any, http.Header) string {
		return `{"id":"m","type":"message","role":"assistant","model":"x","stop_reason":"refusal","content":[],"usage":{"input_tokens":1,"output_tokens":0}}`
	})
	c, _ := New(config.Config{Provider: config.Anthropic, Model: "claude-opus-5-5", BaseURL: srv.URL}, config.Credentials{APIKey: "k"})
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Errorf("got %v", err)
	}
}
