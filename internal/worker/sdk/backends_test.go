package sdk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/llm"
)

func cfgWith(summary, observation, verify string, fallback bool) *config.Config {
	cfg := config.Default()
	cfg.LLMBackendSummary, cfg.LLMBackendObservation, cfg.LLMBackendVerify = summary, observation, verify
	cfg.LLMBackendBrief = config.BackendClaude
	cfg.LLMFallbackToClaude = fallback
	return cfg
}

func TestTaskBackend(t *testing.T) {
	cfg := cfgWith("ollama", "claude", "ollama", true)
	assert.Equal(t, "ollama", taskBackend(cfg, TaskSummary))
	assert.Equal(t, "claude", taskBackend(cfg, TaskObservation))
	assert.Equal(t, "ollama", taskBackend(cfg, TaskVerify))
	assert.Equal(t, "claude", taskBackend(cfg, TaskBrief))
	assert.Equal(t, "claude", taskBackend(cfg, Task("something else")), "an unknown task runs on the default")
}

func TestNeedsClaude(t *testing.T) {
	assert.True(t, needsClaude(config.Default()), "the default is the Claude CLI")
	assert.True(t, needsClaude(cfgWith("ollama", "ollama", "claude", false)), "one task still on Claude")
	assert.True(t, needsClaude(cfgWith("ollama", "ollama", "ollama", true)), "the fallback needs the CLI")
	allLocal := cfgWith("ollama", "ollama", "ollama", false)
	assert.True(t, needsClaude(allLocal), "the brief task is still on Claude")
	allLocal.LLMBackendBrief = config.BackendOllama
	assert.False(t, needsClaude(allLocal), "everything local with no fallback works without the CLI")
}

func TestBuildCompleters(t *testing.T) {
	claude := llm.NewFunc("claude", func(context.Context, llm.Request) (string, error) { return "", nil })
	ollama := llm.NewOllama("http://127.0.0.1:1", "gemma3:12b", 0, "", time.Second)

	t.Run("defaults route nothing", func(t *testing.T) {
		assert.Empty(t, buildCompleters(config.Default(), claude, ollama))
	})
	t.Run("only the selected tasks move, with the fallback", func(t *testing.T) {
		got := buildCompleters(cfgWith("ollama", "claude", "ollama", true), claude, ollama)
		require.Len(t, got, 2)
		assert.Contains(t, got, TaskSummary)
		assert.Contains(t, got, TaskVerify)
		assert.NotContains(t, got, TaskObservation)
		assert.Equal(t, "ollama:gemma3:12b+claude", got[TaskSummary].Name())
	})
	t.Run("without the fallback the task is on Ollama alone", func(t *testing.T) {
		got := buildCompleters(cfgWith("ollama", "claude", "claude", false), claude, ollama)
		assert.Equal(t, "ollama:gemma3:12b", got[TaskSummary].Name())
	})
	t.Run("no model means the task stays on Claude", func(t *testing.T) {
		assert.Empty(t, buildCompleters(cfgWith("ollama", "ollama", "ollama", true), claude, llm.NewOllama("", "", 0, "", time.Second)))
		assert.Empty(t, buildCompleters(cfgWith("ollama", "ollama", "ollama", true), claude, nil))
	})
}

func TestNewOllamaFromConfig(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	cfg := config.Default()
	cfg.OllamaModel, cfg.OllamaURL, cfg.OllamaNumCtx, cfg.OllamaKeepAlive, cfg.OllamaTimeoutSeconds = "granite3.3:8b", "box:11434", 4096, "5m", 30

	o := NewOllamaFromConfig(cfg)
	assert.Equal(t, "http://box:11434", o.BaseURL)
	assert.Equal(t, "granite3.3:8b", o.Model)
	assert.Equal(t, 4096, o.NumCtx)
	assert.Equal(t, "5m", o.KeepAlive)
	assert.Equal(t, 30*time.Second, o.HTTP.Timeout)
}

// fakeOllamaServer answers every chat request with the given status and body and records the requests.
func fakeOllamaServer(t *testing.T, status int, body string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		requests = append(requests, req)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// claudeStandIn writes a script that answers like the Claude CLI would.
func claudeStandIn(t *testing.T, answer string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script")
	}
	path := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho '"+answer+"'\n"), 0o755)) // #nosec G306 -- test script
	return path
}

func processorWith(t *testing.T, cfg *config.Config, ollamaURL, claudePath string) *Processor {
	t.Helper()
	p := &Processor{claudePath: claudePath, sem: make(chan struct{}, 4)}
	ollama := llm.NewOllama(ollamaURL, "gemma3:12b", 8192, "", 2*time.Second)
	p.completers = buildCompleters(cfg, p.claudeCompleter(), ollama)
	return p
}

func TestComplete_RunsEachTaskOnItsBackend(t *testing.T) {
	srv, requests := fakeOllamaServer(t, http.StatusOK, `{"message":{"content":"from ollama"},"done_reason":"stop"}`)
	p := processorWith(t, cfgWith("ollama", "claude", "claude", false), srv.URL, claudeStandIn(t, "from claude"))

	out, err := p.complete(context.Background(), TaskSummary, "summarise")
	require.NoError(t, err)
	assert.Equal(t, "from ollama", out)
	require.Len(t, *requests, 1)
	msgs := (*requests)[0]["messages"].([]any)
	assert.Equal(t, "system", msgs[0].(map[string]any)["role"])
	assert.Equal(t, systemPrompt, msgs[0].(map[string]any)["content"], "Ollama gets the same system prompt the CLI call adds")
	assert.Equal(t, "summarise", msgs[1].(map[string]any)["content"])

	out, err = p.complete(context.Background(), TaskObservation, "extract")
	require.NoError(t, err)
	assert.Contains(t, out, "from claude", "a task that is not selected still runs on the CLI")
	assert.Len(t, *requests, 1, "and does not touch Ollama")
}

func TestComplete_FallsBackToTheCLIWhenOllamaFails(t *testing.T) {
	srv, requests := fakeOllamaServer(t, http.StatusInternalServerError, `{"error":"model crashed"}`)
	p := processorWith(t, cfgWith("ollama", "claude", "claude", true), srv.URL, claudeStandIn(t, "from claude"))

	out, err := p.complete(context.Background(), TaskSummary, "summarise")
	require.NoError(t, err)
	assert.Contains(t, out, "from claude")
	assert.Len(t, *requests, 1, "Ollama was tried first")
}

func TestComplete_WithoutTheFallbackAnOllamaFailureIsReported(t *testing.T) {
	srv, _ := fakeOllamaServer(t, http.StatusInternalServerError, `{"error":"model crashed"}`)
	p := processorWith(t, cfgWith("ollama", "claude", "claude", false), srv.URL, claudeStandIn(t, "from claude"))

	_, err := p.complete(context.Background(), TaskSummary, "summarise")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model crashed")
}

func TestComplete_DefaultsRunEverythingOnTheCLI(t *testing.T) {
	p := &Processor{claudePath: claudeStandIn(t, "from claude"), sem: make(chan struct{}, 4)}
	for _, task := range []Task{TaskSummary, TaskObservation, TaskVerify} {
		out, err := p.complete(context.Background(), task, "prompt")
		require.NoError(t, err, task)
		assert.Contains(t, out, "from claude", task)
	}
}

func TestIsAvailable(t *testing.T) {
	assert.False(t, (&Processor{claudePath: "/nonexistent/claude"}).IsAvailable(), "no CLI and nothing local")
	assert.True(t, (&Processor{claudePath: claudeStandIn(t, "x")}).IsAvailable())

	local := &Processor{completers: map[Task]llm.Completer{
		TaskSummary: &stubCompleter{}, TaskObservation: &stubCompleter{}, TaskVerify: &stubCompleter{}, TaskBrief: &stubCompleter{},
	}}
	assert.True(t, local.IsAvailable(), "every task local: the CLI is not needed")

	partial := &Processor{completers: map[Task]llm.Completer{TaskSummary: &stubCompleter{}}}
	assert.False(t, partial.IsAvailable(), "one task local, the others still need the CLI")
}

type stubCompleter struct{}

func (*stubCompleter) Complete(context.Context, llm.Request) (string, error) { return "x", nil }
func (*stubCompleter) Name() string                                          { return "stub" }
