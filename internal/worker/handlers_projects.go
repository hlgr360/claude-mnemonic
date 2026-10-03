package worker

import (
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

// noStore marks a response as uncacheable. Unlike GET /api/projects, which the
// dashboard may cache, these answers must reflect the latest alias changes.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// handleListProjectSummaries returns every project with its footprint and alias relations.
func (s *Service) handleListProjectSummaries(w http.ResponseWriter, r *http.Request) {
	rows, err := s.sessionStore.ProjectSummaries(r.Context())
	if err != nil {
		http.Error(w, "failed to list projects", http.StatusInternalServerError)
		return
	}
	aliases, err := gorm.NewProjectAliasStore(s.store).AliasMap(r.Context())
	if err != nil {
		http.Error(w, "failed to load aliases", http.StatusInternalServerError)
		return
	}

	reverse := map[string][]string{}
	for alias, canonical := range aliases {
		reverse[canonical] = append(reverse[canonical], alias)
	}

	out := make([]projectSummaryResponse, 0, len(rows))
	for _, row := range rows {
		resp := projectSummaryResponse{
			DisplayName:    projects.DisplayName(row.Project),
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

	rows, err := s.sessionStore.ProjectSummaries(r.Context())
	if err != nil {
		http.Error(w, "failed to list projects", http.StatusInternalServerError)
		return
	}
	known := make([]string, 0, len(rows))
	for _, row := range rows {
		known = append(known, row.Project)
	}
	aliases, err := gorm.NewProjectAliasStore(s.store).AliasMap(r.Context())
	if err != nil {
		http.Error(w, "failed to load aliases", http.StatusInternalServerError)
		return
	}

	noStore(w)
	writeJSON(w, projects.Resolve(ref, known, aliases))
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
