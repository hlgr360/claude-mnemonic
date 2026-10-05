package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSandboxPath(t *testing.T) {
	for _, p := range []string{"/sessions/charming-dazzling-thompson/mnt/notes", "/sessions/x/mnt/a/../notes", " /sessions/x/mnt/notes "} {
		assert.True(t, isSandboxPath(p), p)
	}
	for _, p := range []string{"", "/Users/someone/repos/notes", "/home/someone/notes", "/sessions", "/mysessions/x", "sessions/x/mnt/notes", "/var/sessions/x"} {
		assert.False(t, isSandboxPath(p), p)
	}
}

func TestSandboxPathIsRefusedBeforeAnythingIsAskedOfTheWorker(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){})
	s := desktopServer(t, fw)
	cowork := "/sessions/charming-dazzling-thompson/mnt/notes"

	for name, run := range map[string]func() error{
		"project_resolve": func() error { _, err := call(s, "project_resolve", map[string]any{"path": cowork}); return err },
		"remember":        func() error { _, err := call(s, "remember", map[string]any{"path": cowork, "text": "x"}); return err },
		"checkpoint": func() error {
			_, err := call(s, "checkpoint", map[string]any{"path": cowork, "thread": "t", "goal": "g"})
			return err
		},
		"context": func() error { _, err := call(s, "context", map[string]any{"path": cowork}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "inside Cowork's sandbox")
			assert.Contains(t, err.Error(), "full path on their computer")
		})
	}
	assert.Zero(t, fw.total(), "nothing was resolved or stored for a path that is not on the user's computer")
}

func TestAnUnknownNameSaysHowToStartAProject(t *testing.T) {
	fw := rememberWorker(t, `{"project":"x","id":1}`)
	_, err := call(desktopServer(t, fw), "remember", map[string]any{"project": "made-up", "text": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown project "made-up"`)
	assert.Contains(t, err.Error(), "To start a new project, ask the user for the folder's full path on their computer")
	assert.Contains(t, err.Error(), "not a sandbox path")
}
