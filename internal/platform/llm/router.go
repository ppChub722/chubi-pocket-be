package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
)

// Router picks the client for a user. Today every user gets the server default; a per-user
// choice later changes only this type.
type Router struct {
	provider string
	model    string
	client   Client
}

// Pick is what Router.For hands back: the client plus the names the usage log keeps.
type Pick struct {
	Client   Client
	Provider string
	Model    string
}

func (r *Router) For(context.Context, uuid.UUID) Pick {
	return Pick{Client: r.client, Provider: r.provider, Model: r.model}
}

// Fake reports whether the server runs the keyword fake (cards are then kept in memory only).
func (r *Router) Fake() bool { return r.provider == ProviderFake }

const (
	ProviderAnthropic = "anthropic"
	ProviderFake      = "fake"
)

// NewRouter builds the default client from LLM_*. An unknown provider is an error; a known one
// with no key is Off. The fake provider writes nothing, so the test server may run it.
func NewRouter(cfg config.LLMConfig) (*Router, error) {
	r := &Router{provider: cfg.Provider, model: cfg.Model}
	switch cfg.Provider {
	case ProviderFake:
		r.model, r.client = "keywords", NewKeywordFake()
	case ProviderAnthropic:
		if cfg.APIKey == "" {
			r.client = Off{}
		} else {
			r.client = NewAnthropic(cfg.APIKey, cfg.Model, time.Duration(cfg.TimeoutSec)*time.Second)
		}
	default:
		return nil, fmt.Errorf("llm: unknown LLM_PROVIDER %q", cfg.Provider)
	}
	return r, nil
}

// StaticRouter always answers with c (tests).
func StaticRouter(provider string, c Client) *Router {
	return &Router{provider: provider, model: "test", client: c}
}
