package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"t2c/internal/auth"
)

// chatGPT uses the signed-in user's ChatGPT plan through the Responses API.
// On this route OpenAI requires store=false and stream=true, and rejects many
// optional fields, so the request stays minimal.
type chatGPT struct {
	baseURL string
	model   string
}

func (c *chatGPT) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	token, err := auth.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("couldn't reach ChatGPT. Check your connection")
	}
	return resp, nil
}

func (c *chatGPT) Complete(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":        c.model,
		"instructions": system,
		"input":        []map[string]string{{"role": "user", "content": user}},
		"store":        false,
		"stream":       true,
	})
	if err != nil {
		return "", err
	}
	resp, err := c.request(ctx, http.MethodPost, "/responses", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", chatGPTError(resp)
	}

	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
		case "response.completed":
			return text.String(), nil
		case "response.incomplete":
			return "", errors.New("ChatGPT stopped before finishing the reply")
		case "response.failed":
			if ev.Response.Error != nil {
				return "", planError(ev.Response.Error.Code, ev.Response.Error.Message)
			}
			return "", errors.New("ChatGPT couldn't finish the reply")
		}
	}
	// A reply only counts once OpenAI confirms it finished.
	return "", errors.New("the connection to ChatGPT ended early. Try again")
}

func chatGPTError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if code, message := parseError(body); strings.HasPrefix(code, "subscription_sharing_") {
		return planError(code, message)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return statusError("chatgpt", resp)
}

func planError(code, message string) error {
	switch code {
	case "subscription_sharing_usage_limit_exceeded":
		return errors.New("you've reached your ChatGPT plan's usage limit. Wait for it to reset or use an API key")
	case "subscription_sharing_usage_unavailable":
		return errors.New("ChatGPT plan usage isn't available right now. Try again shortly")
	case "subscription_sharing_user_not_eligible":
		return errors.New("this ChatGPT account or workspace can't use its plan in other apps")
	}
	if message != "" {
		return fmt.Errorf("ChatGPT returned an error: %s", message)
	}
	return fmt.Errorf("ChatGPT returned an error: %s", code)
}

func (c *chatGPT) Models(ctx context.Context) ([]string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, chatGPTError(resp)
	}
	var out struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, errors.New("couldn't read the model list from ChatGPT")
	}
	var ids []string
	for _, m := range out.Models {
		if m.Visibility == "list" {
			ids = append(ids, m.Slug) // Keep OpenAI's order, as its docs ask.
		}
	}
	return ids, nil
}
