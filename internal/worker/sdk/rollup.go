package sdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	// rollupMaxPromptBytes keeps the request below MaxPromptSize with room to spare.
	rollupMaxPromptBytes = 90 * 1024
	// rollupNarrativeChars bounds how much of each note goes into the prompt.
	rollupNarrativeChars = 520
	rollupTitleChars     = 160
	// rollupMaxBodyChars bounds what the model's text may add to a stored roll-up.
	rollupMaxBodyChars = 2400
	rollupMinBodyChars = 60
	// rollupMaxTitleChars bounds a roll-up's title.
	rollupMaxTitleChars = 100
)

// rollupSystemPrompt is deliberately not the memory extraction prompt of the other tasks.
const rollupSystemPrompt = `You condense older notes (observations) of ONE developer project into a single roll-up note inside the developer's memory system. You get the notes, oldest first, each with an id and a date. The originals are kept but hidden from search, so the roll-up must keep what a developer starting new work would still need from them.

Rules:
- Use only what the notes say. Never add facts, file names, ports, versions or numbers that they do not contain.
- Cite the notes behind each point like [#12] or [#12, #40].
- Keep decisions and the reasons for them, causes of problems and how they were fixed, conventions, gotchas, and values that are still true. Drop one-off incidents, progress chatter and anything a later note replaced: when a later note changes or undoes an earlier one, keep only the later state.
- Never include personal data (email addresses, people's names, credentials) or anything about other projects.
- No filler and no praise. At most 220 words, as short markdown bullets.
- Start with one line "TITLE: " followed by a plain title of at most 90 characters that says what the notes were about, then a blank line, then the bullets.
- Reply with the roll-up only.`

// RollupInput is what a roll-up is written from.
type RollupInput struct {
	Now time.Time
	// Name is the project's name without its hash.
	Name string
	// Observations are the notes to condense, oldest first.
	Observations []*models.Observation
}

// RollupResult is a finished roll-up, cleaned and ready to store.
type RollupResult struct {
	// Title is the model's title, or empty when it gave none.
	Title string
	// Body is the roll-up text with its citations.
	Body string
}

// ErrRollupTooLarge means the notes do not fit one request. The caller splits the group into smaller ones.
var ErrRollupTooLarge = errors.New("the notes are too large for one roll-up request")

// ErrNothingToRollUp means there were no notes to condense.
var ErrNothingToRollUp = errors.New("no notes to roll up")

// buildRollupPrompt renders the request. It never drops a note: every note that is in the request is archived when
// the roll-up is stored, so the group has to fit.
func buildRollupPrompt(in RollupInput) (string, error) {
	var b strings.Builder
	b.WriteString("ROLL-UP REQUEST\n")
	first, last := in.Observations[0].CreatedAtEpoch, in.Observations[len(in.Observations)-1].CreatedAtEpoch
	fmt.Fprintf(&b, "AS OF: %s\nPROJECT: %s\nNOTES (%d, from %s to %s, oldest first):\n\n",
		in.Now.UTC().Format("2006-01-02"), in.Name, len(in.Observations), briefDate(first), briefDate(last))
	for _, o := range in.Observations {
		fmt.Fprintf(&b, "[#%d] (%s, %s) %s\n", o.ID, o.Type, briefDate(o.CreatedAtEpoch), clipRunes(o.Title.String, rollupTitleChars))
		if sub := strings.TrimSpace(o.Subtitle.String); sub != "" {
			fmt.Fprintf(&b, "  %s\n", clipRunes(sub, rollupTitleChars))
		}
		if nar := strings.TrimSpace(o.Narrative.String); nar != "" {
			fmt.Fprintf(&b, "  %s\n", clipRunes(nar, rollupNarrativeChars))
		}
		if len(o.Facts) > 0 {
			fmt.Fprintf(&b, "  Facts: %s\n", clipRunes(strings.Join(o.Facts, "; "), rollupNarrativeChars))
		}
		b.WriteString("\n")
	}
	if b.Len()+len(rollupSystemPrompt) > rollupMaxPromptBytes {
		return "", ErrRollupTooLarge
	}
	return b.String(), nil
}

// parseRollup splits the model's answer into a title and a cleaned body. The body is cleaned like a brief's: no
// citation of a note that was not in the request, no email address, no private text, a bounded length.
func parseRollup(raw string, known map[int64]bool) (title, body string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```markdown")
		raw = strings.TrimPrefix(raw, "```md")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "```"))
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 0 {
		if head := strings.TrimSpace(lines[0]); len(head) >= 6 && strings.EqualFold(head[:6], "TITLE:") {
			title = strings.TrimSpace(head[6:])
			raw = strings.Join(lines[1:], "\n")
		}
	}
	title = strings.Trim(strings.NewReplacer("*", "", "`", "", "#", "").Replace(title), "\" ")
	// The title goes through the same cleaning (no citation, email or private text) and is kept to one short line.
	title = strings.Join(strings.Fields(cleanModelBody(title, nil, rollupMaxTitleChars*2, false)), " ")
	title = clipRunes(title, rollupMaxTitleChars)
	return title, cleanModelBody(raw, known, rollupMaxBodyChars, false)
}

// GenerateRollup writes one roll-up on the backend configured for the roll-up task. Nothing is stored here.
func (p *Processor) GenerateRollup(ctx context.Context, in RollupInput) (*RollupResult, error) {
	if len(in.Observations) == 0 {
		return nil, ErrNothingToRollUp
	}
	prompt, err := buildRollupPrompt(in)
	if err != nil {
		return nil, err
	}
	known := make(map[int64]bool, len(in.Observations))
	for _, o := range in.Observations {
		known[o.ID] = true
	}

	// The same limit on concurrent model calls applies as for summaries, observations and briefs.
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	raw, err := p.completeWith(ctx, TaskRollup, rollupSystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("roll-up: %w", err)
	}
	title, body := parseRollup(raw, known)
	if len(body) < rollupMinBodyChars {
		return nil, fmt.Errorf("roll-up: the model's answer was too short to use (%d characters)", len(body))
	}
	return &RollupResult{Title: title, Body: body}, nil
}
