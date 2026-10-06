package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/similarity"
)

const (
	// consolidationMaxIDs bounds one manual consolidation.
	consolidationMaxIDs = 50
	// consolidationScanLimit is how many of a project's newest notes the automatic pass compares with each other.
	consolidationScanLimit = 600
	// consolidationMaxFacts bounds the facts a survivor can carry after merging.
	consolidationMaxFacts = 40
)

var (
	// ErrConsolidationInvalid means the notes given cannot be consolidated; the message says why.
	ErrConsolidationInvalid = errors.New("these notes cannot be consolidated")
	// ErrConsolidationStale means the notes changed since the preview that the confirm token came from.
	ErrConsolidationStale = errors.New("the notes changed since the preview: preview again")
)

// ConsolidationNote is a note in a consolidation plan.
type ConsolidationNote struct {
	Title        string  `json:"title"`
	Type         string  `json:"type"`
	ID           int64   `json:"id"`
	CreatedEpoch int64   `json:"created_epoch"`
	Importance   float64 `json:"importance"`
	// Protected marks a decision, a rated note or one whose scope was chosen on purpose: never consolidated automatically.
	Protected bool `json:"protected"`
}

// ConsolidationPlan is what consolidating a group of notes would do, and what it did.
type ConsolidationPlan struct {
	Project string `json:"project"`
	// Token confirms the plan: pass it back to apply exactly this.
	Token      string              `json:"token"`
	Duplicates []ConsolidationNote `json:"duplicates"`
	// Added is what merging adds to the survivor.
	AddedFacts    []string `json:"added_facts"`
	AddedConcepts []string `json:"added_concepts"`
	AddedFiles    []string `json:"added_files"`

	notes     []*models.Observation
	protected map[int64]bool
	// addedRead and addedModified are AddedFiles by kind: the survivor keeps files read and files modified apart.
	addedRead, addedModified []string

	Survivor ConsolidationNote `json:"survivor"`
	// RelationsToCopy is how many relations of the duplicates the survivor takes over.
	RelationsToCopy int   `json:"relations_to_copy"`
	FoldID          int64 `json:"fold_id,omitempty"`
	Applied         bool  `json:"applied"`
}

// consolidationDetail is what an undo needs: exactly what the consolidation added to the survivor.
type consolidationDetail struct {
	Facts         []string `json:"facts,omitempty"`
	Concepts      []string `json:"concepts,omitempty"`
	FilesRead     []string `json:"files_read,omitempty"`
	FilesModified []string `json:"files_modified,omitempty"`
	Relations     []int64  `json:"relations,omitempty"`
}

// chooseSurvivor picks the note that stays: a protected one first (a decision, a rated note, one saved on purpose), then
// the highest importance, then the newest, then the lowest id.
func chooseSurvivor(notes []*models.Observation, protected map[int64]bool) *models.Observation {
	better := func(a, b *models.Observation) bool {
		switch {
		case protected[a.ID] != protected[b.ID]:
			return protected[a.ID]
		case a.ImportanceScore != b.ImportanceScore:
			return a.ImportanceScore > b.ImportanceScore
		case a.CreatedAtEpoch != b.CreatedAtEpoch:
			return a.CreatedAtEpoch > b.CreatedAtEpoch
		}
		return a.ID < b.ID
	}
	best := notes[0]
	for _, n := range notes[1:] {
		if better(n, best) {
			best = n
		}
	}
	return best
}

// newStrings returns the values of extra that base does not have (compared without case and surrounding space), in
// order, each once.
func newStrings(base, extra []string) []string {
	have := map[string]bool{}
	for _, b := range base {
		have[strings.ToLower(strings.TrimSpace(b))] = true
	}
	var out []string
	for _, e := range extra {
		k := strings.ToLower(strings.TrimSpace(e))
		if k == "" || have[k] {
			continue
		}
		have[k] = true
		out = append(out, strings.TrimSpace(e))
	}
	return out
}

// removeStrings returns list without the given values (exact match).
func removeStrings(list, remove []string) []string {
	drop := map[string]bool{}
	for _, r := range remove {
		drop[r] = true
	}
	out := make([]string, 0, len(list))
	for _, l := range list {
		if !drop[l] {
			out = append(out, l)
		}
	}
	return out
}

func asNote(o *models.Observation, protected map[int64]bool) ConsolidationNote {
	return ConsolidationNote{
		ID: o.ID, Title: o.Title.String, Type: string(o.Type), CreatedEpoch: o.CreatedAtEpoch,
		Importance: o.ImportanceScore, Protected: protected[o.ID],
	}
}

// planToken confirms a plan: it covers the survivor, the duplicates and their content, so a preview that has gone
// stale (a note edited, another one chosen) is refused.
func planToken(survivor *models.Observation, notes []*models.Observation) string {
	ordered := append([]*models.Observation(nil), notes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	h := sha256.New()
	fmt.Fprintf(h, "consolidate|survivor=%d", survivor.ID)
	for _, n := range ordered {
		fmt.Fprintf(h, "|%d:%s:%s:%s:%s:%s", n.ID, n.Title.String, n.Narrative.String,
			strings.Join(n.Facts, "\x1f"), strings.Join(n.Concepts, "\x1f"), strings.Join(n.FilesRead, "\x1f")+strings.Join(n.FilesModified, "\x1f"))
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// capAdded keeps as many of the added values as fit below limit in all.
func capAdded(base, added []string, limit int) []string {
	room := limit - len(base)
	if room < 0 {
		room = 0
	}
	if len(added) > room {
		return added[:room]
	}
	return added
}

// buildConsolidationPlan decides the survivor and what merging adds to it. It needs no database: the notes and the set
// of protected ids are given.
func buildConsolidationPlan(project string, notes []*models.Observation, protected map[int64]bool) *ConsolidationPlan {
	survivor := chooseSurvivor(notes, protected)
	plan := &ConsolidationPlan{Project: project, Survivor: asNote(survivor, protected), notes: notes, protected: protected}

	facts, concepts := append([]string(nil), survivor.Facts...), append([]string(nil), survivor.Concepts...)
	read, modified := append([]string(nil), survivor.FilesRead...), append([]string(nil), survivor.FilesModified...)
	var addedFacts, addedConcepts, addedRead, addedModified, otherIDs []string
	for _, n := range notes {
		if n.ID == survivor.ID {
			continue
		}
		plan.Duplicates = append(plan.Duplicates, asNote(n, protected))
		otherIDs = append(otherIDs, fmt.Sprintf("#%d", n.ID))
		f := newStrings(facts, n.Facts)
		facts, addedFacts = append(facts, f...), append(addedFacts, f...)
		c := newStrings(concepts, n.Concepts)
		concepts, addedConcepts = append(concepts, c...), append(addedConcepts, c...)
		r := newStrings(read, n.FilesRead)
		read, addedRead = append(read, r...), append(addedRead, r...)
		m := newStrings(modified, n.FilesModified)
		modified, addedModified = append(modified, m...), append(addedModified, m...)
	}
	// What the survivor carries stays bounded however many notes are folded in; the trace of the merge always fits.
	addedFacts = capAdded(survivor.Facts, addedFacts, consolidationMaxFacts-1)
	addedFacts = append(addedFacts, "Consolidated with "+strings.Join(otherIDs, ", ")+" (near-duplicate notes)")

	plan.AddedFacts, plan.AddedConcepts = addedFacts, nilToEmpty(addedConcepts)
	plan.addedRead, plan.addedModified = addedRead, addedModified
	plan.AddedFiles = nilToEmpty(append(append([]string(nil), addedRead...), addedModified...))
	plan.Token = planToken(survivor, notes)
	return plan
}

func nilToEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// duplicateGroups groups notes (newest first) that are at least minSim alike by their terms and of the same type. A
// note joins the group of the first note it is that alike to; a group has at least two notes.
func duplicateGroups(notes []*models.Observation, minSim float64) [][]*models.Observation {
	terms := make([]map[string]bool, len(notes))
	for i, n := range notes {
		terms[i] = similarity.ExtractObservationTerms(n)
	}
	used := make([]bool, len(notes))
	var groups [][]*models.Observation
	for i := range notes {
		if used[i] || len(terms[i]) == 0 {
			continue
		}
		group := []*models.Observation{notes[i]}
		for j := i + 1; j < len(notes); j++ {
			if used[j] || notes[j].Type != notes[i].Type {
				continue
			}
			if similarity.JaccardSimilarity(terms[i], terms[j]) >= minSim {
				group = append(group, notes[j])
				used[j] = true
			}
		}
		if len(group) > 1 {
			used[i] = true
			groups = append(groups, group)
		}
	}
	return groups
}

// planConsolidation loads the notes by id, checks that they can be consolidated and builds the plan.
func (s *Service) planConsolidation(ctx context.Context, ids []int64) (*ConsolidationPlan, error) {
	s.initMu.RLock()
	observationStore, relationStore := s.observationStore, s.relationStore
	s.initMu.RUnlock()
	if observationStore == nil {
		return nil, errors.New("not ready")
	}
	seen := map[int64]bool{}
	var uniq []int64
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) < 2 {
		return nil, fmt.Errorf("%w: at least two different notes are needed", ErrConsolidationInvalid)
	}
	if len(uniq) > consolidationMaxIDs {
		return nil, fmt.Errorf("%w: at most %d notes at a time", ErrConsolidationInvalid, consolidationMaxIDs)
	}
	notes, err := observationStore.GetObservationsByIDsPreserveOrder(ctx, uniq) // live notes only
	if err != nil {
		return nil, err
	}
	have := map[int64]bool{}
	for _, n := range notes {
		have[n.ID] = true
	}
	for _, id := range uniq {
		if !have[id] {
			return nil, fmt.Errorf("%w: note #%d does not exist or is archived", ErrConsolidationInvalid, id)
		}
	}
	project := notes[0].Project
	for _, n := range notes {
		if n.Project != project {
			return nil, fmt.Errorf("%w: notes #%d and #%d are in different projects", ErrConsolidationInvalid, notes[0].ID, n.ID)
		}
		if n.IsSuperseded {
			return nil, fmt.Errorf("%w: note #%d is superseded", ErrConsolidationInvalid, n.ID)
		}
		for _, c := range n.Concepts {
			if c == gorm.RollupConcept {
				return nil, fmt.Errorf("%w: note #%d is a roll-up", ErrConsolidationInvalid, n.ID)
			}
		}
	}
	protected, err := observationStore.ProtectedFromFolding(ctx, uniq)
	if err != nil {
		return nil, err
	}
	plan := buildConsolidationPlan(project, notes, protected)
	if relationStore != nil {
		for _, d := range plan.Duplicates {
			rels, rerr := relationStore.GetRelationsByObservationID(ctx, d.ID)
			if rerr != nil {
				return nil, rerr
			}
			for _, r := range rels {
				other := r.TargetID
				if other == d.ID {
					other = r.SourceID
				}
				if !have[other] {
					plan.RelationsToCopy++
				}
			}
		}
	}
	return plan, nil
}

// applyConsolidation consolidates the plan's notes: the survivor takes over what the duplicates have (facts, concepts,
// files, relations), the duplicates are archived with a link, and the fold is recorded with what was added, so an undo
// can take exactly that away. If a step fails the earlier ones are undone and every note is as it was.
func (s *Service) applyConsolidation(ctx context.Context, plan *ConsolidationPlan) (int64, error) {
	s.initMu.RLock()
	observationStore, relationStore, store := s.observationStore, s.relationStore, s.store
	s.initMu.RUnlock()

	// Committed or rolled back whatever the caller does (see applyRollup).
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), rollupCommitTimeout)
	defer stop()

	var survivor *models.Observation
	var dupIDs []int64
	inGroup := map[int64]bool{}
	for _, n := range plan.notes {
		inGroup[n.ID] = true
		if n.ID == plan.Survivor.ID {
			survivor = n
		} else {
			dupIDs = append(dupIDs, n.ID)
		}
	}
	if survivor == nil {
		return 0, errors.New("the survivor is not among the notes")
	}

	detail := consolidationDetail{Facts: plan.AddedFacts, Concepts: plan.AddedConcepts, FilesRead: plan.addedRead, FilesModified: plan.addedModified}
	facts := append(append([]string(nil), survivor.Facts...), detail.Facts...)
	concepts := append(append([]string(nil), survivor.Concepts...), detail.Concepts...)
	filesRead := append(append([]string(nil), survivor.FilesRead...), detail.FilesRead...)
	filesModified := append(append([]string(nil), survivor.FilesModified...), detail.FilesModified...)
	if _, err := observationStore.UpdateObservation(ctx, survivor.ID, &gorm.ObservationUpdate{
		Facts: &facts, Concepts: &concepts, FilesRead: &filesRead, FilesModified: &filesModified,
	}); err != nil {
		return 0, fmt.Errorf("merging into the survivor: %w", err)
	}

	var created []int64
	rollback := func() {
		if relationStore != nil {
			_ = relationStore.DeleteRelationsByIDs(ctx, created)
		}
		orig := struct{ f, c, r, m []string }{survivor.Facts, survivor.Concepts, survivor.FilesRead, survivor.FilesModified}
		_, _ = observationStore.UpdateObservation(ctx, survivor.ID, &gorm.ObservationUpdate{
			Facts: &orig.f, Concepts: &orig.c, FilesRead: &orig.r, FilesModified: &orig.m,
		})
	}

	if relationStore != nil {
		for _, id := range dupIDs {
			rels, err := relationStore.GetRelationsByObservationID(ctx, id)
			if err != nil {
				rollback()
				return 0, fmt.Errorf("reading relations: %w", err)
			}
			for _, r := range rels {
				other := r.TargetID
				if other == id {
					other = r.SourceID
				}
				if inGroup[other] {
					continue
				}
				copyRel := *r
				copyRel.ID = 0
				if r.SourceID == id {
					copyRel.SourceID = survivor.ID
				} else {
					copyRel.TargetID = survivor.ID
				}
				newID, isNew, err := relationStore.StoreRelationIfNew(ctx, &copyRel)
				if err != nil {
					rollback()
					return 0, fmt.Errorf("copying a relation: %w", err)
				}
				if isNew {
					created = append(created, newID)
				}
			}
		}
	}
	detail.Relations = created

	reason := gorm.FoldReason(gorm.FoldConsolidation, survivor.ID)
	archived, err := observationStore.ArchiveInto(ctx, dupIDs, reason)
	if err != nil || len(archived) == 0 {
		rollback()
		if err == nil {
			err = errors.New("none of the notes could be archived")
		}
		return 0, fmt.Errorf("archiving the duplicates: %w", err)
	}
	raw, _ := json.Marshal(detail)
	foldID, err := gorm.NewObservationFoldStore(store).Record(ctx, plan.Project, gorm.FoldConsolidation, survivor.ID, archived, "", string(raw))
	if err != nil {
		_, _ = observationStore.UnarchiveWithReason(ctx, archived, reason)
		rollback()
		return 0, err
	}

	s.dropArchivedFromVectors(archived)
	s.afterExplicitWrite(survivor.ID, plan.Project) // its content changed: sync it again
	s.invalidateObsCountCache(plan.Project)
	log.Info().Str("project", plan.Project).Int64("survivor", survivor.ID).Int("archived", len(archived)).
		Int64("fold", foldID).Int("relations_copied", len(created)).Msg("consolidation: duplicates folded into the survivor")
	return foldID, nil
}

// revertConsolidation takes back what a consolidation added to its survivor: the merged facts, concepts and files and
// the copied relations. A value the survivor has for another reason too is only removed if the consolidation added it.
func (s *Service) revertConsolidation(ctx context.Context, fold *gorm.ObservationFold) {
	s.initMu.RLock()
	observationStore, relationStore := s.observationStore, s.relationStore
	s.initMu.RUnlock()
	var d consolidationDetail
	if fold.Detail == "" || json.Unmarshal([]byte(fold.Detail), &d) != nil {
		return
	}
	if relationStore != nil {
		if err := relationStore.DeleteRelationsByIDs(ctx, d.Relations); err != nil {
			log.Warn().Err(err).Int64("fold", fold.ID).Msg("restore: could not remove the relations a consolidation copied")
		}
	}
	cur, err := observationStore.GetObservationByID(ctx, fold.SurvivorID)
	if err != nil || cur == nil {
		return
	}
	facts, concepts := removeStrings(cur.Facts, d.Facts), removeStrings(cur.Concepts, d.Concepts)
	read, modified := removeStrings(cur.FilesRead, d.FilesRead), removeStrings(cur.FilesModified, d.FilesModified)
	if _, err := observationStore.UpdateObservation(ctx, cur.ID, &gorm.ObservationUpdate{
		Facts: &facts, Concepts: &concepts, FilesRead: &read, FilesModified: &modified,
	}); err != nil {
		log.Warn().Err(err).Int64("fold", fold.ID).Msg("restore: could not take the merged fields back out of the survivor")
		return
	}
	s.afterExplicitWrite(cur.ID, fold.Project)
}

// runConsolidationPass folds near-identical notes together in every project, at most ConsolidationMaxPerRun groups. It
// compares the notes of a project with each other (no model is used) and never touches a protected note. It returns
// how many groups it consolidated.
func (s *Service) runConsolidationPass(ctx context.Context) int {
	cfg := s.config
	if cfg == nil {
		return 0
	}
	s.initMu.RLock()
	sessionStore, observationStore, store := s.sessionStore, s.observationStore, s.store
	s.initMu.RUnlock()
	if sessionStore == nil || observationStore == nil || store == nil {
		return 0
	}
	rows, err := sessionStore.ProjectSummaries(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("consolidation: could not list the projects")
		return 0
	}
	aliases, err := gorm.NewProjectAliasStore(store).AliasMap(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("consolidation: could not read the project aliases")
		return 0
	}
	budget := cfg.ConsolidationMaxPerRun
	if budget <= 0 {
		budget = 5
	}
	minSim := cfg.ConsolidationMinSimilarity
	if minSim < 0.5 || minSim > 1 {
		minSim = 0.92
	}
	done, snapshotted := 0, false
	for _, r := range rows {
		if budget <= 0 || ctx.Err() != nil {
			break
		}
		if _, isAlias := aliases[r.Project]; isAlias || r.Observations < 2 {
			continue
		}
		notes, err := observationStore.ConsolidationCandidates(ctx, r.Project, consolidationScanLimit)
		if err != nil {
			log.Warn().Err(err).Str("project", r.Project).Msg("consolidation: could not read the notes")
			continue
		}
		for _, group := range duplicateGroups(notes, minSim) {
			if budget <= 0 || ctx.Err() != nil {
				break
			}
			ids := make([]int64, len(group))
			for i, n := range group {
				ids[i] = n.ID
			}
			plan, err := s.planConsolidation(ctx, ids)
			if err != nil {
				log.Debug().Err(err).Str("project", r.Project).Msg("consolidation: group skipped")
				continue
			}
			if !snapshotted {
				s.snapshotBeforeCleanup(ctx, "consolidate")
				snapshotted = true
			}
			if _, err := s.applyConsolidation(ctx, plan); err != nil {
				log.Warn().Err(err).Str("project", r.Project).Msg("consolidation: group not consolidated; its notes stay live")
				continue
			}
			done++
			budget--
		}
	}
	return done
}

// consolidationLoop runs runConsolidationPass now and then. It only runs when consolidation is switched on.
func (s *Service) consolidationLoop() {
	defer s.wg.Done()

	interval := time.Duration(s.config.ConsolidationIntervalMinutes) * time.Minute
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
			s.runConsolidationPass(s.ctx)
			timer.Reset(interval)
		}
	}
}

type consolidateRequest struct {
	// Confirm is the token of a preview: with it the consolidation is applied, without it the plan is only shown.
	Confirm string  `json:"confirm"`
	IDs     []int64 `json:"ids"`
}

// handleConsolidate previews a consolidation of the given notes, or applies it when the preview's token comes back.
// POST /api/observations/consolidate
func (s *Service) handleConsolidate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	var req consolidateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	plan, err := s.planConsolidation(ctx, req.IDs)
	switch {
	case errors.Is(err, ErrConsolidationInvalid):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		http.Error(w, "failed to plan: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(req.Confirm) != "" {
		if req.Confirm != plan.Token {
			http.Error(w, ErrConsolidationStale.Error(), http.StatusConflict)
			return
		}
		s.snapshotBeforeCleanup(ctx, "consolidate")
		foldID, err := s.applyConsolidation(ctx, plan)
		if err != nil {
			log.Warn().Err(err).Str("project", plan.Project).Msg("consolidation: failed on request")
			http.Error(w, "failed to consolidate: "+err.Error(), http.StatusInternalServerError)
			return
		}
		plan.Applied, plan.FoldID = true, foldID
	}
	noStore(w)
	writeJSON(w, plan)
}
