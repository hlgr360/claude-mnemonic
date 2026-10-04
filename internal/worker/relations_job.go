package worker

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	// relationBatch is how many observations one pass looks at.
	relationBatch = 100
	// relationFetch is how many vector documents (several per observation) a neighbour search asks for. Wider
	// than the conflict check's, because relations want the project's own notes among the neighbours of every
	// project.
	relationFetch = 300
)

var (
	// relationFirstDelay is how long after start the first pass runs.
	relationFirstDelay = 60 * time.Second
	// relationInterval is how long to wait between passes when everything has been looked at.
	relationInterval = 10 * time.Minute
	// relationBusyInterval is how long to wait when more is waiting, which is how an existing archive is
	// worked through (the backfill) without a long single pass.
	relationBusyInterval = 30 * time.Second
)

// similarOlderFunc finds the older notes close to an observation. It is the seam the tests replace.
type similarOlderFunc func(ctx context.Context, obs *models.Observation, minSimilarity float64, limit, fetch int) ([]similarNote, bool, error)

// typedRelation says whether the rules may give a relation this type. They never decide that one note
// supersedes another (a person does, in the conflict review), and "causes" only came from a rule that does not
// look at the older note at all.
func typedRelation(t models.RelationType) bool {
	switch t {
	case models.RelationFixes, models.RelationDependsOn, models.RelationEvolvesFrom:
		return true
	}
	return false
}

// relationFor makes the relation from a newer note to an older one it is close to. The existing rules (shared
// files, type progression) may name a more specific type; otherwise the notes simply relate. The confidence is how
// close the two are.
func relationFor(newer, older *models.Observation, sim float64) *models.ObservationRelation {
	var best *models.RelationDetectionResult
	for _, r := range []*models.RelationDetectionResult{
		models.DetectFileOverlapRelation(newer, older),
		models.DetectTypeProgressionRelation(newer, older),
	} {
		if r != nil && typedRelation(r.RelationType) && (best == nil || r.Confidence > best.Confidence) {
			best = r
		}
	}
	confidence := math.Round(sim*1000) / 1000
	if best != nil {
		return models.NewObservationRelation(newer.ID, older.ID, best.RelationType, confidence, best.DetectionSource, best.Reason)
	}
	return models.NewObservationRelation(newer.ID, older.ID, models.RelationRelatesTo, confidence,
		models.DetectionSourceEmbeddingSimilarity, fmt.Sprintf("similar content (%.2f)", sim))
}

// runRelationPass looks at the observations the builder has not seen yet, at most relationBatch of them, newest
// first, and stores the relations it finds. It returns how many observations it looked at and whether more are
// waiting.
func (s *Service) runRelationPass(ctx context.Context) (looked int, more bool) {
	cfg := s.config
	if cfg == nil || !cfg.GraphEnabled || !s.relationRunning.CompareAndSwap(false, true) {
		return 0, false
	}
	defer s.relationRunning.Store(false)

	s.initMu.RLock()
	store := s.relationStore
	s.initMu.RUnlock()
	neighbours := s.relationNeighbours
	if neighbours == nil {
		neighbours = s.similarOlder
	}
	if store == nil {
		return 0, false
	}

	batch, err := store.UncheckedForRelations(ctx, relationBatch)
	if err != nil {
		log.Warn().Err(err).Msg("graph: could not read the observations to relate")
		return 0, false
	}
	maxPer := cfg.GraphRelationsMaxPerObs
	if maxPer <= 0 {
		maxPer = 3
	}
	created := 0
	for _, obs := range batch {
		if ctx.Err() != nil {
			break
		}
		found, ok, err := neighbours(ctx, obs, cfg.GraphRelationsMinSim, maxPer, relationFetch)
		if err != nil {
			log.Warn().Err(err).Int64("observation", obs.ID).Msg("graph: neighbour search failed, will retry")
			continue
		}
		if !ok {
			break // no vector search yet: nothing was looked at, try again later
		}
		relations := make([]*models.ObservationRelation, 0, len(found))
		for _, f := range found {
			relations = append(relations, relationFor(obs, f.obs, f.sim))
		}
		if len(relations) > 0 {
			if err := store.StoreRelations(ctx, relations); err != nil {
				log.Warn().Err(err).Int64("observation", obs.ID).Msg("graph: could not store relations, will retry")
				continue
			}
		}
		if err := store.MarkRelationChecked(ctx, obs.ID, len(relations)); err != nil {
			log.Warn().Err(err).Int64("observation", obs.ID).Msg("graph: could not mark an observation as looked at")
		}
		looked++
		created += len(relations)
	}
	if created > 0 {
		log.Info().Int("observations", looked).Int("relations", created).Msg("graph: relations created")
		s.broadcastGraphChange("updated")
	}
	return looked, len(batch) == relationBatch && looked > 0
}

// relationLoop builds relations in the background: a pass shortly after start, then at an interval, and sooner
// again while more observations are waiting.
func (s *Service) relationLoop() {
	defer s.wg.Done()

	timer := time.NewTimer(relationFirstDelay)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		case <-s.relationWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		_, more := s.runRelationPass(s.ctx)
		next := relationInterval
		if more {
			next = relationBusyInterval
		}
		timer.Reset(next)
	}
}

// wakeRelationLoop asks the loop to run a pass now, for example after the graph was reset. It never blocks, and
// does nothing when the loop is not running.
func (s *Service) wakeRelationLoop() {
	select {
	case s.relationWake <- struct{}{}:
	default:
	}
}

// broadcastGraphChange tells open dashboards that the graph changed.
func (s *Service) broadcastGraphChange(action string) {
	if s.sseBroadcaster == nil {
		return
	}
	s.sseBroadcaster.Broadcast(map[string]any{"type": "graph", "action": action})
}
