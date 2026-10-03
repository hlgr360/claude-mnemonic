package sdk

import (
	"context"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/llm"
)

// Task names a kind of LLM work, so each can run on its own backend.
type Task string

// Tasks the processor routes.
const (
	TaskObservation Task = "observation"
	TaskSummary     Task = "summary"
	TaskVerify      Task = "verify"
)

// taskBackend returns the backend the config selects for a task.
func taskBackend(cfg *config.Config, task Task) string {
	switch task {
	case TaskSummary:
		return cfg.LLMBackendSummary
	case TaskObservation:
		return cfg.LLMBackendObservation
	case TaskVerify:
		return cfg.LLMBackendVerify
	}
	return config.BackendClaude
}

// needsClaude reports whether any task can end up on the Claude CLI: it is the default backend
// and the fallback of every task that runs on Ollama.
func needsClaude(cfg *config.Config) bool {
	for _, t := range []Task{TaskObservation, TaskSummary, TaskVerify} {
		if taskBackend(cfg, t) != config.BackendOllama || cfg.LLMFallbackToClaude {
			return true
		}
	}
	return false
}

// NewOllamaFromConfig builds the Ollama client the settings describe.
func NewOllamaFromConfig(cfg *config.Config) *llm.Ollama {
	return llm.NewOllama(llm.URLFromEnv(cfg.OllamaURL), cfg.OllamaModel, cfg.OllamaNumCtx, cfg.OllamaKeepAlive,
		time.Duration(cfg.OllamaTimeoutSeconds)*time.Second)
}

// buildCompleters returns the completer of every task that is not on the Claude CLI. Tasks that
// are absent from the result run on claude. A task on Ollama without a configured model stays on
// Claude, because there is nothing to ask.
func buildCompleters(cfg *config.Config, claude llm.Completer, ollama *llm.Ollama) map[Task]llm.Completer {
	out := map[Task]llm.Completer{}
	if ollama == nil || ollama.Model == "" {
		return out
	}
	for _, t := range []Task{TaskObservation, TaskSummary, TaskVerify} {
		if taskBackend(cfg, t) != config.BackendOllama {
			continue
		}
		if cfg.LLMFallbackToClaude && claude != nil {
			out[t] = &llm.Fallback{Primary: ollama, Secondary: claude}
		} else {
			out[t] = ollama
		}
	}
	return out
}

// claudeCompleter runs requests on the Claude CLI.
func (p *Processor) claudeCompleter() llm.Completer {
	return llm.NewFunc(config.BackendClaude, func(ctx context.Context, req llm.Request) (string, error) {
		// The CLI call adds the system prompt itself.
		return p.callClaudeCLI(ctx, req.Prompt)
	})
}

// completerFor returns the backend a task runs on.
func (p *Processor) completerFor(task Task) llm.Completer {
	if c, ok := p.completers[task]; ok {
		return c
	}
	return p.claudeCompleter()
}

// complete runs a prompt for a task on its configured backend.
func (p *Processor) complete(ctx context.Context, task Task, prompt string) (string, error) {
	return p.completerFor(task).Complete(ctx, llm.Request{System: systemPrompt, Prompt: prompt})
}
