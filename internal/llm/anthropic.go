package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const anthropicDefaultModel = "claude-opus-5-5"

// AnthropicModels are offered at login, most capable first.
var AnthropicModels = []string{anthropicDefaultModel, "claude-sonnet-5-5", "claude-haiku-5-5"}

type anthropicClient struct {
	client anthropic.Client
	model  string
}

func newAnthropic(baseURL, key, model string) *anthropicClient {
	// Only the key saved by t2c login is used, not ANTHROPIC_* variables that
	// may be meant for other tools.
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(key),
		option.WithMaxRetries(1),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &anthropicClient{client: anthropic.NewClient(opts...), model: model}
}

func hasPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func (c *anthropicClient) Complete(ctx context.Context, system, user string) (string, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     c.model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user)),
		},
	}
	// Writing a command is quick work, and low effort keeps the wait short.
	if hasPrefix(c.model, "claude-opus-5", "claude-fable-5", "claude-sonnet-5", "claude-haiku-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-4-6") {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow}
	}
	// Ordinary admin commands can trip a safety classifier, so a declined
	// request is retried on the fallback model Anthropic recommends.
	if hasPrefix(c.model, "claude-opus-5", "claude-fable-5", "claude-sonnet-5-5") {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}

	resp, err := c.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return "", anthropicError(ctx, err)
	}
	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return "", errors.New("the AI declined this request. Try rewording it")
	case anthropic.BetaStopReasonMaxTokens:
		return "", errors.New("the AI's reply was cut off. Try a simpler request")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if b, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return text.String(), nil
}

func (c *anthropicClient) Models(ctx context.Context) ([]string, error) {
	page, err := c.client.Models.List(ctx, anthropic.ModelListParams{})
	if err != nil {
		return nil, anthropicError(ctx, err)
	}
	ids := make([]string, 0, len(page.Data))
	for _, m := range page.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func anthropicError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apierr *anthropic.Error
	if !errors.As(err, &apierr) {
		return errors.New("couldn't reach Anthropic. Check your connection")
	}
	switch apierr.StatusCode {
	case http.StatusUnauthorized:
		return errors.New("Anthropic rejected your API key. Run: t2c login anthropic")
	case http.StatusForbidden:
		return errors.New("Anthropic denied access to this model. Pick another with: t2c model")
	case http.StatusNotFound:
		return errors.New("Anthropic couldn't find that model. Pick another with: t2c model")
	case http.StatusTooManyRequests:
		return errors.New("Anthropic limit reached. Try again shortly")
	}
	return fmt.Errorf("Anthropic returned an error (HTTP %d)", apierr.StatusCode)
}
