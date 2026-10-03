// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// ConflictStore provides conflict-related database operations using GORM.
type ConflictStore struct {
	db *gorm.DB
}

// NewConflictStore creates a new conflict store.
func NewConflictStore(store *Store) *ConflictStore {
	return &ConflictStore{
		db: store.DB,
	}
}

// StoreConflict stores a new observation conflict.
func (s *ConflictStore) StoreConflict(ctx context.Context, conflict *models.ObservationConflict) (int64, error) {
	dbConflict := &ObservationConflict{
		NewerObsID:      conflict.NewerObsID,
		OlderObsID:      conflict.OlderObsID,
		ConflictType:    conflict.ConflictType,
		Resolution:      conflict.Resolution,
		DetectedAt:      conflict.DetectedAt,
		DetectedAtEpoch: conflict.DetectedAtEpoch,
		Resolved:        0,
	}

	// Convert bool to int
	if conflict.Resolved {
		dbConflict.Resolved = 1
	}

	// Handle nullable fields
	if conflict.Reason != "" {
		dbConflict.Reason = sql.NullString{String: conflict.Reason, Valid: true}
	}
	if conflict.ResolvedAt != nil && *conflict.ResolvedAt != "" {
		dbConflict.ResolvedAt = sql.NullString{String: *conflict.ResolvedAt, Valid: true}
	}

	result := s.db.WithContext(ctx).Create(dbConflict)
	if result.Error != nil {
		return 0, result.Error
	}

	return dbConflict.ID, nil
}

// MarkObservationSuperseded marks an observation as superseded.
func (s *ConflictStore) MarkObservationSuperseded(ctx context.Context, obsID int64) error {
	result := s.db.WithContext(ctx).
		Model(&Observation{}).
		Where("id = ?", obsID).
		Update("is_superseded", 1)

	return result.Error
}

// MarkObservationsSuperseded marks multiple observations as superseded.
func (s *ConflictStore) MarkObservationsSuperseded(ctx context.Context, obsIDs []int64) error {
	if len(obsIDs) == 0 {
		return nil
	}

	result := s.db.WithContext(ctx).
		Model(&Observation{}).
		Where("id IN ?", obsIDs).
		Update("is_superseded", 1)

	return result.Error
}

// GetConflictsByObservationID retrieves all conflicts involving an observation.
func (s *ConflictStore) GetConflictsByObservationID(ctx context.Context, obsID int64) ([]*models.ObservationConflict, error) {
	var conflicts []ObservationConflict

	err := s.db.WithContext(ctx).
		Where("newer_obs_id = ? OR older_obs_id = ?", obsID, obsID).
		Order("detected_at_epoch DESC").
		Find(&conflicts).Error

	if err != nil {
		return nil, err
	}

	return toModelConflicts(conflicts), nil
}

// GetUnresolvedConflicts retrieves all unresolved conflicts.
func (s *ConflictStore) GetUnresolvedConflicts(ctx context.Context, limit int) ([]*models.ObservationConflict, error) {
	var conflicts []ObservationConflict

	err := s.db.WithContext(ctx).
		Where("resolved = 0").
		Order("detected_at_epoch DESC").
		Limit(limit).
		Find(&conflicts).Error

	if err != nil {
		return nil, err
	}

	return toModelConflicts(conflicts), nil
}

// GetSupersededObservationIDs returns IDs of all observations that have been superseded.
func (s *ConflictStore) GetSupersededObservationIDs(ctx context.Context, project string) ([]int64, error) {
	var ids []int64

	err := s.db.WithContext(ctx).
		Table("observation_conflicts oc").
		Select("DISTINCT oc.older_obs_id").
		Joins("JOIN observations o ON o.id = oc.older_obs_id").
		Where("oc.resolution = ?", models.ResolutionPreferNewer).
		Where("o.project = ? OR o.scope = 'global'", project).
		Pluck("oc.older_obs_id", &ids).Error

	return ids, err
}

// ResolveConflict marks a conflict as resolved.
func (s *ConflictStore) ResolveConflict(ctx context.Context, conflictID int64, resolution models.ConflictResolution) error {
	now := time.Now().Format(time.RFC3339)

	result := s.db.WithContext(ctx).
		Model(&ObservationConflict{}).
		Where("id = ?", conflictID).
		Updates(map[string]interface{}{
			"resolved":    1,
			"resolved_at": now,
			"resolution":  resolution,
		})

	return result.Error
}

// DeleteConflictsByObservationID deletes all conflicts involving an observation.
// Called when an observation is deleted.
func (s *ConflictStore) DeleteConflictsByObservationID(ctx context.Context, obsID int64) error {
	result := s.db.WithContext(ctx).
		Where("newer_obs_id = ? OR older_obs_id = ?", obsID, obsID).
		Delete(&ObservationConflict{})

	return result.Error
}

// ConflictWithDetails contains a conflict with its observation details.
type ConflictWithDetails struct {
	Conflict      *models.ObservationConflict
	NewerObsTitle string
	OlderObsTitle string
}

// GetConflictsWithDetails retrieves all conflicts with observation titles for display.
func (s *ConflictStore) GetConflictsWithDetails(ctx context.Context, project string, limit int) ([]*ConflictWithDetails, error) {
	var results []struct {
		NewerTitle sql.NullString `gorm:"column:newer_title"`
		OlderTitle sql.NullString `gorm:"column:older_title"`
		ObservationConflict
	}

	err := s.db.WithContext(ctx).
		Table("observation_conflicts oc").
		Select("oc.*, "+
			"COALESCE(newer.title, '') as newer_title, "+
			"COALESCE(older.title, '') as older_title").
		Joins("JOIN observations newer ON newer.id = oc.newer_obs_id").
		Joins("JOIN observations older ON older.id = oc.older_obs_id").
		Where("newer.project = ? OR older.project = ?", project, project).
		Order("oc.detected_at_epoch DESC").
		Limit(limit).
		Scan(&results).Error

	if err != nil {
		return nil, err
	}

	conflicts := make([]*ConflictWithDetails, len(results))
	for i, r := range results {
		conflicts[i] = &ConflictWithDetails{
			Conflict:      toModelConflict(&r.ObservationConflict),
			NewerObsTitle: r.NewerTitle.String,
			OlderObsTitle: r.OlderTitle.String,
		}
	}

	return conflicts, nil
}

// toModelConflict converts a GORM ObservationConflict to a pkg/models ObservationConflict.
func toModelConflict(c *ObservationConflict) *models.ObservationConflict {
	conflict := &models.ObservationConflict{
		ID:              c.ID,
		NewerObsID:      c.NewerObsID,
		OlderObsID:      c.OlderObsID,
		ConflictType:    c.ConflictType,
		Resolution:      c.Resolution,
		DetectedAt:      c.DetectedAt,
		DetectedAtEpoch: c.DetectedAtEpoch,
		Resolved:        c.Resolved == 1,
		Relation:        c.Relation.String,
		Confidence:      c.Confidence.String,
		Proposer:        c.Proposer.String,
		Decision:        c.Decision.String,
		SupersededObsID: c.SupersededObsID,
		ResolvedAtEpoch: c.ResolvedAtEpoch,
	}

	if c.Reason.Valid {
		conflict.Reason = c.Reason.String
	}
	if c.ResolvedAt.Valid {
		s := c.ResolvedAt.String
		conflict.ResolvedAt = &s
	}

	return conflict
}

// toModelConflicts converts a slice of GORM ObservationConflicts to pkg/models ObservationConflicts.
func toModelConflicts(conflicts []ObservationConflict) []*models.ObservationConflict {
	result := make([]*models.ObservationConflict, len(conflicts))
	for i, c := range conflicts {
		result[i] = toModelConflict(&c)
	}
	return result
}
