package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
)

const snapshotsToKeep = 10

// projectActionResponse answers a delete or merge, as a preview or as the real thing.
type projectActionResponse struct {
	Action  string `json:"action"`
	Project string `json:"project"`
	Into    string `json:"into,omitempty"`
	// Confirm is the token that authorises exactly this preview. Present only on a dry run.
	Confirm string `json:"confirm,omitempty"`
	// Backup is the snapshot taken before the change. Present only after a real run.
	Backup  string            `json:"backup,omitempty"`
	Message string            `json:"message"`
	Stats   gorm.ProjectStats `json:"stats"`
	DryRun  bool              `json:"dry_run"`
}

// mergeRequest is the body of POST /api/projects/{id}/merge.
type mergeRequest struct {
	Into    string `json:"into"`
	Confirm string `json:"confirm"`
}

// confirmToken ties a confirmation to one project (and merge target) in its
// current state. If anything is added or removed between the preview and the
// confirmation, the token no longer matches and the action is refused.
func confirmToken(action, project, into string, stats gorm.ProjectStats) string {
	payload, _ := json.Marshal(stats)
	sum := sha256.Sum256([]byte(strings.Join([]string{action, project, into, string(payload)}, "|")))
	return hex.EncodeToString(sum[:])[:16]
}

// writeAdminError maps a store error to the right HTTP status.
func writeAdminError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gorm.ErrProjectNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, gorm.ErrProjectIsAlias):
		http.Error(w, err.Error()+": it is an alias, not a project. Remove the alias with DELETE /api/projects/aliases/{alias}, or act on the project it points to", http.StatusConflict)
	case errors.Is(err, gorm.ErrMergeSelf):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		log.Error().Err(err).Msg("project admin operation failed")
		http.Error(w, "operation failed", http.StatusInternalServerError)
	}
}

// handleProjectStats counts what a project holds.
func (s *Service) handleProjectStats(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "id")
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	admin := gorm.NewProjectAdminStore(s.store)
	if err := admin.CheckRemovable(r.Context(), project); err != nil {
		writeAdminError(w, err)
		return
	}
	stats, err := admin.Stats(r.Context(), project)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	noStore(w)
	writeJSON(w, stats)
}

// handleDeleteProject deletes a project and everything it owns. Without a
// confirmation token it only previews; the preview carries the token to send back.
func (s *Service) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "id")
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	admin := gorm.NewProjectAdminStore(s.store)
	if err := admin.CheckRemovable(r.Context(), project); err != nil {
		writeAdminError(w, err)
		return
	}
	stats, err := admin.Stats(r.Context(), project)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	token := confirmToken("delete", project, "", stats)

	noStore(w)
	confirm := r.URL.Query().Get("confirm")
	if confirm == "" {
		writeJSON(w, projectActionResponse{
			DryRun: true, Action: "delete", Project: project, Stats: stats, Confirm: token,
			Message: fmt.Sprintf("Would permanently delete project %s with %d observations, %d sessions, %d summaries and %d prompts. "+
				"Nothing was changed. Repeat the request with confirm=%s to proceed.", project, stats.Observations, stats.Sessions, stats.Summaries, stats.Prompts, token),
		})
		return
	}
	if confirm != token {
		http.Error(w, "confirmation does not match the project's current state; preview again", http.StatusConflict)
		return
	}

	backup, err := s.snapshotBefore(r.Context(), "delete-"+project)
	if err != nil {
		http.Error(w, "refusing to delete: could not take a backup first: "+err.Error(), http.StatusInternalServerError)
		return
	}
	removed, err := admin.Delete(r.Context(), project)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	s.afterProjectChange("deleted", project)
	log.Info().Str("project", project).Str("backup", backup).Msg("Project deleted")

	writeJSON(w, projectActionResponse{
		Action: "delete", Project: project, Stats: removed, Backup: backup,
		Message: fmt.Sprintf("Deleted project %s. A backup of the database before the change is at %s.", project, backup),
	})
}

// handleMergeProject moves a project's data into another and records an alias.
// Like delete, it previews first and needs the preview's token to proceed.
func (s *Service) handleMergeProject(w http.ResponseWriter, r *http.Request) {
	from := chi.URLParam(r, "id")
	var req mergeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Into = strings.TrimSpace(req.Into)
	if req.Into == "" {
		http.Error(w, "into is required", http.StatusBadRequest)
		return
	}
	for _, name := range []string{from, req.Into} {
		if err := ValidateProjectName(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Merging into an alias means merging into the project it stands for.
	into, _, err := gorm.NewProjectAliasStore(s.store).ResolveAlias(r.Context(), req.Into)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	if from == into {
		writeAdminError(w, gorm.ErrMergeSelf)
		return
	}

	admin := gorm.NewProjectAdminStore(s.store)
	if err := admin.CheckRemovable(r.Context(), from); err != nil {
		writeAdminError(w, err)
		return
	}
	if ok, err := admin.Exists(r.Context(), into); err != nil {
		writeAdminError(w, err)
		return
	} else if !ok {
		http.Error(w, fmt.Sprintf("target project %q does not exist; a merge never creates a project", into), http.StatusUnprocessableEntity)
		return
	}
	stats, err := admin.Stats(r.Context(), from)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	token := confirmToken("merge", from, into, stats)

	noStore(w)
	if req.Confirm == "" {
		writeJSON(w, projectActionResponse{
			DryRun: true, Action: "merge", Project: from, Into: into, Stats: stats, Confirm: token,
			Message: fmt.Sprintf("Would move %d observations, %d sessions and %d summaries from %s into %s, and make %s an alias of %s. "+
				"Nothing was changed. Repeat the request with confirm=%s to proceed.", stats.Observations, stats.Sessions, stats.Summaries, from, into, from, into, token),
		})
		return
	}
	if req.Confirm != token {
		http.Error(w, "confirmation does not match the project's current state; preview again", http.StatusConflict)
		return
	}

	backup, err := s.snapshotBefore(r.Context(), "merge-"+from)
	if err != nil {
		http.Error(w, "refusing to merge: could not take a backup first: "+err.Error(), http.StatusInternalServerError)
		return
	}
	moved, err := admin.Merge(r.Context(), from, into, "merge")
	if err != nil {
		writeAdminError(w, err)
		return
	}
	s.afterProjectChange("merged", from)
	s.afterProjectChange("merged", into)
	log.Info().Str("from", from).Str("into", into).Str("backup", backup).Msg("Project merged")

	writeJSON(w, projectActionResponse{
		Action: "merge", Project: from, Into: into, Stats: moved, Backup: backup,
		Message: fmt.Sprintf("Merged %s into %s; %s now resolves to %s. A backup of the database before the change is at %s.", from, into, from, into, backup),
	})
}

// snapshotBefore backs the database up before a destructive change.
func (s *Service) snapshotBefore(ctx context.Context, label string) (string, error) {
	return s.store.Snapshot(ctx, s.store.DefaultSnapshotDir(), label, snapshotsToKeep)
}

// afterProjectChange refreshes cached counts and tells the dashboard.
func (s *Service) afterProjectChange(action, project string) {
	s.invalidateObsCountCache(project)
	if s.sseBroadcaster != nil {
		s.sseBroadcaster.Broadcast(map[string]any{"type": "project", "action": action, "project": project})
	}
}
