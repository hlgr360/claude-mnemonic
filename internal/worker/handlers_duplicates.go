package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/projects"
)

// duplicateTitleLimit is how many titles of a project are compared with its namesake's.
const duplicateTitleLimit = 300

// duplicateReport is the pairs of projects that are probably one, with what the projects look like.
type duplicateReport struct {
	view        *projectView
	suggestions []projects.DuplicateSuggestion
	dismissed   []gorm.ProjectDuplicateDismissal
	// hidden is how many pairs a person already said are not the same.
	hidden int
}

// pathStillThere reports whether a recorded checkout folder still exists.
func pathStillThere(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// duplicateReport works out which projects are probably the same project. Name and git remote find the pairs;
// evidence confirms them (see projects.SuggestDuplicates). It changes nothing.
func (s *Service) duplicateReport(ctx context.Context) (*duplicateReport, error) {
	view, err := s.loadProjectView(ctx)
	if err != nil {
		return nil, err
	}
	store := s.identityStore
	if store == nil {
		store = gorm.NewProjectIdentityStore(s.store)
	}
	identities, err := store.Identities(ctx)
	if err != nil {
		return nil, err
	}
	dismissedPairs, err := store.Dismissed(ctx)
	if err != nil {
		return nil, err
	}

	cands := make([]projects.DuplicateCandidate, 0, len(view.rows))
	names := map[string]int{}
	remotes := map[string]int{}
	for _, row := range view.rows {
		if _, isAlias := view.aliases[row.Project]; isAlias {
			continue
		}
		c := projects.DuplicateCandidate{ID: row.Project, Observations: int(row.Observations), LastActiveEpoch: row.LastActiveEpoch}
		seen := map[string]bool{}
		for _, id := range identities[row.Project] {
			c.Identities = append(c.Identities, projects.Identity{Remote: id.Remote, Root: id.RootPath})
			if id.Remote != "" && !seen[id.Remote] {
				seen[id.Remote] = true
				remotes[id.Remote]++
			}
		}
		names[projects.DisplayName(row.Project)]++
		cands = append(cands, c)
	}
	// Titles are only compared for projects that have a namesake or share a remote with another.
	for i := range cands {
		c := &cands[i]
		inGroup := names[projects.DisplayName(c.ID)] > 1
		for _, id := range c.Identities {
			if id.Remote != "" && remotes[id.Remote] > 1 {
				inGroup = true
			}
		}
		if !inGroup {
			continue
		}
		if c.Titles, err = store.NormalizedTitles(ctx, c.ID, duplicateTitleLimit); err != nil {
			return nil, err
		}
	}

	dismissed := func(a, b string) bool {
		x, y := gorm.OrderedPair(a, b)
		return dismissedPairs[[2]string{x, y}]
	}
	suggestions, hidden := projects.SuggestDuplicates(cands, dismissed, pathStillThere)

	rep := &duplicateReport{view: view, suggestions: suggestions, hidden: hidden}
	for pair := range dismissedPairs {
		rep.dismissed = append(rep.dismissed, gorm.ProjectDuplicateDismissal{A: pair[0], B: pair[1]})
	}
	return rep, nil
}

type duplicateProject struct {
	Project         string `json:"project"`
	Label           string `json:"label"`
	DisplayName     string `json:"display_name"`
	Observations    int64  `json:"observations"`
	LastActiveEpoch int64  `json:"last_active_epoch"`
}

type duplicateItem struct {
	Survivor duplicateProject `json:"survivor"`
	Other    duplicateProject `json:"other"`
	projects.DuplicateSuggestion
}

type dismissedItem struct {
	A duplicateProject `json:"a"`
	B duplicateProject `json:"b"`
}

func (r *duplicateReport) describe(id string) duplicateProject {
	d := duplicateProject{Project: id, Label: id, DisplayName: projects.DisplayName(id)}
	if l, ok := r.view.labels[id]; ok && l.Label != "" {
		d.Label = l.Label
	}
	for _, row := range r.view.rows {
		if row.Project == id {
			d.Observations, d.LastActiveEpoch = row.Observations, row.LastActiveEpoch
		}
	}
	return d
}

// handleListDuplicates lists the pairs of projects that are probably one project, strongest evidence first, and the
// pairs a person already said are not.
// GET /api/projects/duplicates
func (s *Service) handleListDuplicates(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rep, err := s.duplicateReport(ctx)
	if err != nil {
		http.Error(w, "failed to look for duplicate projects", http.StatusInternalServerError)
		return
	}
	items := make([]duplicateItem, 0, len(rep.suggestions))
	for _, sg := range rep.suggestions {
		items = append(items, duplicateItem{Survivor: rep.describe(sg.Survivor), Other: rep.describe(sg.Other), DuplicateSuggestion: sg})
	}
	dismissed := make([]dismissedItem, 0, len(rep.dismissed))
	for _, d := range rep.dismissed {
		if _, ok := rep.view.infos[d.A]; !ok {
			continue
		}
		if _, ok := rep.view.infos[d.B]; !ok {
			continue
		}
		dismissed = append(dismissed, dismissedItem{A: rep.describe(d.A), B: rep.describe(d.B)})
	}
	noStore(w)
	writeJSON(w, map[string]any{
		"suggestions":    items,
		"dismissed":      dismissed,
		"auto_merge":     s.config != nil && s.config.ProjectAutoMergeEnabled,
		"dismissed_hide": rep.hidden,
	})
}

type pairRequest struct {
	A string `json:"a"`
	B string `json:"b"`
}

// readPair reads and validates the two projects of a dismiss or restore request.
func (s *Service) readPair(w http.ResponseWriter, r *http.Request) (a, b string, ok bool) {
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return "", "", false
	}
	req.A, req.B = strings.TrimSpace(req.A), strings.TrimSpace(req.B)
	if req.A == "" || req.B == "" {
		http.Error(w, "a and b are required", http.StatusBadRequest)
		return "", "", false
	}
	for _, name := range []string{req.A, req.B} {
		if err := ValidateProjectName(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return "", "", false
		}
	}
	if req.A == req.B {
		http.Error(w, "two different projects are required", http.StatusBadRequest)
		return "", "", false
	}
	return req.A, req.B, true
}

// handleDismissDuplicate remembers that two projects are not the same, so the pair is not suggested again.
// POST /api/projects/duplicates/dismiss {a, b}
func (s *Service) handleDismissDuplicate(w http.ResponseWriter, r *http.Request) {
	a, b, ok := s.readPair(w, r)
	if !ok {
		return
	}
	if err := gorm.NewProjectIdentityStore(s.store).Dismiss(r.Context(), a, b); err != nil {
		http.Error(w, "failed to remember that", http.StatusInternalServerError)
		return
	}
	s.afterProjectChange("duplicates_changed", a)
	noStore(w)
	writeJSON(w, map[string]any{"dismissed": true, "a": a, "b": b})
}

// handleRestoreDuplicate forgets a dismissal, so the pair can be suggested again.
// POST /api/projects/duplicates/restore {a, b}
func (s *Service) handleRestoreDuplicate(w http.ResponseWriter, r *http.Request) {
	a, b, ok := s.readPair(w, r)
	if !ok {
		return
	}
	restored, err := gorm.NewProjectIdentityStore(s.store).Restore(r.Context(), a, b)
	if err != nil {
		http.Error(w, "failed to forget that", http.StatusInternalServerError)
		return
	}
	s.afterProjectChange("duplicates_changed", a)
	noStore(w)
	writeJSON(w, map[string]any{"restored": restored, "a": a, "b": b})
}

// probablySame returns, for each of the given projects, the other given projects it is probably the same as (a strong
// or medium suggestion that nobody dismissed), by id. It is what the Desktop tools say when they list candidates.
func (s *Service) probablySame(ctx context.Context, ids []string) map[string][]string {
	out := map[string][]string{}
	if len(ids) < 2 {
		return out
	}
	rep, err := s.duplicateReport(ctx)
	if err != nil {
		log.Debug().Err(err).Msg("duplicate hints unavailable")
		return out
	}
	in := map[string]bool{}
	for _, id := range ids {
		in[id] = true
	}
	for _, sg := range rep.suggestions {
		if sg.Strength == projects.StrengthWeak || !in[sg.Survivor] || !in[sg.Other] {
			continue
		}
		out[sg.Survivor] = append(out[sg.Survivor], sg.Other)
		out[sg.Other] = append(out[sg.Other], sg.Survivor)
	}
	return out
}
