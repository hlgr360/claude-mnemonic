package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/projects"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	// rollupGroupMaxNotes is the most notes one roll-up condenses. A bigger month is split by session.
	rollupGroupMaxNotes = 40
	// rollupGenerateTimeout bounds one roll-up, whichever backend writes it.
	rollupGenerateTimeout = 3 * time.Minute
	// rollupCommitTimeout bounds storing a roll-up and archiving its originals, which is not cancelled with the caller.
	rollupCommitTimeout = 30 * time.Second
	// rollupMaxFailuresInARow stops a pass when the model keeps failing, so a broken backend is not asked all night.
	rollupMaxFailuresInARow = 2
	// rollupConceptsKept is how many of the sources' concepts a roll-up carries besides the roll-up marker.
	rollupConceptsKept = 6
)

// rollupFirstPassDelay is how long after start the first automatic pass runs.
var rollupFirstPassDelay = 45 * time.Second

var (
	// ErrRollupUnavailable means nothing can write a roll-up (no Claude CLI and no local model).
	ErrRollupUnavailable = errors.New("no LLM backend is available to write a roll-up")
	// ErrRollupBusy means a roll-up for the project is being written right now.
	ErrRollupBusy = errors.New("a roll-up for this project is already being written")
)

// rollupGroup is notes that one roll-up condenses: the notes of one month, or one slice of a big month.
type rollupGroup struct {
	Label string
	Notes []*models.Observation
}

func (g rollupGroup) ids() []int64 {
	out := make([]int64, len(g.Notes))
	for i, n := range g.Notes {
		out[i] = n.ID
	}
	return out
}

func monthKey(epochMs int64) string { return time.UnixMilli(epochMs).UTC().Format("2006-01") }

// groupForRollup groups notes (oldest first) by month; a month with more than maxNotes is split by session, a session
// that is itself too big in slices. A group smaller than minSize is not worth a roll-up and is left out, so those
// notes stay live. The result is oldest first and depends only on the notes: the selection is a rule, and the model
// only writes the text.
func groupForRollup(notes []*models.Observation, minSize, maxNotes int) []rollupGroup {
	if maxNotes < 1 {
		maxNotes = rollupGroupMaxNotes
	}
	var months []string
	byMonth := map[string][]*models.Observation{}
	for _, n := range notes {
		k := monthKey(n.CreatedAtEpoch)
		if _, ok := byMonth[k]; !ok {
			months = append(months, k)
		}
		byMonth[k] = append(byMonth[k], n)
	}
	sort.Strings(months)

	var out []rollupGroup
	for _, month := range months {
		bucket := byMonth[month]
		var chunks [][]*models.Observation
		if len(bucket) <= maxNotes {
			chunks = [][]*models.Observation{bucket}
		} else {
			chunks = splitBySession(bucket, maxNotes)
		}
		for i, c := range chunks {
			if len(c) < minSize {
				continue
			}
			label := month
			if len(chunks) > 1 {
				label = fmt.Sprintf("%s (part %d)", month, i+1)
			}
			out = append(out, rollupGroup{Label: label, Notes: c})
		}
	}
	return out
}

// splitBySession packs a month's notes into chunks of at most maxNotes, keeping the notes of a session together
// (sessions in the order they began). A session bigger than maxNotes is cut in order.
func splitBySession(notes []*models.Observation, maxNotes int) [][]*models.Observation {
	var order []string
	bySession := map[string][]*models.Observation{}
	for _, n := range notes {
		if _, ok := bySession[n.SDKSessionID]; !ok {
			order = append(order, n.SDKSessionID)
		}
		bySession[n.SDKSessionID] = append(bySession[n.SDKSessionID], n)
	}
	var chunks [][]*models.Observation
	var cur []*models.Observation
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, cur)
			cur = nil
		}
	}
	for _, sid := range order {
		sess := bySession[sid]
		if len(cur)+len(sess) > maxNotes {
			flush()
		}
		for len(sess) > maxNotes {
			chunks = append(chunks, sess[:maxNotes])
			sess = sess[maxNotes:]
		}
		cur = append(cur, sess...)
	}
	flush()
	return chunks
}

// majorityType is the most common type among the notes (the earliest type of a tie, in the order of the notes).
func majorityType(notes []*models.Observation) models.ObservationType {
	count := map[models.ObservationType]int{}
	var order []models.ObservationType
	for _, n := range notes {
		if _, ok := count[n.Type]; !ok {
			order = append(order, n.Type)
		}
		count[n.Type]++
	}
	best := models.ObsTypeDiscovery
	top := 0
	for _, t := range order {
		if count[t] > top {
			best, top = t, count[t]
		}
	}
	return best
}

// topConcepts returns the most common concepts among the notes, at most n, without the roll-up marker.
func topConcepts(notes []*models.Observation, n int) []string {
	count := map[string]int{}
	var order []string
	for _, o := range notes {
		for _, c := range o.Concepts {
			c = strings.TrimSpace(c)
			if c == "" || c == gorm.RollupConcept {
				continue
			}
			if _, ok := count[c]; !ok {
				order = append(order, c)
			}
			count[c]++
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return count[order[i]] > count[order[j]] })
	if len(order) > n {
		order = order[:n]
	}
	return order
}

// rollupObservation builds the note that stands for a group: the model's text, a line that says what it was written
// from and where the originals are, and the common concepts plus the roll-up marker. Its scope is the project's: a
// roll-up never leaves the project it summarises, whatever concepts its sources had.
func rollupObservation(g rollupGroup, res *sdk.RollupResult) *models.ParsedObservation {
	first, last := g.Notes[0].CreatedAtEpoch, g.Notes[len(g.Notes)-1].CreatedAtEpoch
	title := strings.TrimSpace(res.Title)
	if title == "" {
		title = "notes of " + g.Label
	}
	span := fmt.Sprintf("%s to %s", time.UnixMilli(first).UTC().Format("2006-01-02"), time.UnixMilli(last).UTC().Format("2006-01-02"))
	return &models.ParsedObservation{
		Type:  majorityType(g.Notes),
		Title: "Roll-up: " + title,
		Narrative: res.Body + fmt.Sprintf("\n\nRolled up from %d notes (%s). The originals are archived, not deleted; "+
			"restore them from the dashboard, or with the restore action of the memory_admin tool.", len(g.Notes), span),
		Facts:    []string{fmt.Sprintf("Condenses %d notes from %s", len(g.Notes), span)},
		Concepts: append([]string{gorm.RollupConcept}, topConcepts(g.Notes, rollupConceptsKept)...),
		Scope:    models.ScopeProject,
	}
}

// RollupGroupReport says what happened to one group.
type RollupGroupReport struct {
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
	// Error is why the group was not rolled up, when it was not.
	Error string `json:"error,omitempty"`
	// IDs are the notes of the group.
	IDs []int64 `json:"ids"`
	// RollupID is the roll-up note and FoldID the record of the roll-up; zero in a preview or on an error.
	RollupID int64 `json:"rollup_id,omitempty"`
	FoldID   int64 `json:"fold_id,omitempty"`
	// Archived is how many of the originals were archived.
	Archived int `json:"archived"`
}

// RollupReport is the result of a roll-up run for one project.
type RollupReport struct {
	Project string              `json:"project"`
	Groups  []RollupGroupReport `json:"groups"`
	// Candidates is how many notes qualified, Remaining how many groups were left for a later run.
	Candidates int `json:"candidates"`
	Remaining  int `json:"remaining"`
	// DryRun is true when nothing was written: the groups are what a run would condense.
	DryRun bool `json:"dry_run"`
}

func groupReport(g rollupGroup) RollupGroupReport {
	return RollupGroupReport{
		Label: g.Label, IDs: g.ids(),
		From: time.UnixMilli(g.Notes[0].CreatedAtEpoch).UTC().Format("2006-01-02"),
		To:   time.UnixMilli(g.Notes[len(g.Notes)-1].CreatedAtEpoch).UTC().Format("2006-01-02"),
	}
}

// planRollup selects the notes of a project that may be rolled up and groups them.
func (s *Service) planRollup(ctx context.Context, project string) (groups []rollupGroup, candidates int, err error) {
	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()
	if observationStore == nil {
		return nil, 0, ErrRollupUnavailable
	}
	cfg := s.config
	minAge, keep, minSize := cfg.RollupMinAgeDays, cfg.RollupKeepNewest, cfg.RollupMinGroupSize
	if minAge <= 0 {
		minAge = 60
	}
	if minSize <= 0 {
		minSize = 8
	}
	cutoff := time.Now().Add(-time.Duration(minAge) * 24 * time.Hour).UnixMilli()
	notes, err := observationStore.RollupCandidates(ctx, project, cutoff, keep)
	if err != nil {
		return nil, 0, err
	}
	return groupForRollup(notes, minSize, rollupGroupMaxNotes), len(notes), nil
}

// rollupProject condenses up to maxGroups groups of a project's older notes. With dryRun it only reports the groups.
// A group is written in this order, so that a failure at any step leaves every note live: the model writes the text,
// the roll-up note is stored, the originals are archived, and the record is kept; if archiving or the record fails
// the roll-up note is removed again. consecutiveFailures is shared across projects by a pass.
func (s *Service) rollupProject(ctx context.Context, project string, maxGroups int, dryRun bool, consecutiveFailures *int) (*RollupReport, error) {
	s.initMu.RLock()
	processor, observationStore := s.processor, s.observationStore
	s.initMu.RUnlock()
	if observationStore == nil {
		return nil, ErrRollupUnavailable
	}
	write := s.rollupWriter
	if write == nil && processor != nil {
		write = processor.GenerateRollup
	}
	if write == nil && !dryRun {
		return nil, ErrRollupUnavailable
	}

	s.rollupMu.Lock()
	if _, busy := s.rollupRunning[project]; busy {
		s.rollupMu.Unlock()
		return nil, ErrRollupBusy
	}
	if s.rollupRunning == nil {
		s.rollupRunning = map[string]struct{}{}
	}
	s.rollupRunning[project] = struct{}{}
	s.rollupMu.Unlock()
	defer func() {
		s.rollupMu.Lock()
		delete(s.rollupRunning, project)
		s.rollupMu.Unlock()
	}()

	groups, candidates, err := s.planRollup(ctx, project)
	if err != nil {
		return nil, err
	}
	rep := &RollupReport{Project: project, DryRun: dryRun, Candidates: candidates, Groups: []RollupGroupReport{}}
	if maxGroups <= 0 {
		maxGroups = s.config.RollupMaxGroupsPerRun
	}
	if maxGroups <= 0 {
		maxGroups = 3
	}
	if len(groups) > maxGroups {
		rep.Remaining = len(groups) - maxGroups
		groups = groups[:maxGroups]
	}
	if dryRun {
		for _, g := range groups {
			rep.Groups = append(rep.Groups, groupReport(g))
		}
		return rep, nil
	}

	snapshotted := false
	for _, g := range groups {
		if ctx.Err() != nil {
			break
		}
		gr := groupReport(g)
		if !snapshotted {
			// A copy of the database before the first archive of the run (rate limited, like the cap's).
			s.snapshotBeforeCleanup(ctx, "rollup")
			snapshotted = true
		}
		if err := s.applyRollup(ctx, project, g, write, &gr); err != nil {
			gr.Error = err.Error()
			log.Warn().Err(err).Str("project", project).Str("group", g.Label).Int("notes", len(g.Notes)).
				Msg("roll-up: group not rolled up; its notes stay live")
			if consecutiveFailures != nil {
				*consecutiveFailures++
			}
			rep.Groups = append(rep.Groups, gr)
			if consecutiveFailures != nil && *consecutiveFailures >= rollupMaxFailuresInARow {
				break
			}
			continue
		}
		if consecutiveFailures != nil {
			*consecutiveFailures = 0
		}
		rep.Groups = append(rep.Groups, gr)
	}
	return rep, nil
}

// applyRollup writes one group's roll-up and archives the originals. On error nothing is left changed.
func (s *Service) applyRollup(ctx context.Context, project string, g rollupGroup,
	write func(context.Context, sdk.RollupInput) (*sdk.RollupResult, error), gr *RollupGroupReport) error {
	s.initMu.RLock()
	observationStore, store := s.observationStore, s.store
	s.initMu.RUnlock()

	genCtx, cancel := context.WithTimeout(ctx, rollupGenerateTimeout)
	res, err := write(genCtx, sdk.RollupInput{Now: time.Now(), Name: projects.DisplayName(project), Observations: g.Notes})
	cancel()
	if err != nil {
		return err // the model did not answer, or not usably: nothing was stored or archived
	}

	// From here the group is committed (store, archive, record) or rolled back, whatever the caller does: a request
	// that gives up half way must not leave originals archived without the record that restores them.
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), rollupCommitTimeout)
	defer stop()

	sessionID := fmt.Sprintf("rollup-%s-%s", project, time.Now().Format("20060102"))
	rollupID, _, err := observationStore.StoreObservation(ctx, sessionID, project, rollupObservation(g, res), 0, 0)
	if err != nil {
		return fmt.Errorf("storing the roll-up: %w", err)
	}
	undo := func() { _ = observationStore.DeleteObservation(ctx, rollupID) }

	archived, err := observationStore.ArchiveInto(ctx, g.ids(), gorm.FoldReason(gorm.FoldRollup, rollupID))
	if err != nil || len(archived) == 0 {
		undo()
		if err == nil {
			err = errors.New("none of the notes could be archived")
		}
		return fmt.Errorf("archiving the originals: %w", err)
	}
	foldID, err := gorm.NewObservationFoldStore(store).Record(ctx, project, gorm.FoldRollup, rollupID, archived, g.Label, "")
	if err != nil {
		// Without the record the roll-up could not be undone: put everything back.
		_, _ = observationStore.UnarchiveWithReason(ctx, archived, gorm.FoldReason(gorm.FoldRollup, rollupID))
		undo()
		return err
	}

	// A roll-up is dated at the newest note it condenses, so it sits in history where its sources were and does not
	// look like new work. With a cap set it keeps today's date: the cap archives the oldest notes first.
	if s.config.MaxObservationsPerProject <= 0 {
		if err := observationStore.SetObservationCreated(ctx, rollupID, g.Notes[len(g.Notes)-1].CreatedAtEpoch); err != nil {
			log.Warn().Err(err).Int64("id", rollupID).Msg("roll-up: could not date the roll-up; it keeps today's date")
		}
	}

	s.dropArchivedFromVectors(archived)
	s.afterExplicitWrite(rollupID, project)
	s.invalidateObsCountCache(project)

	gr.RollupID, gr.FoldID, gr.Archived = rollupID, foldID, len(archived)
	log.Info().Str("project", project).Str("group", g.Label).Int64("rollup", rollupID).Int64("fold", foldID).
		Int("archived", len(archived)).Msg("roll-up: written, originals archived")
	return nil
}

// runRollupPass rolls up what is due in every project, at most RollupMaxGroupsPerRun groups in all. It returns how
// many roll-ups it wrote.
func (s *Service) runRollupPass(ctx context.Context) int {
	cfg := s.config
	if cfg == nil {
		return 0
	}
	s.initMu.RLock()
	sessionStore, store := s.sessionStore, s.store
	s.initMu.RUnlock()
	if sessionStore == nil || store == nil {
		return 0
	}
	rows, err := sessionStore.ProjectSummaries(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("roll-up: could not list the projects")
		return 0
	}
	aliases, err := gorm.NewProjectAliasStore(store).AliasMap(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("roll-up: could not read the project aliases")
		return 0
	}
	budget := cfg.RollupMaxGroupsPerRun
	if budget <= 0 {
		budget = 3
	}
	written, failures := 0, 0
	for _, r := range rows {
		if budget <= 0 || ctx.Err() != nil || failures >= rollupMaxFailuresInARow {
			break
		}
		if _, isAlias := aliases[r.Project]; isAlias || r.Observations == 0 {
			continue
		}
		rep, err := s.rollupProject(ctx, r.Project, budget, false, &failures)
		switch {
		case errors.Is(err, ErrRollupUnavailable):
			log.Info().Msg("roll-up: no model backend is available; notes stay live")
			return written
		case errors.Is(err, ErrRollupBusy):
			continue
		case err != nil:
			log.Warn().Err(err).Str("project", r.Project).Msg("roll-up: could not look for notes to roll up")
			continue
		}
		for _, g := range rep.Groups {
			if g.Error == "" {
				written++
				budget--
			}
		}
	}
	return written
}

// rollupLoop runs runRollupPass now and then. It only runs when roll-ups are switched on.
func (s *Service) rollupLoop() {
	defer s.wg.Done()

	interval := time.Duration(s.config.RollupIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	timer := time.NewTimer(rollupFirstPassDelay)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.runRollupPass(s.ctx)
			timer.Reset(interval)
		}
	}
}

// ---- undo ----

var (
	// ErrFoldNotFound means there is no fold with that id.
	ErrFoldNotFound = errors.New("no such roll-up or consolidation")
	// ErrFoldUndone means the fold was restored already.
	ErrFoldUndone = errors.New("already restored")
)

// FoldRestoreReport says what restoring a fold did.
type FoldRestoreReport struct {
	Kind string `json:"kind"`
	// Restored are the notes that are live again; Kept are the ones that stay archived because they were archived
	// for another reason in the meantime.
	Restored []int64 `json:"restored"`
	Kept     []int64 `json:"kept"`
	FoldID   int64   `json:"fold_id"`
	// Survivor is the note that stood for them: a roll-up is archived again, a consolidation's survivor stays.
	Survivor         int64 `json:"survivor"`
	SurvivorArchived bool  `json:"survivor_archived"`
}

// restoreFold undoes a fold: the originals are live again (those that are still archived for this fold), a roll-up is
// archived in their place, and the fold is marked undone. The mutations come first, so a failure leaves the fold
// restorable; a second restore at the same time finds nothing left to do.
func (s *Service) restoreFold(ctx context.Context, foldID int64) (*FoldRestoreReport, error) {
	s.initMu.RLock()
	observationStore, store := s.observationStore, s.store
	s.initMu.RUnlock()
	if observationStore == nil || store == nil {
		return nil, ErrRollupUnavailable
	}
	folds := gorm.NewObservationFoldStore(store)
	fold, err := folds.Get(ctx, foldID)
	if err != nil {
		return nil, err
	}
	if fold == nil {
		return nil, ErrFoldNotFound
	}
	if fold.Undone() {
		return nil, ErrFoldUndone
	}

	sources := fold.Sources()
	restored, err := observationStore.UnarchiveWithReason(ctx, sources, gorm.FoldReason(fold.Kind, fold.SurvivorID))
	if err != nil {
		return nil, fmt.Errorf("restoring the notes: %w", err)
	}
	rep := &FoldRestoreReport{Kind: fold.Kind, FoldID: fold.ID, Survivor: fold.SurvivorID, Restored: restored, Kept: []int64{}}
	if rep.Restored == nil {
		rep.Restored = []int64{}
	}
	back := map[int64]bool{}
	for _, id := range restored {
		back[id] = true
	}
	for _, id := range sources {
		if !back[id] {
			rep.Kept = append(rep.Kept, id)
		}
	}

	if fold.Kind == gorm.FoldRollup {
		if err := observationStore.ArchiveObservation(ctx, fold.SurvivorID, "roll-up restored: its notes are live again"); err == nil {
			rep.SurvivorArchived = true
			s.dropArchivedFromVectors([]int64{fold.SurvivorID})
		} else {
			log.Warn().Err(err).Int64("id", fold.SurvivorID).Msg("roll-up: could not archive the roll-up note when restoring")
		}
	}
	if fold.Kind == gorm.FoldConsolidation {
		// The survivor stays; it gives back what it took over from the duplicates.
		s.revertConsolidation(ctx, fold)
	}
	if _, err := folds.MarkUndone(ctx, fold.ID); err != nil {
		return nil, fmt.Errorf("marking the fold restored: %w", err)
	}

	// The restored notes go back into the vector index.
	if len(restored) > 0 && s.vectorSync != nil {
		ids := append([]int64(nil), restored...)
		s.asyncVectorSync(func() {
			vctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			defer cancel()
			for _, id := range ids {
				obs, gerr := observationStore.GetObservationByID(vctx, id)
				if gerr != nil || obs == nil {
					continue
				}
				if serr := s.vectorSync.SyncObservation(vctx, obs); serr != nil && s.ctx.Err() == nil {
					log.Debug().Err(serr).Int64("id", id).Msg("restore: failed to sync observation")
				}
			}
		})
	}
	s.invalidateObsCountCache(fold.Project)
	log.Info().Int64("fold", fold.ID).Str("kind", fold.Kind).Int("restored", len(restored)).Int("kept", len(rep.Kept)).Msg("fold restored")
	return rep, nil
}

// ---- HTTP ----

type rollupRequest struct {
	// DryRun only reports the groups a run would condense.
	DryRun bool `json:"dry_run"`
	// MaxGroups bounds the run; zero uses the setting.
	MaxGroups int `json:"max_groups"`
}

// handlePostRollup rolls up a project's older notes now (or previews it). It works whether or not the automatic
// roll-up is switched on: asking for one is the user's own decision to spend the usage.
// POST /api/projects/{id}/rollup
func (s *Service) handlePostRollup(w http.ResponseWriter, r *http.Request) {
	// A roll-up that was asked for runs to the end even if the caller stops waiting (the MCP client gives up after 30 s):
	// it is bounded by its own timeout instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 4*rollupGenerateTimeout)
	defer cancel()
	project, ok := s.canonicalProject(ctx, w, r)
	if !ok {
		return
	}
	var req rollupRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
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
	failures := 0
	rep, err := s.rollupProject(ctx, project, req.MaxGroups, req.DryRun, &failures)
	switch {
	case errors.Is(err, ErrRollupUnavailable):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, ErrRollupBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		log.Warn().Err(err).Str("project", project).Msg("roll-up: failed on request")
		http.Error(w, "failed to roll up: "+err.Error(), http.StatusBadGateway)
	default:
		noStore(w)
		writeJSON(w, rep)
	}
}

// FoldItem is one fold as listed.
type FoldItem struct {
	Kind          string  `json:"kind"`
	Project       string  `json:"project"`
	Label         string  `json:"label"`
	SurvivorTitle string  `json:"survivor_title"`
	Sources       []int64 `json:"sources"`
	ID            int64   `json:"id"`
	Survivor      int64   `json:"survivor"`
	CreatedEpoch  int64   `json:"created_epoch"`
	UndoneEpoch   int64   `json:"undone_epoch,omitempty"`
	Undone        bool    `json:"undone"`
}

// handleListFolds lists roll-ups and consolidations, newest first.
// GET /api/folds?project=&kind=&include_undone=true&limit=
func (s *Service) handleListFolds(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && kind != gorm.FoldRollup && kind != gorm.FoldConsolidation {
		http.Error(w, "kind must be 'rollup' or 'consolidation'", http.StatusBadRequest)
		return
	}
	project := q.Get("project")
	if project != "" {
		canonical, _, err := gorm.NewProjectAliasStore(s.store).ResolveAlias(ctx, project)
		if err != nil {
			http.Error(w, "failed to resolve project", http.StatusInternalServerError)
			return
		}
		project = canonical
	}
	rows, err := gorm.NewObservationFoldStore(s.store).List(ctx, project, kind, q.Get("include_undone") == "true", gorm.ParseLimitParam(r, 100))
	if err != nil {
		http.Error(w, "failed to list", http.StatusInternalServerError)
		return
	}
	items := make([]FoldItem, 0, len(rows))
	for _, f := range rows {
		item := FoldItem{
			ID: f.ID, Kind: f.Kind, Project: f.Project, Label: f.Note, Survivor: f.SurvivorID, Sources: f.Sources(),
			CreatedEpoch: f.CreatedAtEpoch, Undone: f.Undone(), UndoneEpoch: f.UndoneAtEpoch.Int64,
		}
		if o, err := s.observationStore.GetObservationByID(ctx, f.SurvivorID); err == nil && o != nil {
			item.SurvivorTitle = o.Title.String
		}
		items = append(items, item)
	}
	noStore(w)
	writeJSON(w, map[string]any{"folds": items})
}

// handleRestoreFold undoes a roll-up or consolidation.
// POST /api/folds/{id}/restore
func (s *Service) handleRestoreFold(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	id, ok := parseIDParam(w, chi.URLParam(r, "id"), "fold")
	if !ok {
		return
	}
	rep, err := s.restoreFold(ctx, id)
	switch {
	case errors.Is(err, ErrFoldNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrFoldUndone):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		log.Warn().Err(err).Int64("fold", id).Msg("fold: restore failed")
		http.Error(w, "failed to restore: "+err.Error(), http.StatusInternalServerError)
	default:
		noStore(w)
		writeJSON(w, rep)
	}
}
