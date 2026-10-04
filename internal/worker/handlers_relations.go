// Package worker provides the main worker service for claude-mnemonic.
package worker

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// DefaultRelationsLimit is the default number of relations to return.
const DefaultRelationsLimit = 50

// handleGetRelations returns relations for an observation.
func (s *Service) handleGetRelations(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	relations, err := s.relationStore.GetRelationsWithDetails(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if relations == nil {
		relations = []*models.RelationWithDetails{}
	}

	writeJSON(w, relations)
}

// handleGetRelationGraph returns the relation graph for an observation.
func (s *Service) handleGetRelationGraph(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	// Get depth parameter (default 2). The MCP tool sends max_depth, so both names are read.
	depth := 2
	for _, name := range []string{"depth", "max_depth"} {
		if depthStr := r.URL.Query().Get(name); depthStr != "" {
			if d, parseErr := strconv.Atoi(depthStr); parseErr == nil && d > 0 && d <= 5 {
				depth = d
			}
		}
	}
	types, err := parseRelationTypes(r.URL.Query().Get("types"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	minConfidence, err := parseMinConfidence(r, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	graph, err := s.relationStore.GetRelationGraph(r.Context(), id, depth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(types) > 0 || minConfidence > 0 {
		graph.Relations = filterRelations(graph.Relations, types, minConfidence)
	}

	writeJSON(w, graph)
}

// filterRelations keeps the relations of the given types (any type when none are given) with at least minConfidence.
func filterRelations(relations []*models.RelationWithDetails, types []models.RelationType, minConfidence float64) []*models.RelationWithDetails {
	want := map[models.RelationType]bool{}
	for _, t := range types {
		want[t] = true
	}
	kept := make([]*models.RelationWithDetails, 0, len(relations))
	for _, r := range relations {
		if (len(want) == 0 || want[r.Relation.RelationType]) && r.Relation.Confidence >= minConfidence {
			kept = append(kept, r)
		}
	}
	return kept
}

// handleGetRelatedObservations returns observations related to a given one: only live ones (not superseded, not
// archived), at most ?limit= (default 50), of the relation types in ?types= when given.
func (s *Service) handleGetRelatedObservations(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	// Get minimum confidence parameter (default 0.4)
	minConfidence := 0.4
	if confStr := r.URL.Query().Get("min_confidence"); confStr != "" {
		if c, parseErr := strconv.ParseFloat(confStr, 64); parseErr == nil && c >= 0 && c <= 1 {
			minConfidence = c
		}
	}

	types, err := parseRelationTypes(r.URL.Query().Get("types"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := 50
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, parseErr := strconv.Atoi(limitStr); parseErr == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	// The most certain relations first, live notes only
	connections, _, err := s.relationStore.Connections(r.Context(), id, gorm.ConnectionFilter{Types: types, MinConfidence: minConfidence, Limit: limit})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	relatedIDs := make([]int64, 0, len(connections))
	seen := map[int64]bool{}
	for _, c := range connections {
		if !seen[c.Note.ID] {
			seen[c.Note.ID] = true
			relatedIDs = append(relatedIDs, c.Note.ID)
		}
	}

	if len(relatedIDs) == 0 {
		writeJSON(w, []*models.Observation{})
		return
	}

	// Fetch full observations
	observations, err := s.observationStore.GetObservationsByIDs(r.Context(), relatedIDs, "importance", limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if observations == nil {
		observations = []*models.Observation{}
	}

	writeJSON(w, observations)
}

// handleGetRelationsByType returns all relations of a specific type.
func (s *Service) handleGetRelationsByType(w http.ResponseWriter, r *http.Request) {
	relType := chi.URLParam(r, "type")

	// Validate relation type
	validType := false
	for _, t := range models.AllRelationTypes {
		if string(t) == relType {
			validType = true
			break
		}
	}
	if !validType {
		http.Error(w, "invalid relation type", http.StatusBadRequest)
		return
	}

	limit := DefaultRelationsLimit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	relations, err := s.relationStore.GetRelationsByType(r.Context(), models.RelationType(relType), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if relations == nil {
		relations = []*models.ObservationRelation{}
	}

	writeJSON(w, relations)
}

// handleGetRelationStats returns statistics about relations.
func (s *Service) handleGetRelationStats(w http.ResponseWriter, r *http.Request) {
	// Get total relation count
	totalCount, err := s.relationStore.GetTotalRelationCount(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get high confidence relations count
	highConfRelations, err := s.relationStore.GetHighConfidenceRelations(r.Context(), 0.7, 1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Count by relation type
	typeCounts := make(map[string]int)
	for _, t := range models.AllRelationTypes {
		relations, err := s.relationStore.GetRelationsByType(r.Context(), t, 1000)
		if err == nil {
			typeCounts[string(t)] = len(relations)
		}
	}

	writeJSON(w, map[string]interface{}{
		"total_count":         totalCount,
		"high_confidence":     len(highConfRelations),
		"by_type":             typeCounts,
		"min_confidence_used": 0.4,
	})
}
