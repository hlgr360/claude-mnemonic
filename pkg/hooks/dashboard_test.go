package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardURL_FollowsThePort(t *testing.T) {
	assert.Equal(t, "http://localhost:37777", DashboardURL(DefaultWorkerPort))
	assert.Equal(t, "http://localhost:4100", DashboardURL(4100))
}

func TestDashboardNotice_SaysItOncePerVersionAndNamesThePortAndTheCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data") // does not exist yet

	first := DashboardNotice(dir, "v1.0.0", 37777)
	assert.Contains(t, first, "http://localhost:37777")
	assert.Contains(t, first, "/claude-mnemonic:dashboard")
	assert.NotContains(t, first, "\n", "one line")

	assert.Empty(t, DashboardNotice(dir, "v1.0.0", 37777), "the same version does not say it again")
	assert.Empty(t, DashboardNotice(dir, "v1.0.0", 37777))

	after := DashboardNotice(dir, "v1.1.0", 4100)
	assert.Contains(t, after, "http://localhost:4100", "an update says it again, with the port in use")
	assert.Empty(t, DashboardNotice(dir, "v1.1.0", 4100))

	seen, err := os.ReadFile(filepath.Join(dir, ".dashboard-announced"))
	require.NoError(t, err)
	assert.Equal(t, "v1.1.0\n", string(seen), "the marker holds the version that said it")
}

func TestDashboardNotice_AMarkerThatCannotBeWrittenMeansItIsSaidAgainNotAnError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	// the data directory is a file: nothing can be written inside it
	msg := DashboardNotice(file, "v1", 37777)
	assert.Contains(t, msg, "http://localhost:37777")
	assert.Contains(t, DashboardNotice(file, "v1", 37777), "http://localhost:37777", "harmless: it may be repeated")
}

func TestDataDir_IsTheWorkersDirectory(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	assert.Equal(t, filepath.Join("/home/someone", ".claude-mnemonic"), DataDir())
}

func TestBuildHookResponse_UserMessageAndModelContextStaySeparate(t *testing.T) {
	assert.Nil(t, buildHookResponse("SessionStart", "", ""), "nothing to say: the plain response is printed")

	onlyMessage := buildHookResponse("SessionStart", "", "dashboard is at http://localhost:37777")
	assert.Equal(t, true, onlyMessage["continue"])
	assert.Equal(t, "dashboard is at http://localhost:37777", onlyMessage["systemMessage"])
	assert.NotContains(t, onlyMessage, "hookSpecificOutput", "a message alone adds nothing to the model's context")

	onlyContext := buildHookResponse("SessionStart", "<claude-mnemonic-context>", "")
	assert.NotContains(t, onlyContext, "systemMessage")
	assert.Equal(t, map[string]interface{}{"hookEventName": "SessionStart", "additionalContext": "<claude-mnemonic-context>"}, onlyContext["hookSpecificOutput"])

	both := buildHookResponse("SessionStart", "<claude-mnemonic-context>", "dashboard is at http://localhost:37777")
	assert.Equal(t, "dashboard is at http://localhost:37777", both["systemMessage"])
	ctx := both["hookSpecificOutput"].(map[string]interface{})["additionalContext"].(string)
	assert.NotContains(t, ctx, "localhost", "the user's message is not in the model's context")
}
