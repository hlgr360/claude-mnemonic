package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// scopedService stores notes the way the old rule left them (global, decided by the rule) and one a person pinned.
func scopedService(t *testing.T) (*Service, []int64, int64, func()) {
	t.Helper()
	svc, cleanup := testService(t)
	var tooGlobal []int64
	for _, title := range []string{"first architecture note", "second testing note"} {
		id, _, err := svc.observationStore.StoreObservation(context.Background(), "s", "proj_aaaaaa", &models.ParsedObservation{
			Type: models.ObsTypeDiscovery, Title: title, Narrative: "narrative of " + title, Concepts: []string{"architecture", "testing"},
		}, 1, 1)
		require.NoError(t, err)
		require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET scope = 'global', scope_source = 'auto' WHERE id = ?`, id).Error)
		tooGlobal = append(tooGlobal, id)
	}
	pinned, _, err := svc.observationStore.StoreObservation(context.Background(), "s", "proj_aaaaaa", &models.ParsedObservation{
		Type: models.ObsTypeDiscovery, Title: "pinned global", Narrative: "narrative of pinned", Concepts: []string{"architecture"}, Scope: models.ScopeGlobal,
	}, 1, 1)
	require.NoError(t, err)
	return svc, tooGlobal, pinned, cleanup
}

func TestScopePreview_ShowsWhatWouldChangeAndChangesNothing(t *testing.T) {
	svc, tooGlobal, pinned, cleanup := scopedService(t)
	defer cleanup()

	rec := doRequest(t, svc, http.MethodGet, "/api/scope/preview", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var resp rescopeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.DryRun)
	assert.Equal(t, 3, resp.Total)
	assert.Equal(t, 2, resp.ToProject)
	assert.Equal(t, 0, resp.ToGlobal)
	assert.Equal(t, 1, resp.Protected, "the pinned one")
	assert.NotEmpty(t, resp.Confirm)
	require.Len(t, resp.Sample, 2)
	assert.ElementsMatch(t, tooGlobal, []int64{resp.Sample[0].ID, resp.Sample[1].ID})
	assert.NotContains(t, []int64{resp.Sample[0].ID, resp.Sample[1].ID}, pinned)

	var scope string
	require.NoError(t, svc.store.DB.Raw(`SELECT scope FROM observations WHERE id = ?`, tooGlobal[0]).Scan(&scope).Error)
	assert.Equal(t, "global", scope, "a preview changes nothing")
}

func TestScopeApply_NeedsTheTokenOfAPreview(t *testing.T) {
	svc, _, _, cleanup := scopedService(t)
	defer cleanup()

	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPost, "/api/scope/apply", nil).Code, "no body")
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPost, "/api/scope/apply", map[string]string{}).Code, "no token")
	rec := doRequest(t, svc, http.MethodPost, "/api/scope/apply", map[string]string{"confirm": "deadbeefdeadbeef"})
	assert.Equal(t, http.StatusConflict, rec.Code, "a token that is not the current one")
}

func TestScopeApply_ChangesTheScopesAfterASnapshotAndRefusesAStaleToken(t *testing.T) {
	svc, tooGlobal, pinned, cleanup := scopedService(t)
	defer cleanup()

	var preview rescopeResponse
	require.NoError(t, json.Unmarshal(doRequest(t, svc, http.MethodGet, "/api/scope/preview", nil).Body.Bytes(), &preview))

	// The archive changes after the preview: a person pins one of the notes. The old token is stale.
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET scope_source = 'explicit' WHERE id = ?`, tooGlobal[1]).Error)
	assert.Equal(t, http.StatusConflict, doRequest(t, svc, http.MethodPost, "/api/scope/apply", map[string]string{"confirm": preview.Confirm}).Code)
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET scope_source = 'auto' WHERE id = ?`, tooGlobal[1]).Error)

	rec := doRequest(t, svc, http.MethodPost, "/api/scope/apply", map[string]string{"confirm": preview.Confirm})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp rescopeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, 2, resp.Changed)
	require.NotEmpty(t, resp.Backup, "a snapshot is taken first")
	_, err := os.Stat(resp.Backup)
	assert.NoError(t, err)

	for _, id := range tooGlobal {
		var scope string
		require.NoError(t, svc.store.DB.Raw(`SELECT scope FROM observations WHERE id = ?`, id).Scan(&scope).Error)
		assert.Equal(t, "project", scope)
	}
	var pinnedScope string
	require.NoError(t, svc.store.DB.Raw(`SELECT scope FROM observations WHERE id = ?`, pinned).Scan(&pinnedScope).Error)
	assert.Equal(t, "global", pinnedScope, "a scope that was chosen is untouched")

	// Nothing is left: a second preview has no changes and applying it does nothing, without another backup.
	var again rescopeResponse
	require.NoError(t, json.Unmarshal(doRequest(t, svc, http.MethodGet, "/api/scope/preview", nil).Body.Bytes(), &again))
	assert.Equal(t, 0, again.ToProject+again.ToGlobal)
	rec = doRequest(t, svc, http.MethodPost, "/api/scope/apply", map[string]string{"confirm": again.Confirm})
	require.Equal(t, http.StatusOK, rec.Code)
	var none rescopeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &none))
	assert.Equal(t, 0, none.Changed)
	assert.Empty(t, none.Backup)
	assert.Equal(t, "Nothing to change.", none.Message)
}

func TestEditingAScopeThroughTheAPIPinsIt(t *testing.T) {
	svc, tooGlobal, _, cleanup := scopedService(t)
	defer cleanup()

	rec := doRequest(t, svc, http.MethodPut, "/api/observations/"+itoa(tooGlobal[0]), map[string]any{"scope": "global"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var preview rescopeResponse
	require.NoError(t, json.Unmarshal(doRequest(t, svc, http.MethodGet, "/api/scope/preview", nil).Body.Bytes(), &preview))
	assert.Equal(t, 1, preview.ToProject, "only the other one: the edited note is pinned now")
	assert.Equal(t, 2, preview.Protected)
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
