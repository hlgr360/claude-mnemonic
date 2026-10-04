package worker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
)

const (
	// autoMergeFirstDelay is how long after start the first automatic pass runs.
	autoMergeFirstDelay = 45 * time.Second
	// autoMergeSource is the alias source an automatic merge leaves, so it can be told from a manual one.
	autoMergeSource = "auto-merge"
	// autoMergeMaxPerPass keeps one pass small; the rest waits for the next.
	autoMergeMaxPerPass = 2
)

// runAutoMergePass merges the projects that are certainly one: the same git remote and every recorded folder of the
// smaller one gone (a checkout that moved or was deleted, not a second live clone). It is off unless the
// PROJECT_AUTO_MERGE_ENABLED setting is on. Each merge takes a backup first, leaves an alias so the old id keeps
// resolving, and is announced; anything less certain is only ever suggested. It returns how many it merged.
func (s *Service) runAutoMergePass(ctx context.Context) int {
	cfg := s.config
	if cfg == nil || !cfg.ProjectAutoMergeEnabled || !s.autoMergeRunning.CompareAndSwap(false, true) {
		return 0
	}
	defer s.autoMergeRunning.Store(false)

	rep, err := s.duplicateReport(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("project auto-merge: could not look for duplicates")
		return 0
	}
	merged := 0
	gone := map[string]bool{} // a project merged away in this pass
	for _, sg := range rep.suggestions {
		if merged >= autoMergeMaxPerPass {
			break
		}
		if !sg.AutoMergeable || gone[sg.Survivor] || gone[sg.Other] {
			continue
		}
		backup, err := s.mergeProjectsNow(ctx, sg.Other, sg.Survivor, autoMergeSource)
		if err != nil {
			log.Warn().Err(err).Str("from", sg.Other).Str("into", sg.Survivor).Msg("project auto-merge: merge failed")
			continue
		}
		gone[sg.Other] = true
		merged++
		log.Info().Str("from", sg.Other).Str("into", sg.Survivor).Str("backup", backup).Msg("Projects merged automatically (same git remote, old folder gone)")
		if s.sseBroadcaster != nil {
			s.sseBroadcaster.Broadcast(map[string]any{"type": "project", "action": "auto_merged", "project": sg.Other, "into": sg.Survivor, "backup": backup})
		}
	}
	return merged
}

// mergeProjectsNow merges project from into project into the way a person's confirmed merge does: backup first, one
// transaction, an alias left behind. It returns the backup's path.
func (s *Service) mergeProjectsNow(ctx context.Context, from, into, source string) (string, error) {
	admin := gorm.NewProjectAdminStore(s.store)
	if err := admin.CheckRemovable(ctx, from); err != nil {
		return "", err
	}
	backup, err := s.snapshotBefore(ctx, "merge-"+from)
	if err != nil {
		return "", err
	}
	if _, err := admin.Merge(ctx, from, into, source); err != nil {
		return backup, err
	}
	s.afterProjectChange("merged", from)
	s.afterProjectChange("merged", into)
	return backup, nil
}

func (s *Service) autoMergeLoop() {
	defer s.wg.Done()
	timer := time.NewTimer(autoMergeFirstDelay)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
		s.runAutoMergePass(s.ctx)
		every := 30
		if s.config != nil && s.config.ProjectAutoMergeIntervalMin >= 1 {
			every = s.config.ProjectAutoMergeIntervalMin
		}
		timer.Reset(time.Duration(every) * time.Minute)
	}
}
