package worker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
)

// snapshotFirstPassDelay is how long after start the first regular snapshot is considered, so a restarting worker is not
// slowed down by it. A variable so a test does not wait.
var snapshotFirstPassDelay = 30 * time.Second

const (
	// snapshotCheckEvery is how often the loop looks at whether a regular snapshot is due.
	snapshotCheckEvery = time.Hour
	// cleanupSnapshotMinGap is how recent a snapshot of any kind must be for a cleanup to skip taking its own: a burst of
	// archived notes should not write a copy of the database each time.
	cleanupSnapshotMinGap = time.Hour
)

// snapshotDue says whether a regular snapshot is due: there is none yet, or the newest is at least interval old.
// An interval of zero or less is never.
func snapshotDue(newest, now time.Time, interval time.Duration) bool {
	return interval > 0 && (newest.IsZero() || now.Sub(newest) >= interval)
}

// runSnapshotPass takes the regular snapshot when it is due and returns its path ("" when none was taken).
func (s *Service) runSnapshotPass(ctx context.Context) (string, error) {
	s.initMu.RLock()
	store, cfg := s.store, s.config
	s.initMu.RUnlock()
	if store == nil || cfg == nil || cfg.SnapshotIntervalHours <= 0 {
		return "", nil
	}
	dir := store.DefaultSnapshotDir()
	if !snapshotDue(gorm.NewestDailySnapshot(dir), time.Now(), time.Duration(cfg.SnapshotIntervalHours)*time.Hour) {
		return "", nil
	}
	keep := cfg.SnapshotsDailyKeep
	if keep <= 0 {
		keep = 7
	}
	path, err := store.Snapshot(ctx, dir, gorm.DailySnapshotLabel, keep)
	if err != nil {
		return "", err
	}
	log.Info().Str("path", path).Int("keep", keep).Msg("Regular snapshot written")
	return path, nil
}

// snapshotLoop takes the regular snapshot now and then. It only runs when the interval is above zero.
func (s *Service) snapshotLoop() {
	defer s.wg.Done()

	timer := time.NewTimer(snapshotFirstPassDelay)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
			if _, err := s.runSnapshotPass(ctx); err != nil && s.ctx.Err() == nil {
				log.Warn().Err(err).Msg("Regular snapshot failed")
			}
			cancel()
			timer.Reset(snapshotCheckEvery)
		}
	}
}

// snapshotBeforeCleanup takes a snapshot before the cap archives notes, unless a snapshot of any kind is under an hour
// old (the regular one, or one taken for an earlier cleanup).
func (s *Service) snapshotBeforeCleanup(ctx context.Context, reason string) {
	s.initMu.RLock()
	store := s.store
	s.initMu.RUnlock()
	if store == nil {
		return
	}
	dir := store.DefaultSnapshotDir()
	if _, newest := gorm.SnapshotSummary(dir); !newest.IsZero() && time.Since(newest) < cleanupSnapshotMinGap {
		return
	}
	path, err := store.Snapshot(ctx, dir, "before-"+reason, snapshotsToKeep)
	if err != nil {
		log.Warn().Err(err).Str("reason", reason).Msg("Snapshot before cleanup failed; continuing")
		return
	}
	log.Info().Str("path", path).Str("reason", reason).Msg("Snapshot taken before archiving notes")
}
