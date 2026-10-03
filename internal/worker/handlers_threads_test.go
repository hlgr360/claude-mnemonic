package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func checkpoint(t *testing.T, svc *Service, req CheckpointRequest) (*httptest.ResponseRecorder, CheckpointResponse) {
	t.Helper()
	rec := doRequest(t, svc, http.MethodPost, "/api/threads/checkpoint", req)
	var resp CheckpointResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	}
	return rec, resp
}

func catchUp(t *testing.T, svc *Service, target string) (*httptest.ResponseRecorder, CatchUpResponse) {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, target, nil)
	var resp CatchUpResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	}
	return rec, resp
}

func TestHandleCheckpoint_WritesThenUpdatesTheSameThread(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	rec, first := checkpoint(t, svc, CheckpointRequest{
		Project: "repo_aaaaaa", Thread: "Overlay design", Goal: "ship the overlay", Progress: "store done",
		NextSteps: "write the handlers", Source: "claude-ai",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, first.Created)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	// the same name, written differently, is the same thread
	rec, second := checkpoint(t, svc, CheckpointRequest{
		Project: "repo_aaaaaa", Thread: "  overlay   DESIGN! ", Goal: "ship the overlay", Progress: "store and handlers done",
		Decisions: "names, not ids",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, second.Created)
	assert.Equal(t, first.ID, second.ID)

	_, digest := catchUp(t, svc, "/api/projects/repo_aaaaaa/catch-up")
	require.Len(t, digest.Threads, 1)
	got := digest.Threads[0]
	assert.Equal(t, "overlay   DESIGN!", got.Thread, "the latest wording of the name is shown")
	assert.Equal(t, "store and handlers done", got.Progress)
	assert.Equal(t, "names, not ids", got.Decisions)
	assert.Empty(t, got.NextSteps, "a note is the current state, what was resolved is gone")
}

func TestHandleCheckpoint_ValidatesTheRequest(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")
	long := strings.Repeat("x", checkpointFieldRunes+1)

	cases := []struct {
		name string
		req  CheckpointRequest
		code int
		want string
	}{
		{"no project", CheckpointRequest{Thread: "t", Goal: "g"}, 400, "project is required"},
		{"bad project", CheckpointRequest{Project: "../etc", Thread: "t", Goal: "g"}, 400, ""},
		{"no thread", CheckpointRequest{Project: "repo_aaaaaa", Goal: "g"}, 400, "thread is required"},
		{"thread of only symbols", CheckpointRequest{Project: "repo_aaaaaa", Thread: "!!!", Goal: "g"}, 400, "thread is required"},
		{"nothing to say", CheckpointRequest{Project: "repo_aaaaaa", Thread: "t", NextSteps: "only next"}, 400, "nothing to store"},
		{"entirely private", CheckpointRequest{Project: "repo_aaaaaa", Thread: "t", Goal: "<private>secret</private>"}, 400, "nothing to store"},
		{"too long", CheckpointRequest{Project: "repo_aaaaaa", Thread: "t", Goal: "g", Progress: long}, 400, "too long"},
		{"unknown project", CheckpointRequest{Project: "invented_zzzzzz", Thread: "t", Goal: "g"}, 422, "unknown project"},
	}
	for _, c := range cases {
		rec, _ := checkpoint(t, svc, c.req)
		assert.Equal(t, c.code, rec.Code, c.name)
		assert.Contains(t, rec.Body.String(), c.want, c.name)
	}

	rec, _ := checkpoint(t, svc, CheckpointRequest{Project: "fresh_bbbbbb", Thread: "t", Goal: "g", AllowNewProject: true})
	assert.Equal(t, http.StatusOK, rec.Code, "a folder-backed project may start with a checkpoint")
}

func TestHandleCheckpoint_StripsPrivateParts(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	rec, _ := checkpoint(t, svc, CheckpointRequest{
		Project: "repo_aaaaaa", Thread: "Keys <private>my-secret-thread</private>", Goal: "rotate <private>sk-123</private> the keys",
		Progress: "done",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, digest := catchUp(t, svc, "/api/projects/repo_aaaaaa/catch-up")
	raw, _ := json.Marshal(digest)
	assert.NotContains(t, string(raw), "sk-123")
	assert.NotContains(t, string(raw), "my-secret-thread")
	assert.Contains(t, string(raw), "rotate")
}

func TestHandleCheckpoint_FollowsAliasesAndKeepsProjectsApart(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa", "other_cccccc")
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases",
		setAliasRequest{Alias: "frag_bbbbbb", Canonical: "repo_aaaaaa"}).Code)

	rec, resp := checkpoint(t, svc, CheckpointRequest{Project: "frag_bbbbbb", Thread: "Same name", Goal: "g"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "repo_aaaaaa", resp.Project, "an alias is not a project to write into")
	_, _ = checkpoint(t, svc, CheckpointRequest{Project: "other_cccccc", Thread: "Same name", Goal: "elsewhere"})

	_, a := catchUp(t, svc, "/api/projects/frag_bbbbbb/catch-up")
	assert.Equal(t, "repo_aaaaaa", a.Project, "reading through the alias finds the same notes")
	require.Len(t, a.Threads, 1)
	assert.Equal(t, "g", a.Threads[0].Goal)
	_, b := catchUp(t, svc, "/api/projects/other_cccccc/catch-up")
	require.Len(t, b.Threads, 1)
	assert.Equal(t, "elsewhere", b.Threads[0].Goal)
}

func TestHandleCatchUp_NewestThreadFirstWithDecisions(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")
	for _, name := range []string{"first line", "second line", "third line"} {
		rec, _ := checkpoint(t, svc, CheckpointRequest{Project: "repo_aaaaaa", Thread: name, Goal: "g " + name})
		require.Equal(t, http.StatusOK, rec.Code)
		time.Sleep(4 * time.Millisecond)
	}
	time.Sleep(4 * time.Millisecond)
	_, _ = checkpoint(t, svc, CheckpointRequest{Project: "repo_aaaaaa", Thread: "first line", Goal: "g first line", Progress: "back to it"})

	_, _, err := svc.observationStore.StoreObservation(context.Background(), "s", "repo_aaaaaa",
		&models.ParsedObservation{Type: models.ObsTypeDecision, Title: "Use names", Subtitle: "ids are cumbersome", Narrative: "n"}, 1, 1)
	require.NoError(t, err)

	rec, digest := catchUp(t, svc, "/api/projects/repo_aaaaaa/catch-up")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var names []string
	for _, th := range digest.Threads {
		names = append(names, th.Thread)
	}
	assert.Equal(t, []string{"first line", "third line", "second line"}, names, "the thread worked on last comes first")
	require.Len(t, digest.Decisions, 1)
	assert.Equal(t, "Use names", digest.Decisions[0].Title)

	_, limited := catchUp(t, svc, "/api/projects/repo_aaaaaa/catch-up?threads=2&decisions=1")
	assert.Len(t, limited.Threads, 2)
	_, junk := catchUp(t, svc, "/api/projects/repo_aaaaaa/catch-up?threads=abc&decisions=-4")
	assert.Len(t, junk.Threads, 3, "an unusable limit falls back to the default")
}

func TestHandleCatchUp_UnknownProjectAndEmptyProject(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")

	rec, _ := catchUp(t, svc, "/api/projects/invented_zzzzzz/catch-up")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec, digest := catchUp(t, svc, "/api/projects/repo_aaaaaa/catch-up")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotNil(t, digest.Threads, "an empty digest is an empty list, not null")
	assert.Empty(t, digest.Threads)
	assert.Contains(t, rec.Body.String(), `"threads":[]`)
	assert.Contains(t, rec.Body.String(), `"decisions":[]`)
}

func TestHandleCatchUp_ClipsVeryLongFields(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedProjects(t, svc, "repo_aaaaaa")
	_, _ = checkpoint(t, svc, CheckpointRequest{Project: "repo_aaaaaa", Thread: "t", Goal: strings.Repeat("a", checkpointFieldRunes)})

	_, digest := catchUp(t, svc, "/api/projects/repo_aaaaaa/catch-up")
	require.Len(t, digest.Threads, 1)
	assert.LessOrEqual(t, len([]rune(digest.Threads[0].Goal)), catchUpFieldRunes+1)
	assert.True(t, strings.HasSuffix(digest.Threads[0].Goal, "…"))
}

func TestThreadSlug(t *testing.T) {
	cases := map[string]string{
		"Overlay design":          "overlay-design",
		"  overlay   DESIGN!":     "overlay-design",
		"Über cool_thing #2":      "über-cool-thing-2",
		"!!!":                     "",
		"":                        "",
		strings.Repeat("ab ", 40): strings.TrimRight(strings.Repeat("ab-", 40)[:threadSlugRunes], "-"),
	}
	for in, want := range cases {
		assert.Equal(t, want, threadSlug(in), "%q", in)
	}
}
