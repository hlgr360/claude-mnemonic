// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"errors"
	"sort"

	"gorm.io/gorm"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// BriefSessionPrefix marks the session summary row that holds a project's brief, like ThreadSessionPrefix
// does for thread notes. The row's session id is BriefSessionID(project), so a project has one brief.
const BriefSessionPrefix = "brief-"

// BriefSessionID is the session id of a project's brief row.
func BriefSessionID(project string) string { return BriefSessionPrefix + project }

// Brief is a project's current brief as stored.
type Brief struct {
	Project string
	// Text is the finished brief, ready to show.
	Text string
	// Source says what it was written from, for example "71 of 71 observations and 3 checkpoint notes".
	Source string
	ID     int64
	// GeneratedEpoch is when it was written, in milliseconds.
	GeneratedEpoch int64
}

// UpsertBrief stores a project's brief, replacing the previous one in place. It reads then writes, so it
// takes the write lock up front (see immediateTx).
func (s *SummaryStore) UpsertBrief(ctx context.Context, project, text, source string) (id int64, created bool, err error) {
	return s.UpsertThreadSummary(ctx, BriefSessionID(project), project, &models.ParsedSummary{
		Request:      "Project brief",
		Investigated: source,
		Notes:        text,
	})
}

// GetBrief returns a project's brief, or nil when it has none.
func (s *SummaryStore) GetBrief(ctx context.Context, project string) (*Brief, error) {
	var row SessionSummary
	err := s.db.WithContext(ctx).
		Where("sdk_session_id = ? AND project = ?", BriefSessionID(project), project).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Brief{
		ID: row.ID, Project: row.Project, Text: row.Notes.String, Source: row.Investigated.String,
		GeneratedEpoch: row.CreatedAtEpoch,
	}, nil
}

// liveProjectObservations is the project's own observations that still count: not archived and not
// superseded. A note's scope only decides where it is injected, so the project's global notes are its own too
// (they used to be left out, and with most notes global a brief was written from one note in ten).
func liveProjectObservations(db *gorm.DB, project string) *gorm.DB {
	return db.Model(&Observation{}).
		Where("project = ?", project).
		Where("COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0")
}

// BriefInputs returns up to limit of a project's live observations, the most important first picked and
// then ordered oldest first, together with how many live observations the project has in all.
func (s *ObservationStore) BriefInputs(ctx context.Context, project string, limit int) ([]*models.Observation, int, error) {
	var total int64
	if err := liveProjectObservations(s.db.WithContext(ctx), project).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []Observation
	err := liveProjectObservations(s.db.WithContext(ctx), project).
		Scopes(importanceOrdering()).
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	obs := toModelObservations(rows)
	// Oldest first, so the brief reads in the order things happened.
	sort.SliceStable(obs, func(i, j int) bool {
		if obs[i].CreatedAtEpoch != obs[j].CreatedAtEpoch {
			return obs[i].CreatedAtEpoch < obs[j].CreatedAtEpoch
		}
		return obs[i].ID < obs[j].ID
	})
	return obs, int(total), nil
}

// CountLiveObservationsSince counts a project's live observations created after sinceEpoch (milliseconds).
func (s *ObservationStore) CountLiveObservationsSince(ctx context.Context, project string, sinceEpoch int64) (int, error) {
	var n int64
	err := liveProjectObservations(s.db.WithContext(ctx), project).
		Where("created_at_epoch > ?", sinceEpoch).
		Count(&n).Error
	return int(n), err
}
