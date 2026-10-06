package sdk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/llm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func rollupProcessor(c llm.Completer) *Processor {
	return &Processor{sem: make(chan struct{}, 1), completers: map[Task]llm.Completer{TaskRollup: c}}
}

func rollupInput() RollupInput {
	return RollupInput{
		Now: briefNow, Name: "shop",
		Observations: []*models.Observation{
			briefObs(12, models.ObsTypeBugfix, "Fix rounding", "Prices were rounded twice.", 3),
			briefObs(40, models.ObsTypeDiscovery, "Cache keys", "The cache key includes the currency.", 9),
		},
	}
}

const goodRollup = "TITLE: Pricing fixes and cache keys\n\n- Prices were rounded twice, fixed in one place [#12].\n- The cache key includes the currency [#40, #999].\n- Contact me@example.com about it.\n"

func TestParseRollup_TitleBodyAndCleaning(t *testing.T) {
	title, body := parseRollup(goodRollup, map[int64]bool{12: true, 40: true})
	assert.Equal(t, "Pricing fixes and cache keys", title)
	assert.Contains(t, body, "rounded twice, fixed in one place [#12].")
	assert.Contains(t, body, "[#40]", "a known citation stays")
	assert.NotContains(t, body, "999", "a citation of a note that was not in the request is removed")
	assert.NotContains(t, body, "me@example.com")
	assert.NotContains(t, body, "TITLE:")

	t.Run("a code fence around the answer", func(t *testing.T) {
		title, body := parseRollup("```markdown\n"+goodRollup+"```", map[int64]bool{12: true})
		assert.Equal(t, "Pricing fixes and cache keys", title)
		assert.NotContains(t, body, "```")
	})
	t.Run("no title line: the whole answer is the body", func(t *testing.T) {
		title, body := parseRollup("- one point [#12] about something that was done.", map[int64]bool{12: true})
		assert.Empty(t, title)
		assert.Contains(t, body, "one point [#12]")
	})
	t.Run("a title with markup, a citation or an address is made plain and short", func(t *testing.T) {
		title, _ := parseRollup("TITLE: **Fixes [#12] for a@b.org** "+strings.Repeat("x", 300)+"\n\n- a point that has enough words in it to be a body.", map[int64]bool{12: true})
		assert.NotContains(t, title, "[#")
		assert.NotContains(t, title, "a@b.org")
		assert.NotContains(t, title, "**")
		assert.LessOrEqual(t, len([]rune(title)), rollupMaxTitleChars+1)
	})
	t.Run("a long body is bounded", func(t *testing.T) {
		_, body := parseRollup("TITLE: t\n\n"+strings.Repeat("- a line of text for the roll-up\n", 400), nil)
		assert.LessOrEqual(t, len(body), rollupMaxBodyChars+4)
	})
}

func TestBuildRollupPrompt(t *testing.T) {
	prompt, err := buildRollupPrompt(rollupInput())
	require.NoError(t, err)
	assert.Contains(t, prompt, "ROLL-UP REQUEST")
	assert.Contains(t, prompt, "PROJECT: shop")
	assert.Contains(t, prompt, "NOTES (2, from 2026-09-03 to 2026-09-09")
	assert.Contains(t, prompt, "[#12] (bugfix, 2026-09-03) Fix rounding")
	assert.Contains(t, prompt, "Prices were rounded twice.")
	assert.Less(t, strings.Index(prompt, "[#12]"), strings.Index(prompt, "[#40]"), "oldest first")

	t.Run("a group that does not fit is refused, not trimmed", func(t *testing.T) {
		in := rollupInput()
		for i := 0; i < 400; i++ {
			in.Observations = append(in.Observations, briefObs(int64(1000+i), models.ObsTypeChange, strings.Repeat("t", 150), strings.Repeat("narrative ", 80), 12))
		}
		_, err := buildRollupPrompt(in)
		assert.ErrorIs(t, err, ErrRollupTooLarge)
	})
}

func TestGenerateRollup(t *testing.T) {
	t.Run("uses its own system prompt and returns the cleaned text", func(t *testing.T) {
		c := &recordingCompleter{out: goodRollup}
		res, err := rollupProcessor(c).GenerateRollup(context.Background(), rollupInput())
		require.NoError(t, err)
		assert.Equal(t, rollupSystemPrompt, c.got.System)
		assert.NotContains(t, c.got.System, "memory extraction agent")
		assert.Contains(t, c.got.Prompt, "ROLL-UP REQUEST")
		assert.Equal(t, "Pricing fixes and cache keys", res.Title)
		assert.NotContains(t, res.Body, "999")
	})
	t.Run("failures leave the caller with an error and nothing to store", func(t *testing.T) {
		_, err := rollupProcessor(&recordingCompleter{out: goodRollup}).GenerateRollup(context.Background(), RollupInput{Now: briefNow, Name: "x"})
		assert.ErrorIs(t, err, ErrNothingToRollUp)

		boom := errors.New("model down")
		_, err = rollupProcessor(&recordingCompleter{err: boom}).GenerateRollup(context.Background(), rollupInput())
		assert.ErrorIs(t, err, boom)

		_, err = rollupProcessor(&recordingCompleter{out: "TITLE: t\n\nok."}).GenerateRollup(context.Background(), rollupInput())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too short")
	})
	t.Run("waits for a free slot and stops when cancelled", func(t *testing.T) {
		p := rollupProcessor(&recordingCompleter{out: goodRollup})
		p.sem <- struct{}{}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := p.GenerateRollup(ctx, rollupInput())
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestRollupTaskIsRoutedLikeTheOthers(t *testing.T) {
	assert.Contains(t, allTasks, TaskRollup)
}
