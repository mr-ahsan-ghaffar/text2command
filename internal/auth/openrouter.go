package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// OpenRouterBase is OpenRouter's website. Tests point it at a local server.
var OpenRouterBase = "https://openrouter.ai"

const openRouterPort = 1456

// LoginOpenRouter signs in through the browser and returns a new API key.
func LoginOpenRouter(ctx context.Context, out io.Writer) (string, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", openRouterPort))
	if err != nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return "", err
		}
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	verifier, challenge, err := newPKCE()
	if err != nil {
		return "", err
	}
	// OpenRouter has no state parameter, so an unguessable path does that job.
	secret, err := randomString()
	if err != nil {
		return "", err
	}
	path := "/callback/" + secret
	q := url.Values{
		"callback_url":          {fmt.Sprintf("http://localhost:%d%s", port, path)},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	Show(out, OpenRouterBase+"/auth?"+q.Encode(), port)
	params, err := waitForCallback(ctx, ln, path, "")
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(map[string]string{
		"code":                  params.Get("code"),
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OpenRouterBase+"/api/v1/auth/keys", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", reachError(ctx, "OpenRouter")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenRouter rejected the sign-in (HTTP %d)", resp.StatusCode)
	}
	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	if result.Key == "" {
		return "", errors.New("OpenRouter's response had no key")
	}
	return result.Key, nil
}
