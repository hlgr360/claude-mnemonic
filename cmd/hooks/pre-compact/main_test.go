package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/hooks"
)

type fakeWorker struct {
	*httptest.Server
	summaries []map[string]any
	paths     []string
	mu        sync.Mutex
}

func newFakeWorker(t *testing.T, sessionReply string, summarizeStatus int) *fakeWorker {
	t.Helper()
	fw := &fakeWorker{}
	fw.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions":
			_, _ = w.Write([]byte(sessionReply))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/summarize"):
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			fw.mu.Lock()
			fw.summaries = append(fw.summaries, body)
			fw.paths = append(fw.paths, r.URL.Path)
			fw.mu.Unlock()
			w.WriteHeader(summarizeStatus)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fw.Close)
	return fw
}

func (fw *fakeWorker) context(t *testing.T) *hooks.HookContext {
	t.Helper()
	u, err := url.Parse(fw.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return &hooks.HookContext{HookName: "PreCompact", Port: port, SessionID: "claude-session-1"}
}

func transcriptFile(t *testing.T, turns ...[2]string) string {
	t.Helper()
	var b strings.Builder
	for _, turn := range turns {
		raw, _ := json.Marshal(map[string]any{"type": turn[0], "message": map[string]any{"role": turn[0], "content": turn[1]}})
		b.Write(raw)
		b.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

func TestPreCompact_SendsTheConversationForTheSessionToBeSummarised(t *testing.T) {
	fw := newFakeWorker(t, `{"id": 7}`, http.StatusOK)
	path := transcriptFile(t, [2]string{"user", "decide names or ids"}, [2]string{"assistant", "names"}, [2]string{"user", "ok, now the checkpoints"})

	out, err := handlePreCompact(fw.context(t), &Input{TranscriptPath: path, Trigger: "auto"})
	require.NoError(t, err)
	assert.Empty(t, out, "a PreCompact hook cannot add context, so it adds none")

	require.Len(t, fw.summaries, 1)
	assert.Equal(t, []string{"/sessions/7/summarize"}, fw.paths)
	body := fw.summaries[0]
	assert.Equal(t, "User: decide names or ids\n\nAssistant: names\n\nUser: ok, now the checkpoints", body["conversation"])
	assert.Equal(t, "ok, now the checkpoints", body["lastUserMessage"])
	assert.Equal(t, "names", body["lastAssistantMessage"])
}

func TestPreCompact_DoesNothingWithoutAConversationOrASession(t *testing.T) {
	path := transcriptFile(t, [2]string{"user", "hello"})

	t.Run("no session", func(t *testing.T) {
		fw := newFakeWorker(t, `{}`, http.StatusOK)
		_, err := handlePreCompact(fw.context(t), &Input{TranscriptPath: path})
		require.NoError(t, err)
		assert.Empty(t, fw.summaries)
	})
	t.Run("missing transcript", func(t *testing.T) {
		fw := newFakeWorker(t, `{"id": 7}`, http.StatusOK)
		_, err := handlePreCompact(fw.context(t), &Input{TranscriptPath: filepath.Join(t.TempDir(), "gone.jsonl")})
		require.NoError(t, err)
		assert.Empty(t, fw.summaries)
	})
	t.Run("no transcript path", func(t *testing.T) {
		fw := newFakeWorker(t, `{"id": 7}`, http.StatusOK)
		_, err := handlePreCompact(fw.context(t), &Input{})
		require.NoError(t, err)
		assert.Empty(t, fw.summaries)
	})
}

func TestPreCompact_NeverBlocksTheCompactionWhenTheWorkerFails(t *testing.T) {
	fw := newFakeWorker(t, `{"id": 7}`, http.StatusInternalServerError)
	path := transcriptFile(t, [2]string{"user", "something to keep"})

	_, err := handlePreCompact(fw.context(t), &Input{TranscriptPath: path})
	assert.NoError(t, err, "a failing summary request is a warning, never an error that stops the hook")
	assert.Len(t, fw.summaries, 1)

	fw.Close()
	_, err = handlePreCompact(fw.context(t), &Input{TranscriptPath: path})
	assert.NoError(t, err, "a worker that is gone is not an error either")
}
