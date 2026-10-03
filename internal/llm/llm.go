// Package llm abstracts the text-completion backends the worker uses for summaries,
// observation extraction and background jobs: the Claude CLI and a local Ollama.
package llm

import (
	"context"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// Request is one completion request.
type Request struct {
	// Schema, when set, asks the backend for JSON matching this JSON schema (Ollama's structured
	// output). Backends that cannot constrain their output ignore it.
	Schema map[string]any
	System string
	Prompt string
}

// Completer produces a text completion for a request.
type Completer interface {
	Complete(ctx context.Context, req Request) (string, error)
	// Name identifies the backend in logs and status, for example "claude" or "ollama:gemma3:12b".
	Name() string
}

type funcCompleter struct {
	fn   func(ctx context.Context, req Request) (string, error)
	name string
}

func (f funcCompleter) Complete(ctx context.Context, req Request) (string, error) {
	return f.fn(ctx, req)
}

func (f funcCompleter) Name() string { return f.name }

// NewFunc adapts a function to a Completer.
func NewFunc(name string, fn func(ctx context.Context, req Request) (string, error)) Completer {
	return funcCompleter{name: name, fn: fn}
}

// Fallback tries Primary and, when it fails, Secondary. A request that Primary could not serve
// is logged once per outage, not once per call, and again when Primary recovers.
type Fallback struct {
	Primary   Completer
	Secondary Completer
	failing   atomic.Bool
}

// Name reports both backends, primary first.
func (f *Fallback) Name() string { return f.Primary.Name() + "+" + f.Secondary.Name() }

// Complete implements Completer.
func (f *Fallback) Complete(ctx context.Context, req Request) (string, error) {
	out, err := f.Primary.Complete(ctx, req)
	if err == nil {
		if f.failing.Swap(false) {
			log.Info().Str("backend", f.Primary.Name()).Msg("LLM backend recovered")
		}
		return out, nil
	}
	if ctx.Err() != nil {
		return "", err
	}
	if !f.failing.Swap(true) {
		log.Warn().Err(err).Str("backend", f.Primary.Name()).Str("fallback", f.Secondary.Name()).
			Msg("LLM backend failed, using the fallback until it recovers")
	}
	return f.Secondary.Complete(ctx, req)
}
