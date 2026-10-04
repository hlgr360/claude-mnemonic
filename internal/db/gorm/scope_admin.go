package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// Who decided an observation's scope.
const (
	// ScopeSourceAuto is the rule at save time, or a re-scope.
	ScopeSourceAuto = "auto"
	// ScopeSourceExplicit is a person or a client choosing it (remember, an edit, an import that says it).
	ScopeSourceExplicit = "explicit"
)

// explicitFact marks the notes the `remember` tool saved: their scope was chosen by the client, whatever the
// column says on rows from before scope_source existed.
const explicitFact = "Saved explicitly via"

// backfillScopeSource marks the rows that predate the column: notes saved with `remember` are explicit, everything
// else came from the rule and is auto.
func backfillScopeSource(tx *gorm.DB) error {
	if err := tx.Exec(`UPDATE observations SET scope_source = ? WHERE scope_source = '' AND facts LIKE ?`,
		ScopeSourceExplicit, "%"+explicitFact+"%").Error; err != nil {
		return err
	}
	return tx.Exec(`UPDATE observations SET scope_source = ? WHERE scope_source = ''`, ScopeSourceAuto).Error
}

// ScopeChange is one observation whose scope the current rule would change.
type ScopeChange struct {
	Project string                  `json:"project"`
	Title   string                  `json:"title"`
	From    models.ObservationScope `json:"from"`
	To      models.ObservationScope `json:"to"`
	ID      int64                   `json:"id"`
}

// RescopePlan is what applying the current scope rule to the archive would do.
type RescopePlan struct {
	Changes []ScopeChange `json:"changes"`
	// Total is every observation, Unchanged those the rule agrees with, Protected those whose scope a person or a
	// client chose (never changed).
	Total, Unchanged, Protected int
	ToProject, ToGlobal         int
}

// Token ties a confirmation to exactly this plan: if anything that matters changed since the preview, it no
// longer matches.
func (p *RescopePlan) Token() string {
	type item struct {
		To models.ObservationScope
		ID int64
	}
	items := make([]item, len(p.Changes))
	for i, c := range p.Changes {
		items[i] = item{c.To, c.ID}
	}
	payload, _ := json.Marshal(items)
	sum := sha256.Sum256([]byte(strings.Join([]string{"rescope", string(payload)}, "|")))
	return hex.EncodeToString(sum[:])[:16]
}

// PlanRescope decides, for every observation whose scope was not chosen by a person or a client, what the current rule
// says, and lists the differences. It changes nothing.
func (s *ObservationStore) PlanRescope(ctx context.Context) (*RescopePlan, error) {
	var rows []Observation
	if err := s.db.WithContext(ctx).
		Select("id", "project", "title", "concepts", "files_modified", "scope", "scope_source").
		Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	plan := &RescopePlan{Changes: []ScopeChange{}, Total: len(rows)}
	for _, r := range rows {
		if r.ScopeSource == ScopeSourceExplicit {
			plan.Protected++
			continue
		}
		want := models.DetermineScopeFor(r.Concepts, r.FilesModified)
		have := r.Scope
		if have == "" {
			have = models.ScopeProject
		}
		if want == have {
			plan.Unchanged++
			continue
		}
		plan.Changes = append(plan.Changes, ScopeChange{ID: r.ID, Project: r.Project, Title: r.Title.String, From: have, To: want})
		if want == models.ScopeGlobal {
			plan.ToGlobal++
		} else {
			plan.ToProject++
		}
	}
	return plan, nil
}

// ApplyRescope makes the changes of a plan in one transaction: the scope of each observation and, with it, the scope
// metadata of its vectors (a metadata column of the vec0 table, so no embedding is touched). A row a person
// chose a scope for since the plan was made is left alone. It returns how many observations changed.
func (s *ObservationStore) ApplyRescope(ctx context.Context, plan *RescopePlan) (int, error) {
	byTarget := map[models.ObservationScope][]int64{}
	for _, c := range plan.Changes {
		byTarget[c.To] = append(byTarget[c.To], c.ID)
	}
	changed := 0
	err := immediateTx(ctx, s.db, func(tx *gorm.DB) error {
		targets := make([]string, 0, len(byTarget))
		for t := range byTarget {
			targets = append(targets, string(t))
		}
		sort.Strings(targets)
		for _, t := range targets {
			ids := byTarget[models.ObservationScope(t)]
			for start := 0; start < len(ids); start += 400 {
				chunk := ids[start:min(start+400, len(ids))]
				res := tx.Exec(`UPDATE observations SET scope = ?, scope_source = ? WHERE id IN ? AND scope_source <> ?`,
					t, ScopeSourceAuto, chunk, ScopeSourceExplicit)
				if res.Error != nil {
					return res.Error
				}
				changed += int(res.RowsAffected)
				// Only the rows that really changed: one a person fixed since the preview was skipped above.
				var done []int64
				if err := tx.Raw(`SELECT id FROM observations WHERE id IN ? AND scope = ? AND scope_source = ?`, chunk, t, ScopeSourceAuto).Scan(&done).Error; err != nil {
					return err
				}
				if len(done) > 0 {
					if err := tx.Exec(`UPDATE vectors SET scope = ? WHERE doc_type = 'observation' AND sqlite_id IN ?`, t, done).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	return changed, err
}
