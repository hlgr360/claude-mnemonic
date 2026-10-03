// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// What a person can decide about a proposed conflict.
const (
	// DecisionSupersedeOlder hides the older observation: the newer one replaces it.
	DecisionSupersedeOlder = "supersede_older"
	// DecisionSupersedeNewer hides the newer observation: the older one is right.
	DecisionSupersedeNewer = "supersede_newer"
	// DecisionKeepBoth says the two are not in conflict. It is remembered, so the pair is not proposed again.
	DecisionKeepBoth = "keep_both"
)

// Which proposals to list.
const (
	ConflictStatusOpen     = "open"
	ConflictStatusResolved = "resolved"
	ConflictStatusAll      = "all"
)

var (
	// ErrConflictNotFound means no conflict has that id.
	ErrConflictNotFound = errors.New("conflict not found")
	// ErrConflictResolved means the conflict has already been decided.
	ErrConflictResolved = errors.New("conflict is already resolved")
	// ErrConflictOpen means the conflict has not been decided, so there is nothing to undo.
	ErrConflictOpen = errors.New("conflict is not resolved")
	// ErrObservationGone means one of the two observations no longer exists.
	ErrObservationGone = errors.New("one of the observations no longer exists")
	// ErrBadDecision means the decision is not one of the known ones.
	ErrBadDecision = errors.New("unknown decision")
	// ErrInvalidPair means the two observations cannot be a conflict (same one, other projects, wrong order).
	ErrInvalidPair = errors.New("not a valid pair of observations")
)

// Proposal is a suggested conflict between two observations.
type Proposal struct {
	Type       models.ConflictType
	Relation   string
	Confidence string
	Reason     string
	Proposer   string
	NewerID    int64
	OlderID    int64
}

// ConflictRecord is a conflict with the two observations it is about.
type ConflictRecord struct {
	Conflict *models.ObservationConflict
	Older    *models.Observation
	Newer    *models.Observation
}

// ConflictFilter selects proposals to list. A zero value lists the first 50 open ones of every project.
type ConflictFilter struct {
	Project string
	Status  string
	Limit   int
	Offset  int
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// Propose records a suggested conflict. It never decides anything and never touches the observations. The pair
// is recorded once: asking again, in either order, returns the existing proposal (created is false), and that
// includes pairs the user has already decided, so a "keep both" is remembered.
//
// The newer observation must really be the newer one, both must exist and belong to the same project.
func (s *ConflictStore) Propose(ctx context.Context, p Proposal) (id int64, created bool, err error) {
	now := time.Now()
	err = immediateTx(ctx, s.db, func(tx *gorm.DB) error {
		if p.NewerID == p.OlderID {
			return ErrInvalidPair
		}
		var obs []Observation
		if err := tx.Where("id IN ?", []int64{p.NewerID, p.OlderID}).Find(&obs).Error; err != nil {
			return err
		}
		if len(obs) != 2 {
			return ErrObservationGone
		}
		byID := map[int64]Observation{obs[0].ID: obs[0], obs[1].ID: obs[1]}
		newer, older := byID[p.NewerID], byID[p.OlderID]
		if newer.Project != older.Project || newer.CreatedAtEpoch < older.CreatedAtEpoch {
			return ErrInvalidPair
		}

		var existing ObservationConflict
		findErr := tx.Where("(newer_obs_id = ? AND older_obs_id = ?) OR (newer_obs_id = ? AND older_obs_id = ?)",
			p.NewerID, p.OlderID, p.OlderID, p.NewerID).First(&existing).Error
		switch {
		case findErr == nil:
			id, created = existing.ID, false
			return nil
		case !errors.Is(findErr, gorm.ErrRecordNotFound):
			return findErr
		}

		kind := p.Type
		switch kind {
		case models.ConflictSuperseded, models.ConflictContradicts, models.ConflictOutdatedPattern:
		default:
			kind = models.ConflictSuperseded
		}
		row := ObservationConflict{
			NewerObsID: p.NewerID, OlderObsID: p.OlderID, ConflictType: kind, Resolution: models.ResolutionManual,
			DetectedAt: now.Format(time.RFC3339), DetectedAtEpoch: now.UnixMilli(),
			Reason: nullStr(p.Reason), Relation: nullStr(p.Relation), Confidence: nullStr(p.Confidence), Proposer: nullStr(p.Proposer),
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		id, created = row.ID, true
		return nil
	})
	return id, created, err
}

// conflictQuery joins a conflict to both of its observations, so a conflict whose observation was deleted is
// simply not there.
func (s *ConflictStore) conflictQuery(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).
		Table("observation_conflicts oc").
		Joins("JOIN observations newer ON newer.id = oc.newer_obs_id").
		Joins("JOIN observations older ON older.id = oc.older_obs_id")
}

func statusFilter(q *gorm.DB, status string) *gorm.DB {
	switch status {
	case ConflictStatusResolved:
		return q.Where("oc.resolved = 1")
	case ConflictStatusAll:
		return q
	default:
		return q.Where("oc.resolved = 0")
	}
}

func conflictOrder(status string) string {
	const byConfidence = "CASE oc.confidence WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END"
	switch status {
	case ConflictStatusResolved:
		return "oc.resolved_at_epoch DESC, oc.id DESC"
	case ConflictStatusAll:
		return "oc.resolved ASC, " + byConfidence + ", oc.detected_at_epoch DESC, oc.id DESC"
	default:
		return byConfidence + ", oc.detected_at_epoch DESC, oc.id DESC"
	}
}

// CountOpen returns how many proposals wait for a decision, for one project or (empty) all of them.
func (s *ConflictStore) CountOpen(ctx context.Context, project string) (int, error) {
	q := statusFilter(s.conflictQuery(ctx), ConflictStatusOpen)
	if project != "" {
		q = q.Where("older.project = ?", project)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

// ListConflicts returns proposals with both observations in full, and how many match in all. Open ones come
// with the most confident first; resolved ones with the latest decision first.
func (s *ConflictStore) ListConflicts(ctx context.Context, f ConflictFilter) ([]*ConflictRecord, int, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	build := func() *gorm.DB {
		q := statusFilter(s.conflictQuery(ctx), f.Status)
		if f.Project != "" {
			q = q.Where("older.project = ?", f.Project)
		}
		return q
	}

	var total int64
	if err := build().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []ObservationConflict
	if err := build().Select("oc.*").Order(conflictOrder(f.Status)).Limit(limit).Offset(f.Offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	records, err := s.withObservations(ctx, rows)
	return records, int(total), err
}

// GetConflict returns one conflict with both observations.
func (s *ConflictStore) GetConflict(ctx context.Context, id int64) (*ConflictRecord, error) {
	var row ObservationConflict
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrConflictNotFound
		}
		return nil, err
	}
	records, err := s.withObservations(ctx, []ObservationConflict{row})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, ErrObservationGone
	}
	return records[0], nil
}

func (s *ConflictStore) withObservations(ctx context.Context, rows []ObservationConflict) ([]*ConflictRecord, error) {
	if len(rows) == 0 {
		return []*ConflictRecord{}, nil
	}
	ids := make([]int64, 0, 2*len(rows))
	for _, r := range rows {
		ids = append(ids, r.NewerObsID, r.OlderObsID)
	}
	var obs []Observation
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&obs).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]*models.Observation, len(obs))
	for i, o := range toModelObservations(obs) {
		byID[obs[i].ID] = o
	}
	records := make([]*ConflictRecord, 0, len(rows))
	for i := range rows {
		older, newer := byID[rows[i].OlderObsID], byID[rows[i].NewerObsID]
		if older == nil || newer == nil {
			continue
		}
		records = append(records, &ConflictRecord{Conflict: toModelConflict(&rows[i]), Older: older, Newer: newer})
	}
	return records, nil
}

// ResolveProposal applies a person's decision to an open proposal: it records the decision and when it was made
// and, for the two supersede decisions, hides the chosen observation (is_superseded) in the same transaction.
func (s *ConflictStore) ResolveProposal(ctx context.Context, id int64, decision string) (*ConflictRecord, error) {
	switch decision {
	case DecisionSupersedeOlder, DecisionSupersedeNewer, DecisionKeepBoth:
	default:
		return nil, ErrBadDecision
	}
	now := time.Now()
	err := immediateTx(ctx, s.db, func(tx *gorm.DB) error {
		var c ObservationConflict
		if err := tx.First(&c, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflictNotFound
			}
			return err
		}
		if c.Resolved == 1 {
			return ErrConflictResolved
		}
		var present int64
		if err := tx.Model(&Observation{}).Where("id IN ?", []int64{c.NewerObsID, c.OlderObsID}).Count(&present).Error; err != nil {
			return err
		}
		if present != 2 {
			return ErrObservationGone
		}

		resolution, hidden := models.ResolutionManual, int64(0)
		switch decision {
		case DecisionSupersedeOlder:
			resolution, hidden = models.ResolutionPreferNewer, c.OlderObsID
		case DecisionSupersedeNewer:
			resolution, hidden = models.ResolutionPreferOlder, c.NewerObsID
		}
		if hidden != 0 {
			if err := tx.Model(&Observation{}).Where("id = ?", hidden).Update("is_superseded", 1).Error; err != nil {
				return err
			}
		}
		return tx.Model(&ObservationConflict{}).Where("id = ?", id).Updates(map[string]any{
			"resolved": 1, "resolution": resolution, "decision": decision, "superseded_obs_id": hidden,
			"resolved_at": now.Format(time.RFC3339), "resolved_at_epoch": now.UnixMilli(),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetConflict(ctx, id)
}

// UndoProposal takes back a decision: the conflict is open again and an observation it hid is visible again,
// unless another decision still hides it.
func (s *ConflictStore) UndoProposal(ctx context.Context, id int64) (*ConflictRecord, error) {
	err := immediateTx(ctx, s.db, func(tx *gorm.DB) error {
		var c ObservationConflict
		if err := tx.First(&c, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflictNotFound
			}
			return err
		}
		if c.Resolved != 1 {
			return ErrConflictOpen
		}
		if c.SupersededObsID != 0 {
			var others int64
			if err := tx.Model(&ObservationConflict{}).
				Where("superseded_obs_id = ? AND resolved = 1 AND id <> ?", c.SupersededObsID, id).Count(&others).Error; err != nil {
				return err
			}
			if others == 0 {
				if err := tx.Model(&Observation{}).Where("id = ?", c.SupersededObsID).Update("is_superseded", 0).Error; err != nil {
					return err
				}
			}
		}
		return tx.Model(&ObservationConflict{}).Where("id = ?", id).Updates(map[string]any{
			"resolved": 0, "resolution": models.ResolutionManual, "decision": nil, "superseded_obs_id": 0,
			"resolved_at": nil, "resolved_at_epoch": 0,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetConflict(ctx, id)
}

// CleanupSuperseded deletes observations that a person superseded more than retentionDays ago, together
// with their conflicts. The clock starts at the person's decision, not at the proposal. A value of zero or
// less keeps them for ever. It returns the deleted ids so their vectors can be removed too.
func (s *ConflictStore) CleanupSuperseded(ctx context.Context, retentionDays int) ([]int64, error) {
	if retentionDays <= 0 {
		return nil, nil
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()

	var deleted []int64
	err := immediateTx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Table("observations o").
			Select("o.id").
			Joins("JOIN observation_conflicts oc ON oc.superseded_obs_id = o.id AND oc.resolved = 1").
			Where("o.is_superseded = 1").
			Group("o.id").
			Having("MAX(oc.resolved_at_epoch) > 0 AND MAX(oc.resolved_at_epoch) < ?", cutoff).
			Pluck("o.id", &deleted).Error; err != nil {
			return err
		}
		if len(deleted) == 0 {
			return nil
		}
		if err := tx.Where("newer_obs_id IN ? OR older_obs_id IN ?", deleted, deleted).Delete(&ObservationConflict{}).Error; err != nil {
			return err
		}
		if err := tx.Where("observation_id IN ?", deleted).Delete(&ConflictCheck{}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", deleted).Delete(&Observation{}).Error
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// UncheckedObservations returns up to limit live observations of the project that the proposer has not looked
// at yet, newest first.
func (s *ConflictStore) UncheckedObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error) {
	var rows []Observation
	err := s.db.WithContext(ctx).
		Where("project = ? AND (scope IS NULL OR scope = 'project')", project).
		Where("COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0").
		Where("id NOT IN (SELECT observation_id FROM conflict_checks)").
		Order("created_at_epoch DESC, id DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return toModelObservations(rows), nil
}

// MarkChecked records that the proposer looked at an observation and how many proposals came out of it.
func (s *ConflictStore) MarkChecked(ctx context.Context, observationID int64, proposals int) error {
	return s.db.WithContext(ctx).Save(&ConflictCheck{
		ObservationID: observationID, CheckedAtEpoch: time.Now().UnixMilli(), Proposals: proposals,
	}).Error
}

// ProjectsWithUncheckedObservations lists projects that have live observations the proposer has not seen.
func (s *ConflictStore) ProjectsWithUncheckedObservations(ctx context.Context) ([]string, error) {
	var projects []string
	err := s.db.WithContext(ctx).Raw(`
		SELECT project FROM observations
		WHERE project IS NOT NULL AND project != '' AND (scope IS NULL OR scope = 'project')
		  AND COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0
		  AND id NOT IN (SELECT observation_id FROM conflict_checks)
		GROUP BY project ORDER BY MAX(created_at_epoch) DESC`).Scan(&projects).Error
	return projects, err
}
