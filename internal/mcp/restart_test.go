package mcp

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastRestart shortens the waits so the tests do not take real time.
func fastRestart(t *testing.T) {
	t.Helper()
	oldPoll, oldGone, oldReady := restartPollInterval, restartGoneWithin, restartReadyWithin
	restartPollInterval, restartGoneWithin, restartReadyWithin = 5*time.Millisecond, 150*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { restartPollInterval, restartGoneWithin, restartReadyWithin = oldPoll, oldGone, oldReady })
}

func healthJSON(w http.ResponseWriter, code int, version string, uptime int, ready bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"status":"x","ready":%t,"uptime_seconds":%d,"version":%q}`, ready, uptime, version)
}

func TestRestartTool_AsksTheWorkerAndWaitsForTheNewOne(t *testing.T) {
	fastRestart(t)
	var restarted atomic.Bool
	var polls atomic.Int32
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/restart": func(w http.ResponseWriter, _ *http.Request) {
			restarted.Store(true)
			_, _ = w.Write([]byte(`{"success":true}`))
		},
		"GET /health": func(w http.ResponseWriter, _ *http.Request) {
			switch {
			case !restarted.Load():
				healthJSON(w, 200, "1.0.0", 500, true)
			case polls.Add(1) <= 2:
				healthJSON(w, 200, "1.0.0", 501, true) // the old worker still answers for a moment
			case polls.Load() <= 4:
				healthJSON(w, 503, "1.0.1", 0, false) // the new one is starting
			default:
				healthJSON(w, 200, "1.0.1", 1, true)
			}
		},
	})
	s := desktopServer(t, fw)

	out, err := call(s, "restart", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, out, "restarted and is ready again")
	assert.Contains(t, out, "version 1.0.1", "the new worker's version, not the old one's")
	assert.Len(t, fw.requests("/api/restart"), 1)
	assert.Equal(t, http.MethodPost, fw.requests("/api/restart")[0].method)
}

func TestRestartTool_ANewWorkerThatIsAlreadyReadyIsToldByItsUptime(t *testing.T) {
	fastRestart(t)
	var restarted atomic.Bool
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/restart": func(w http.ResponseWriter, _ *http.Request) { restarted.Store(true); _, _ = w.Write([]byte(`{}`)) },
		"GET /health": func(w http.ResponseWriter, _ *http.Request) {
			if restarted.Load() {
				healthJSON(w, 200, "2.0.0", 0, true)
				return
			}
			healthJSON(w, 200, "2.0.0", 900, true)
		},
	})
	out, err := call(desktopServer(t, fw), "restart", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, out, "restarted and is ready again")
}

func TestRestartTool_AWorkerThatStaysTheSameProcessIsAFailureNotASuccess(t *testing.T) {
	fastRestart(t)
	var uptime atomic.Int32
	uptime.Store(500)
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/restart": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"success":true}`)) },
		"GET /health":       func(w http.ResponseWriter, _ *http.Request) { healthJSON(w, 200, "1.0.0", int(uptime.Add(1)), true) },
	})
	out, err := call(desktopServer(t, fw), "restart", map[string]any{})
	require.Error(t, err, "the old worker is still answering: nothing was restarted, and it must not say so")
	assert.Contains(t, err.Error(), "still the same process")
	assert.Empty(t, out)
}

func TestRestartTool_ANewWorkerThatNeverGetsReadyIsReportedAsStillStarting(t *testing.T) {
	fastRestart(t)
	var restarted atomic.Bool
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/restart": func(w http.ResponseWriter, _ *http.Request) { restarted.Store(true); _, _ = w.Write([]byte(`{}`)) },
		"GET /health": func(w http.ResponseWriter, _ *http.Request) {
			if restarted.Load() {
				healthJSON(w, 503, "1.0.1", 3, false)
				return
			}
			healthJSON(w, 200, "1.0.0", 500, true)
		},
	})
	out, err := call(desktopServer(t, fw), "restart", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, out, "not ready yet", "it does not claim success")
	assert.NotContains(t, out, "ready again")
}

func TestRestartTool_AWorkerThatDoesNotAnswerCannotBeAskedAndTheMessageSaysWhatToDo(t *testing.T) {
	fastRestart(t)
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){}) // /health is a 404
	s := desktopServer(t, fw)
	_, err := call(s, "restart", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not answer")
	assert.Empty(t, fw.requests("/api/restart"), "nothing is posted to a worker that is not there")
}

func TestRestartTool_IsADesktopToolThatSaysNotToUseAShell(t *testing.T) {
	var found *Tool
	for _, tool := range desktopTools() {
		if tool.Name == "restart" {
			tool := tool
			found = &tool
		}
	}
	require.NotNil(t, found)
	assert.True(t, isDesktopTool("restart"))
	assert.Contains(t, found.Description, "sandbox")
	assert.Equal(t, "object", found.InputSchema["type"])
	assert.Contains(t, desktopInstructions, "call restart")

	code := NewServer(nil, "", "p_aaaaaa", "v")
	code.SetMode(ModeCode)
	_, err := call(code, "restart", map[string]any{})
	require.Error(t, err, "Claude Code's tool list does not change: it has the slash command instead")
}
