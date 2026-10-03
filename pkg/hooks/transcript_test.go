package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTranscript(t *testing.T, lines ...any) string {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		raw, err := json.Marshal(l)
		require.NoError(t, err)
		b.Write(raw)
		b.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

func entry(typ string, content any) map[string]any {
	return map[string]any{"type": typ, "message": map[string]any{"role": typ, "content": content}}
}

func TestReadConversation_KeepsOnlyTheTextOfUserAndAssistantTurns(t *testing.T) {
	path := writeTranscript(t,
		map[string]any{"type": "summary", "summary": "not a turn"},
		entry("user", "Let's design the overlay"),
		entry("assistant", []any{
			map[string]any{"type": "text", "text": "Plan: names instead of ids."},
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ls"}},
		}),
		entry("user", []any{map[string]any{"type": "tool_result", "content": "file list"}}),
		entry("assistant", []any{map[string]any{"type": "tool_use", "name": "Read"}}),
		entry("user", []any{map[string]any{"type": "text", "text": "  Go ahead  "}}),
	)

	got := ReadConversation(path)
	assert.Equal(t, []Message{
		{Role: "user", Text: "Let's design the overlay"},
		{Role: "assistant", Text: "Plan: names instead of ids."},
		{Role: "user", Text: "Go ahead"},
	}, got, "tool calls, tool results and non-turn lines carry no text")
}

func TestReadConversation_SurvivesMissingEmptyAndCorruptFiles(t *testing.T) {
	assert.Empty(t, ReadConversation(filepath.Join(t.TempDir(), "missing.jsonl")))
	assert.Empty(t, ReadConversation(""))

	path := filepath.Join(t.TempDir(), "bad.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("not json\n{\"type\":\"user\",\"message\":{\"content\":\"kept\"}}\n{broken"), 0o600))
	assert.Equal(t, []Message{{Role: "user", Text: "kept"}}, ReadConversation(path))
}

func TestReadConversation_OnlyReadsTheTailOfAHugeTranscript(t *testing.T) {
	filler := strings.Repeat("x", 4000)
	var lines []any
	lines = append(lines, entry("user", "the very first message"))
	for i := 0; i < 700; i++ { // about 2.8 MB, more than the tail that is read
		lines = append(lines, entry("assistant", filler))
	}
	lines = append(lines, entry("user", "the last message"))
	got := ReadConversation(writeTranscript(t, lines...))

	require.NotEmpty(t, got)
	assert.Equal(t, "the last message", got[len(got)-1].Text)
	assert.NotEqual(t, "the very first message", got[0].Text, "the start of a huge transcript is not read")
	assert.Less(t, len(got), 702)
}

func TestLastOf(t *testing.T) {
	msgs := []Message{{"user", "a"}, {"assistant", "b"}, {"user", "c"}}
	assert.Equal(t, "c", LastOf(msgs, "user"))
	assert.Equal(t, "b", LastOf(msgs, "assistant"))
	assert.Equal(t, "", LastOf(nil, "user"))
	assert.Equal(t, "", LastOf(msgs, "system"))
}

func TestExcerpt_IsOldestFirstAndLabelled(t *testing.T) {
	got := Excerpt([]Message{{"user", "first question"}, {"assistant", "first answer"}, {"user", "second question"}}, 10000)
	assert.Equal(t, "User: first question\n\nAssistant: first answer\n\nUser: second question", got)
}

func TestExcerpt_KeepsTheNewestMessagesThatFitAndSaysSo(t *testing.T) {
	msgs := []Message{{"user", "oldest " + strings.Repeat("a", 100)}, {"assistant", "middle " + strings.Repeat("b", 100)}, {"user", "newest " + strings.Repeat("c", 100)}}
	got := Excerpt(msgs, 260)
	assert.True(t, strings.HasPrefix(got, "(earlier messages left out)"))
	assert.Contains(t, got, "newest")
	assert.Contains(t, got, "middle")
	assert.NotContains(t, got, "oldest")
	assert.Less(t, strings.Index(got, "middle"), strings.Index(got, "newest"), "still oldest first")
}

func TestExcerpt_CutsLongMessagesWithoutBreakingCharacters(t *testing.T) {
	long := strings.Repeat("é", excerptMessageChars+50)
	got := Excerpt([]Message{{"assistant", long}}, 100000)
	assert.True(t, strings.HasSuffix(got, " …"))
	assert.Equal(t, excerptMessageChars, strings.Count(got, "é"))
	assert.True(t, utf8Valid(got))
}

func TestExcerpt_NothingToSayGivesNothing(t *testing.T) {
	assert.Equal(t, "", Excerpt(nil, 1000))
	assert.Equal(t, "", Excerpt([]Message{{"user", strings.Repeat("a", 500)}}, 10), "a budget too small for any message gives an empty excerpt, not a stray note")
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }
