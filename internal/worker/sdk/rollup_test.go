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
	title, body := parseRollup(goodRollup, map[int64]bool{12: true, 40: true}, rollupMaxBodyChars)
	assert.Equal(t, "Pricing fixes and cache keys", title)
	assert.Contains(t, body, "rounded twice, fixed in one place [#12].")
	assert.Contains(t, body, "[#40]", "a known citation stays")
	assert.NotContains(t, body, "999", "a citation of a note that was not in the request is removed")
	assert.NotContains(t, body, "me@example.com")
	assert.NotContains(t, body, "TITLE:")

	t.Run("a code fence around the answer", func(t *testing.T) {
		title, body := parseRollup("```markdown\n"+goodRollup+"```", map[int64]bool{12: true}, rollupMaxBodyChars)
		assert.Equal(t, "Pricing fixes and cache keys", title)
		assert.NotContains(t, body, "```")
	})
	t.Run("no title line: the whole answer is the body", func(t *testing.T) {
		title, body := parseRollup("- one point [#12] about something that was done.", map[int64]bool{12: true}, rollupMaxBodyChars)
		assert.Empty(t, title)
		assert.Contains(t, body, "one point [#12]")
	})
	t.Run("a title with markup, a citation or an address is made plain and short", func(t *testing.T) {
		title, _ := parseRollup("TITLE: **Fixes [#12] for a@b.org** "+strings.Repeat("x", 300)+"\n\n- a point that has enough words in it to be a body.", map[int64]bool{12: true}, rollupMaxBodyChars)
		assert.NotContains(t, title, "[#")
		assert.NotContains(t, title, "a@b.org")
		assert.NotContains(t, title, "**")
		assert.LessOrEqual(t, len([]rune(title)), rollupMaxTitleChars+1)
	})
	t.Run("a long body is bounded", func(t *testing.T) {
		_, body := parseRollup("TITLE: t\n\n"+strings.Repeat("- a line of text for the roll-up\n", 400), nil, rollupMaxBodyChars)
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

func TestQuarterLevelUsesItsOwnPromptAndLimits(t *testing.T) {
	in := rollupInput()
	in.Level, in.Period = LevelQuarter, "2026-Q1"
	prompt, err := buildRollupPrompt(in)
	require.NoError(t, err)
	assert.Contains(t, prompt, "ROLL-UP REQUEST", "the same request marker, so a backend that routes on it still answers")
	assert.Contains(t, prompt, "LEVEL: quarter")
	assert.Contains(t, prompt, "PERIOD: 2026-Q1")
	assert.Contains(t, prompt, "MONTHLY ROLL-UPS (2,")
	assert.NotContains(t, prompt, "NOTES (2,")

	month := rollupInput()
	month.Period = "2026-03"
	monthPrompt, err := buildRollupPrompt(month)
	require.NoError(t, err)
	assert.NotContains(t, monthPrompt, "LEVEL: quarter")
	assert.Contains(t, monthPrompt, "PERIOD: 2026-03")
	assert.Contains(t, monthPrompt, "NOTES (2,")

	t.Run("a monthly roll-up is shown in more detail than a raw note", func(t *testing.T) {
		long := strings.Repeat("alpha beta gamma delta ", 80) // about 1800 characters
		qi := RollupInput{Level: LevelQuarter, Now: briefNow, Name: "shop", Observations: []*models.Observation{briefObs(1, models.ObsTypeDiscovery, "March", long, 3)}}
		mi := RollupInput{Now: briefNow, Name: "shop", Observations: []*models.Observation{briefObs(1, models.ObsTypeDiscovery, "March", long, 3)}}
		qp, _ := buildRollupPrompt(qi)
		mp, _ := buildRollupPrompt(mi)
		assert.Greater(t, len(qp), len(mp)+500)
	})

	t.Run("the generator sends the quarter system prompt and keeps a longer body", func(t *testing.T) {
		c := &recordingCompleter{out: "TITLE: The first quarter\n\n" + strings.Repeat("- a point about the quarter [#12]\n", 80)}
		res, err := rollupProcessor(c).GenerateRollup(context.Background(), in)
		require.NoError(t, err)
		assert.Equal(t, quarterSystemPrompt, c.got.System)
		assert.Contains(t, c.got.System, "final record")
		assert.Greater(t, len(res.Body), rollupMaxBodyChars-200, "a quarter's body may be longer than a month's")
		assert.LessOrEqual(t, len(res.Body), quarterMaxBodyChars+4)
	})
	assert.Equal(t, rollupSystemPrompt, systemPromptFor(""))
	assert.Equal(t, rollupSystemPrompt, systemPromptFor(LevelMonth))
}
