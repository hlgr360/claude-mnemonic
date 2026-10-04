package worker

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type searchAnalyticsBody struct {
	QueryTypes  map[string]int `json:"query_types"`
	Project     string         `json:"project"`
	TopKeywords []struct {
		Keyword string `json:"keyword"`
		Count   int    `json:"count"`
	} `json:"top_keywords"`
	TotalQueries     int     `json:"total_queries"`
	VectorSearches   int     `json:"vector_searches"`
	KeywordSearches  int     `json:"keyword_searches"`
	VectorSearchRate float64 `json:"vector_search_rate"`
	AvgResults       float64 `json:"avg_results"`
	ZeroResultRate   float64 `json:"zero_result_rate"`
}

func getSearchAnalytics(t *testing.T, svc *Service, query string) (searchAnalyticsBody, map[string]json.RawMessage) {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/search/analytics"+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out searchAnalyticsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	return out, raw
}

// The dashboard's popup reads these keys; if one disappears it shows nothing or fails.
func TestSearchAnalytics_ReturnsTheKeysThePopupReadsEvenWhenNothingWasSearched(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	got, raw := getSearchAnalytics(t, svc, "")
	for _, key := range []string{"total_queries", "vector_searches", "keyword_searches", "vector_search_rate", "avg_results", "zero_result_rate", "query_types", "top_keywords", "project"} {
		assert.Contains(t, raw, key)
	}
	assert.Zero(t, got.TotalQueries)
	assert.JSONEq(t, "{}", string(raw["query_types"]), "an empty map, not null")
	assert.JSONEq(t, "[]", string(raw["top_keywords"]), "an empty list, not null")
}

func TestSearchAnalytics_CountsTheRecentSearches(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	svc.trackSearchQuery("retry policy for webhooks", "alpha_aaaaaa", "observations", 4, true)
	svc.trackSearchQuery("retry backoff", "alpha_aaaaaa", "observations", 2, true)
	svc.trackSearchQuery("queue naming", "beta_bbbbbb", "observations", 0, false)
	svc.trackSearchQuery("retry limits", "beta_bbbbbb", "observations", 0, false)

	got, _ := getSearchAnalytics(t, svc, "")
	assert.Equal(t, 4, got.TotalQueries)
	assert.Equal(t, 2, got.VectorSearches)
	assert.Equal(t, 2, got.KeywordSearches, "the searches that did not use the vector index")
	assert.InDelta(t, 50, got.VectorSearchRate, 0.001)
	assert.InDelta(t, 1.5, got.AvgResults, 0.001)
	assert.InDelta(t, 50, got.ZeroResultRate, 0.001, "two of four found nothing")
	assert.Equal(t, 4, got.QueryTypes["observations"])
	require.NotEmpty(t, got.TopKeywords)
	assert.Equal(t, "retry", got.TopKeywords[0].Keyword)
	assert.Equal(t, 3, got.TopKeywords[0].Count)
	assert.Equal(t, got.TotalQueries, got.VectorSearches+got.KeywordSearches)

	only, _ := getSearchAnalytics(t, svc, "?project=beta_bbbbbb")
	assert.Equal(t, 2, only.TotalQueries, "the project filter narrows it")
	assert.Equal(t, "beta_bbbbbb", only.Project)
}

// The popup's "recent searches" list reads {queries: [...]} with these fields.
func TestRecentSearches_ReturnsTheFieldsThePopupReads(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	svc.trackSearchQuery("retry policy", "alpha_aaaaaa", "observations", 3, true)

	rec := doRequest(t, svc, http.MethodGet, "/api/search/recent?limit=5", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Queries []map[string]any `json:"queries"`
		Count   int              `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Queries, 1)
	assert.Equal(t, 1, out.Count)
	for _, key := range []string{"query", "project", "type", "results", "used_vector", "timestamp"} {
		assert.Contains(t, out.Queries[0], key)
	}
}
