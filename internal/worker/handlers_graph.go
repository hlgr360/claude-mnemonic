package worker

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
)

const (
	graphDefaultNodes = 400
	graphMaxNodes     = 1000
)

// handleGraph returns the knowledge graph: the observations that have relations, and the relations.
// GET /api/graph?project=&min_confidence=&types=a,b&max_nodes=
func (s *Service) handleGraph(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	project, ok := s.conflictProjectFilter(ctx, w, r)
	if !ok {
		return
	}
	filter := gorm.GraphFilter{Project: project, MaxNodes: graphDefaultNodes}
	if v := r.URL.Query().Get("min_confidence"); v != "" {
		c, err := strconv.ParseFloat(v, 64)
		if err != nil || c < 0 || c > 1 {
			http.Error(w, "min_confidence must be a number from 0 to 1", http.StatusBadRequest)
			return
		}
		filter.MinConfidence = c
	}
	if v := r.URL.Query().Get("max_nodes"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > graphMaxNodes {
			http.Error(w, "max_nodes must be a number from 1 to "+strconv.Itoa(graphMaxNodes), http.StatusBadRequest)
			return
		}
		filter.MaxNodes = n
	}
	types, err := parseRelationTypes(r.URL.Query().Get("types"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	filter.Types = types

	graph, err := s.relationStore.Graph(ctx, filter)
	if err != nil {
		http.Error(w, "failed to read the graph", http.StatusInternalServerError)
		return
	}
	noStore(w)
	writeJSON(w, graph)
}

// handleGraphStats returns the real numbers of the graph for the dashboard's sidebar.
// GET /api/graph/stats?project=
func (s *Service) handleGraphStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	project, ok := s.conflictProjectFilter(ctx, w, r)
	if !ok {
		return
	}
	stats, err := s.relationStore.GraphStats(ctx, project)
	if err != nil {
		http.Error(w, "failed to count the graph", http.StatusInternalServerError)
		return
	}
	pending, err := s.relationStore.CountUncheckedForRelations(ctx)
	if err != nil {
		pending = 0
	}

	edgeTypes := make(map[string]int, len(stats.EdgeTypes))
	for t, n := range stats.EdgeTypes {
		edgeTypes[string(t)] = n
	}
	resp := map[string]any{
		"enabled":      s.config.GraphEnabled,
		"nodeCount":    stats.Nodes,
		"edgeCount":    stats.Edges,
		"avgDegree":    stats.AvgDegree,
		"maxDegree":    stats.MaxDegree,
		"minDegree":    stats.MinDegree,
		"medianDegree": stats.MedianDegree,
		"edgeTypes":    edgeTypes,
		"pending":      pending,
		"config": map[string]any{
			"minSimilarity":      s.config.GraphRelationsMinSim,
			"maxPerNote":         s.config.GraphRelationsMaxPerObs,
			"maxHops":            2,
			"branchFactor":       10,
			"edgeWeight":         0.3,
			"rebuildIntervalMin": 30,
		},
	}
	if !s.config.GraphEnabled {
		resp["message"] = "The knowledge graph is switched off (CLAUDE_MNEMONIC_GRAPH_ENABLED)."
	} else if stats.Edges == 0 && pending > 0 {
		resp["message"] = "Building the graph from existing observations."
	}
	noStore(w)
	writeJSON(w, resp)
}

// handleRebuildRelations deletes every relation and starts the builder over, for example after its thresholds
// were changed. POST /api/relations/rebuild
func (s *Service) handleRebuildRelations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	deleted, err := s.relationStore.ResetRelations(ctx)
	if err != nil {
		http.Error(w, "failed to reset the graph", http.StatusInternalServerError)
		return
	}
	s.broadcastGraphChange("reset")
	s.wakeRelationLoop()
	noStore(w)
	writeJSON(w, map[string]any{"deleted": deleted, "message": "Relations will be rebuilt in the background."})
}
