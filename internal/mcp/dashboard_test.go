package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardTool_GivesTheWorkersAddressAsALink(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){})
	s := desktopServer(t, fw)

	out, err := call(s, "dashboard", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, out, fw.URL+" .", "the address of the worker this server talks to, set off from the sentence so a link does not swallow the full stop")
	assert.Contains(t, out, "Give the user this link")
	assert.Contains(t, out, "browser on this computer")
	assert.Equal(t, 0, fw.total(), "it only says the address: no request to the worker beyond the usual start check")
}

func TestDashboardTool_FollowsTheConfiguredPort(t *testing.T) {
	s := NewServer(nil, "http://localhost:4100/", "p_aaaaaa", "v")
	assert.Contains(t, s.toolDashboard(), "http://localhost:4100 .", "a custom WORKER_PORT is respected and a trailing slash is not doubled")
}

func TestDashboardTool_IsADesktopToolAndSaysWhenToUseIt(t *testing.T) {
	var found *Tool
	for _, tool := range desktopTools() {
		if tool.Name == "dashboard" {
			tool := tool
			found = &tool
		}
	}
	require.NotNil(t, found)
	assert.True(t, isDesktopTool("dashboard"))
	assert.Contains(t, found.Description, "browser")
	assert.Contains(t, found.Description, "Read-only")
	assert.Equal(t, "object", found.InputSchema["type"])
	assert.Contains(t, desktopInstructions, "call dashboard", "the server's instructions say when to use it")

	code := NewServer(nil, "", "p_aaaaaa", "v")
	code.SetMode(ModeCode)
	_, err := call(code, "dashboard", map[string]any{})
	require.Error(t, err, "Claude Code's tool list does not change: it has the slash command instead")
}
