package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
)

func ollamaServer(t *testing.T, installed string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.12.3"}`))
		case "/api/tags":
			_, _ = w.Write([]byte(installed))
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[{"name":"gemma3:12b","size":9000000000,"details":{}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withLLMConfig(t *testing.T, mutate func(*config.Config)) {
	t.Helper()
	orig := llmConfig
	cfg := config.Default()
	mutate(cfg)
	llmConfig = func() *config.Config { return cfg }
	t.Cleanup(func() { llmConfig = orig })
}

func llmStatus(t *testing.T, svc *Service) (*httptest.ResponseRecorder, LLMStatus) {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/llm/status", nil)
	var st LLMStatus
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	return rec, st
}

func TestHandleLLMStatus_ReportsInstalledAndLoadedModels(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	srv := ollamaServer(t, `{"models":[{"name":"gemma3:12b","size":8100000000,"details":{"parameter_size":"12.2B"}},{"name":"llama3.2:3b-instruct-q4_K_M","size":2000000000,"details":{}}]}`)
	withLLMConfig(t, func(c *config.Config) {
		c.OllamaURL, c.OllamaModel = srv.URL, "gemma3:12b"
		c.LLMBackendSummary = config.BackendOllama
	})

	rec, st := llmStatus(t, svc)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, map[string]string{"summary": "ollama", "observation": "claude", "verify": "claude", "brief": "claude"}, st.Backends)
	assert.True(t, st.Fallback)
	assert.True(t, st.Ollama.Reachable)
	assert.Equal(t, "0.12.3", st.Ollama.Version)
	assert.Equal(t, "gemma3:12b", st.Ollama.ConfiguredModel)
	assert.True(t, st.Ollama.ModelInstalled)
	assert.Len(t, st.Ollama.Installed, 2)
	require.Len(t, st.Ollama.Loaded, 1)
	assert.Equal(t, "gemma3:12b", st.Ollama.Loaded[0].Name)
	assert.Empty(t, st.Ollama.Error)
}

func TestHandleLLMStatus_ASetModelThatIsNotInstalledIsSaid(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	srv := ollamaServer(t, `{"models":[{"name":"llama3.2:3b-instruct-q4_K_M","size":2000000000,"details":{}}]}`)
	withLLMConfig(t, func(c *config.Config) { c.OllamaURL, c.OllamaModel = srv.URL, "gemma3:12b" })

	_, st := llmStatus(t, svc)
	assert.True(t, st.Ollama.Reachable)
	assert.False(t, st.Ollama.ModelInstalled, "the installer uses this to offer the pull")
}

func TestHandleLLMStatus_AnUnreachableOllamaIsANormalAnswer(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	withLLMConfig(t, func(c *config.Config) { c.OllamaURL = url })

	_, st := llmStatus(t, svc)
	assert.False(t, st.Ollama.Reachable)
	assert.Contains(t, st.Ollama.Error, "unreachable")
	assert.NotNil(t, st.Ollama.Installed, "lists are empty arrays, not null, for the installer and the dashboard")
	assert.Empty(t, st.Ollama.Installed)
	assert.Equal(t, "claude", st.Backends["summary"], "the backends are reported even when Ollama is down")
}
