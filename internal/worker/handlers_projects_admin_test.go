package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
)

const (
	adminDoomed = "doomed_aaaaaa"
	adminKeeper = "keeper_bbbbbb"
)

// seedAdminProjects gives each project one observation (and the session it creates) plus a vector.
func seedAdminProjects(t *testing.T, svc *Service) (doomedObs, keeperObs int64) {
	t.Helper()
	doomedObs = createTestObservation(t, svc.observationStore, adminDoomed, "doomed finding", "zebra notes", []string{"x"})
	keeperObs = createTestObservation(t, svc.observationStore, adminKeeper, "keeper finding", "giraffe notes", nil)
	for _, v := range []struct {
		project string
		id      int64
	}{{adminDoomed, doomedObs}, {adminKeeper, keeperObs}} {
		require.NoError(t, svc.store.DB.Exec(
			`INSERT INTO vectors (doc_id, embedding, sqlite_id, doc_type, field_type, project, scope, model_version)
			 VALUES (?, ?, ?, 'observation', 'narrative', ?, 'project', 't')`,
			fmt.Sprintf("obs_%d_narrative", v.id), make([]byte, 384*4), v.id, v.project).Error)
	}
	return doomedObs, keeperObs
}

func decodeAction(t *testing.T, rec *httptest.ResponseRecorder) projectActionResponse {
	t.Helper()
	var resp projectActionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return resp
}

func countRows(t *testing.T, svc *Service, query string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, svc.store.DB.Raw(query, args...).Scan(&n).Error)
	return n
}

func TestHandleProjectStats(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)
	require.NoError(t, gorm.NewProjectAliasStore(svc.store).SetAlias(context.Background(), "frag_ffffff", adminDoomed, "manual"))

	rec := doRequest(t, svc, http.MethodGet, "/api/projects/"+adminDoomed+"/stats", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var stats gorm.ProjectStats
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &stats))
	assert.Equal(t, adminDoomed, stats.Project)
	assert.Equal(t, int64(1), stats.Observations)
	assert.Equal(t, int64(1), stats.Vectors)
	assert.Equal(t, []string{"frag_ffffff"}, stats.Aliases)

	assert.Equal(t, http.StatusNotFound, doRequest(t, svc, http.MethodGet, "/api/projects/nothing_000000/stats", nil).Code)
	assert.Equal(t, http.StatusConflict, doRequest(t, svc, http.MethodGet, "/api/projects/frag_ffffff/stats", nil).Code, "an alias has no data of its own")
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, "/api/projects/a;b/stats", nil).Code)
}

func TestHandleDeleteProject_WithoutConfirmOnlyPreviews(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)

	rec := doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeAction(t, rec)

	assert.True(t, resp.DryRun)
	assert.Equal(t, "delete", resp.Action)
	assert.NotEmpty(t, resp.Confirm)
	assert.Equal(t, int64(1), resp.Stats.Observations)
	assert.Contains(t, resp.Message, "Nothing was changed")
	assert.Contains(t, resp.Message, resp.Confirm, "the message tells the caller exactly what to send back")

	assert.Equal(t, int64(1), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed), "a preview deletes nothing")
	_, err := os.Stat(svc.store.DefaultSnapshotDir())
	assert.True(t, os.IsNotExist(err), "and takes no backup")
}

func TestHandleDeleteProject_ConfirmedDeleteBacksUpThenRemovesEverything(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)
	token := decodeAction(t, doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed, nil)).Confirm

	rec := doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed+"?confirm="+token, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeAction(t, rec)

	assert.False(t, resp.DryRun)
	assert.Empty(t, resp.Confirm)
	assert.Equal(t, int64(1), resp.Stats.Observations, "reports what was removed")
	require.NotEmpty(t, resp.Backup)
	fi, err := os.Stat(resp.Backup)
	require.NoError(t, err)
	assert.Greater(t, fi.Size(), int64(0))
	assert.Equal(t, svc.store.DefaultSnapshotDir(), filepath.Dir(resp.Backup))

	assert.Zero(t, countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed))
	assert.Zero(t, countRows(t, svc, `SELECT COUNT(*) FROM vectors WHERE project = ?`, adminDoomed))
	assert.Equal(t, int64(1), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminKeeper), "other projects are untouched")

	// the backup still holds what was deleted
	snap, err := gorm.NewStore(gorm.Config{Path: resp.Backup, MaxConns: 1})
	require.NoError(t, err)
	defer snap.Close()
	var n int64
	require.NoError(t, snap.DB.Raw(`SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed).Scan(&n).Error)
	assert.Equal(t, int64(1), n, "the pre-delete snapshot can restore the project")

	// and the project is gone from every listing
	assert.Equal(t, http.StatusNotFound, doRequest(t, svc, http.MethodGet, "/api/projects/"+adminDoomed+"/stats", nil).Code)
	rows := doRequest(t, svc, http.MethodGet, "/api/projects/summary", nil)
	assert.NotContains(t, rows.Body.String(), adminDoomed)
}

func TestHandleDeleteProject_RefusesAWrongOrStaleConfirmation(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)

	rec := doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed+"?confirm=notatoken", nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, int64(1), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed))

	// a token from before new data arrived must not authorise deleting the new data
	stale := decodeAction(t, doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed, nil)).Confirm
	createTestObservation(t, svc.observationStore, adminDoomed, "added after the preview", "new", nil)

	rec = doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed+"?confirm="+stale, nil)
	assert.Equal(t, http.StatusConflict, rec.Code, "the project changed since the preview")
	assert.Equal(t, int64(2), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed))
	_, err := os.Stat(svc.store.DefaultSnapshotDir())
	assert.True(t, os.IsNotExist(err), "a refused request takes no backup")

	// a token for one project cannot delete another
	otherToken := decodeAction(t, doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminKeeper, nil)).Confirm
	assert.Equal(t, http.StatusConflict, doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed+"?confirm="+otherToken, nil).Code)
}

func TestHandleDeleteProject_MissingAliasAndInvalidNames(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)
	require.NoError(t, gorm.NewProjectAliasStore(svc.store).SetAlias(context.Background(), "frag_ffffff", adminDoomed, "manual"))

	assert.Equal(t, http.StatusNotFound, doRequest(t, svc, http.MethodDelete, "/api/projects/nothing_000000", nil).Code)
	rec := doRequest(t, svc, http.MethodDelete, "/api/projects/frag_ffffff", nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "alias")
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodDelete, "/api/projects/a;b", nil).Code)
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodDelete, "/api/projects/..%2Fetc", nil).Code)
}

func TestHandleDeleteProject_FailedBackupAbortsTheDelete(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)
	token := decodeAction(t, doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed, nil)).Confirm
	// A regular file where the backup directory should be makes the snapshot fail.
	require.NoError(t, os.WriteFile(svc.store.DefaultSnapshotDir(), []byte("in the way"), 0o600))

	rec := doRequest(t, svc, http.MethodDelete, "/api/projects/"+adminDoomed+"?confirm="+token, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "could not take a backup")
	assert.Equal(t, int64(1), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed),
		"no backup, no delete")
}

func mergeBody(into, confirm string) map[string]string {
	return map[string]string{"into": into, "confirm": confirm}
}

func TestHandleMergeProject_PreviewThenConfirm(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)

	rec := doRequest(t, svc, http.MethodPost, "/api/projects/"+adminDoomed+"/merge", mergeBody(adminKeeper, ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	preview := decodeAction(t, rec)
	assert.True(t, preview.DryRun)
	assert.Equal(t, adminKeeper, preview.Into)
	assert.Contains(t, preview.Message, "make "+adminDoomed+" an alias of "+adminKeeper)
	assert.Equal(t, int64(1), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed), "a preview moves nothing")

	rec = doRequest(t, svc, http.MethodPost, "/api/projects/"+adminDoomed+"/merge", mergeBody(adminKeeper, preview.Confirm))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	done := decodeAction(t, rec)
	assert.False(t, done.DryRun)
	require.NotEmpty(t, done.Backup)
	_, err := os.Stat(done.Backup)
	require.NoError(t, err)

	assert.Zero(t, countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed))
	assert.Equal(t, int64(2), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminKeeper))
	assert.Equal(t, int64(2), countRows(t, svc, `SELECT COUNT(*) FROM vectors WHERE project = ?`, adminKeeper), "vectors followed the observations")

	canonical, isAlias, err := gorm.NewProjectAliasStore(svc.store).ResolveAlias(context.Background(), adminDoomed)
	require.NoError(t, err)
	assert.True(t, isAlias)
	assert.Equal(t, adminKeeper, canonical)
}

func TestHandleMergeProject_IntoAnAliasMergesIntoTheCanonicalProject(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)
	require.NoError(t, gorm.NewProjectAliasStore(svc.store).SetAlias(context.Background(), "nick_cccccc", adminKeeper, "manual"))

	preview := decodeAction(t, doRequest(t, svc, http.MethodPost, "/api/projects/"+adminDoomed+"/merge", mergeBody("nick_cccccc", "")))
	assert.Equal(t, adminKeeper, preview.Into, "the alias is followed to its project")
	rec := doRequest(t, svc, http.MethodPost, "/api/projects/"+adminDoomed+"/merge", mergeBody("nick_cccccc", preview.Confirm))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, int64(2), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminKeeper))
}

func TestHandleMergeProject_ValidationAndRefusals(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	seedAdminProjects(t, svc)
	require.NoError(t, gorm.NewProjectAliasStore(svc.store).SetAlias(context.Background(), "frag_ffffff", adminDoomed, "manual"))
	url := "/api/projects/" + adminDoomed + "/merge"

	tests := []struct {
		body any
		name string
		path string
		want int
	}{
		{mergeBody("", ""), "missing into", url, http.StatusBadRequest},
		{mergeBody(adminDoomed, ""), "into itself", url, http.StatusBadRequest},
		{mergeBody("frag_ffffff", ""), "into an alias of itself", url, http.StatusBadRequest},
		{mergeBody("typo_000000", ""), "target does not exist", url, http.StatusUnprocessableEntity},
		{mergeBody(adminKeeper, ""), "source does not exist", "/api/projects/nothing_000000/merge", http.StatusNotFound},
		{mergeBody(adminKeeper, ""), "source is an alias", "/api/projects/frag_ffffff/merge", http.StatusConflict},
		{mergeBody("a;b", ""), "bad target name", url, http.StatusBadRequest},
		{mergeBody(adminKeeper, "notatoken"), "wrong token", url, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(t, svc, http.MethodPost, tt.path, tt.body)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}

	req := httptest.NewRequest(http.MethodPost, url, nil)
	rec := httptest.NewRecorder()
	svc.router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "an empty body is not a merge request")

	assert.Equal(t, int64(1), countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = ?`, adminDoomed), "every refusal changed nothing")
	_, err := os.Stat(svc.store.DefaultSnapshotDir())
	assert.True(t, os.IsNotExist(err))
}

func TestConfirmToken(t *testing.T) {
	a := gorm.ProjectStats{Project: "p", Observations: 3}
	b := gorm.ProjectStats{Project: "p", Observations: 4}

	assert.Equal(t, confirmToken("delete", "p", "", a), confirmToken("delete", "p", "", a), "deterministic")
	assert.Len(t, confirmToken("delete", "p", "", a), 16)
	assert.NotEqual(t, confirmToken("delete", "p", "", a), confirmToken("delete", "p", "", b), "changes with the data")
	assert.NotEqual(t, confirmToken("delete", "p", "", a), confirmToken("merge", "p", "", a), "a delete token cannot authorise a merge")
	assert.NotEqual(t, confirmToken("merge", "p", "x", a), confirmToken("merge", "p", "y", a), "and a merge token is bound to its target")
}
