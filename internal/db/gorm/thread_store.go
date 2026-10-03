// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// ThreadSessionPrefix marks the session ids of thread notes, so they can be told apart from the
// end-of-session summaries Claude Code writes into the same table.
const ThreadSessionPrefix = "thread-"

// UpsertThreadSummary creates or updates the living note for one thread of work.
//
// Claude Desktop chat has no hooks to record its work, so the model saves where a piece of work
// stands. The note is kept in place: saving the same thread again replaces its content and moves it
// to the front, so a long session leaves one current note per thread, not one row per save.
//
// sdkSessionID must be unique per project and thread. It reads and then writes, so it takes the write
// lock up front (see immediateTx).
func (s *SummaryStore) UpsertThreadSummary(ctx context.Context, sdkSessionID, project string, summary *models.ParsedSummary) (id int64, created bool, err error) {
	now := time.Now()
	err = immediateTx(ctx, s.db, func(tx *gorm.DB) error {
		if err := EnsureSessionExists(ctx, tx, sdkSessionID, project); err != nil {
			return err
		}

		var existing SessionSummary
		findErr := tx.Where("sdk_session_id = ? AND project = ?", sdkSessionID, project).First(&existing).Error
		switch {
		case findErr == nil:
			existing.Request = nullString(summary.Request)
			existing.Investigated = nullString(summary.Investigated)
			existing.Learned = nullString(summary.Learned)
			existing.Completed = nullString(summary.Completed)
			existing.NextSteps = nullString(summary.NextSteps)
			existing.Notes = nullString(summary.Notes)
			// For a living note the timestamp means "last updated", which is what recency ordering needs.
			existing.CreatedAt = now.Format(time.RFC3339)
			existing.CreatedAtEpoch = now.UnixMilli()
			id = existing.ID
			return tx.Save(&existing).Error
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			row := &SessionSummary{
				SDKSessionID: sdkSessionID, Project: project,
				Request: nullString(summary.Request), Investigated: nullString(summary.Investigated),
				Learned: nullString(summary.Learned), Completed: nullString(summary.Completed),
				NextSteps: nullString(summary.NextSteps), Notes: nullString(summary.Notes),
				PromptNumber: sql.NullInt64{}, CreatedAt: now.Format(time.RFC3339), CreatedAtEpoch: now.UnixMilli(),
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
			id, created = row.ID, true
			return nil
		default:
			return findErr
		}
	})
	return id, created, err
}

// GetThreadSummaries returns the project's thread notes, most recently updated first.
func (s *SummaryStore) GetThreadSummaries(ctx context.Context, project string, limit int) ([]*models.SessionSummary, error) {
	if limit <= 0 {
		limit = 5
	}
	var rows []SessionSummary
	err := s.db.WithContext(ctx).
		Where("project = ? AND sdk_session_id LIKE ?", project, ThreadSessionPrefix+"%").
		Order("created_at_epoch DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return toModelSessionSummaries(rows), nil
}

// DecisionRef is a short reference to a recorded decision.
type DecisionRef struct {
	Title        string
	Subtitle     string
	CreatedAt    string
	CreatedEpoch int64
	ID           int64
}

// RecentDecisions returns the project's most recent live decisions (not archived or superseded).
func (s *SummaryStore) RecentDecisions(ctx context.Context, project string, limit int) ([]DecisionRef, error) {
	if limit <= 0 {
		limit = 5
	}
	var rows []Observation
	err := s.db.WithContext(ctx).
		Where("project = ? AND type = ?", project, string(models.ObsTypeDecision)).
		Where("(is_archived = 0 OR is_archived IS NULL) AND (is_superseded = 0 OR is_superseded IS NULL)").
		Order("created_at_epoch DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]DecisionRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, DecisionRef{
			ID: r.ID, Title: strings.TrimSpace(r.Title.String), Subtitle: strings.TrimSpace(r.Subtitle.String),
			CreatedAt: r.CreatedAt, CreatedEpoch: r.CreatedAtEpoch,
		})
	}
	return out, nil
}
