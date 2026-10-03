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

func conflictProcessor(c llm.Completer) *Processor {
	return &Processor{sem: make(chan struct{}, 1), completers: map[Task]llm.Completer{TaskConflict: c}}
}

func newerAndOlders() (*models.Observation, []*models.Observation) {
	newer := briefObs(530, models.ObsTypeDecision, "Issues enabled", "The GitHub issues were enabled on the fork.", 25)
	olders := []*models.Observation{
		briefObs(412, models.ObsTypeDiscovery, "Issues disabled", "Issues are disabled on the public fork.", 20),
		briefObs(413, models.ObsTypeDiscovery, "Unrelated thing", "The Dockerfile uses a distroless base.", 21),
	}
	return newer, olders
}

func TestBuildConflictPrompt_HasTheMarkerTheNewerNoteAndTheOlderOnes(t *testing.T) {
	newer, olders := newerAndOlders()
	prompt := buildConflictPrompt(newer, olders)

	assert.True(t, strings.HasPrefix(prompt, "CONFLICT CHECK REQUEST\n"))
	assert.Contains(t, prompt, "NEWER NOTE:\n[#530] saved 2026-09-25, decision\n  Title: Issues enabled")
	assert.Contains(t, prompt, "OLDER NOTES:\n[#412] saved 2026-09-20")
	assert.Contains(t, prompt, "[#413]")
	assert.Less(t, strings.Index(prompt, "[#530]"), strings.Index(prompt, "[#412]"))
	assert.Contains(t, prompt, "Details: Issues are disabled on the public fork.")
}

func TestBuildConflictPrompt_LongDetailsAreCutOnOneLine(t *testing.T) {
	newer, olders := newerAndOlders()
	newer.Narrative.String = "first line\n\nsecond line " + strings.Repeat("é", 2000)
	prompt := buildConflictPrompt(newer, olders)
	assert.Contains(t, prompt, "first line second line")
	assert.Equal(t, conflictNarrativeChars-len("first line second line "), strings.Count(prompt, "é"))
}

func TestConflictSystemPrompt_IsTheStrictOne(t *testing.T) {
	assert.Contains(t, conflictSystemPrompt, "Use supersedes ONLY if all three hold")
	assert.Contains(t, conflictSystemPrompt, "NEWER fixes a problem that the older note described")
	assert.Contains(t, conflictSystemPrompt, "When unsure, choose related")
	assert.NotContains(t, conflictSystemPrompt, "memory extraction agent")
}

func TestParseConflictVerdicts(t *testing.T) {
	valid := map[int64]bool{412: true, 413: true}

	t.Run("a clean answer", func(t *testing.T) {
		got := parseConflictVerdicts(`[{"older_id": 412, "relation": "supersedes", "confidence": "high", "reason": "Issues were disabled, now enabled."},
			{"older_id": 413, "relation": "unrelated", "confidence": "medium", "reason": "Different thing."}]`, valid)
		require.Len(t, got, 2)
		assert.Equal(t, ConflictVerdict{OlderID: 412, Relation: "supersedes", Confidence: "high", Reason: "Issues were disabled, now enabled."}, got[0])
		assert.True(t, got[0].Proposable())
		assert.False(t, got[1].Proposable(), "unrelated is not a proposal")
	})
	t.Run("text and a code fence around the array", func(t *testing.T) {
		got := parseConflictVerdicts("Here you go:\n```json\n[{\"older_id\": 412, \"relation\": \"Contradicts\", \"confidence\": \"HIGH\", \"reason\": \"x\"}]\n```", valid)
		require.Len(t, got, 1)
		assert.Equal(t, "contradicts", got[0].Relation, "case does not matter")
		assert.Equal(t, "high", got[0].Confidence)
	})
	t.Run("unknown notes, relations and repeats are dropped", func(t *testing.T) {
		got := parseConflictVerdicts(`[{"older_id": 999, "relation": "supersedes"},
			{"older_id": 412, "relation": "replaces everything"},
			{"older_id": 413, "relation": "duplicate", "confidence": "high", "reason": "same"},
			{"older_id": 413, "relation": "supersedes", "confidence": "high", "reason": "again"}]`, valid)
		require.Len(t, got, 1)
		assert.Equal(t, int64(413), got[0].OlderID)
		assert.Equal(t, "duplicate", got[0].Relation, "the first answer about a note counts")
	})
	t.Run("the confidence defaults to low", func(t *testing.T) {
		got := parseConflictVerdicts(`[{"older_id": 412, "relation": "supersedes", "confidence": "certain"},{"older_id": 413, "relation": "related"}]`, valid)
		require.Len(t, got, 2)
		assert.Equal(t, "low", got[0].Confidence)
		assert.Equal(t, "low", got[1].Confidence)
	})
	t.Run("the reason is cleaned and bounded", func(t *testing.T) {
		got := parseConflictVerdicts(`[{"older_id": 412, "relation": "supersedes", "confidence": "low", "reason": "Ask me@example.com about <private>the secret</private> `+strings.Repeat("long ", 200)+`"}]`, valid)
		require.Len(t, got, 1)
		assert.NotContains(t, got[0].Reason, "me@example.com")
		assert.Contains(t, got[0].Reason, "[email removed]")
		assert.NotContains(t, got[0].Reason, "secret")
		assert.LessOrEqual(t, len([]rune(got[0].Reason)), conflictReasonChars+1)
	})
	t.Run("a quoted id is accepted, a float one is not", func(t *testing.T) {
		got := parseConflictVerdicts(`[{"older_id": "412", "relation": "supersedes"}]`, valid)
		require.Len(t, got, 1, "models often quote numbers; the id must still be one of the candidates")
		assert.Equal(t, int64(412), got[0].OlderID)
		got = parseConflictVerdicts(`[{"older_id": 412.0, "relation": "supersedes"}]`, valid)
		assert.Empty(t, got, "a float id is not an id: dropped, not guessed")
	})
	t.Run("garbage", func(t *testing.T) {
		for _, raw := range []string{"", "no json here", "[not json]", `{"older_id": 412}`, "[]"} {
			assert.Empty(t, parseConflictVerdicts(raw, valid), raw)
		}
	})
}

func TestProposeConflicts_AsksOnceWithItsOwnSystemPromptAndReturnsEveryVerdict(t *testing.T) {
	c := &recordingCompleter{out: `[{"older_id": 412, "relation": "supersedes", "confidence": "high", "reason": "now enabled"},
		{"older_id": 413, "relation": "unrelated", "confidence": "high", "reason": "other"}]`}
	newer, olders := newerAndOlders()

	got, err := conflictProcessor(c).ProposeConflicts(context.Background(), newer, olders)
	require.NoError(t, err)

	assert.Equal(t, 1, c.used, "one request for all candidates")
	assert.Equal(t, conflictSystemPrompt, c.got.System)
	assert.True(t, strings.HasPrefix(c.got.Prompt, "CONFLICT CHECK REQUEST"))
	require.Len(t, got, 2)
	assert.True(t, got[0].Proposable())
	assert.False(t, got[1].Proposable())
}

func TestProposeConflicts_NothingToCompareMeansNoRequest(t *testing.T) {
	c := &recordingCompleter{out: "[]"}
	newer, _ := newerAndOlders()
	got, err := conflictProcessor(c).ProposeConflicts(context.Background(), newer, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Zero(t, c.used, "no model call without candidates")

	got, err = conflictProcessor(c).ProposeConflicts(context.Background(), nil, []*models.Observation{newer})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Zero(t, c.used)
}

func TestProposeConflicts_OnlyTheFirstCandidatesAreAsked(t *testing.T) {
	var olders []*models.Observation
	for i := 1; i <= 9; i++ {
		olders = append(olders, briefObs(int64(100+i), models.ObsTypeDiscovery, "old", "text", 1))
	}
	c := &recordingCompleter{out: `[{"older_id": 101, "relation": "related"},{"older_id": 108, "relation": "supersedes"}]`}
	newer, _ := newerAndOlders()

	got, err := conflictProcessor(c).ProposeConflicts(context.Background(), newer, olders)
	require.NoError(t, err)
	assert.Equal(t, maxConflictCandidates, strings.Count(c.got.Prompt, "\n[#1")-0, "six older notes were sent")
	require.Len(t, got, 1, "an answer about a note that was not sent is dropped")
	assert.Equal(t, int64(101), got[0].OlderID)
}

func TestProposeConflicts_Failures(t *testing.T) {
	newer, olders := newerAndOlders()
	boom := errors.New("model down")
	_, err := conflictProcessor(&recordingCompleter{err: boom}).ProposeConflicts(context.Background(), newer, olders)
	assert.ErrorIs(t, err, boom)

	_, err = conflictProcessor(&recordingCompleter{out: "I cannot decide."}).ProposeConflicts(context.Background(), newer, olders)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable verdict", "an answer that cannot be read is an error, so the note is not marked as checked")

	p := conflictProcessor(&recordingCompleter{out: "[]"})
	p.sem <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = p.ProposeConflicts(ctx, newer, olders)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
