package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/privacy"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	rememberMaxBody    = 256 << 10
	rememberTitleRunes = 80
	rememberDefaultTyp = string(models.ObsTypeDiscovery)
)

// RememberRequest is the body of POST /api/observations/remember: one
// observation written on purpose by a client that has no hooks (Claude Desktop).
type RememberRequest struct {
	Project string `json:"project"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	Type    string `json:"type"`
	Scope   string `json:"scope"`
	// Source names the client that wrote the memory, for example "claude-ai".
	Source   string   `json:"source"`
	Concepts []string `json:"concepts"`
	// AllowNewProject lets the write create a project that has no history yet.
	// Callers set it only when the project came from a real host path.
	AllowNewProject bool `json:"allow_new_project"`
}

// RememberResponse reports where the memory landed.
type RememberResponse struct {
	Project   string `json:"project"`
	ID        int64  `json:"id"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// handleRemember stores one observation in an explicitly named project.
//
// The project is mandatory and must already exist (or the caller must say a new
// one is intended), so a client that guesses or invents a project cannot create
// stray ones. Aliases are followed so writes land on the canonical project.
func (s *Service) handleRemember(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var req RememberRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, rememberMaxBody)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	project := strings.TrimSpace(req.Project)
	if project == "" {
		http.Error(w, "project is required: remembering without a project would store to nowhere", http.StatusBadRequest)
		return
	}
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if privacy.IsEntirelyPrivate(req.Text) {
		http.Error(w, "nothing to store: text is empty or entirely private", http.StatusBadRequest)
		return
	}
	text := privacy.Clean(req.Text)
	if text == "" {
		http.Error(w, "nothing to store: text is empty or entirely private", http.StatusBadRequest)
		return
	}

	obsType := req.Type
	if obsType == "" {
		obsType = rememberDefaultTyp
	}
	if !IsValidObservationType(obsType) {
		http.Error(w, fmt.Sprintf("invalid type %q", obsType), http.StatusBadRequest)
		return
	}
	scope := models.ObservationScope(req.Scope)
	switch scope {
	case "":
		scope = models.ScopeProject
	case models.ScopeProject, models.ScopeGlobal:
	default:
		http.Error(w, fmt.Sprintf("invalid scope %q", req.Scope), http.StatusBadRequest)
		return
	}

	canonical, _, err := gorm.NewProjectAliasStore(s.store).ResolveAlias(ctx, project)
	if err != nil {
		http.Error(w, "failed to resolve project", http.StatusInternalServerError)
		return
	}
	if !req.AllowNewProject {
		known, kerr := s.projectExists(ctx, canonical)
		if kerr != nil {
			http.Error(w, "failed to check project", http.StatusInternalServerError)
			return
		}
		if !known {
			http.Error(w, fmt.Sprintf("unknown project %q: pick an existing project, or pass the folder path to start a new one", canonical),
				http.StatusUnprocessableEntity)
			return
		}
	}

	title := strings.TrimSpace(privacy.Clean(req.Title))
	if title == "" {
		title = deriveTitle(text)
	}

	if id, found, derr := s.findDuplicateObservation(ctx, canonical, title, text); derr != nil {
		http.Error(w, "failed to check for duplicates", http.StatusInternalServerError)
		return
	} else if found {
		noStore(w)
		writeJSON(w, RememberResponse{Project: canonical, ID: id, Duplicate: true})
		return
	}

	parsed := &models.ParsedObservation{
		Type:      models.ObservationType(obsType),
		Title:     title,
		Narrative: text,
		Scope:     scope,
		Concepts:  req.Concepts,
	}
	if src := strings.TrimSpace(req.Source); src != "" {
		parsed.Facts = []string{"Saved explicitly via " + src}
	}

	sessionID := fmt.Sprintf("remember-%s-%s", canonical, time.Now().Format("20060102"))
	id, _, err := s.observationStore.StoreObservation(ctx, sessionID, canonical, parsed, 0, 0)
	if err != nil {
		log.Error().Err(err).Str("project", canonical).Msg("remember: failed to store observation")
		http.Error(w, "failed to store observation", http.StatusInternalServerError)
		return
	}

	s.afterExplicitWrite(id, canonical)

	noStore(w)
	writeJSON(w, RememberResponse{Project: canonical, ID: id})
}

// projectExists reports whether the project has any sessions or observations.
func (s *Service) projectExists(ctx context.Context, project string) (bool, error) {
	rows, err := s.sessionStore.ProjectSummaries(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Project == project {
			return true, nil
		}
	}
	return false, nil
}

// findDuplicateObservation returns an existing observation in the project with
// the same title and narrative, so a model that repeats a call does not store twice.
func (s *Service) findDuplicateObservation(ctx context.Context, project, title, narrative string) (int64, bool, error) {
	var ids []int64
	err := s.store.DB.WithContext(ctx).
		Model(&gorm.Observation{}).
		Where("project = ? AND title = ? AND narrative = ?", project, title, narrative).
		Limit(1).
		Pluck("id", &ids).Error
	if err != nil {
		return 0, false, err
	}
	if len(ids) == 0 {
		return 0, false, nil
	}
	return ids[0], true, nil
}

// afterExplicitWrite mirrors what bulk import does after storing: sync the
// vector index, refresh counts and tell the dashboard.
func (s *Service) afterExplicitWrite(obsID int64, project string) {
	if s.vectorSync != nil {
		s.asyncVectorSync(func() {
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			defer cancel()
			obs, err := s.observationStore.GetObservationByID(ctx, obsID)
			if err != nil || obs == nil {
				return
			}
			if syncErr := s.vectorSync.SyncObservation(ctx, obs); syncErr != nil && s.ctx.Err() == nil {
				log.Debug().Err(syncErr).Int64("id", obsID).Msg("remember: failed to sync observation")
			}
		})
	}
	s.invalidateObsCountCache(project)
	if s.sseBroadcaster != nil {
		s.sseBroadcaster.Broadcast(map[string]any{
			"type":    "observation",
			"action":  "remember",
			"project": project,
			"count":   1,
		})
	}
}

// deriveTitle makes a short title from the first non-empty line of text.
func deriveTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return truncateTitle(line)
	}
	return "Untitled memory"
}

func truncateTitle(s string) string {
	r := []rune(s)
	if len(r) <= rememberTitleRunes {
		return s
	}
	return strings.TrimSpace(string(r[:rememberTitleRunes])) + "…"
}
