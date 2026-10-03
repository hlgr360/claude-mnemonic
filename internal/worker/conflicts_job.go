package worker

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/vector/sqlitevec"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	// conflictNeighbours is how many older notes are shown to the model next to a newer one.
	conflictNeighbours = 4
	// conflictCheckTimeout bounds the question about one observation, whichever backend answers.
	conflictCheckTimeout = 2 * time.Minute
	// conflictQueryChars clips the text a neighbour search is made from.
	conflictQueryChars = 600
	// conflictProposerLLM is recorded on proposals written by the model.
	conflictProposerLLM = "llm"
)

// conflictFirstPassDelay is how long after start the first automatic pass runs.
var conflictFirstPassDelay = 45 * time.Second

// conflictProposeFunc asks how a newer observation relates to older ones. It is the seam the tests replace.
type conflictProposeFunc func(ctx context.Context, newer *models.Observation, olders []*models.Observation) ([]sdk.ConflictVerdict, error)

// conflictTypeFor maps a verdict's relation to the stored conflict type. A duplicate is stored as
// "superseded" (one of the two can go) and keeps its relation so the panel can say so.
func conflictTypeFor(relation string) (models.ConflictType, bool) {
	switch relation {
	case sdk.RelationSupersedes, sdk.RelationDuplicate:
		return models.ConflictSuperseded, true
	case sdk.RelationContradicts:
		return models.ConflictContradicts, true
	}
	return "", false
}

// conflictQuery is the text an observation's neighbours are looked up with.
func conflictQuery(o *models.Observation) string {
	text := strings.TrimSpace(o.Title.String + ". " + o.Narrative.String)
	if r := []rune(text); len(r) > conflictQueryChars {
		text = string(r[:conflictQueryChars])
	}
	return text
}

// olderNeighbours finds the live observations of the same project that are older than obs and close to it.
func (s *Service) olderNeighbours(ctx context.Context, obs *models.Observation, minSimilarity float64) ([]*models.Observation, bool, error) {
	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()
	if observationStore == nil {
		return nil, false, nil
	}
	results, ok, err := s.vectorSearch(ctx, conflictQuery(obs), conflictNeighbours*vectorFetchFactor*2,
		sqlitevec.BuildWhereFilter(sqlitevec.DocTypeObservation, obs.Project))
	if err != nil || !ok {
		return nil, ok, err
	}
	var ids []int64
	for _, h := range distinctObservationHits(results, minSimilarity, "") {
		if h.id != obs.ID {
			ids = append(ids, h.id)
		}
	}
	if len(ids) == 0 {
		return nil, true, nil
	}
	found, err := observationStore.GetObservationsByIDsPreserveOrder(ctx, ids)
	if err != nil {
		return nil, true, err
	}
	var out []*models.Observation
	for _, o := range found {
		if o.IsSuperseded || o.Project != obs.Project || o.CreatedAtEpoch >= obs.CreatedAtEpoch {
			continue
		}
		out = append(out, o)
		if len(out) == conflictNeighbours {
			break
		}
	}
	return out, true, nil
}

// checkObservationForConflicts looks at one observation: finds its older neighbours, asks the model and
// records the proposals. It returns how many new proposals were written. The observation is marked as checked
// by the caller, and only when this succeeds, so a failed call is tried again next time.
func (s *Service) checkObservationForConflicts(ctx context.Context, propose conflictProposeFunc, store *gorm.ConflictStore, obs *models.Observation) (int, error) {
	olders, ok, err := s.olderNeighbours(ctx, obs, s.config.ConflictProposalsMinSim)
	if err != nil {
		return 0, err
	}
	if !ok {
		return -1, nil // no vector search: nothing was checked
	}
	if len(olders) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, conflictCheckTimeout)
	defer cancel()
	verdicts, err := propose(ctx, obs, olders)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, v := range verdicts {
		kind, proposable := conflictTypeFor(v.Relation)
		if !proposable || !v.Proposable() {
			continue
		}
		_, created, err := store.Propose(ctx, gorm.Proposal{
			Type: kind, Relation: v.Relation, Confidence: v.Confidence, Reason: v.Reason,
			Proposer: conflictProposerLLM, NewerID: obs.ID, OlderID: v.OlderID,
		})
		if err != nil {
			log.Warn().Err(err).Int64("newer", obs.ID).Int64("older", v.OlderID).Msg("conflict proposals: could not store a proposal")
			continue
		}
		if created {
			written++
		}
	}
	return written, nil
}

// runConflictPass looks at the observations the proposer has not seen yet, at most ConflictProposalsMaxPerRun
// of them, newest first, and stores what it finds as proposals. It returns how many proposals it wrote.
func (s *Service) runConflictPass(ctx context.Context) int {
	cfg := s.config
	if cfg == nil || !s.conflictRunning.CompareAndSwap(false, true) {
		return 0
	}
	defer s.conflictRunning.Store(false)

	s.initMu.RLock()
	processor, store := s.processor, s.conflictStore
	s.initMu.RUnlock()
	propose := s.conflictProposer
	if propose == nil {
		if processor == nil {
			return 0
		}
		propose = processor.ProposeConflicts
	}
	if store == nil {
		return 0
	}

	projectNames, err := store.ProjectsWithUncheckedObservations(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("conflict proposals: could not look for observations to check")
		return 0
	}
	budget := cfg.ConflictProposalsMaxPerRun
	if budget <= 0 {
		budget = 20
	}
	proposals := 0
	for _, project := range projectNames {
		if budget <= 0 || ctx.Err() != nil {
			break
		}
		batch, err := store.UncheckedObservations(ctx, project, budget)
		if err != nil {
			log.Warn().Err(err).Str("project", project).Msg("conflict proposals: could not read observations")
			continue
		}
		for _, obs := range batch {
			if budget <= 0 || ctx.Err() != nil {
				break
			}
			n, err := s.checkObservationForConflicts(ctx, propose, store, obs)
			if err != nil {
				log.Warn().Err(err).Int64("observation", obs.ID).Msg("conflict proposals: check failed, will retry")
				budget--
				continue
			}
			if n < 0 {
				return proposals // vector search is not available yet
			}
			if err := store.MarkChecked(ctx, obs.ID, n); err != nil {
				log.Warn().Err(err).Int64("observation", obs.ID).Msg("conflict proposals: could not mark an observation as checked")
			}
			budget--
			proposals += n
		}
	}
	if proposals > 0 {
		log.Info().Int("proposals", proposals).Msg("conflict proposals: new proposals to review")
		s.broadcastConflictChange("proposed")
	}
	return proposals
}

// conflictLoop runs runConflictPass now and then. It only runs when the proposals are switched on.
func (s *Service) conflictLoop() {
	defer s.wg.Done()

	interval := time.Duration(s.config.ConflictProposalsIntervalMin) * time.Minute
	if interval <= 0 {
		interval = time.Hour
	}
	timer := time.NewTimer(conflictFirstPassDelay)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.runConflictPass(s.ctx)
			timer.Reset(interval)
		}
	}
}

// broadcastConflictChange tells open dashboards that the list of proposals changed.
func (s *Service) broadcastConflictChange(action string) {
	if s.sseBroadcaster == nil {
		return
	}
	s.sseBroadcaster.Broadcast(map[string]any{"type": "conflict", "action": action})
}
