// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProjectIdentity records where a project's folder came from: the normalised git remote and the root of the checkout
// the worker saw the project used from. A project can have several (two clones, a folder that moved). Credentials
// never reach it (see projects.NormalizeRemote).
type ProjectIdentity struct {
	Project        string `gorm:"not null;index;uniqueIndex:idx_project_identity"`
	Remote         string `gorm:"type:text;not null;default:'';uniqueIndex:idx_project_identity"`
	RootPath       string `gorm:"type:text;not null;default:'';uniqueIndex:idx_project_identity"`
	ID             uint   `gorm:"primaryKey"`
	FirstSeenEpoch int64  `gorm:"not null"`
	LastSeenEpoch  int64  `gorm:"not null"`
}

func (ProjectIdentity) TableName() string { return "project_identities" }

// ProjectDuplicateDismissal remembers that a person said two projects are not the same, so the pair is not suggested
// again. A is always the smaller id of the pair.
type ProjectDuplicateDismissal struct {
	A              string `gorm:"primaryKey"`
	B              string `gorm:"primaryKey"`
	CreatedAtEpoch int64  `gorm:"not null"`
}

func (ProjectDuplicateDismissal) TableName() string { return "project_duplicate_dismissals" }

// OrderedPair puts two project ids in the order the dismissals are stored in.
func OrderedPair(a, b string) (string, string) {
	if b < a {
		return b, a
	}
	return a, b
}

// ProjectIdentityStore keeps the identities of projects and the pairs a person dismissed.
type ProjectIdentityStore struct {
	db *gorm.DB
}

// NewProjectIdentityStore creates a project identity store.
func NewProjectIdentityStore(store *Store) *ProjectIdentityStore {
	return &ProjectIdentityStore{db: store.DB}
}

// RecordIdentity notes that the project was used from a checkout with this remote and root. Seeing it again only
// updates when it was last seen. An identity with neither a remote nor a root says nothing and is ignored.
func (s *ProjectIdentityStore) RecordIdentity(ctx context.Context, project, remote, root string) error {
	project = strings.TrimSpace(project)
	if project == "" || (remote == "" && root == "") {
		return nil
	}
	now := time.Now().UnixMilli()
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project"}, {Name: "remote"}, {Name: "root_path"}},
		DoUpdates: clause.Assignments(map[string]any{"last_seen_epoch": now}),
	}).Create(&ProjectIdentity{Project: project, Remote: remote, RootPath: root, FirstSeenEpoch: now, LastSeenEpoch: now}).Error
	if err != nil {
		return fmt.Errorf("record project identity: %w", err)
	}
	return nil
}

// Identities returns every recorded identity, by project.
func (s *ProjectIdentityStore) Identities(ctx context.Context) (map[string][]ProjectIdentity, error) {
	var rows []ProjectIdentity
	if err := s.db.WithContext(ctx).Order("last_seen_epoch DESC, id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list project identities: %w", err)
	}
	out := map[string][]ProjectIdentity{}
	for _, r := range rows {
		out[r.Project] = append(out[r.Project], r)
	}
	return out, nil
}

// Dismiss records that the two projects are not the same.
func (s *ProjectIdentityStore) Dismiss(ctx context.Context, a, b string) error {
	a, b = OrderedPair(strings.TrimSpace(a), strings.TrimSpace(b))
	if a == "" || b == "" || a == b {
		return fmt.Errorf("two different projects are required")
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&ProjectDuplicateDismissal{A: a, B: b, CreatedAtEpoch: time.Now().UnixMilli()}).Error
}

// Restore forgets a dismissal, so the pair can be suggested again. It reports whether there was one.
func (s *ProjectIdentityStore) Restore(ctx context.Context, a, b string) (bool, error) {
	a, b = OrderedPair(strings.TrimSpace(a), strings.TrimSpace(b))
	res := s.db.WithContext(ctx).Where("a = ? AND b = ?", a, b).Delete(&ProjectDuplicateDismissal{})
	return res.RowsAffected > 0, res.Error
}

// Dismissed returns the dismissed pairs, each as {smaller id, larger id}.
func (s *ProjectIdentityStore) Dismissed(ctx context.Context) (map[[2]string]bool, error) {
	var rows []ProjectDuplicateDismissal
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list dismissed pairs: %w", err)
	}
	out := make(map[[2]string]bool, len(rows))
	for _, r := range rows {
		out[[2]string{r.A, r.B}] = true
	}
	return out, nil
}

// NormalizedTitles returns the lower-cased titles of the project's live notes (at most limit).
func (s *ProjectIdentityStore) NormalizedTitles(ctx context.Context, project string, limit int) (map[string]bool, error) {
	var titles []string
	err := s.db.WithContext(ctx).Raw(`SELECT title FROM observations
		WHERE project = ? AND title IS NOT NULL AND title != '' AND COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0
		ORDER BY created_at_epoch DESC LIMIT ?`, project, limit).Scan(&titles).Error
	if err != nil {
		return nil, fmt.Errorf("list titles: %w", err)
	}
	out := make(map[string]bool, len(titles))
	for _, t := range titles {
		out[strings.ToLower(strings.Join(strings.Fields(t), " "))] = true
	}
	return out, nil
}

// moveProjectIdentity makes a merged-away project's identities and dismissals the survivor's: the survivor now
// stands for every clone, and a pair a person said was different stays different.
func moveProjectIdentity(tx *gorm.DB, from, into string) error {
	if err := tx.Exec(`UPDATE OR IGNORE project_identities SET project = ? WHERE project = ?`, into, from).Error; err != nil {
		return fmt.Errorf("move identities: %w", err)
	}
	if err := tx.Exec(`DELETE FROM project_identities WHERE project = ?`, from).Error; err != nil {
		return fmt.Errorf("drop identities: %w", err)
	}
	var rows []ProjectDuplicateDismissal
	if err := tx.Where("a = ? OR b = ?", from, from).Find(&rows).Error; err != nil {
		return fmt.Errorf("find dismissals: %w", err)
	}
	for _, r := range rows {
		if err := tx.Where("a = ? AND b = ?", r.A, r.B).Delete(&ProjectDuplicateDismissal{}).Error; err != nil {
			return fmt.Errorf("drop dismissal: %w", err)
		}
		other := r.A
		if other == from {
			other = r.B
		}
		if other == into {
			continue // the pair that was just merged
		}
		a, b := OrderedPair(into, other)
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ProjectDuplicateDismissal{A: a, B: b, CreatedAtEpoch: r.CreatedAtEpoch}).Error; err != nil {
			return fmt.Errorf("move dismissal: %w", err)
		}
	}
	return nil
}

// dropProjectIdentity removes a deleted project's identities and dismissals.
func dropProjectIdentity(tx *gorm.DB, project string) error {
	if err := tx.Exec(`DELETE FROM project_identities WHERE project = ?`, project).Error; err != nil {
		return fmt.Errorf("drop identities: %w", err)
	}
	if err := tx.Exec(`DELETE FROM project_duplicate_dismissals WHERE a = ? OR b = ?`, project, project).Error; err != nil {
		return fmt.Errorf("drop dismissals: %w", err)
	}
	return nil
}
