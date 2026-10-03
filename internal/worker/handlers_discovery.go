package worker

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/projects"
	"github.com/lukaszraczylo/claude-mnemonic/internal/vector/sqlitevec"
)

const (
	suggestDefaultLimit = 4
	suggestMaxLimit     = 10
	crossSearchDefault  = 10
	crossSearchMax      = 50
	vectorFetchFactor   = 6 // vector docs are per field, so over-fetch to get enough distinct observations
	narrativeMaxChars   = 600
)

// vectorSearch runs a semantic query over every project. ok is false when
// vector search is unavailable, so callers can degrade instead of failing.
func (s *Service) vectorSearch(ctx context.Context, text string, n int, where map[string]interface{}) (results []sqlitevec.QueryResult, ok bool, err error) {
	switch {
	case s.vectorQueryFn != nil:
		results, err = s.vectorQueryFn(ctx, text, n, where)
		return results, true, err
	case s.vectorClient != nil && s.vectorClient.IsConnected():
		results, err = s.vectorClient.Query(ctx, text, n, where)
		return results, true, err
	}
	return nil, false, nil
}

// observationHit is one distinct observation found by a cross-project query.
type observationHit struct {
	project    string
	title      string
	id         int64
	similarity float64
}

// distinctObservationHits reduces per-field vector documents to one hit per
// observation (its best similarity), best first, keeping only those at or above threshold.
func distinctObservationHits(results []sqlitevec.QueryResult, threshold float64, obsType string) []observationHit {
	best := map[int64]*observationHit{}
	var order []int64
	for _, r := range results {
		if r.Similarity < threshold {
			continue
		}
		if docType, _ := r.Metadata["doc_type"].(string); docType != string(sqlitevec.DocTypeObservation) {
			continue
		}
		if obsType != "" {
			if t, _ := r.Metadata["type"].(string); t != obsType {
				continue
			}
		}
		idf, ok := r.Metadata["sqlite_id"].(float64)
		if !ok {
			if i64, isInt := r.Metadata["sqlite_id"].(int64); isInt {
				idf, ok = float64(i64), true
			}
		}
		if !ok {
			continue
		}
		id := int64(idf)
		project, _ := r.Metadata["project"].(string)
		title, _ := r.Metadata["title"].(string)
		if cur, seen := best[id]; seen {
			if r.Similarity > cur.similarity {
				cur.similarity = r.Similarity
			}
			continue
		}
		best[id] = &observationHit{id: id, project: project, title: title, similarity: r.Similarity}
		order = append(order, id)
	}

	out := make([]observationHit, 0, len(order))
	for _, id := range order {
		out = append(out, *best[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].similarity > out[j].similarity })
	return out
}

// handleSuggestProjects ranks projects for a piece of text, so a client with no
// working directory (Desktop chat) can offer the user a short list to choose from.
func (s *Service) handleSuggestProjects(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	limit := gorm.ParseLimitParamWithMax(r, suggestDefaultLimit, suggestMaxLimit)

	view, err := s.loadProjectView(ctx)
	if err != nil {
		http.Error(w, "failed to list projects", http.StatusInternalServerError)
		return
	}
	aliases := view.aliases
	activity := make([]projects.Activity, 0, len(view.rows))
	for _, row := range view.rows {
		activity = append(activity, projects.Activity{
			Project: row.Project, Sessions: row.Sessions, Observations: row.Observations, LastActiveEpoch: row.LastActiveEpoch,
		})
	}

	var hits []projects.Hit
	vectorUsed := false
	if query != "" {
		results, ok, qerr := s.vectorSearch(ctx, query, limit*vectorFetchFactor*5, sqlitevec.BuildWhereFilter(sqlitevec.DocTypeObservation, ""))
		if ok && qerr == nil {
			vectorUsed = true
			for _, h := range distinctObservationHits(results, s.config.ContextRelevanceThreshold, "") {
				hits = append(hits, projects.Hit{Project: h.project, Title: h.title, Similarity: h.similarity})
			}
		}
	}

	out := projects.Suggest(query, hits, activity, aliases, time.Now().UnixMilli(), limit)
	for i := range out.Suggestions {
		l := view.labels[out.Suggestions[i].Project]
		out.Suggestions[i].Label, out.Suggestions[i].Use = l.Label, l.Use
	}
	noStore(w)
	writeJSON(w, map[string]any{
		"query":       query,
		"suggestions": out.Suggestions,
		"confident":   out.Confident,
		"vector_used": vectorUsed,
	})
}

// crossProjectObservation is one result of a cross-project search.
type crossProjectObservation struct {
	Project    string   `json:"project"`
	Canonical  string   `json:"canonical_project,omitempty"`
	Type       string   `json:"type"`
	Title      string   `json:"title,omitempty"`
	Subtitle   string   `json:"subtitle,omitempty"`
	Narrative  string   `json:"narrative,omitempty"`
	CreatedAt  string   `json:"created_at"`
	Concepts   []string `json:"concepts,omitempty"`
	Similarity float64  `json:"similarity"`
	ID         int64    `json:"id"`
}

// handleCrossProjectSearch searches observations across every project. It is
// the read path for clients that have not (or have declined to) pick a project.
func (s *Service) handleCrossProjectSearch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		http.Error(w, "query required", http.StatusBadRequest)
		return
	}
	obsType := r.URL.Query().Get("obs_type")
	limit := gorm.ParseLimitParamWithMax(r, crossSearchDefault, crossSearchMax)

	results, ok, err := s.vectorSearch(ctx, query, limit*vectorFetchFactor, sqlitevec.BuildWhereFilter(sqlitevec.DocTypeObservation, ""))
	if !ok {
		http.Error(w, "vector search unavailable: cross-project search needs the vector index", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "vector search failed", http.StatusInternalServerError)
		return
	}

	hits := distinctObservationHits(results, s.config.ContextRelevanceThreshold, obsType)
	if len(hits) > limit {
		hits = hits[:limit]
	}

	ids := make([]int64, 0, len(hits))
	sim := make(map[int64]float64, len(hits))
	for _, h := range hits {
		ids = append(ids, h.id)
		sim[h.id] = h.similarity
	}
	observations, err := s.observationStore.GetObservationsByIDsPreserveOrder(ctx, ids)
	if err != nil {
		http.Error(w, "failed to load observations", http.StatusInternalServerError)
		return
	}
	observations = withoutSuperseded(observations)
	aliases, err := gorm.NewProjectAliasStore(s.store).AliasMap(ctx)
	if err != nil {
		http.Error(w, "failed to load aliases", http.StatusInternalServerError)
		return
	}

	out := make([]crossProjectObservation, 0, len(observations))
	for _, o := range observations {
		item := crossProjectObservation{
			ID:         o.ID,
			Project:    o.Project,
			Canonical:  aliases[o.Project],
			Type:       string(o.Type),
			Title:      o.Title.String,
			Subtitle:   o.Subtitle.String,
			Narrative:  truncateRunes(o.Narrative.String, narrativeMaxChars),
			CreatedAt:  o.CreatedAt,
			Concepts:   []string(o.Concepts),
			Similarity: sim[o.ID],
		}
		out = append(out, item)
	}

	noStore(w)
	writeJSON(w, map[string]any{"query": query, "count": len(out), "observations": out})
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
