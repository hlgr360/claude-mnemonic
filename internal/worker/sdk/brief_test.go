package sdk

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/llm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

var briefNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func briefObs(id int64, typ models.ObservationType, title, narrative string, day int) *models.Observation {
	return &models.Observation{
		ID: id, Type: typ,
		Title:          sql.NullString{String: title, Valid: true},
		Subtitle:       sql.NullString{String: "subtitle " + title, Valid: true},
		Narrative:      sql.NullString{String: narrative, Valid: true},
		CreatedAtEpoch: time.Date(2026, 9, day, 10, 0, 0, 0, time.UTC).UnixMilli(),
	}
}

func briefThread(name, goal, progress, decisions, next string, day int) *models.SessionSummary {
	ns := func(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
	return &models.SessionSummary{
		Request: ns(name), Notes: ns(goal), Completed: ns(progress), Learned: ns(decisions), NextSteps: ns(next),
		CreatedAtEpoch: time.Date(2026, 10, day, 9, 0, 0, 0, time.UTC).UnixMilli(),
	}
}

func baseInput() BriefInput {
	return BriefInput{
		Now: briefNow, Name: "claude-mnemonic", Total: 3,
		Observations: []*models.Observation{
			briefObs(12, models.ObsTypeDecision, "Use names", "Projects are addressed by name because ids are cumbersome.", 20),
			briefObs(40, models.ObsTypeBugfix, "Fix race", "Added a mutex around the client state.", 25),
		},
	}
}

func TestBuildBriefPrompt_HasTheMarkerDatesAndObservations(t *testing.T) {
	prompt, used := buildBriefPrompt(baseInput())

	assert.True(t, strings.HasPrefix(prompt, "PROJECT BRIEF REQUEST\n"))
	assert.Contains(t, prompt, "AS OF: 2026-10-03")
	assert.Contains(t, prompt, "PROJECT: claude-mnemonic")
	assert.Contains(t, prompt, "OBSERVATIONS (2 of 3, oldest first):")
	assert.Contains(t, prompt, "[#12] (decision, 2026-09-20) Use names")
	assert.Contains(t, prompt, "[#40] (bugfix, 2026-09-25) Fix race")
	assert.Less(t, strings.Index(prompt, "[#12]"), strings.Index(prompt, "[#40]"), "oldest first")
	assert.NotContains(t, prompt, "CURRENT WORK", "no checkpoint notes, no such section")
	assert.Len(t, used, 2)
}

func TestBuildBriefPrompt_CheckpointNotesAreContextButTheirNextStepsAreNot(t *testing.T) {
	in := baseInput()
	in.Threads = []*models.SessionSummary{briefThread("Overlay design", "ship it", "store done", "names, not ids", "write the docs", 3)}
	prompt, _ := buildBriefPrompt(in)

	assert.Contains(t, prompt, "CURRENT WORK")
	assert.Contains(t, prompt, "- Overlay design (updated 2026-10-03); goal: ship it; progress: store done; decisions: names, not ids")
	assert.NotContains(t, prompt, "write the docs", "open items are added from the notes afterwards, the model does not write them")
}

func TestBuildBriefPrompt_LongTextIsCutOnOneLineAndWithoutBreakingCharacters(t *testing.T) {
	in := baseInput()
	in.Observations[0].Narrative.String = "line one\n\nline two " + strings.Repeat("é", 1000)
	prompt, _ := buildBriefPrompt(in)

	assert.Contains(t, prompt, "line one line two", "whitespace is collapsed")
	assert.Equal(t, briefNarrativeChars-len("line one line two "), strings.Count(prompt, "é"))
	assert.Contains(t, prompt, "…")
}

func TestBuildBriefPrompt_TooMuchDropsTheOldestObservationsAndSaysHowManyWereUsed(t *testing.T) {
	var all []*models.Observation
	for i := 1; i <= 400; i++ {
		all = append(all, briefObs(int64(i), models.ObsTypeDiscovery, "title", strings.Repeat("word ", 120), 1+i%27))
	}
	in := BriefInput{Now: briefNow, Name: "big", Total: 400, Observations: all}
	prompt, used := buildBriefPrompt(in)

	assert.LessOrEqual(t, len(prompt)+len(briefSystemPrompt), briefMaxPromptBytes)
	assert.Less(t, len(used), 400)
	assert.Equal(t, int64(400), used[len(used)-1].ID, "the newest are kept")
	assert.Contains(t, prompt, "OBSERVATIONS ("+strconv.Itoa(len(used))+" of 400")
	assert.Less(t, len(prompt)+len(briefSystemPrompt), MaxPromptSize)
}

func TestCleanBriefBody(t *testing.T) {
	known := map[int64]bool{12: true, 40: true}
	cases := []struct{ name, in, want string }{
		{"a code fence around the whole answer is removed", "```markdown\n## What this is\nA tool [#12].\n```", "## What this is\nA tool [#12]."},
		{"citations of unknown observations go, with the space before them", "Uses names [#12] and a port [#999].", "Uses names [#12] and a port."},
		{"a list keeps only the known ones", "Both [#12, #77, #40].", "Both [#12, #40]."},
		{"a list of only unknown ones disappears", "Nothing [#77, #78] here.", "Nothing here."},
		{"email addresses are removed", "Contact me@example.com or x.y+z@mail.example.org.", "Contact [email removed] or [email removed]."},
		{"private text is removed", "Keep <private>the secret</private> out.", "Keep  out."},
		{"an open items section is removed up to the next section", "## Current state\nok\n\n## Open items\n- do a thing\n- do another\n\n## Conventions and gotchas\nbe careful", "## Current state\nok\n\n## Conventions and gotchas\nbe careful"},
		{"to-do and next steps sections too, at the end", "## What this is\nx\n## To-do\n- y\n## Next steps\n- z", "## What this is\nx"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, cleanBriefBody(c.in, known), c.name)
	}
}

func TestCleanBriefBody_LengthIsBoundedAtALineWithoutBreakingCharacters(t *testing.T) {
	var lines []string
	for i := 0; i < 400; i++ {
		lines = append(lines, "- a line with an é and more words to fill the space")
	}
	got := cleanBriefBody(strings.Join(lines, "\n"), nil)

	assert.LessOrEqual(t, len(got), briefMaxBodyChars+len("\n…"))
	assert.True(t, strings.HasSuffix(got, "\n…"))
	assert.True(t, strings.ToValidUTF8(got, "�") == got)
	assert.False(t, strings.HasSuffix(strings.TrimSuffix(got, "\n…"), "an"), "cut at the end of a line")
}

func TestAssembleBrief_HeaderSourceAndOpenThreads(t *testing.T) {
	in := baseInput()
	in.Threads = []*models.SessionSummary{
		briefThread("Overlay design", "g", "p", "d", "write the docs", 3),
		briefThread("Finished thread", "g", "p", "d", "", 2),
		briefThread("Compaction recovery", "g", "p", "d", "ship the tools", 1),
	}
	text, source := assembleBrief(in, 2, "## What this is\nA tool.")

	assert.Equal(t, "2 of 3 observations and 3 checkpoint notes", source)
	assert.True(t, strings.HasPrefix(text, "As of 2026-10-03, written from 2 of 3 observations and 3 checkpoint notes. It can lag behind recent work.\n\n## What this is\nA tool."), text)
	assert.Contains(t, text, "\n\n## Open threads (from your checkpoint notes)\n- Overlay design (updated 2026-10-03): write the docs\n- Compaction recovery (updated 2026-10-01): ship the tools")
	assert.NotContains(t, text, "Finished thread", "a thread with nothing open is not listed")
}

func TestAssembleBrief_NoThreadsMeansNoOpenSection(t *testing.T) {
	text, source := assembleBrief(baseInput(), 2, "body text")
	assert.Equal(t, "2 of 3 observations", source)
	assert.NotContains(t, text, "Open threads")

	in := baseInput()
	in.Threads = []*models.SessionSummary{briefThread("One", "g", "p", "d", "", 3)}
	text, source = assembleBrief(in, 2, "body text")
	assert.Equal(t, "2 of 3 observations and 1 checkpoint note", source)
	assert.NotContains(t, text, "Open threads", "a note with nothing open adds no section")
}

func TestAssembleBrief_OpenThreadsAreBounded(t *testing.T) {
	in := baseInput()
	for i := 0; i < 12; i++ {
		in.Threads = append(in.Threads, briefThread("Thread", "g", "p", "d", "next", 3))
	}
	text, _ := assembleBrief(in, 2, "body text")
	assert.Equal(t, briefMaxOpenThreads, strings.Count(text, "\n- Thread"))
}

type recordingCompleter struct {
	err  error
	out  string
	got  llm.Request
	used int
}

func (r *recordingCompleter) Complete(_ context.Context, req llm.Request) (string, error) {
	r.got, r.used = req, r.used+1
	return r.out, r.err
}
func (r *recordingCompleter) Name() string { return "recording" }

func briefProcessor(c llm.Completer) *Processor {
	return &Processor{sem: make(chan struct{}, 1), completers: map[Task]llm.Completer{TaskBrief: c}}
}

const goodBody = "## What this is\nA memory plugin [#12] with a fix [#999].\n\n## Current state\nStable [#40].\n\n## Open items\n- invented thing\n"

func TestGenerateBrief_UsesItsOwnSystemPromptCleansTheAnswerAndAddsTheHeader(t *testing.T) {
	c := &recordingCompleter{out: goodBody}
	in := baseInput()
	in.Threads = []*models.SessionSummary{briefThread("Overlay", "g", "p", "d", "write the docs", 3)}

	res, err := briefProcessor(c).GenerateBrief(context.Background(), in)
	require.NoError(t, err)

	assert.Equal(t, briefSystemPrompt, c.got.System)
	assert.NotContains(t, c.got.System, "memory extraction agent", "not the extraction prompt of the other tasks")
	assert.Contains(t, c.got.Prompt, "PROJECT BRIEF REQUEST")
	assert.Equal(t, "2 of 3 observations and 1 checkpoint note", res.Source)
	assert.True(t, strings.HasPrefix(res.Text, "As of 2026-10-03, written from 2 of 3 observations and 1 checkpoint note."))
	assert.Contains(t, res.Text, "A memory plugin [#12] with a fix.", "the unknown citation is gone, the known one stays")
	assert.NotContains(t, res.Text, "invented thing", "the model's own open items are dropped")
	assert.Contains(t, res.Text, "## Open threads (from your checkpoint notes)\n- Overlay (updated 2026-10-03): write the docs")
}

func TestGenerateBrief_Failures(t *testing.T) {
	_, err := briefProcessor(&recordingCompleter{out: goodBody}).GenerateBrief(context.Background(), BriefInput{Now: briefNow, Name: "empty"})
	assert.ErrorIs(t, err, ErrNothingToBrief)

	boom := errors.New("model down")
	_, err = briefProcessor(&recordingCompleter{err: boom}).GenerateBrief(context.Background(), baseInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)

	_, err = briefProcessor(&recordingCompleter{out: "ok."}).GenerateBrief(context.Background(), baseInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too short", "an answer that is nearly empty is not stored")

	// the open-items-only answer would leave nothing once those are removed
	_, err = briefProcessor(&recordingCompleter{out: "## Open items\n- a\n- b\n"}).GenerateBrief(context.Background(), baseInput())
	assert.Error(t, err)
}

func TestGenerateBrief_WaitsForAFreeSlotAndStopsWhenCancelled(t *testing.T) {
	p := briefProcessor(&recordingCompleter{out: goodBody})
	p.sem <- struct{}{} // the one slot is taken
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.GenerateBrief(ctx, baseInput())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestGenerateBrief_ThroughTheClaudeCLIItSendsTheBriefSystemPromptNotTheExtractionOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\nprintf '%s' \"$last\" > " + log + "\ncat <<'EOF'\n" + goodBody + "EOF\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755)) // #nosec G306 -- test script

	p := &Processor{claudePath: script, sem: make(chan struct{}, 1)}
	res, err := p.GenerateBrief(context.Background(), baseInput())
	require.NoError(t, err)
	assert.Contains(t, res.Text, "A memory plugin [#12]")

	sent, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Contains(t, string(sent), "You maintain a short project brief")
	assert.NotContains(t, string(sent), "memory extraction agent")
	assert.Contains(t, string(sent), "PROJECT BRIEF REQUEST")
}
