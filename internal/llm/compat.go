package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
)

// compat talks to OpenAI-style APIs: OpenAI, OpenRouter, Gemini, Ollama, and
// any other service with the same request format.
type compat struct {
	provider string
	baseURL  string
	key      string
	model    string
}

func (c *compat) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("couldn't reach %s. Check your connection", c.provider)
	}
	return resp, nil
}

func (c *compat) Complete(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	if err != nil {
		return "", err
	}
	resp, err := c.request(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", statusError(c.provider, resp)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<22)).Decode(&out); err != nil {
		return "", fmt.Errorf("couldn't read the reply from %s", c.provider)
	}
	if len(out.Choices) == 0 {
		return "", errors.New(c.provider + " returned no reply")
	}
	m := out.Choices[0].Message
	if m.Refusal != "" {
		return "", errors.New("the AI declined: " + m.Refusal)
	}
	return m.Content, nil
}

func (c *compat) Models(ctx context.Context) ([]string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(c.provider, resp)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<24)).Decode(&out); err != nil {
		return nil, fmt.Errorf("couldn't read the model list from %s", c.provider)
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}
