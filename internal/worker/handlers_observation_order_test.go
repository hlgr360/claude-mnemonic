//go:build fts5

package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listedObservationIDs asks the worker for a list and returns the ids in the order given.
func listedObservationIDs(t *testing.T, svc *Service, url string) (int, []int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	svc.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var body struct {
		Observations []struct {
			ID int64 `json:"id"`
		} `json:"observations"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	ids := make([]int64, len(body.Observations))
	for i, o := range body.Observations {
		ids[i] = o.ID
	}
	return rec.Code, ids
}

// The dashboard's timeline asks for ?sort=date: a note saved a moment ago (importance 1) must be in it even when older
// notes have earned a higher score and fill the first page of the default, importance-ordered list.
func TestHandleGetObservations_SortByDateListsTheNewestFirstAndTheDefaultIsUnchanged(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	ctx := context.Background()

	var ids []int64
	for _, title := range []string{"old one", "old two", "old three", "fresh note"} {
		ids = append(ids, createTestObservation(t, svc.observationStore, "project-a", title, "text "+title, []string{"test"}))
	}
	// Distinct creation times in id order, and the three old notes have earned a higher score than the fresh one.
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET created_at_epoch = 1700000000000 + id * 1000`).Error)
	for _, id := range ids[:3] {
		require.NoError(t, svc.observationStore.UpdateImportanceScore(ctx, id, 1.4))
	}
	fresh := ids[3]

	// Default and sort=importance: the old, high-scored notes first, the fresh one last; a page of 3 leaves it out.
	code, byDefault := listedObservationIDs(t, svc, "/api/observations?limit=3")
	require.Equal(t, http.StatusOK, code)
	assert.NotContains(t, byDefault, fresh, "the importance order leaves the fresh note off the first page")
	_, explicit := listedObservationIDs(t, svc, "/api/observations?limit=3&sort=importance")
	assert.Equal(t, byDefault, explicit, "sort=importance is the default")

	// sort=date: newest first, for all projects and for one.
	_, all := listedObservationIDs(t, svc, "/api/observations?limit=3&sort=date")
	assert.Equal(t, []int64{ids[3], ids[2], ids[1]}, all)
	_, one := listedObservationIDs(t, svc, "/api/observations?limit=3&sort=date&project=project-a")
	assert.Equal(t, []int64{ids[3], ids[2], ids[1]}, one)
}

func TestHandleGetObservations_AnUnknownSortIsRefused(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	for _, bad := range []string{"newest", "Date", "id", "date%3Bdrop", "date%20"} {
		code, _ := listedObservationIDs(t, svc, "/api/observations?sort="+bad)
		assert.Equal(t, http.StatusBadRequest, code, bad)
	}
}
