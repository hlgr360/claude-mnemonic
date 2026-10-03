package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/vector/sqlitevec"
)

// vecDoc builds a fake per-field vector document for an observation.
func vecDoc(id int64, project, title, obsType string, similarity float64) sqlitevec.QueryResult {
	return sqlitevec.QueryResult{
		Similarity: similarity,
		Metadata: map[string]any{
			"sqlite_id": float64(id), "doc_type": "observation",
			"project": project, "title": title, "type": obsType,
		},
	}
}

func fakeVectors(results ...sqlitevec.QueryResult) func(context.Context, string, int, map[string]interface{}) ([]sqlitevec.QueryResult, error) {
	return func(context.Context, string, int, map[string]interface{}) ([]sqlitevec.QueryResult, error) {
		return results, nil
	}
}

type suggestResponse struct {
	Query       string `json:"query"`
	Suggestions []struct {
		Project   string   `json:"project"`
		Reason    string   `json:"reason"`
		TopTitles []string `json:"top_titles"`
		Hits      int      `json:"hits"`
	} `json:"suggestions"`
	Confident  bool `json:"confident"`
	VectorUsed bool `json:"vector_used"`
}

func getSuggest(t *testing.T, svc *Service, query string) suggestResponse {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/projects/suggest?query="+url.QueryEscape(query), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var got suggestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

func TestHandleSuggestProjects_RanksByContent(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "awx_aaaaaa", "mnemonic_bbbbbb")
	svc.vectorQueryFn = fakeVectors(
		vecDoc(1, "awx_aaaaaa", "Credential rotation", "discovery", 0.8),
		vecDoc(1, "awx_aaaaaa", "Credential rotation", "discovery", 0.6), // second field of the same observation
		vecDoc(2, "awx_aaaaaa", "Key vault access", "decision", 0.7),
		vecDoc(3, "mnemonic_bbbbbb", "Vector search", "discovery", 0.5),
	)

	got := getSuggest(t, svc, "rotate the credentials")
	require.Len(t, got.Suggestions, 2)
	assert.True(t, got.VectorUsed)
	assert.Equal(t, "awx_aaaaaa", got.Suggestions[0].Project)
	assert.Equal(t, 2, got.Suggestions[0].Hits, "per-field documents of one observation count once")
	assert.Equal(t, []string{"Credential rotation", "Key vault access"}, got.Suggestions[0].TopTitles)
	assert.True(t, got.Confident)
}

func TestHandleSuggestProjects_BelowThresholdHitsAreIgnored(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "awx_aaaaaa")
	svc.vectorQueryFn = fakeVectors(vecDoc(1, "awx_aaaaaa", "weak", "discovery", 0.05))

	got := getSuggest(t, svc, "something unrelated")
	require.Len(t, got.Suggestions, 1)
	assert.Equal(t, 0, got.Suggestions[0].Hits)
	assert.Equal(t, "recent", got.Suggestions[0].Reason)
}

func TestHandleSuggestProjects_WithoutVectorsFallsBackToNameAndRecency(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "knowledge_base_aaaaaa", "other_bbbbbb")

	got := getSuggest(t, svc, "fix the knowledge base importer")
	assert.False(t, got.VectorUsed)
	require.NotEmpty(t, got.Suggestions)
	assert.Equal(t, "knowledge_base_aaaaaa", got.Suggestions[0].Project)
	assert.Equal(t, "name match", got.Suggestions[0].Reason)
}

func TestHandleSuggestProjects_VectorErrorDegradesInsteadOfFailing(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "a_aaaaaa")
	svc.vectorQueryFn = func(context.Context, string, int, map[string]interface{}) ([]sqlitevec.QueryResult, error) {
		return nil, errors.New("index offline")
	}

	got := getSuggest(t, svc, "anything")
	assert.False(t, got.VectorUsed)
	assert.Len(t, got.Suggestions, 1)
}

func TestHandleSuggestProjects_EmptyQueryReturnsRecentProjects(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "a_aaaaaa", "b_bbbbbb")

	got := getSuggest(t, svc, "")
	assert.Len(t, got.Suggestions, 2)
	assert.False(t, got.Confident)
	for _, s := range got.Suggestions {
		assert.Equal(t, "recent", s.Reason)
	}
}

func TestHandleSuggestProjects_FoldsAliasesIntoCanonical(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa", "frag_bbbbbb")
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
		setAliasRequest{Alias: "frag_bbbbbb", Canonical: "repo_aaaaaa"}).Code)
	svc.vectorQueryFn = fakeVectors(vecDoc(1, "frag_bbbbbb", "from the fragment", "discovery", 0.7))

	got := getSuggest(t, svc, "x")
	require.Len(t, got.Suggestions, 1)
	assert.Equal(t, "repo_aaaaaa", got.Suggestions[0].Project)
	assert.Equal(t, 1, got.Suggestions[0].Hits)
}

func TestHandleSuggestProjects_RespectsLimit(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "a_aaaaaa", "b_bbbbbb", "c_cccccc")

	rec := doRequest(t, svc, http.MethodGet, "/api/projects/suggest?limit=2", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var got suggestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Len(t, got.Suggestions, 2)
}

type crossSearchResponse struct {
	Query        string `json:"query"`
	Observations []struct {
		Project    string  `json:"project"`
		Canonical  string  `json:"canonical_project"`
		Type       string  `json:"type"`
		Title      string  `json:"title"`
		Narrative  string  `json:"narrative"`
		Similarity float64 `json:"similarity"`
		ID         int64   `json:"id"`
	} `json:"observations"`
	Count int `json:"count"`
}

func TestHandleCrossProjectSearch_ReturnsObservationsFromEveryProject(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	idA := createTestObservation(t, svc.observationStore, "alpha_aaaaaa", "Alpha finding", "alpha narrative", []string{"x"})
	idB := createTestObservation(t, svc.observationStore, "beta_bbbbbb", "Beta finding", "beta narrative", nil)
	svc.vectorQueryFn = fakeVectors(
		vecDoc(idA, "alpha_aaaaaa", "Alpha finding", "discovery", 0.6),
		vecDoc(idB, "beta_bbbbbb", "Beta finding", "discovery", 0.9),
		vecDoc(idB, "beta_bbbbbb", "Beta finding", "discovery", 0.4), // lower-scoring field of the same observation
	)

	rec := doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=finding", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var got crossSearchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	require.Equal(t, 2, got.Count, "one row per observation")
	assert.Equal(t, idB, got.Observations[0].ID, "best similarity first")
	assert.Equal(t, "beta_bbbbbb", got.Observations[0].Project)
	assert.Equal(t, "beta narrative", got.Observations[0].Narrative)
	assert.InDelta(t, 0.9, got.Observations[0].Similarity, 0.001)
	assert.Equal(t, "alpha_aaaaaa", got.Observations[1].Project)
}

func TestHandleCrossProjectSearch_FiltersByTypeThresholdAndLimit(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	var docs []sqlitevec.QueryResult
	for i := 0; i < 5; i++ {
		id := createTestObservation(t, svc.observationStore, "p_aaaaaa", "obs", "n", nil)
		docs = append(docs, vecDoc(id, "p_aaaaaa", "obs", "discovery", 0.9-float64(i)*0.05))
	}
	decisionID := createTestObservation(t, svc.observationStore, "p_aaaaaa", "dec", "n", nil)
	docs = append(docs,
		vecDoc(createTestObservation(t, svc.observationStore, "p_aaaaaa", "weak", "n", nil), "p_aaaaaa", "weak", "discovery", 0.05),
		vecDoc(decisionID, "p_aaaaaa", "dec", "decision", 0.95), // fixture: the filter reads the vector metadata type
	)
	svc.vectorQueryFn = fakeVectors(docs...)

	t.Run("limit caps results and the weak hit is dropped by the threshold", func(t *testing.T) {
		rec := doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=q&limit=3", nil)
		var got crossSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, 3, got.Count)
		for _, o := range got.Observations {
			assert.GreaterOrEqual(t, o.Similarity, 0.3)
		}
	})
	t.Run("obs_type filter", func(t *testing.T) {
		rec := doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=q&obs_type=decision", nil)
		var got crossSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, 1, got.Count)
		assert.Equal(t, decisionID, got.Observations[0].ID)
	})
}

func TestHandleCrossProjectSearch_ReportsCanonicalForAliasedProjects(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	id := createTestObservation(t, svc.observationStore, "frag_bbbbbb", "from fragment", "n", nil)
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
		setAliasRequest{Alias: "frag_bbbbbb", Canonical: "repo_aaaaaa"}).Code)
	svc.vectorQueryFn = fakeVectors(vecDoc(id, "frag_bbbbbb", "from fragment", "discovery", 0.8))

	rec := doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=q", nil)
	var got crossSearchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, 1, got.Count)
	assert.Equal(t, "frag_bbbbbb", got.Observations[0].Project)
	assert.Equal(t, "repo_aaaaaa", got.Observations[0].Canonical)
}

func TestHandleCrossProjectSearch_ErrorsAndTruncation(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	t.Run("query is required", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, "/api/search/cross-project", nil).Code)
	})
	t.Run("unavailable vector index is a 503, not an empty result", func(t *testing.T) {
		rec := doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=q", nil)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
	t.Run("vector failure is a 500", func(t *testing.T) {
		svc.vectorQueryFn = func(context.Context, string, int, map[string]interface{}) ([]sqlitevec.QueryResult, error) {
			return nil, errors.New("boom")
		}
		assert.Equal(t, http.StatusInternalServerError, doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=q", nil).Code)
	})
	t.Run("long narratives are truncated on a rune boundary", func(t *testing.T) {
		long := ""
		for i := 0; i < 700; i++ {
			long += "é"
		}
		id := createTestObservation(t, svc.observationStore, "p_aaaaaa", "long", long, nil)
		svc.vectorQueryFn = fakeVectors(vecDoc(id, "p_aaaaaa", "long", "discovery", 0.9))
		rec := doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=q", nil)
		var got crossSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, 1, got.Count)
		assert.Equal(t, narrativeMaxChars+1, len([]rune(got.Observations[0].Narrative)), "600 runes plus the ellipsis")
	})
	t.Run("results with no matching documents yield an empty list", func(t *testing.T) {
		svc.vectorQueryFn = fakeVectors()
		rec := doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=q", nil)
		assert.Equal(t, http.StatusOK, rec.Code)
		var got crossSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, 0, got.Count)
	})
}

func TestDistinctObservationHits_SkipsMalformedDocuments(t *testing.T) {
	results := []sqlitevec.QueryResult{
		{Similarity: 0.9, Metadata: map[string]any{"doc_type": "summary", "sqlite_id": float64(1)}},                   // not an observation
		{Similarity: 0.9, Metadata: map[string]any{"doc_type": "observation"}},                                        // no id
		{Similarity: 0.9, Metadata: map[string]any{"doc_type": "observation", "sqlite_id": "7"}},                      // id of the wrong type
		{Similarity: 0.8, Metadata: map[string]any{"doc_type": "observation", "sqlite_id": int64(5), "project": "p"}}, // int64 ids are accepted
	}
	got := distinctObservationHits(results, 0.3, "")
	require.Len(t, got, 1)
	assert.Equal(t, int64(5), got[0].id)
}
