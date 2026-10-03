package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// conflictProposerManual is recorded on proposals a person made by hand.
const conflictProposerManual = "manual"

// ConflictItem is one proposal as the dashboard sees it: the conflict, both observations in full and, for a
// decision that hid a note while hidden notes are cleaned up, until when it can still be undone.
type ConflictItem struct {
	Older *models.Observation `json:"older"`
	Newer *models.Observation `json:"newer"`
	models.ObservationConflict
	// RestorableUntilEpoch is when the hidden note will be deleted (milliseconds), only when retention is on.
	RestorableUntilEpoch int64 `json:"restorable_until_epoch,omitempty"`
}

// ConflictListResponse is the body of GET /api/conflicts.
type ConflictListResponse struct {
	Conflicts []ConflictItem `json:"conflicts"`
	// Total is how many match the filter, OpenCount how many wait for a decision (in the same project filter).
	Total     int `json:"total"`
	OpenCount int `json:"open_count"`
}

func (s *Service) conflictItem(rec *gorm.ConflictRecord) ConflictItem {
	item := ConflictItem{Older: rec.Older, Newer: rec.Newer, ObservationConflict: *rec.Conflict}
	if days := s.config.SupersededRetentionDays; days > 0 && rec.Conflict.SupersededObsID != 0 && rec.Conflict.ResolvedAtEpoch > 0 {
		item.RestorableUntilEpoch = rec.Conflict.ResolvedAtEpoch + int64(days)*24*int64(time.Hour/time.Millisecond)
	}
	return item
}

// conflictProjectFilter reads the optional project query parameter and resolves aliases.
func (s *Service) conflictProjectFilter(ctx context.Context, w http.ResponseWriter, r *http.Request) (string, bool) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		return "", true
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

// handleListConflicts lists proposals: ?status=open|resolved|all (default open), ?project=, ?limit=, ?offset=.
func (s *Service) handleListConflicts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	project, ok := s.conflictProjectFilter(ctx, w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch status {
	case "":
		status = gorm.ConflictStatusOpen
	case gorm.ConflictStatusOpen, gorm.ConflictStatusResolved, gorm.ConflictStatusAll:
	default:
		http.Error(w, "status must be open, resolved or all", http.StatusBadRequest)
		return
	}

	records, total, err := s.conflictStore.ListConflicts(ctx, gorm.ConflictFilter{
		Project: project, Status: status,
		Limit: gorm.ParseLimitParamWithMax(r, 50, 200), Offset: gorm.ParseOffsetParam(r),
	})
	if err != nil {
		log.Error().Err(err).Msg("conflicts: failed to list")
		http.Error(w, "failed to list conflicts", http.StatusInternalServerError)
		return
	}
	open, err := s.conflictStore.CountOpen(ctx, project)
	if err != nil {
		http.Error(w, "failed to count conflicts", http.StatusInternalServerError)
		return
	}
	resp := ConflictListResponse{Conflicts: make([]ConflictItem, 0, len(records)), Total: total, OpenCount: open}
	for _, rec := range records {
		resp.Conflicts = append(resp.Conflicts, s.conflictItem(rec))
	}
	noStore(w)
	writeJSON(w, resp)
}

// handleCountConflicts returns how many proposals wait for a decision, for the dashboard's tab badge.
func (s *Service) handleCountConflicts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	project, ok := s.conflictProjectFilter(ctx, w, r)
	if !ok {
		return
	}
	open, err := s.conflictStore.CountOpen(ctx, project)
	if err != nil {
		http.Error(w, "failed to count conflicts", http.StatusInternalServerError)
		return
	}
	noStore(w)
	writeJSON(w, map[string]int{"open": open})
}

// conflictError turns the review store's errors into HTTP answers.
func conflictError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, gorm.ErrConflictNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, gorm.ErrConflictResolved), errors.Is(err, gorm.ErrConflictOpen):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, gorm.ErrObservationGone):
		http.Error(w, err.Error(), http.StatusGone)
	case errors.Is(err, gorm.ErrBadDecision):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, gorm.ErrInvalidPair):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		log.Error().Err(err).Msg("conflicts: " + what + " failed")
		http.Error(w, "failed to "+what, http.StatusInternalServerError)
	}
}

// handleResolveConflict applies a decision: {"decision": "supersede_older" | "supersede_newer" | "keep_both"}.
func (s *Service) handleResolveConflict(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id, ok := parseIDParam(w, chi.URLParam(r, "id"), "conflict")
	if !ok {
		return
	}
	var body struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rec, err := s.conflictStore.ResolveProposal(ctx, id, body.Decision)
	if err != nil {
		conflictError(w, err, "resolve the conflict")
		return
	}
	s.broadcastConflictChange("resolved")
	noStore(w)
	writeJSON(w, s.conflictItem(rec))
}

// handleUndoConflict takes a decision back: the note it hid is visible again and the proposal is open again.
func (s *Service) handleUndoConflict(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id, ok := parseIDParam(w, chi.URLParam(r, "id"), "conflict")
	if !ok {
		return
	}
	rec, err := s.conflictStore.UndoProposal(ctx, id)
	if err != nil {
		conflictError(w, err, "undo the decision")
		return
	}
	s.broadcastConflictChange("reopened")
	noStore(w)
	writeJSON(w, s.conflictItem(rec))
}

// handleCreateConflict lets a person propose a pair by hand: {"older_id", "newer_id", "reason"}. The two are
// put in order by their age whichever way they were given. It answers 201 for a new proposal and 200 with the
// existing one when the pair is already known (open or decided).
func (s *Service) handleCreateConflict(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var body struct {
		Reason  string `json:"reason"`
		OlderID int64  `json:"older_id"`
		NewerID int64  `json:"newer_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.OlderID <= 0 || body.NewerID <= 0 || body.OlderID == body.NewerID {
		http.Error(w, "older_id and newer_id must be two different observation ids", http.StatusBadRequest)
		return
	}
	pair, err := s.observationStore.GetObservationsByIDsPreserveOrder(ctx, []int64{body.OlderID, body.NewerID})
	if err != nil {
		http.Error(w, "failed to read the observations", http.StatusInternalServerError)
		return
	}
	if len(pair) != 2 {
		http.Error(w, gorm.ErrObservationGone.Error(), http.StatusNotFound)
		return
	}
	older, newer := pair[0], pair[1]
	if older.CreatedAtEpoch > newer.CreatedAtEpoch || (older.CreatedAtEpoch == newer.CreatedAtEpoch && older.ID > newer.ID) {
		older, newer = newer, older
	}

	id, created, err := s.conflictStore.Propose(ctx, gorm.Proposal{
		Type: models.ConflictSuperseded, Relation: conflictProposerManual, Reason: strings.TrimSpace(body.Reason),
		Proposer: conflictProposerManual, NewerID: newer.ID, OlderID: older.ID,
	})
	if err != nil {
		conflictError(w, err, "create the proposal")
		return
	}
	rec, err := s.conflictStore.GetConflict(ctx, id)
	if err != nil {
		conflictError(w, err, "read the proposal")
		return
	}
	if created {
		s.broadcastConflictChange("proposed")
		w.WriteHeader(http.StatusCreated)
	}
	noStore(w)
	writeJSON(w, s.conflictItem(rec))
}
