package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type Anthropic struct {
	c      anthropic.Client
	model  anthropic.Model
	effort anthropic.OutputConfigEffort
}

// NewAnthropic returns a Client for one model. Retries stay at one: the caller is a person waiting
// in a chat, and a second try that also fails should be an answer, not a longer wait.
func NewAnthropic(apiKey, model, effort string, timeout time.Duration) *Anthropic {
	return &Anthropic{
		c: anthropic.NewClient(
			option.WithAPIKey(apiKey),
			option.WithMaxRetries(1),
			option.WithRequestTimeout(timeout),
		),
		model:  anthropic.Model(model),
		effort: anthropic.OutputConfigEffort(effort),
	}
}

func (a *Anthropic) Complete(ctx context.Context, r Request) (Result, error) {
	var blocks []anthropic.ContentBlockParamUnion
	if r.Context != "" {
		c := anthropic.NewTextBlock(r.Context)
		c.OfText.CacheControl = anthropic.NewCacheControlEphemeralParam()
		blocks = append(blocks, c)
	}
	blocks = append(blocks, anthropic.NewTextBlock(r.User))
	// A short extraction gains nothing from thinking; the API allows it off at high effort or below.
	var thinking anthropic.ThinkingConfigParamUnion
	switch a.effort {
	case anthropic.OutputConfigEffortLow, anthropic.OutputConfigEffortMedium, anthropic.OutputConfigEffortHigh:
		thinking.OfDisabled = &anthropic.ThinkingConfigDisabledParam{}
	}
	msg, err := a.c.Messages.New(ctx, anthropic.MessageNewParams{
		Thinking:  thinking,
		Model:     a.model,
		MaxTokens: int64(r.MaxTokens),
		System: []anthropic.TextBlockParam{{
			Text:         r.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: a.effort,
			Format: anthropic.JSONOutputFormatParam{Schema: r.Schema},
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("anthropic: %w", err)
	}
	res := Result{
		Model: string(msg.Model),
		Usage: Usage{
			In:         int(msg.Usage.InputTokens),
			Out:        int(msg.Usage.OutputTokens),
			CacheRead:  int(msg.Usage.CacheReadInputTokens),
			CacheWrite: int(msg.Usage.CacheCreationInputTokens),
		},
		Stop: StopDone,
	}
	switch msg.StopReason {
	case anthropic.StopReasonRefusal:
		res.Stop = StopRefused
	case anthropic.StopReasonMaxTokens:
		res.Stop = StopTruncated
	}
	var text strings.Builder
	for _, b := range msg.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	res.Text = text.String()
	return res, nil
}

// Error codes: where a failed call broke. The chatbot shows them to the owner and logs them.
const (
	CodeTimeout = "E-TIMEOUT"
	CodeNet     = "E-NET"
	Code400     = "E-LLM400"
	Code401     = "E-LLM401"
	Code402     = "E-LLM402"
	Code429     = "E-LLM429"
	Code5xx     = "E-LLM5XX"
)

// ErrorCode names the failure of a Complete call.
func ErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout
	}
	if errors.Is(err, ErrFakeDown) {
		return Code5xx
	}
	var api *anthropic.Error
	if errors.As(err, &api) {
		switch s := api.StatusCode; {
		case s == 401 || s == 403:
			return Code401
		case s == 402 || strings.Contains(strings.ToLower(api.Error()), "credit balance"):
			return Code402
		case s == 429:
			return Code429
		case s >= 500:
			return Code5xx
		}
		return Code400
	}
	return CodeNet
}
