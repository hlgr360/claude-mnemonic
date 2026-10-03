package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type seen struct {
	body   map[string]any
	method string
	path   string
}

func fakeOllama(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *[]seen) {
	t.Helper()
	var log []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		log = append(log, seen{method: r.Method, path: r.URL.Path, body: body})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &log
}

func reply(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestComplete_SendsTheRequestOllamaExpects(t *testing.T) {
	srv, log := fakeOllama(t, reply(`{"message":{"role":"assistant","content":"<summary>ok</summary>"},"done":true,"done_reason":"stop"}`))
	o := NewOllama(srv.URL, "gemma3:12b", 8192, "15m", time.Second)

	out, err := o.Complete(context.Background(), Request{System: "be brief", Prompt: "summarise this", Schema: map[string]any{"type": "object"}})
	require.NoError(t, err)
	assert.Equal(t, "<summary>ok</summary>", out)

	require.Len(t, *log, 1)
	got := (*log)[0]
	assert.Equal(t, "POST", got.method)
	assert.Equal(t, "/api/chat", got.path)
	assert.Equal(t, "gemma3:12b", got.body["model"])
	assert.Equal(t, false, got.body["stream"], "a single answer, not a stream")
	assert.Equal(t, "15m", got.body["keep_alive"])
	assert.Equal(t, map[string]any{"type": "object"}, got.body["format"])
	opts := got.body["options"].(map[string]any)
	assert.EqualValues(t, 8192, opts["num_ctx"])
	assert.EqualValues(t, 0.2, opts["temperature"])
	msgs := got.body["messages"].([]any)
	require.Len(t, msgs, 2)
	assert.Equal(t, map[string]any{"role": "system", "content": "be brief"}, msgs[0])
	assert.Equal(t, map[string]any{"role": "user", "content": "summarise this"}, msgs[1])
}

func TestComplete_LeavesOutWhatWasNotAskedFor(t *testing.T) {
	srv, log := fakeOllama(t, reply(`{"message":{"content":"x"},"done_reason":"stop"}`))
	o := &Ollama{BaseURL: srv.URL, Model: "m"}

	_, err := o.Complete(context.Background(), Request{Prompt: "p"})
	require.NoError(t, err)
	body := (*log)[0].body
	assert.NotContains(t, body, "format")
	assert.NotContains(t, body, "keep_alive")
	assert.Len(t, body["messages"], 1, "no system message when there is no system prompt")
	assert.NotContains(t, body["options"], "num_ctx", "the server default applies when no window is configured")
}

func TestComplete_Failures(t *testing.T) {
	t.Run("no model configured", func(t *testing.T) {
		_, err := (&Ollama{BaseURL: "http://127.0.0.1:1"}).Complete(context.Background(), Request{Prompt: "p"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no model configured")
	})
	t.Run("model not installed", func(t *testing.T) {
		srv, _ := fakeOllama(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"model 'nope' not found"}`))
		})
		_, err := NewOllama(srv.URL, "nope", 0, "", time.Second).Complete(context.Background(), Request{Prompt: "p"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model 'nope' not found")
		assert.Contains(t, err.Error(), "404")
	})
	t.Run("error in an OK response", func(t *testing.T) {
		srv, _ := fakeOllama(t, reply(`{"error":"out of memory"}`))
		_, err := NewOllama(srv.URL, "m", 0, "", time.Second).Complete(context.Background(), Request{Prompt: "p"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of memory")
	})
	t.Run("empty answer", func(t *testing.T) {
		srv, _ := fakeOllama(t, reply(`{"message":{"content":"  \n"},"done_reason":"stop"}`))
		_, err := NewOllama(srv.URL, "m", 0, "", time.Second).Complete(context.Background(), Request{Prompt: "p"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty response")
	})
	t.Run("answer cut off", func(t *testing.T) {
		srv, _ := fakeOllama(t, reply(`{"message":{"content":"<summary><request>half"},"done_reason":"length"}`))
		_, err := NewOllama(srv.URL, "m", 0, "", time.Second).Complete(context.Background(), Request{Prompt: "p"})
		assert.ErrorIs(t, err, ErrTruncated, "half an XML document is not an answer")
	})
	t.Run("not JSON", func(t *testing.T) {
		srv, _ := fakeOllama(t, reply(`<html>proxy error</html>`))
		_, err := NewOllama(srv.URL, "m", 0, "", time.Second).Complete(context.Background(), Request{Prompt: "p"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected response")
	})
	t.Run("server error without a message", func(t *testing.T) {
		srv, _ := fakeOllama(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
		_, err := NewOllama(srv.URL, "m", 0, "", time.Second).Complete(context.Background(), Request{Prompt: "p"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 502")
	})
	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		_, err := NewOllama(url, "m", 0, "", time.Second).Complete(context.Background(), Request{Prompt: "p"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unreachable")
	})
	t.Run("slow server hits the timeout", func(t *testing.T) {
		srv, _ := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
		})
		_, err := NewOllama(srv.URL, "m", 0, "", 100*time.Millisecond).Complete(context.Background(), Request{Prompt: "p"})
		require.Error(t, err)
	})
	t.Run("cancelled context", func(t *testing.T) {
		srv, _ := fakeOllama(t, reply(`{"message":{"content":"x"},"done_reason":"stop"}`))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewOllama(srv.URL, "m", 0, "", time.Second).Complete(ctx, Request{Prompt: "p"})
		require.Error(t, err)
	})
}

func TestModelListsAndVersion(t *testing.T) {
	srv, log := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			reply(`{"models":[{"name":"gemma3:12b","size":8100000000,"details":{"parameter_size":"12.2B","quantization_level":"Q4_K_M","family":"gemma3"}},{"name":"qllama/bge-small-en-v1.5:latest","size":37000000,"details":{}}]}`)(w, r)
		case "/api/ps":
			reply(`{"models":[{"name":"gemma3:12b","size":9000000000,"expires_at":"2026-10-03T20:00:00Z","details":{}}]}`)(w, r)
		case "/api/version":
			reply(`{"version":"0.12.3"}`)(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	o := NewOllama(srv.URL, "gemma3:12b", 0, "", time.Second)
	ctx := context.Background()

	installed, err := o.Installed(ctx)
	require.NoError(t, err)
	require.Len(t, installed, 2)
	assert.Equal(t, ModelInfo{Name: "gemma3:12b", Size: 8100000000, ParameterSize: "12.2B", Quantization: "Q4_K_M", Family: "gemma3"}, installed[0])

	loaded, err := o.Loaded(ctx)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, "2026-10-03T20:00:00Z", loaded[0].ExpiresAt)

	v, err := o.Version(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0.12.3", v)

	var paths []string
	for _, s := range *log {
		assert.Equal(t, "GET", s.method)
		paths = append(paths, s.path)
	}
	assert.Equal(t, []string{"/api/tags", "/api/ps", "/api/version"}, paths)
}

func TestModelListsWhenNothingIsInstalled(t *testing.T) {
	srv, _ := fakeOllama(t, reply(`{"models":[]}`))
	installed, err := NewOllama(srv.URL, "m", 0, "", time.Second).Installed(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, installed)
	assert.Empty(t, installed)
}

func TestHasModel(t *testing.T) {
	installed := []ModelInfo{{Name: "gemma3:12b"}, {Name: "qllama/bge-small-en-v1.5:latest"}, {Name: "llama3.2:3b-instruct-q4_K_M"}}
	assert.True(t, HasModel(installed, "gemma3:12b"))
	assert.True(t, HasModel(installed, "qllama/bge-small-en-v1.5"), "a name without a tag means :latest")
	assert.True(t, HasModel(installed, "qllama/bge-small-en-v1.5:latest"))
	assert.False(t, HasModel(installed, "gemma3"), "gemma3 without a tag is gemma3:latest, which is not installed")
	assert.False(t, HasModel(installed, "gemma3:4b"))
	assert.False(t, HasModel(installed, ""))
	assert.False(t, HasModel(nil, "gemma3:12b"))
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                       DefaultOllamaURL,
		"  ":                     DefaultOllamaURL,
		"127.0.0.1:11434":        "http://127.0.0.1:11434",
		"http://box.local:11434": "http://box.local:11434",
		"https://ollama.lan/":    "https://ollama.lan",
		" localhost:9 ":          "http://localhost:9",
	} {
		assert.Equal(t, want, NormalizeURL(in), "%q", in)
	}
}

func TestURLFromEnv(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "10.0.0.5:11434")
	assert.Equal(t, "http://10.0.0.5:11434", URLFromEnv(""), "OLLAMA_HOST is honoured")
	assert.Equal(t, "http://configured:1", URLFromEnv("configured:1"), "a setting wins over the environment")
	t.Setenv("OLLAMA_HOST", "")
	assert.Equal(t, DefaultOllamaURL, URLFromEnv(""))
}
