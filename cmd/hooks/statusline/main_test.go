package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const osc8Close = "\033]8;;\a"

func readyStats() *WorkerStats {
	s := &WorkerStats{Ready: true, ProjectObservations: 28}
	s.Retrieval.ObservationsServed = 42
	return s
}

func TestHyperlink_IsTheOSC8FormClaudeCodeDocuments(t *testing.T) {
	assert.Equal(t, "\033]8;;http://localhost:37777\a[mnemonic]\033]8;;\a", hyperlink("http://localhost:37777", "[mnemonic]"))
	assert.Equal(t, "[mnemonic]", hyperlink("", "[mnemonic]"), "without an address the text is left alone")
}

func TestDashboardURL_FollowsThePortAndFallsBackToTheDefault(t *testing.T) {
	assert.Equal(t, "http://localhost:4100", dashboardURL(4100))
	assert.Equal(t, "http://localhost:37777", dashboardURL(0))
}

func TestTheNameTagLinksToTheDashboardWhileTheWorkerIsReady(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_LINK", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_FORMAT", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_COLORS", "true")

	out := formatStatusLine(readyStats(), StatusInput{}, 4100)
	assert.Contains(t, out, "\033]8;;http://localhost:4100\a[mnemonic]"+osc8Close, "the tag is the link, on the configured port")
	assert.Contains(t, out, "project:28 memories")
	assert.Equal(t, 1, strings.Count(out, "\033]8;;http"), "only the tag is a link")
}

func TestTheLinkDoesNotDependOnColours(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_LINK", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_FORMAT", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_COLORS", "false")
	out := formatStatusLine(readyStats(), StatusInput{}, 37777)
	assert.Contains(t, out, "\033]8;;http://localhost:37777\a[mnemonic]"+osc8Close)
	assert.NotContains(t, out, "\033[", "no colour codes")
}

func TestTheCompactTagLinksToo(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_LINK", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_FORMAT", "compact")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_COLORS", "false")
	assert.Contains(t, formatStatusLine(readyStats(), StatusInput{}, 37777), "\033]8;;http://localhost:37777\a[m]"+osc8Close)
}

func TestTheMinimalFormatHasNoTagToLink(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_LINK", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_FORMAT", "minimal")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_COLORS", "false")
	assert.NotContains(t, formatStatusLine(readyStats(), StatusInput{}, 37777), "\033]8;")
}

func TestTheLinkCanBeSwitchedOffAndADumbTerminalNeverGetsOne(t *testing.T) {
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_FORMAT", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_COLORS", "false")

	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_LINK", "false")
	off := formatStatusLine(readyStats(), StatusInput{}, 37777)
	assert.NotContains(t, off, "\033]8;")
	assert.Contains(t, off, "[mnemonic] ●", "the plain tag is unchanged")

	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_LINK", "")
	t.Setenv("TERM", "dumb")
	assert.NotContains(t, formatStatusLine(readyStats(), StatusInput{}, 37777), "\033]8;")
}

func TestOfflineAndStartingAreNotLinks(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_LINK", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_FORMAT", "")
	t.Setenv("CLAUDE_MNEMONIC_STATUSLINE_COLORS", "false")
	// The dashboard does not answer a worker that is offline or still starting.
	assert.Equal(t, "[mnemonic] ○ offline", formatStatusLine(nil, StatusInput{}, 37777))
	assert.Equal(t, "[mnemonic] ○ starting...", formatStatusLine(&WorkerStats{Ready: false}, StatusInput{}, 37777))
}
