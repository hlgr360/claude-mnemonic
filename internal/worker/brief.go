package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/projects"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
)

const (
	// briefObservationLimit is how many of a project's observations a brief is written from.
	briefObservationLimit = 100
	briefThreadLimit      = 8
	// briefGenerateTimeout bounds one brief, whichever backend writes it.
	briefGenerateTimeout = 3 * time.Minute
)

// briefFirstPassDelay is how long after start the first automatic pass runs, so a restarting worker is not
// busy writing briefs while it is still starting up.
var briefFirstPassDelay = 30 * time.Second

var (
	// ErrBriefUnavailable means there is nothing that can write a brief (no Claude CLI and no local model).
	ErrBriefUnavailable = errors.New("no LLM backend is available to write a brief")
	// ErrBriefBusy means a brief for the project is being written right now.
	ErrBriefBusy = errors.New("a brief for this project is already being written")
)

// briefDue says whether a project's brief should be written (or rewritten): after enough new observations,
// or when it is old and something is new. liveObservations is the project's live observations in all;
// newObservations those created since the current brief was written.
func briefDue(cfg *config.Config, now time.Time, hasBrief bool, generatedEpoch int64, newObservations, liveObservations int) bool {
	minNew := cfg.ProjectBriefMinNewObs
	if minNew <= 0 {
		minNew = 10
	}
	if !hasBrief {
		return liveObservations >= minNew
	}
	if newObservations >= minNew {
		return true
	}
	maxAge := time.Duration(cfg.ProjectBriefMaxAgeDays) * 24 * time.Hour
	return newObservations >= 1 && maxAge > 0 && now.Sub(time.UnixMilli(generatedEpoch)) >= maxAge
}

type briefCandidate struct {
	project         string
	newObservations int
}

// briefCandidates lists the projects whose brief is due, the ones with the most new observations first.
func (s *Service) briefCandidates(ctx context.Context, cfg *config.Config, now time.Time) ([]briefCandidate, error) {
	s.initMu.RLock()
	sessionStore, observationStore, summaryStore, store := s.sessionStore, s.observationStore, s.summaryStore, s.store
	s.initMu.RUnlock()
	if sessionStore == nil || observationStore == nil || summaryStore == nil || store == nil {
		return nil, nil
	}

	rows, err := sessionStore.ProjectSummaries(ctx)
	if err != nil {
		return nil, err
	}
	aliases, err := gorm.NewProjectAliasStore(store).AliasMap(ctx)
	if err != nil {
		return nil, err
	}

	var out []briefCandidate
	for _, r := range rows {
		if _, isAlias := aliases[r.Project]; isAlias || r.Observations == 0 {
			continue
		}
		brief, err := summaryStore.GetBrief(ctx, r.Project)
		if err != nil {
			return nil, err
		}
		since := int64(0)
		if brief != nil {
			since = brief.GeneratedEpoch
		}
		fresh, err := observationStore.CountLiveObservationsSince(ctx, r.Project, since)
		if err != nil {
			return nil, err
		}
		live := fresh
		if brief != nil {
			if live, err = observationStore.CountLiveObservationsSince(ctx, r.Project, 0); err != nil {
				return nil, err
			}
		}
		generated := int64(0)
		if brief != nil {
			generated = brief.GeneratedEpoch
		}
		if briefDue(cfg, now, brief != nil, generated, fresh, live) {
			out = append(out, briefCandidate{project: r.Project, newObservations: fresh})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].newObservations != out[j].newObservations {
			return out[i].newObservations > out[j].newObservations
		}
		return out[i].project < out[j].project
	})
	return out, nil
}

// runBriefPass writes the briefs that are due, at most ProjectBriefMaxPerRun of them. It returns how many.
func (s *Service) runBriefPass(ctx context.Context) int {
	cfg := s.config
	if cfg == nil {
		return 0
	}
	candidates, err := s.briefCandidates(ctx, cfg, time.Now())
	if err != nil {
		log.Warn().Err(err).Msg("project brief: could not look for projects that need a brief")
		return 0
	}
	maxPerRun := cfg.ProjectBriefMaxPerRun
	if maxPerRun <= 0 {
		maxPerRun = 3
	}
	written := 0
	for _, c := range candidates {
		if written >= maxPerRun || ctx.Err() != nil {
			break
		}
		if _, err := s.generateBrief(ctx, c.project); err != nil {
			log.Warn().Err(err).Str("project", c.project).Msg("project brief: failed to write a brief")
			continue
		}
		written++
		log.Info().Str("project", c.project).Int("new_observations", c.newObservations).Msg("project brief: written")
	}
	return written
}

// briefLoop runs runBriefPass now and then. It only runs when the briefs are switched on.
func (s *Service) briefLoop() {
	defer s.wg.Done()

	interval := time.Duration(s.config.ProjectBriefIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Hour
	}
	timer := time.NewTimer(briefFirstPassDelay)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.runBriefPass(s.ctx)
			timer.Reset(interval)
		}
	}
}

// generateBrief writes, stores and announces one project's brief. Only one brief per project is written at
// a time, whoever asks.
func (s *Service) generateBrief(ctx context.Context, project string) (*gorm.Brief, error) {
	s.initMu.RLock()
	processor, observationStore, summaryStore := s.processor, s.observationStore, s.summaryStore
	s.initMu.RUnlock()
	if observationStore == nil || summaryStore == nil {
		return nil, ErrBriefUnavailable
	}
	write := s.briefWriter
	if write == nil {
		if processor == nil {
			return nil, ErrBriefUnavailable
		}
		write = processor.GenerateBrief
	}

	s.briefMu.Lock()
	if _, busy := s.briefRunning[project]; busy {
		s.briefMu.Unlock()
		return nil, ErrBriefBusy
	}
	if s.briefRunning == nil {
		s.briefRunning = map[string]struct{}{}
	}
	s.briefRunning[project] = struct{}{}
	s.briefMu.Unlock()
	defer func() {
		s.briefMu.Lock()
		delete(s.briefRunning, project)
		s.briefMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, briefGenerateTimeout)
	defer cancel()

	obs, total, err := observationStore.BriefInputs(ctx, project, briefObservationLimit)
	if err != nil {
		return nil, fmt.Errorf("reading observations: %w", err)
	}
	threads, err := summaryStore.GetThreadSummaries(ctx, project, briefThreadLimit)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint notes: %w", err)
	}

	res, err := write(ctx, sdk.BriefInput{
		Now: time.Now(), Name: projects.DisplayName(project), Observations: obs, Total: total, Threads: threads,
	})
	if err != nil {
		return nil, err
	}
	id, created, err := summaryStore.UpsertBrief(ctx, project, res.Text, res.Source)
	if err != nil {
		return nil, fmt.Errorf("storing the brief: %w", err)
	}
	s.afterSummaryWrite(id, project, created, "brief")
	return summaryStore.GetBrief(ctx, project)
}

// BriefResponse is the body of GET and POST /api/projects/{id}/brief.
type BriefResponse struct {
	Project string `json:"project"`
	// Text is the finished brief: a dated header, the sections and the open threads.
	Text string `json:"text"`
	// Source says what it was written from.
	Source string `json:"source"`
	// AsOf is the day it was written, AsOfEpoch the exact time in milliseconds.
	AsOf      string `json:"as_of"`
	AsOfEpoch int64  `json:"as_of_epoch"`
}

func briefResponse(b *gorm.Brief) BriefResponse {
	return BriefResponse{
		Project: b.Project, Text: b.Text, Source: b.Source,
		AsOf: time.UnixMilli(b.GeneratedEpoch).UTC().Format("2006-01-02"), AsOfEpoch: b.GeneratedEpoch,
	}
}

// canonicalProject resolves a project reference from the URL to the project that holds the data.
func (s *Service) canonicalProject(ctx context.Context, w http.ResponseWriter, r *http.Request) (string, bool) {
	project := strings.TrimSpace(chi.URLParam(r, "id"))
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return "", false
	}
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	canonical, _, err := gorm.NewProjectAliasStore(s.store).ResolveAlias(ctx, project)
	if err != nil {
		http.Error(w, "failed to resolve project", http.StatusInternalServerError)
		return "", false
	}
	return canonical, true
}

// handleGetBrief returns a project's current brief, or 404 when it has none.
func (s *Service) handleGetBrief(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	project, ok := s.canonicalProject(ctx, w, r)
	if !ok {
		return
	}
	brief, err := s.summaryStore.GetBrief(ctx, project)
	if err != nil {
		http.Error(w, "failed to read the brief", http.StatusInternalServerError)
		return
	}
	if brief == nil {
		http.Error(w, fmt.Sprintf("no brief for project %q", project), http.StatusNotFound)
		return
	}
	noStore(w)
	writeJSON(w, briefResponse(brief))
}

// handlePostBrief writes (or rewrites) a project's brief now. It works whether or not the automatic
// briefs are switched on: asking for one is the user's own decision to spend the usage.
func (s *Service) handlePostBrief(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), briefGenerateTimeout+10*time.Second)
	defer cancel()
	project, ok := s.canonicalProject(ctx, w, r)
	if !ok {
		return
	}
	known, err := s.projectExists(ctx, project)
	if err != nil {
		http.Error(w, "failed to check project", http.StatusInternalServerError)
		return
	}
	if !known {
		http.Error(w, fmt.Sprintf("unknown project %q", project), http.StatusUnprocessableEntity)
		return
	}

	brief, err := s.generateBrief(ctx, project)
	switch {
	case errors.Is(err, ErrBriefUnavailable):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, ErrBriefBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, sdk.ErrNothingToBrief):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case err != nil:
		log.Warn().Err(err).Str("project", project).Msg("brief: failed to write a brief on request")
		http.Error(w, "failed to write the brief: "+err.Error(), http.StatusBadGateway)
	case brief == nil:
		http.Error(w, "the brief was written but could not be read back", http.StatusInternalServerError)
	default:
		noStore(w)
		writeJSON(w, briefResponse(brief))
	}
}
