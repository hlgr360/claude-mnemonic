package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func remember(t *testing.T, svc *Service, req RememberRequest) (*httptest.ResponseRecorder, RememberResponse) {
	t.Helper()
	rec := doRequest(t, svc, http.MethodPost, "/api/observations/remember", req)
	var resp RememberResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	}
	return rec, resp
}

func TestHandleRemember_StoresInAnExistingProject(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	rec, resp := remember(t, svc, RememberRequest{
		Project: "repo_aaaaaa", Title: "Chose sqlite-vec", Text: "We use sqlite-vec for vectors.\nIt ships with the worker.",
		Type: "decision", Concepts: []string{"vector"}, Source: "claude-ai",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "repo_aaaaaa", resp.Project)
	assert.Greater(t, resp.ID, int64(0))
	assert.False(t, resp.Duplicate)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	obs, err := svc.observationStore.GetObservationByID(context.Background(), resp.ID)
	require.NoError(t, err)
	require.NotNil(t, obs)
	assert.Equal(t, "repo_aaaaaa", obs.Project)
	assert.Equal(t, "Chose sqlite-vec", obs.Title.String)
	assert.Equal(t, "We use sqlite-vec for vectors.\nIt ships with the worker.", obs.Narrative.String)
	assert.Equal(t, "decision", string(obs.Type))
	assert.Equal(t, "project", string(obs.Scope))
	assert.Contains(t, []string(obs.Concepts), "vector")
	assert.Contains(t, []string(obs.Facts), "Saved explicitly via claude-ai")
}

func TestHandleRemember_RequiresAProject(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	for _, p := range []string{"", "   "} {
		rec, _ := remember(t, svc, RememberRequest{Project: p, Text: "x"})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "project %q", p)
		assert.Contains(t, rec.Body.String(), "project is required")
	}
}

func TestHandleRemember_RejectsUnknownProjectUnlessNewIsAllowed(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	rec, _ := remember(t, svc, RememberRequest{Project: "invented_zzzzzz", Text: "x"})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown project")

	rec, resp := remember(t, svc, RememberRequest{Project: "fresh_bbbbbb", Text: "first memory", AllowNewProject: true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "fresh_bbbbbb", resp.Project)

	rec, _ = remember(t, svc, RememberRequest{Project: "fresh_bbbbbb", Text: "second memory"})
	assert.Equal(t, http.StatusOK, rec.Code, "the project exists once it has a memory")
}

func TestHandleRemember_FollowsAliasesToTheCanonicalProject(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
		setAliasRequest{Alias: "frag_bbbbbb", Canonical: "repo_aaaaaa"}).Code)

	rec, resp := remember(t, svc, RememberRequest{Project: "frag_bbbbbb", Text: "via the fragment id"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "repo_aaaaaa", resp.Project, "an alias is not a project to write into")

	obs, err := svc.observationStore.GetObservationByID(context.Background(), resp.ID)
	require.NoError(t, err)
	assert.Equal(t, "repo_aaaaaa", obs.Project)
}

func TestHandleRemember_PrivacyHandling(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	t.Run("entirely private text is refused", func(t *testing.T) {
		rec, _ := remember(t, svc, RememberRequest{Project: "repo_aaaaaa", Text: "<private>the password is hunter2</private>"})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "entirely private")
	})
	t.Run("private spans are stripped, the rest is kept", func(t *testing.T) {
		rec, resp := remember(t, svc, RememberRequest{Project: "repo_aaaaaa",
			Text: "Use the vault. <private>token abc123</private> Rotate monthly."})
		require.Equal(t, http.StatusOK, rec.Code)
		obs, _ := svc.observationStore.GetObservationByID(context.Background(), resp.ID)
		assert.NotContains(t, obs.Narrative.String, "abc123")
		assert.Contains(t, obs.Narrative.String, "Use the vault.")
		assert.Contains(t, obs.Narrative.String, "Rotate monthly.")
	})
	t.Run("injected memory context is not re-ingested", func(t *testing.T) {
		rec, resp := remember(t, svc, RememberRequest{Project: "repo_aaaaaa",
			Text: "Real note. <claude-mnemonic-context>old injected context</claude-mnemonic-context>"})
		require.Equal(t, http.StatusOK, rec.Code)
		obs, _ := svc.observationStore.GetObservationByID(context.Background(), resp.ID)
		assert.NotContains(t, obs.Narrative.String, "injected context")
	})
	t.Run("a private title is dropped and a title is derived from the text", func(t *testing.T) {
		rec, resp := remember(t, svc, RememberRequest{Project: "repo_aaaaaa", Title: "<private>secret title</private>", Text: "Visible body line"})
		require.Equal(t, http.StatusOK, rec.Code)
		obs, _ := svc.observationStore.GetObservationByID(context.Background(), resp.ID)
		assert.Equal(t, "Visible body line", obs.Title.String)
	})
}

func TestHandleRemember_Validation(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	tests := []struct {
		name string
		req  RememberRequest
	}{
		{"empty text", RememberRequest{Project: "repo_aaaaaa", Text: ""}},
		{"whitespace text", RememberRequest{Project: "repo_aaaaaa", Text: " \n\t "}},
		{"bad type", RememberRequest{Project: "repo_aaaaaa", Text: "x", Type: "gossip"}},
		{"bad scope", RememberRequest{Project: "repo_aaaaaa", Text: "x", Scope: "universe"}},
		{"path traversal", RememberRequest{Project: "../etc", Text: "x"}},
		{"shell characters", RememberRequest{Project: "a;b", Text: "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := remember(t, svc, tt.req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}

	t.Run("malformed JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/observations/remember", bytes.NewBufferString("{nope"))
		rec := httptest.NewRecorder()
		svc.router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("oversized body", func(t *testing.T) {
		rec, _ := remember(t, svc, RememberRequest{Project: "repo_aaaaaa", Text: strings.Repeat("x", rememberMaxBody+1)})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestHandleRemember_ScopeAndTypeDefaults(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	_, resp := remember(t, svc, RememberRequest{Project: "repo_aaaaaa", Text: "defaults"})
	obs, err := svc.observationStore.GetObservationByID(context.Background(), resp.ID)
	require.NoError(t, err)
	assert.Equal(t, "discovery", string(obs.Type))
	assert.Equal(t, "project", string(obs.Scope))

	_, resp = remember(t, svc, RememberRequest{Project: "repo_aaaaaa", Text: "shared knowledge", Scope: "global"})
	obs, err = svc.observationStore.GetObservationByID(context.Background(), resp.ID)
	require.NoError(t, err)
	assert.Equal(t, "global", string(obs.Scope))
}

func TestHandleRemember_RepeatedCallIsNotStoredTwice(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	_, first := remember(t, svc, RememberRequest{Project: "repo_aaaaaa", Title: "Same", Text: "same text"})
	rec, second := remember(t, svc, RememberRequest{Project: "repo_aaaaaa", Title: "Same", Text: "same text"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, second.Duplicate)
	assert.Equal(t, first.ID, second.ID)

	_, other := remember(t, svc, RememberRequest{Project: "repo_aaaaaa", Title: "Same", Text: "different text"})
	assert.False(t, other.Duplicate)
	assert.NotEqual(t, first.ID, other.ID)
}

func TestDeriveTitle(t *testing.T) {
	assert.Equal(t, "first line", deriveTitle("\n\n  first line  \nsecond"))
	assert.Equal(t, "Untitled memory", deriveTitle(" \n "))

	long := strings.Repeat("é", 200)
	got := deriveTitle(long)
	assert.Equal(t, rememberTitleRunes+1, len([]rune(got)), "80 runes plus the ellipsis, cut on a rune boundary")
	assert.True(t, strings.HasSuffix(got, "…"))
}
