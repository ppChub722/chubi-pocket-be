// Package llm is the one door to a language model (0.4.2-4, the Telegram chatbot). Callers see only
// Client; the provider behind it (Anthropic today) can change without touching them. The model's
// answer is requested as JSON in a given schema and handed back as text: validating it is the
// caller's job, nothing the model says is trusted.
package llm

import (
	"context"
	"errors"
	"sync"
)

// ErrOff means no key is configured: the feature is off.
var ErrOff = errors.New("llm: off")

type Request struct {
	System    string         // fixed text, cached by the provider
	Context   string         // the per-user part that repeats across calls (lists), cached after System
	User      string         // the per-call part
	Schema    map[string]any // JSON schema of the answer
	MaxTokens int

	// Input is the user's own words and Current the card being edited (answer-shaped JSON, nil
	// for a new message). Both are already inside User; a real provider ignores them, the fake
	// provider answers from them.
	Input   string
	Current []byte
}

// Stop tells why the model stopped; anything but StopDone has no usable answer.
type Stop string

const (
	StopDone      Stop = "done"
	StopRefused   Stop = "refused"
	StopTruncated Stop = "truncated"
)

type Usage struct {
	In, Out, CacheRead, CacheWrite int
}

type Result struct {
	Text  string // the JSON answer
	Stop  Stop
	Usage Usage
	Model string
}

type Client interface {
	Complete(ctx context.Context, r Request) (Result, error)
}

// Off is the Client used when no key is configured.
type Off struct{}

func (Off) Complete(context.Context, Request) (Result, error) { return Result{}, ErrOff }

// Fake answers from Reply and records every request. For tests only.
type Fake struct {
	Reply func(Request) (Result, error)

	mu    sync.Mutex
	calls []Request
}

func (f *Fake) Complete(_ context.Context, r Request) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r)
	f.mu.Unlock()
	return f.Reply(r)
}

func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.calls...)
}

// Say is a Fake that always answers with this JSON.
func Say(json string) *Fake {
	return &Fake{Reply: func(Request) (Result, error) {
		return Result{Text: json, Stop: StopDone, Usage: Usage{In: 100, Out: 20}, Model: "fake"}, nil
	}}
}
