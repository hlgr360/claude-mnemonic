package sdk

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lukaszraczylo/claude-mnemonic/internal/privacy"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	// briefMaxPromptBytes keeps the whole request below MaxPromptSize with room to spare.
	briefMaxPromptBytes = 90 * 1024
	// briefNarrativeChars bounds how much of an observation's text goes into the prompt.
	briefNarrativeChars = 420
	briefTitleChars     = 160
	briefThreadChars    = 280
	// briefMaxBodyChars bounds what the model's text may add to a stored brief.
	briefMaxBodyChars = 3600
	briefMinBodyChars = 80
	// briefMaxOpenThreads bounds the open-threads list that is added from checkpoint notes.
	briefMaxOpenThreads = 8
)

// briefSystemPrompt is deliberately not the memory extraction prompt of the other tasks.
const briefSystemPrompt = `You maintain a short project brief inside a developer's memory system. You get notes (observations) that were saved automatically from coding sessions about ONE project, oldest first, each with an id and a date, and sometimes the developer's own notes about current work. Write the brief that an assistant starting a fresh conversation about this project should read first.

Rules:
- Use only what the notes say. Never add facts, file names, ports, versions or numbers that they do not contain.
- Cite the notes behind each point like [#12] or [#12, #40].
- Notes are point-in-time: prefer recent ones. When a later note changes or undoes an earlier one, keep only the later state and say that it changed. Leave out one-off incidents (a slow test run, a flaky failure) unless a later note shows they are a standing problem.
- If a point rests only on notes that are more than 14 days older than the AS OF date, say so in a few words.
- Never include personal data (email addresses, people's names, credentials) or anything about other projects.
- No filler and no praise. At most 300 words.
- Write exactly these markdown sections in this order: "## What this is", "## Current state", "## Key decisions (and why)", "## Conventions and gotchas". Do not write open items or a to-do list: they are added separately from the developer's own notes.
- Reply with the brief only.`

// BriefInput is what a brief is written from.
type BriefInput struct {
	Now time.Time
	// Name is the project's name without its hash.
	Name string
	// Observations are the project's live observations, oldest first (at most as many as should be used).
	Observations []*models.Observation
	// Threads are the developer's checkpoint notes, most recently worked on first.
	Threads []*models.SessionSummary
	// Total is how many live observations the project has in all.
	Total int
}

// BriefResult is a finished brief.
type BriefResult struct {
	// Text is ready to store and show: a dated header, the model's sections and the open threads.
	Text string
	// Source says what it was written from.
	Source string
}

func briefDate(epochMs int64) string { return time.UnixMilli(epochMs).UTC().Format("2006-01-02") }

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// buildBriefPrompt renders the request. When it would be too large the oldest observations are dropped,
// and the ones actually used are returned so the brief can say how many it was written from.
func buildBriefPrompt(in BriefInput) (prompt string, used []*models.Observation) {
	used = in.Observations
	for {
		var b strings.Builder
		b.WriteString("PROJECT BRIEF REQUEST\n")
		fmt.Fprintf(&b, "AS OF: %s\nPROJECT: %s\n\n", in.Now.UTC().Format("2006-01-02"), in.Name)

		if len(in.Threads) > 0 {
			b.WriteString("CURRENT WORK (the developer's own checkpoint notes, most recent first):\n")
			for _, t := range in.Threads {
				fmt.Fprintf(&b, "- %s (updated %s)", clipRunes(t.Request.String, 80), briefDate(t.CreatedAtEpoch))
				for _, f := range []struct{ label, text string }{
					{"goal", t.Notes.String}, {"progress", t.Completed.String}, {"decisions", t.Learned.String},
				} {
					if strings.TrimSpace(f.text) != "" {
						fmt.Fprintf(&b, "; %s: %s", f.label, clipRunes(f.text, briefThreadChars))
					}
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}

		fmt.Fprintf(&b, "OBSERVATIONS (%d of %d, oldest first):\n\n", len(used), in.Total)
		for _, o := range used {
			fmt.Fprintf(&b, "[#%d] (%s, %s) %s\n", o.ID, o.Type, briefDate(o.CreatedAtEpoch), clipRunes(o.Title.String, briefTitleChars))
			if sub := strings.TrimSpace(o.Subtitle.String); sub != "" {
				fmt.Fprintf(&b, "  %s\n", clipRunes(sub, briefTitleChars))
			}
			if nar := strings.TrimSpace(o.Narrative.String); nar != "" {
				fmt.Fprintf(&b, "  %s\n", clipRunes(nar, briefNarrativeChars))
			}
			b.WriteString("\n")
		}
		if b.Len()+len(briefSystemPrompt) <= briefMaxPromptBytes || len(used) <= 1 {
			return b.String(), used
		}
		drop := len(used) / 10
		if drop < 1 {
			drop = 1
		}
		used = used[drop:]
	}
}

var (
	citationRe = regexp.MustCompile(`([ \t]*)\[#([0-9][0-9,\s#]*)\]`)
	emailRe    = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	numberRe   = regexp.MustCompile(`\d+`)
)

// unwantedSections are headings the model was told not to write: open work comes from the developer's notes.
var unwantedSections = []string{"open item", "open thread", "to-do", "todo", "next step"}

// dropUnwantedSections removes a section whose heading is one of unwantedSections, up to the next section.
func dropUnwantedSections(body string) string {
	var out []string
	skipping := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") || line == "##" {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "##")))
			skipping = false
			for _, u := range unwantedSections {
				if strings.HasPrefix(heading, u) {
					skipping = true
					break
				}
			}
		}
		if !skipping {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// cleanBriefBody makes the model's text safe to store: no code fence around it, no section it was not
// asked for, no citation of an observation that was not in the request, no email address, no private
// text, and a bounded length.
func cleanBriefBody(body string, known map[int64]bool) string {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "```") {
		body = strings.TrimPrefix(body, "```markdown")
		body = strings.TrimPrefix(body, "```md")
		body = strings.TrimPrefix(body, "```")
		body = strings.TrimSuffix(strings.TrimSpace(body), "```")
		body = strings.TrimSpace(body)
	}
	body = dropUnwantedSections(body)
	body = citationRe.ReplaceAllStringFunc(body, func(m string) string {
		lead := m[:len(m)-len(strings.TrimLeft(m, " \t"))]
		var keep []string
		for _, num := range numberRe.FindAllString(m, -1) {
			if id, err := strconv.ParseInt(num, 10, 64); err == nil && known[id] {
				keep = append(keep, "#"+num)
			}
		}
		if len(keep) == 0 {
			return "" // a citation of something that was not in the request, and the space before it
		}
		return lead + "[" + strings.Join(keep, ", ") + "]"
	})
	body = emailRe.ReplaceAllString(body, "[email removed]")
	body = strings.TrimSpace(privacy.Clean(body))

	if len(body) > briefMaxBodyChars {
		end := briefMaxBodyChars
		for end > 0 && !utf8.RuneStart(body[end]) {
			end-- // never cut a character in half
		}
		cut := body[:end]
		if i := strings.LastIndex(cut, "\n"); i > briefMaxBodyChars/2 {
			cut = cut[:i]
		}
		body = strings.TrimSpace(cut) + "\n…"
	}
	return body
}

// assembleBrief puts the pieces together: a header that says when and from what the brief was written, the
// model's sections, and the open threads taken straight from the developer's own checkpoint notes.
func assembleBrief(in BriefInput, usedCount int, body string) (text, source string) {
	source = fmt.Sprintf("%d of %d observations", usedCount, in.Total)
	switch len(in.Threads) {
	case 0:
	case 1:
		source += " and 1 checkpoint note"
	default:
		source += fmt.Sprintf(" and %d checkpoint notes", len(in.Threads))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "As of %s, written from %s. It can lag behind recent work.\n\n", in.Now.UTC().Format("2006-01-02"), source)
	b.WriteString(body)

	var open []string
	for _, t := range in.Threads {
		if next := strings.TrimSpace(t.NextSteps.String); next != "" && len(open) < briefMaxOpenThreads {
			open = append(open, fmt.Sprintf("- %s (updated %s): %s", clipRunes(t.Request.String, 80), briefDate(t.CreatedAtEpoch), clipRunes(next, briefThreadChars)))
		}
	}
	if len(open) > 0 {
		b.WriteString("\n\n## Open threads (from your checkpoint notes)\n")
		b.WriteString(strings.Join(open, "\n"))
	}
	return b.String(), source
}

// ErrNothingToBrief means the project has no live observations to write a brief from.
var ErrNothingToBrief = errors.New("no observations to write a brief from")

// GenerateBrief writes a project's brief on the backend configured for the brief task.
func (p *Processor) GenerateBrief(ctx context.Context, in BriefInput) (*BriefResult, error) {
	if len(in.Observations) == 0 {
		return nil, ErrNothingToBrief
	}
	prompt, used := buildBriefPrompt(in)

	known := make(map[int64]bool, len(used))
	for _, o := range used {
		known[o.ID] = true
	}

	// The same limit on concurrent model calls applies as for summaries and observations.
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	raw, err := p.completeWith(ctx, TaskBrief, briefSystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("brief: %w", err)
	}
	body := cleanBriefBody(raw, known)
	if len(body) < briefMinBodyChars {
		return nil, fmt.Errorf("brief: the model's answer was too short to use (%d characters)", len(body))
	}
	text, source := assembleBrief(in, len(used), body)
	return &BriefResult{Text: text, Source: source}, nil
}
