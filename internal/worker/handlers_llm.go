package worker

import (
	"context"
	"net/http"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/llm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
)

// llmConfig returns the configuration the LLM status is computed from. Tests replace it.
var llmConfig = config.Get

const llmStatusTimeout = 3 * time.Second

// OllamaStatus describes the local Ollama server and the configured model.
type OllamaStatus struct {
	URL             string          `json:"url"`
	Version         string          `json:"version,omitempty"`
	Error           string          `json:"error,omitempty"`
	ConfiguredModel string          `json:"configured_model"`
	Installed       []llm.ModelInfo `json:"installed"`
	Loaded          []llm.ModelInfo `json:"loaded"`
	Reachable       bool            `json:"reachable"`
	ModelInstalled  bool            `json:"model_installed"`
}

// LLMStatus is the answer of GET /api/llm/status: which backend each task runs on and what the
// local Ollama offers.
type LLMStatus struct {
	Backends map[string]string `json:"backends"`
	Ollama   OllamaStatus      `json:"ollama"`
	Fallback bool              `json:"fallback_to_claude"`
}

// handleLLMStatus reports the LLM backends. Ollama is asked every time, with a short timeout, so
// an installer or the dashboard sees the current state; an unreachable Ollama is a normal answer.
func (s *Service) handleLLMStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), llmStatusTimeout)
	defer cancel()

	cfg := llmConfig()
	client := sdk.NewOllamaFromConfig(cfg)
	client.HTTP = &http.Client{Timeout: llmStatusTimeout}

	status := LLMStatus{
		Backends: map[string]string{
			string(sdk.TaskSummary):     cfg.LLMBackendSummary,
			string(sdk.TaskObservation): cfg.LLMBackendObservation,
			string(sdk.TaskVerify):      cfg.LLMBackendVerify,
			string(sdk.TaskBrief):       cfg.LLMBackendBrief,
			string(sdk.TaskConflict):    cfg.LLMBackendConflict,
			string(sdk.TaskRollup):      cfg.LLMBackendRollup,
		},
		Fallback: cfg.LLMFallbackToClaude,
		Ollama: OllamaStatus{
			URL: client.BaseURL, ConfiguredModel: cfg.OllamaModel,
			Installed: []llm.ModelInfo{}, Loaded: []llm.ModelInfo{},
		},
	}

	version, err := client.Version(ctx)
	if err != nil {
		status.Ollama.Error = err.Error()
	} else {
		status.Ollama.Reachable = true
		status.Ollama.Version = version
		if installed, ierr := client.Installed(ctx); ierr == nil {
			status.Ollama.Installed = installed
			status.Ollama.ModelInstalled = llm.HasModel(installed, cfg.OllamaModel)
		} else {
			status.Ollama.Error = ierr.Error()
		}
		if loaded, lerr := client.Loaded(ctx); lerr == nil {
			status.Ollama.Loaded = loaded
		}
	}

	noStore(w)
	writeJSON(w, status)
}
