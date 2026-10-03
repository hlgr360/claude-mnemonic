package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/projects"
)

// projectSummaryResponse is one row of GET /api/projects/summary.
type projectSummaryResponse struct {
	DisplayName string `json:"display_name"`
	// Label is how to show the project to a person, Use what to pass back to refer to it
	// (see projects.Labels): the name when it is unique, otherwise the id.
	Label string `json:"label"`
	Use   string `json:"use"`
	// SampleTitles are a couple of the project's observation titles, to tell namesakes apart.
	SampleTitles []string `json:"sample_titles,omitempty"`
	// AliasOf is set when this project ID has been declared an alias of another project.
	AliasOf string `json:"alias_of,omitempty"`
	// Aliases lists the project IDs that resolve to this project.
	Aliases []string `json:"aliases,omitempty"`
	gorm.ProjectSummary
}

// setAliasRequest is the body of POST /api/projects/aliases.
type setAliasRequest struct {
	Alias     string `json:"alias"`
	Canonical string `json:"canonical"`
	Source    string `json:"source"`
}

// candidateDetail describes one project among several that share a name.
type candidateDetail struct {
	Project         string   `json:"project"`
	DisplayName     string   `json:"display_name"`
	Label           string   `json:"label"`
	Detail          string   `json:"detail"`
	SampleTitles    []string `json:"sample_titles,omitempty"`
	Observations    int64    `json:"observations"`
	Sessions        int64    `json:"sessions"`
	LastActiveEpoch int64    `json:"last_active_epoch"`
}

// resolveResponse is a Resolution plus, when it names candidates, what tells them apart.
type resolveResponse struct {
	projects.Resolution
	CandidateDetails []candidateDetail `json:"candidate_details,omitempty"`
}

// projectView is everything needed to label and describe projects.
type projectView struct {
	aliases map[string]string
	labels  map[string]projects.Label
	titles  map[string][]string
	infos   map[string]projects.Info
	rows    []gorm.ProjectSummary
}

// loadProjectView reads the projects, aliases and sample titles and works out their labels.
func (s *Service) loadProjectView(ctx context.Context) (*projectView, error) {
	rows, err := s.sessionStore.ProjectSummaries(ctx)
	if err != nil {
		return nil, err
	}
	aliases, err := gorm.NewProjectAliasStore(s.store).AliasMap(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Project)
	}
	titles, err := s.sessionStore.ProjectSampleTitles(ctx, ids, 2)
	if err != nil {
		return nil, err
	}

	v := &projectView{rows: rows, aliases: aliases, titles: titles, infos: make(map[string]projects.Info, len(rows))}
	infos := make([]projects.Info, 0, len(rows))
	for _, r := range rows {
		in := projects.Info{ID: r.Project, Observations: r.Observations, LastActiveEpoch: r.LastActiveEpoch}
		if t := titles[r.Project]; len(t) > 0 {
			in.SampleTitle = t[0]
		}
		v.infos[r.Project] = in
		infos = append(infos, in)
	}
	v.labels = projects.Labels(infos, aliases)
	return v, nil
}

// details describes the given project ids for a person choosing between them.
func (v *projectView) details(ids []string) []candidateDetail {
	out := make([]candidateDetail, 0, len(ids))
	byID := make(map[string]gorm.ProjectSummary, len(v.rows))
	for _, r := range v.rows {
		byID[r.Project] = r
	}
	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			continue
		}
		in := v.infos[id]
		out = append(out, candidateDetail{
			Project: id, DisplayName: projects.DisplayName(id), Label: v.labels[id].Label, Detail: projects.Detail(in),
			SampleTitles: v.titles[id], Observations: row.Observations, Sessions: row.Sessions, LastActiveEpoch: row.LastActiveEpoch,
		})
	}
	return out
}

// noStore marks a response as uncacheable. Unlike GET /api/projects, which the
// dashboard may cache, these answers must reflect the latest alias changes.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// handleListProjectSummaries returns every project with its footprint and alias relations.
func (s *Service) handleListProjectSummaries(w http.ResponseWriter, r *http.Request) {
	view, err := s.loadProjectView(r.Context())
	if err != nil {
		http.Error(w, "failed to list projects", http.StatusInternalServerError)
		return
	}
	aliases := view.aliases

	reverse := map[string][]string{}
	for alias, canonical := range aliases {
		reverse[canonical] = append(reverse[canonical], alias)
	}

	out := make([]projectSummaryResponse, 0, len(view.rows))
	for _, row := range view.rows {
		resp := projectSummaryResponse{
			DisplayName:    projects.DisplayName(row.Project),
			Label:          view.labels[row.Project].Label,
			Use:            view.labels[row.Project].Use,
			SampleTitles:   view.titles[row.Project],
			AliasOf:        aliases[row.Project],
			Aliases:        reverse[row.Project],
			ProjectSummary: row,
		}
		sort.Strings(resp.Aliases)
		out = append(out, resp)
	}

	noStore(w)
	writeJSON(w, out)
}

// handleResolveProject resolves an id, host path or name to a canonical project.
func (s *Service) handleResolveProject(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref := projects.Ref{ID: q.Get("id"), Path: q.Get("path"), Name: q.Get("name")}
	if ref.ID == "" && ref.Path == "" && ref.Name == "" {
		http.Error(w, "one of id, path or name is required", http.StatusBadRequest)
		return
	}

	view, err := s.loadProjectView(r.Context())
	if err != nil {
		http.Error(w, "failed to list projects", http.StatusInternalServerError)
		return
	}
	known := make([]string, 0, len(view.rows))
	for _, row := range view.rows {
		known = append(known, row.Project)
	}

	res := projects.Resolve(ref, known, view.aliases)
	resp := resolveResponse{Resolution: res}
	if len(res.Candidates) > 0 {
		resp.CandidateDetails = view.details(res.Candidates)
	}

	noStore(w)
	writeJSON(w, resp)
}

// handleListProjectAliases returns every alias.
func (s *Service) handleListProjectAliases(w http.ResponseWriter, r *http.Request) {
	rows, err := gorm.NewProjectAliasStore(s.store).ListAliases(r.Context())
	if err != nil {
		http.Error(w, "failed to list aliases", http.StatusInternalServerError)
		return
	}
	noStore(w)
	writeJSON(w, rows)
}

// handleSetProjectAlias records that one project ID is an alias of another.
func (s *Service) handleSetProjectAlias(w http.ResponseWriter, r *http.Request) {
	var req setAliasRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Alias, req.Canonical = strings.TrimSpace(req.Alias), strings.TrimSpace(req.Canonical)
	if req.Alias == "" || req.Canonical == "" {
		http.Error(w, "alias and canonical are required", http.StatusBadRequest)
		return
	}
	for _, name := range []string{req.Alias, req.Canonical} {
		if err := ValidateProjectName(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	store := gorm.NewProjectAliasStore(s.store)
	if err := store.SetAlias(r.Context(), req.Alias, req.Canonical, req.Source); err != nil {
		if errors.Is(err, gorm.ErrAliasSelf) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to set alias", http.StatusInternalServerError)
		return
	}

	// Report where the alias ended up: the store flattens chains.
	canonical, _, err := store.ResolveAlias(r.Context(), req.Alias)
	if err != nil {
		http.Error(w, "failed to read alias back", http.StatusInternalServerError)
		return
	}
	noStore(w)
	writeJSON(w, map[string]string{"alias": req.Alias, "canonical": canonical})
}

// handleDeleteProjectAlias removes an alias.
func (s *Service) handleDeleteProjectAlias(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "alias")
	deleted, err := gorm.NewProjectAliasStore(s.store).DeleteAlias(r.Context(), alias)
	if err != nil {
		http.Error(w, "failed to delete alias", http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.Error(w, "alias not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
