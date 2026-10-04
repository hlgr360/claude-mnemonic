package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
)

// rescopeSample is how many of the changes a preview lists.
const rescopeSample = 25

// rescopeResponse answers GET /api/scope/preview and POST /api/scope/apply.
type rescopeResponse struct {
	// Confirm is the token that authorises exactly this preview. Present only on a preview.
	Confirm string `json:"confirm,omitempty"`
	// Backup is the snapshot taken before the change. Present only after a real run with something to change.
	Backup  string `json:"backup,omitempty"`
	Message string `json:"message"`
	// Sample lists the first changes, with the note's title.
	Sample []gorm.ScopeChange `json:"sample"`
	// Total is every observation; Protected those whose scope a person or a client chose (never changed);
	// Unchanged those the rule agrees with.
	Total     int  `json:"total"`
	Protected int  `json:"protected"`
	Unchanged int  `json:"unchanged"`
	ToProject int  `json:"to_project"`
	ToGlobal  int  `json:"to_global"`
	Changed   int  `json:"changed"`
	DryRun    bool `json:"dry_run"`
}

func scopeResponse(plan *gorm.RescopePlan) rescopeResponse {
	sample := plan.Changes
	if len(sample) > rescopeSample {
		sample = sample[:rescopeSample]
	}
	return rescopeResponse{
		Total: plan.Total, Protected: plan.Protected, Unchanged: plan.Unchanged,
		ToProject: plan.ToProject, ToGlobal: plan.ToGlobal, Sample: sample,
	}
}

// handleScopePreview shows what applying the current scope rule to the archive would change, and changes nothing.
// GET /api/scope/preview
func (s *Service) handleScopePreview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	plan, err := s.observationStore.PlanRescope(ctx)
	if err != nil {
		log.Error().Err(err).Msg("scope: could not plan a re-scope")
		http.Error(w, "failed to plan the re-scope", http.StatusInternalServerError)
		return
	}
	resp := scopeResponse(plan)
	resp.DryRun = true
	resp.Confirm = plan.Token()
	resp.Message = fmt.Sprintf("%d of %d notes would change scope (%d to project, %d to global). %d keep a scope that was chosen by hand. Nothing was changed.",
		len(plan.Changes), plan.Total, plan.ToProject, plan.ToGlobal, plan.Protected)
	noStore(w)
	writeJSON(w, resp)
}

// handleScopeApply applies the current scope rule to the archive: POST /api/scope/apply {"confirm": "<token>"}. The token
// is the one a preview returned; if the archive changed since, it no longer matches and nothing happens. A snapshot of the
// database is taken first.
func (s *Service) handleScopeApply(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Confirm == "" {
		http.Error(w, "confirm is required: preview first (GET /api/scope/preview) and send its token back", http.StatusBadRequest)
		return
	}
	plan, err := s.observationStore.PlanRescope(ctx)
	if err != nil {
		http.Error(w, "failed to plan the re-scope", http.StatusInternalServerError)
		return
	}
	if body.Confirm != plan.Token() {
		http.Error(w, "confirmation does not match the archive's current state; preview again", http.StatusConflict)
		return
	}
	resp := scopeResponse(plan)
	noStore(w)
	if len(plan.Changes) == 0 {
		resp.Message = "Nothing to change."
		writeJSON(w, resp)
		return
	}

	backup, err := s.snapshotBefore(ctx, "rescope")
	if err != nil {
		http.Error(w, "refusing to change scopes: could not take a backup first: "+err.Error(), http.StatusInternalServerError)
		return
	}
	changed, err := s.observationStore.ApplyRescope(ctx, plan)
	if err != nil {
		log.Error().Err(err).Msg("scope: re-scope failed")
		http.Error(w, "failed to change the scopes (nothing was changed)", http.StatusInternalServerError)
		return
	}
	if s.vectorClient != nil {
		s.vectorClient.InvalidateResultCache()
	}
	if s.sseBroadcaster != nil {
		s.sseBroadcaster.Broadcast(map[string]any{"type": "observation", "action": "rescoped"})
	}
	resp.Backup, resp.Changed = backup, changed
	resp.Message = fmt.Sprintf("Changed the scope of %d notes. A backup was taken first: %s", changed, backup)
	writeJSON(w, resp)
}
