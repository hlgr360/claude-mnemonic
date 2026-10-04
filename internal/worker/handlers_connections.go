package worker

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// parseRelationTypes reads a comma separated list of relation types; empty means every type.
func parseRelationTypes(csv string) ([]models.RelationType, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil, nil
	}
	valid := map[string]bool{}
	names := make([]string, 0, len(models.AllRelationTypes))
	for _, t := range models.AllRelationTypes {
		valid[string(t)] = true
		names = append(names, string(t))
	}
	var types []models.RelationType
	for _, t := range strings.Split(csv, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !valid[t] {
			return nil, fmt.Errorf("unknown relation type: %s (one of %s)", t, strings.Join(names, ", "))
		}
		types = append(types, models.RelationType(t))
	}
	return types, nil
}

// parseMinConfidence reads ?min_confidence=; absent means def.
func parseMinConfidence(r *http.Request, def float64) (float64, error) {
	v := r.URL.Query().Get("min_confidence")
	if v == "" {
		return def, nil
	}
	c, err := strconv.ParseFloat(v, 64)
	if err != nil || c < 0 || c > 1 {
		return 0, fmt.Errorf("min_confidence must be a number from 0 to 1")
	}
	return c, nil
}

// connectionsAnswer is the answer of GET /api/observations/{id}/connections.
type connectionsAnswer struct {
	Connections []gorm.Connection   `json:"connections"`
	Note        gorm.ConnectionNote `json:"observation"`
	// Total is how many connections match before the limit.
	Total int `json:"total"`
}

// handleGetConnections lists the notes related to one note, with how: the relation type, its confidence and reason, and
// whether the other note is older or newer. Only live notes (not superseded, not archived), the most certain first.
// GET /api/observations/{id}/connections?direction=older|newer&types=a,b&min_confidence=&limit=
func (s *Service) handleGetConnections(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}
	filter := gorm.ConnectionFilter{Direction: strings.TrimSpace(r.URL.Query().Get("direction"))}
	if filter.Direction != "" && filter.Direction != gorm.DirectionOlder && filter.Direction != gorm.DirectionNewer {
		http.Error(w, "direction must be older or newer", http.StatusBadRequest)
		return
	}
	if filter.Types, err = parseRelationTypes(r.URL.Query().Get("types")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if filter.MinConfidence, err = parseMinConfidence(r, 0); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 || n > 100 {
			http.Error(w, "limit must be a number from 1 to 100", http.StatusBadRequest)
			return
		}
		filter.Limit = n
	}

	obs, err := s.observationStore.GetObservationByID(ctx, id)
	if err != nil {
		http.Error(w, "failed to read the observation", http.StatusInternalServerError)
		return
	}
	if obs == nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}
	connections, total, err := s.relationStore.Connections(ctx, id, filter)
	if err != nil {
		http.Error(w, "failed to read the connections", http.StatusInternalServerError)
		return
	}
	noStore(w)
	writeJSON(w, connectionsAnswer{
		Note: gorm.ConnectionNote{
			ID: obs.ID, Title: obs.Title.String, Subtitle: obs.Subtitle.String, Type: string(obs.Type), Project: obs.Project,
			Concepts: obs.Concepts, CreatedAtEpoch: obs.CreatedAtEpoch,
		},
		Connections: connections,
		Total:       total,
	})
}

// relationTypeAnswer is one entry of GET /api/relations/types.
type relationTypeAnswer struct {
	models.RelationTypeInfo
	Count int `json:"count"`
}

// handleRelationTypes lists every relation type with what it means, whether the graph creates it by itself and how
// many relations of it there are: in the whole graph, in one project, or around one note.
// GET /api/relations/types?project=&observation_id=
func (s *Service) handleRelationTypes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	project, ok := s.conflictProjectFilter(ctx, w, r)
	if !ok {
		return
	}
	var observationID int64
	if v := r.URL.Query().Get("observation_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			http.Error(w, "observation_id must be a positive number", http.StatusBadRequest)
			return
		}
		observationID = n
	}
	counts, err := s.relationStore.RelationTypeCounts(ctx, project, observationID)
	if err != nil {
		http.Error(w, "failed to count the relations", http.StatusInternalServerError)
		return
	}
	byType := map[models.RelationType]int{}
	total := 0
	for _, c := range counts {
		byType[c.Type] = c.Count
		total += c.Count
	}
	types := make([]relationTypeAnswer, 0, len(models.RelationTypeInfos))
	for _, info := range models.RelationTypeInfos {
		types = append(types, relationTypeAnswer{RelationTypeInfo: info, Count: byType[info.Type]})
	}
	noStore(w)
	writeJSON(w, map[string]any{"types": types, "total": total})
}
