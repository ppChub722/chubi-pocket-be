package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type Anthropic struct {
	c     anthropic.Client
	model anthropic.Model
}

// NewAnthropic returns a Client for one model. Retries stay at one: the caller is a person waiting
// in a chat, and a second try that also fails should be an answer, not a longer wait.
func NewAnthropic(apiKey, model string, timeout time.Duration) *Anthropic {
	return &Anthropic{
		c: anthropic.NewClient(
			option.WithAPIKey(apiKey),
			option.WithMaxRetries(1),
			option.WithRequestTimeout(timeout),
		),
		model: anthropic.Model(model),
	}
}

func (a *Anthropic) Complete(ctx context.Context, r Request) (Result, error) {
	msg, err := a.c.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: int64(r.MaxTokens),
		System: []anthropic.TextBlockParam{{
			Text:         r.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(r.User))},
		OutputConfig: anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: r.Schema}},
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
