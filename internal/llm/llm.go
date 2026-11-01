// Package llm sends prompts to whichever AI provider the user signed in to.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"t2c/internal/config"
)

// Client sends one prompt and returns the reply text.
type Client interface {
	Complete(ctx context.Context, system, user string) (string, error)
	// Models lists the models this login can use.
	Models(ctx context.Context) ([]string, error)
}

// DefaultBaseURL returns the API address for providers that have a fixed one.
func DefaultBaseURL(provider string) string {
	switch provider {
	case config.OpenAI:
		return "https://api.openai.com/v1"
	case config.OpenRouter:
		return "https://openrouter.ai/api/v1"
	case config.Gemini:
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	case config.Ollama:
		return "http://127.0.0.1:11434/v1"
	case config.ChatGPT:
		return "https://api.openai.com/v1"
	}
	return ""
}

// DefaultModel returns the model to use when the user hasn't chosen one, or ""
// when it should come from the provider's list.
func DefaultModel(provider string) string {
	switch provider {
	case config.Anthropic:
		return anthropicDefaultModel
	case config.OpenRouter:
		return "openrouter/auto"
	}
	return ""
}

// New builds a client for the given provider and credentials.
func New(c config.Config, creds config.Credentials) (Client, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL(c.Provider)
	}
	switch c.Provider {
	case config.ChatGPT:
		return &chatGPT{baseURL: base, model: c.Model}, nil
	case config.Anthropic:
		return newAnthropic(base, creds.APIKey, c.Model), nil
	case config.OpenAI, config.OpenRouter, config.Gemini, config.Ollama, config.Custom:
		if base == "" {
			return nil, errors.New("no API address is set. Run: t2c login custom --base-url <url>")
		}
		return &compat{provider: c.Provider, baseURL: strings.TrimRight(base, "/"), key: creds.APIKey, model: c.Model}, nil
	}
	return nil, errors.New("not signed in. Run: t2c login")
}

// statusError turns a failed HTTP response into a message a person can act on.
// It never includes request headers, so keys and tokens can't end up in it.
func statusError(provider string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	code, detail := parseError(body)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%s rejected your login. Run: t2c login %s", provider, provider)
	case http.StatusForbidden:
		if detail != "" {
			return fmt.Errorf("%s denied access: %s", provider, detail)
		}
		return fmt.Errorf("%s denied access to this model", provider)
	case http.StatusNotFound:
		return fmt.Errorf("%s couldn't find that model or address. Pick another with: t2c model", provider)
	case http.StatusTooManyRequests:
		if detail != "" {
			return fmt.Errorf("%s limit reached: %s", provider, detail)
		}
		return fmt.Errorf("%s limit reached. Try again shortly", provider)
	}
	if detail == "" {
		detail = code
	}
	if detail == "" {
		detail = resp.Status
	}
	return fmt.Errorf("%s returned an error: %s", provider, detail)
}

func parseError(body []byte) (code, message string) {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error == nil {
		return "", ""
	}
	var detail struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Error, &detail) == nil {
		if s, ok := detail.Code.(string); ok {
			code = s
		}
		return code, detail.Message
	}
	var plain string
	json.Unmarshal(e.Error, &plain)
	return "", plain
}
