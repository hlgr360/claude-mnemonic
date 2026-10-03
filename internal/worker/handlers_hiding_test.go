package worker

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func supersede(t *testing.T, svc *Service, id int64) {
	t.Helper()
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, id).Error)
}

func observationIDs(t *testing.T, body []byte) []int64 {
	t.Helper()
	var resp struct {
		Observations []struct {
			ID int64 `json:"id"`
		} `json:"observations"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	var ids []int64
	for _, o := range resp.Observations {
		ids = append(ids, o.ID)
	}
	return ids
}

// twoDifferentObservations stores two observations about different things (so they are not clustered) that
// both match the word "shipping".
func twoDifferentObservations(t *testing.T, svc *Service) (kept, hidden int64) {
	t.Helper()
	hidden = createTestObservation(t, svc.observationStore, "proj_aaaaaa", "Shipping uses the old courier", "Parcels leave through the old courier contract", []string{"logistics"})
	time.Sleep(3 * time.Millisecond)
	kept = createTestObservation(t, svc.observationStore, "proj_aaaaaa", "Shipping moved to the new carrier", "Redis caching TTL for the new carrier rates", []string{"carrier"})
	return kept, hidden
}

func TestSupersededObservationsAreNotInjectedIntoASession(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	kept, hidden := twoDifferentObservations(t, svc)

	rec := doRequest(t, svc, http.MethodGet, "/api/context/inject?project=proj_aaaaaa", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.ElementsMatch(t, []int64{kept, hidden}, observationIDs(t, rec.Body.Bytes()), "both are injected before anything is superseded")

	supersede(t, svc, hidden)
	rec = doRequest(t, svc, http.MethodGet, "/api/context/inject?project=proj_aaaaaa", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []int64{kept}, observationIDs(t, rec.Body.Bytes()), "the superseded one is left out")
}

func TestSupersededObservationsAreNotReturnedByASearchForAPrompt(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	kept, hidden := twoDifferentObservations(t, svc)
	supersede(t, svc, hidden)

	rec := doRequest(t, svc, http.MethodGet, "/api/context/search?project=proj_aaaaaa&query=shipping", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	ids := observationIDs(t, rec.Body.Bytes())
	assert.Contains(t, ids, kept)
	assert.NotContains(t, ids, hidden)
}

func TestSupersededObservationsAreNotReturnedByTheCrossProjectSearch(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	kept, hidden := twoDifferentObservations(t, svc)
	supersede(t, svc, hidden)
	svc.vectorQueryFn = fakeVectors(
		vecDoc(hidden, "proj_aaaaaa", "Shipping uses the old courier", "discovery", 0.95),
		vecDoc(kept, "proj_aaaaaa", "Shipping moved to the new carrier", "discovery", 0.9),
	)

	rec := doRequest(t, svc, http.MethodGet, "/api/search/cross-project?query=shipping", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got crossSearchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, 1, got.Count)
	assert.Equal(t, kept, got.Observations[0].ID)
}

func TestSupersededObservationsStayInTheDashboardMarkedAndCanBeFetchedById(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	kept, hidden := twoDifferentObservations(t, svc)
	supersede(t, svc, hidden)

	rec := doRequest(t, svc, http.MethodGet, "/api/observations?project=proj_aaaaaa", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var feed struct {
		Observations []struct {
			ID           int64 `json:"id"`
			IsSuperseded bool  `json:"is_superseded"`
		} `json:"observations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil || feed.Observations == nil {
		var plain []struct {
			ID           int64 `json:"id"`
			IsSuperseded bool  `json:"is_superseded"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &plain))
		for _, o := range plain {
			feed.Observations = append(feed.Observations, struct {
				ID           int64 `json:"id"`
				IsSuperseded bool  `json:"is_superseded"`
			}{o.ID, o.IsSuperseded})
		}
	}
	marks := map[int64]bool{}
	for _, o := range feed.Observations {
		marks[o.ID] = o.IsSuperseded
	}
	assert.Contains(t, marks, hidden, "the feed still lists it")
	assert.True(t, marks[hidden], "and marks it")
	assert.False(t, marks[kept])

	rec = doRequest(t, svc, http.MethodGet, "/api/observations/"+strconv.FormatInt(hidden, 10), nil)
	assert.Equal(t, http.StatusOK, rec.Code, "an explicit fetch by id still works")
}

func TestWithoutSuperseded(t *testing.T) {
	a := &models.Observation{ID: 1}
	b := &models.Observation{ID: 2, IsSuperseded: true}
	c := &models.Observation{ID: 3, Title: sql.NullString{String: "c", Valid: true}}

	got := withoutSuperseded([]*models.Observation{a, b, c})
	assert.Equal(t, []*models.Observation{a, c}, got)
	assert.Empty(t, withoutSuperseded(nil))
	assert.Empty(t, withoutSuperseded([]*models.Observation{b}))

	in := []*models.Observation{a, b, c}
	withoutSuperseded(in)
	assert.Equal(t, []*models.Observation{a, b, c}, in, "the caller's slice is not rearranged")
}
