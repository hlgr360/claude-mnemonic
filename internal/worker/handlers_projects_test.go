package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/projects"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/hooks"
)

func doRequest(t *testing.T, svc *Service, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, target, &buf)
	rec := httptest.NewRecorder()
	svc.router.ServeHTTP(rec, req)
	return rec
}

func seedProjects(t *testing.T, svc *Service, names ...string) {
	t.Helper()
	for i, n := range names {
		_, err := svc.sessionStore.CreateSDKSession(context.Background(), "claude-"+string(rune('a'+i)), n, "p")
		require.NoError(t, err)
	}
}

func TestHandleResolveProject_RequiresAReference(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	rec := doRequest(t, svc, http.MethodGet, "/api/projects/resolve", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleResolveProject_ByIDNameAndPath(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	dir := filepath.Join(t.TempDir(), "knowledge_base")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	pathID := hooks.ProjectIDWithName(dir)
	seedProjects(t, svc, "claude-mnemonic_41bfcd", pathID)

	resolve := func(query string) projects.Resolution {
		t.Helper()
		rec := doRequest(t, svc, http.MethodGet, "/api/projects/resolve?"+query, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		var got projects.Resolution
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		return got
	}

	assert.Equal(t, projects.Resolution{ID: "claude-mnemonic_41bfcd", Match: projects.MatchExact, Known: true},
		resolve("id=claude-mnemonic_41bfcd"))
	assert.Equal(t, projects.Resolution{ID: "claude-mnemonic_41bfcd", Match: projects.MatchName, Known: true},
		resolve("name=claude-mnemonic"))
	assert.Equal(t, projects.Resolution{ID: pathID, Match: projects.MatchPath, Known: true},
		resolve("path="+url.QueryEscape(dir)))

	unseen := filepath.Join(t.TempDir(), "brand-new")
	got := resolve("path=" + url.QueryEscape(unseen))
	assert.Equal(t, projects.MatchPath, got.Match)
	assert.False(t, got.Known, "a path with no history resolves to a valid but unknown project")
	assert.Equal(t, hooks.ProjectIDWithName(unseen), got.ID)
}

func TestHandleResolveProject_UsesAliases(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa", "frag_bbbbbb")

	rec := doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
		setAliasRequest{Alias: "frag_bbbbbb", Canonical: "repo_aaaaaa", Source: "merge"})
	require.Equal(t, http.StatusOK, rec.Code)

	rec = doRequest(t, svc, http.MethodGet, "/api/projects/resolve?id=frag_bbbbbb", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var got projects.Resolution
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, projects.Resolution{ID: "repo_aaaaaa", Match: projects.MatchAlias, Known: true}, got)
}

func TestHandleSetProjectAlias_Validation(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	tests := []struct {
		body any
		name string
		want int
	}{
		{setAliasRequest{Alias: "a_111111"}, "missing canonical", http.StatusBadRequest},
		{setAliasRequest{Canonical: "b_222222"}, "missing alias", http.StatusBadRequest},
		{setAliasRequest{Alias: "  ", Canonical: " "}, "blank values", http.StatusBadRequest},
		{setAliasRequest{Alias: "a_111111", Canonical: "a_111111"}, "self alias", http.StatusBadRequest},
		{setAliasRequest{Alias: "../etc", Canonical: "b_222222"}, "path traversal in alias", http.StatusBadRequest},
		{setAliasRequest{Alias: "a_111111", Canonical: "b;rm -rf"}, "shell characters in canonical", http.StatusBadRequest},
		{setAliasRequest{Alias: "a_111111", Canonical: "b_222222"}, "valid", http.StatusOK},
		{setAliasRequest{Alias: "b_222222", Canonical: "a_111111"}, "cycle", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(t, svc, http.MethodPost, "/api/projects/aliases", tt.body)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}

	t.Run("malformed JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/aliases", bytes.NewBufferString("{nope"))
		rec := httptest.NewRecorder()
		svc.router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestHandleSetProjectAlias_ReportsFlattenedCanonical(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
		setAliasRequest{Alias: "b_222222", Canonical: "c_333333"}).Code)
	rec := doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
		setAliasRequest{Alias: "a_111111", Canonical: "b_222222"})
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, map[string]string{"alias": "a_111111", "canonical": "c_333333"}, got)
}

func TestHandleProjectAliases_ListAndDelete(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	for _, a := range []string{"z_999999", "a_111111"} {
		require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
			setAliasRequest{Alias: a, Canonical: "p_000001"}).Code)
	}

	rec := doRequest(t, svc, http.MethodGet, "/api/projects/aliases", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var rows []struct {
		Alias     string `json:"alias"`
		Canonical string `json:"canonical"`
		Source    string `json:"source"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Contains(t, rec.Body.String(), `"alias":"a_111111"`, "the API uses lowercase keys the dashboard can rely on")
	require.Len(t, rows, 2)
	assert.Equal(t, "a_111111", rows[0].Alias)
	assert.Equal(t, "manual", rows[0].Source)

	assert.Equal(t, http.StatusNoContent, doRequest(t, svc, http.MethodDelete, "/api/projects/aliases/a_111111", nil).Code)
	assert.Equal(t, http.StatusNotFound, doRequest(t, svc, http.MethodDelete, "/api/projects/aliases/a_111111", nil).Code)
}

func TestHandleListProjectSummaries(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa", "frag_bbbbbb")
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
		setAliasRequest{Alias: "frag_bbbbbb", Canonical: "repo_aaaaaa"}).Code)

	rec := doRequest(t, svc, http.MethodGet, "/api/projects/summary", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var rows []projectSummaryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	byID := map[string]projectSummaryResponse{}
	for _, r := range rows {
		byID[r.Project] = r
	}
	require.Len(t, byID, 2)

	assert.Equal(t, "repo", byID["repo_aaaaaa"].DisplayName)
	assert.Equal(t, []string{"frag_bbbbbb"}, byID["repo_aaaaaa"].Aliases)
	assert.Empty(t, byID["repo_aaaaaa"].AliasOf)
	assert.Equal(t, int64(1), byID["repo_aaaaaa"].Sessions)

	assert.Equal(t, "repo_aaaaaa", byID["frag_bbbbbb"].AliasOf)
	assert.Empty(t, byID["frag_bbbbbb"].Aliases)
}

func TestHandleListProjectSummaries_Empty(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	rec := doRequest(t, svc, http.MethodGet, "/api/projects/summary", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, "[]", rec.Body.String(), "an empty store is an empty list, not null")
}
