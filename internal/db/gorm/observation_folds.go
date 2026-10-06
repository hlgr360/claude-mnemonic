// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// Kinds of fold: notes that were folded into one that stands for them.
const (
	// FoldRollup condenses older notes into a roll-up note; the originals are archived and the roll-up is a new note.
	FoldRollup = "rollup"
	// FoldConsolidation merges near-duplicate notes into one of them (the survivor); the others are archived.
	FoldConsolidation = "consolidation"
)

// Archive reasons that link an archived note to the note that stands for it. They end in "#<id>" of that note.
const (
	RolledUpReasonPrefix     = "rolled-up into #"
	ConsolidatedReasonPrefix = "consolidated into #"
)

// RollupConcept marks a roll-up note. The note type has a fixed set of values in the database, so a roll-up is an
// ordinary note that carries this concept (and is listed in ObservationFold).
const RollupConcept = "rollup"

// FoldReason is the archive reason of the sources of a fold, for the note that stands for them.
func FoldReason(kind string, survivorID int64) string {
	prefix := RolledUpReasonPrefix
	if kind == FoldConsolidation {
		prefix = ConsolidatedReasonPrefix
	}
	return fmt.Sprintf("%s%d", prefix, survivorID)
}

// ObservationFold records that notes were folded into another note: a roll-up of older notes, or a consolidation of
// near-duplicates. It is what restoring works from, and what the dashboard lists.
type ObservationFold struct {
	Project string `gorm:"index;not null"`
	Kind    string `gorm:"index;not null"`
	// SourceIDs is a JSON array of the ids of the notes that were archived into the survivor.
	SourceIDs string `gorm:"type:text;not null"`
	Note      string `gorm:"type:text;not null;default:''"`
	// Detail is JSON that undoing needs beyond the source ids: what a consolidation added to the survivor.
	Detail         string `gorm:"type:text;not null;default:''"`
	ID             int64  `gorm:"primaryKey;autoIncrement"`
	SurvivorID     int64  `gorm:"index;not null"`
	CreatedAtEpoch int64  `gorm:"not null"`
	// UndoneAtEpoch is set when the fold was undone (restored); the row stays as a record.
	UndoneAtEpoch sql.NullInt64
}

func (ObservationFold) TableName() string { return "observation_folds" }

// Sources returns the ids of the notes that were folded.
func (f *ObservationFold) Sources() []int64 {
	var ids []int64
	_ = json.Unmarshal([]byte(f.SourceIDs), &ids)
	return ids
}

// Undone says whether the fold was restored.
func (f *ObservationFold) Undone() bool { return f.UndoneAtEpoch.Valid }

// ObservationFoldStore keeps the record of folds.
type ObservationFoldStore struct {
	db *gorm.DB
}

// NewObservationFoldStore creates a fold store.
func NewObservationFoldStore(store *Store) *ObservationFoldStore {
	return &ObservationFoldStore{db: store.DB}
}

// Record stores a fold and returns its id.
func (s *ObservationFoldStore) Record(ctx context.Context, project, kind string, survivorID int64, sources []int64, note, detail string) (int64, error) {
	if len(sources) == 0 {
		return 0, errors.New("a fold needs at least one source note")
	}
	raw, err := json.Marshal(sources)
	if err != nil {
		return 0, err
	}
	row := &ObservationFold{
		Project: project, Kind: kind, SurvivorID: survivorID, SourceIDs: string(raw), Note: note, Detail: detail,
		CreatedAtEpoch: time.Now().UnixMilli(),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return 0, fmt.Errorf("record fold: %w", err)
	}
	return row.ID, nil
}

// Get returns a fold, or nil when there is none with that id.
func (s *ObservationFoldStore) Get(ctx context.Context, id int64) (*ObservationFold, error) {
	var row ObservationFold
	err := s.db.WithContext(ctx).First(&row, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// List returns folds, newest first. Empty project or kind means any; undone ones are left out unless asked for.
func (s *ObservationFoldStore) List(ctx context.Context, project, kind string, includeUndone bool, limit int) ([]ObservationFold, error) {
	q := s.db.WithContext(ctx).Model(&ObservationFold{}).Order("created_at_epoch DESC, id DESC")
	if project != "" {
		q = q.Where("project = ?", project)
	}
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if !includeUndone {
		q = q.Where("undone_at_epoch IS NULL")
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []ObservationFold
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkUndone marks a fold as restored. It reports false when it was undone already (or does not exist), so two
// restores at once cannot both go on.
func (s *ObservationFoldStore) MarkUndone(ctx context.Context, id int64) (bool, error) {
	res := s.db.WithContext(ctx).Model(&ObservationFold{}).
		Where("id = ? AND undone_at_epoch IS NULL", id).
		Update("undone_at_epoch", time.Now().UnixMilli())
	return res.RowsAffected > 0, res.Error
}

// protectedFromFolding is the rule for notes that are never rolled up or consolidated automatically: decisions,
// notes a person rated, notes whose scope was set by hand, global notes (they belong to more than one project) and
// roll-ups themselves.
const protectedFromFolding = `COALESCE(type, '') = 'decision' OR COALESCE(user_feedback, 0) != 0 OR COALESCE(scope_source, '') = 'explicit'
	OR COALESCE(scope, '') = 'global' OR COALESCE(concepts, '') LIKE '%"` + RollupConcept + `"%'`

// ProtectedFromFolding returns which of the given notes are protected (see protectedFromFolding), so a caller that is
// handed ids (a consolidation) applies the same rule as the roll-up's own selection.
func (s *ObservationStore) ProtectedFromFolding(ctx context.Context, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	var hit []int64
	if err := s.db.WithContext(ctx).Model(&Observation{}).Where("id IN ?", ids).Where("("+protectedFromFolding+")").Pluck("id", &hit).Error; err != nil {
		return nil, err
	}
	for _, id := range hit {
		out[id] = true
	}
	return out, nil
}

// RollupCandidates returns the notes of a project that a roll-up may condense, oldest first: live notes created
// before cutoffEpoch, except the newest keepNewest live notes, and the notes the cap archived (they are hidden, and a
// roll-up brings what they said back into search). Protected notes are never returned.
func (s *ObservationStore) RollupCandidates(ctx context.Context, project string, cutoffEpoch int64, keepNewest int) ([]*models.Observation, error) {
	live := "COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0"
	var newest []int64
	if keepNewest > 0 {
		err := s.db.WithContext(ctx).Model(&Observation{}).
			Where("project = ?", project).Where(live).
			Order("created_at_epoch DESC, id DESC").Limit(keepNewest).Pluck("id", &newest).Error
		if err != nil {
			return nil, err
		}
	}

	q := s.db.WithContext(ctx).Where("project = ?", project).Where("NOT (" + protectedFromFolding + ")")
	liveOld := s.db.Where(live).Where("created_at_epoch < ?", cutoffEpoch)
	if len(newest) > 0 {
		liveOld = liveOld.Where("id NOT IN ?", newest)
	}
	capped := s.db.Where("is_archived = 1 AND archived_reason = ?", ArchivedByCapReason)
	var rows []Observation
	if err := q.Where(s.db.Where(liveOld).Or(capped)).Order("created_at_epoch ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return toModelObservations(rows), nil
}

// ArchiveInto archives notes with a reason that links them to the note that stands for them, and returns the ids
// that were archived. A note that is already folded into another one is left alone.
func (s *ObservationStore) ArchiveInto(ctx context.Context, ids []int64, reason string) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var done []int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Observation{}).
			Where("id IN ?", ids).
			Where("COALESCE(archived_reason, '') NOT LIKE ? AND COALESCE(archived_reason, '') NOT LIKE ?",
				RolledUpReasonPrefix+"%", ConsolidatedReasonPrefix+"%").
			Pluck("id", &done).Error; err != nil || len(done) == 0 {
			return err
		}
		return tx.Model(&Observation{}).Where("id IN ?", done).Updates(map[string]any{
			"is_archived":       1,
			"archived_at_epoch": time.Now().UnixMilli(),
			"archived_reason":   reason,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return done, nil
}

// UnarchiveWithReason restores the archived notes among ids whose archive reason is exactly reason (so a note a
// person archived for another reason stays archived) and returns the ids that were restored.
func (s *ObservationStore) UnarchiveWithReason(ctx context.Context, ids []int64, reason string) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var done []int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Observation{}).
			Where("id IN ? AND is_archived = 1 AND archived_reason = ?", ids, reason).
			Pluck("id", &done).Error; err != nil || len(done) == 0 {
			return err
		}
		return tx.Model(&Observation{}).Where("id IN ?", done).Updates(map[string]any{
			"is_archived":       0,
			"archived_at_epoch": nil,
			"archived_reason":   nil,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return done, nil
}

// SetObservationCreated moves a note's creation time. A roll-up is dated at the newest note it condenses, so it sits
// in history where its sources were and does not look like new work.
func (s *ObservationStore) SetObservationCreated(ctx context.Context, id, epochMs int64) error {
	return s.db.WithContext(ctx).Model(&Observation{}).Where("id = ?", id).Updates(map[string]any{
		"created_at_epoch": epochMs,
		"created_at":       time.UnixMilli(epochMs).Format(time.RFC3339),
	}).Error
}

// ConsolidationCandidates returns the newest live notes of a project that may be consolidated automatically (at most
// limit), newest first. Protected notes are never returned.
func (s *ObservationStore) ConsolidationCandidates(ctx context.Context, project string, limit int) ([]*models.Observation, error) {
	var rows []Observation
	q := s.db.WithContext(ctx).Where("project = ?", project).
		Where("COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0").
		Where("NOT (" + protectedFromFolding + ")").
		Order("created_at_epoch DESC, id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return toModelObservations(rows), nil
}
