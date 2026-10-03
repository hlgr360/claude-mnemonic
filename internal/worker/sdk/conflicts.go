package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/lukaszraczylo/claude-mnemonic/internal/privacy"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	// conflictNarrativeChars bounds how much of a note goes into the request.
	conflictNarrativeChars = 700
	conflictReasonChars    = 300
	// maxConflictCandidates bounds how many older notes one request compares against.
	maxConflictCandidates = 6
)

// Relations the proposer may answer with. Only the first three become proposals.
const (
	RelationSupersedes  = "supersedes"
	RelationContradicts = "contradicts"
	RelationDuplicate   = "duplicate"
	RelationRelated     = "related"
	RelationUnrelated   = "unrelated"
)

// conflictSystemPrompt is the strict prompt from the evaluation in scripts/llm-eval: on invented pairs with
// known answers it never called notes that both stay true "replaced", and on real pairs it flagged about half
// as many as the looser one. Even so its answers are proposals for a person to decide.
const conflictSystemPrompt = `You compare notes that were saved automatically from a developer's coding sessions. You get ONE NEWER note and some OLDER notes about the same project. For each OLDER note decide how the NEWER note relates to it.

Choose exactly one relation for each older note:
- duplicate: NEWER says essentially the same thing as the older note and adds nothing new.
- supersedes: NEWER describes a change that makes the older note out of date (a value, behaviour, decision or state that has since changed), so the older note should no longer be trusted.
- contradicts: the two make claims that cannot both be true and nothing says one came after the other.
- related: they are about the same area, but both stay true and each adds something; neither replaces the other.
- unrelated: they are about different things.

Use supersedes ONLY if all three hold: (1) both notes are about the same specific thing (one setting, behaviour, decision, file or state); (2) the older note says how that thing is, and NEWER says it is now different or that the older note's statement is no longer true; (3) a reader who trusted the older note after reading NEWER would be wrong.
These are NOT supersedes, choose related instead: NEWER adds detail or a later step; NEWER reports progress or a milestone in the same project; NEWER fixes a problem that the older note described (the problem note stays true as history); NEWER rewords or summarises; NEWER is a new feature in the same area.
When unsure, choose related. Being similar, or about the same file or tool, is not enough.

Reply with only a JSON array with one object per older note: {"older_id": <the number after #>, "relation": "...", "confidence": "low|medium|high", "reason": "one sentence"}.`

// ConflictVerdict is the proposer's answer about one older note.
type ConflictVerdict struct {
	Relation   string
	Confidence string
	Reason     string
	OlderID    int64
}

// Proposable says whether the verdict is worth showing to a person: only a note that is outdated, in
// conflict or a duplicate is. "Related" and "unrelated" are answers to "is there a conflict?", and it is no.
func (v ConflictVerdict) Proposable() bool {
	return v.Relation == RelationSupersedes || v.Relation == RelationContradicts || v.Relation == RelationDuplicate
}

func conflictDate(o *models.Observation) string { return briefDate(o.CreatedAtEpoch) }

func writeConflictNote(b *strings.Builder, o *models.Observation) {
	fmt.Fprintf(b, "[#%d] saved %s, %s\n  Title: %s\n", o.ID, conflictDate(o), o.Type, clipRunes(o.Title.String, briefTitleChars))
	if sub := strings.TrimSpace(o.Subtitle.String); sub != "" {
		fmt.Fprintf(b, "  Summary: %s\n", clipRunes(sub, briefTitleChars))
	}
	if nar := strings.TrimSpace(o.Narrative.String); nar != "" {
		fmt.Fprintf(b, "  Details: %s\n", clipRunes(nar, conflictNarrativeChars))
	}
}

// buildConflictPrompt renders the comparison request: the newer note and its older candidates.
func buildConflictPrompt(newer *models.Observation, olders []*models.Observation) string {
	var b strings.Builder
	b.WriteString("CONFLICT CHECK REQUEST\n\nNEWER NOTE:\n")
	writeConflictNote(&b, newer)
	b.WriteString("\nOLDER NOTES:\n")
	for _, o := range olders {
		writeConflictNote(&b, o)
		b.WriteString("\n")
	}
	return b.String()
}

var jsonArrayRe = regexp.MustCompile(`(?s)\[.*\]`)

// parseConflictVerdicts reads the proposer's answer. It keeps only well-formed verdicts about notes that were in
// the request, once per note: an unknown id, an unknown relation or a repeated note is dropped, a missing or
// unknown confidence becomes "low", and the reason is cleaned (private text, email addresses, length).
func parseConflictVerdicts(raw string, valid map[int64]bool) []ConflictVerdict {
	m := jsonArrayRe.FindString(raw)
	if m == "" {
		return nil
	}
	var items []struct {
		OlderID    json.Number `json:"older_id"`
		Relation   string      `json:"relation"`
		Confidence string      `json:"confidence"`
		Reason     string      `json:"reason"`
	}
	dec := json.NewDecoder(strings.NewReader(m))
	dec.UseNumber()
	if err := dec.Decode(&items); err != nil {
		return nil
	}

	seen := map[int64]bool{}
	var out []ConflictVerdict
	for _, it := range items {
		id, err := it.OlderID.Int64()
		if err != nil || !valid[id] || seen[id] {
			continue
		}
		relation := strings.ToLower(strings.TrimSpace(it.Relation))
		switch relation {
		case RelationSupersedes, RelationContradicts, RelationDuplicate, RelationRelated, RelationUnrelated:
		default:
			continue
		}
		confidence := strings.ToLower(strings.TrimSpace(it.Confidence))
		switch confidence {
		case "low", "medium", "high":
		default:
			confidence = "low"
		}
		reason := strings.TrimSpace(privacy.Clean(it.Reason))
		reason = emailRe.ReplaceAllString(reason, "[email removed]")
		seen[id] = true
		out = append(out, ConflictVerdict{OlderID: id, Relation: relation, Confidence: confidence, Reason: clipRunes(reason, conflictReasonChars)})
	}
	return out
}

// ProposeConflicts asks, once, how a newer observation relates to each of its older candidates, on the backend
// configured for the conflict task. It returns every well-formed verdict; the caller keeps the proposable ones.
func (p *Processor) ProposeConflicts(ctx context.Context, newer *models.Observation, olders []*models.Observation) ([]ConflictVerdict, error) {
	if newer == nil || len(olders) == 0 {
		return nil, nil
	}
	if len(olders) > maxConflictCandidates {
		olders = olders[:maxConflictCandidates]
	}
	valid := make(map[int64]bool, len(olders))
	for _, o := range olders {
		valid[o.ID] = true
	}
	prompt := buildConflictPrompt(newer, olders)

	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	raw, err := p.completeWith(ctx, TaskConflict, conflictSystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("conflict check: %w", err)
	}
	verdicts := parseConflictVerdicts(raw, valid)
	if len(verdicts) == 0 {
		return nil, fmt.Errorf("conflict check: the answer had no usable verdict")
	}
	return verdicts, nil
}
