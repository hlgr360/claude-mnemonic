package worker

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countsBody struct {
	Observations int64 `json:"observations"`
	Prompts      int64 `json:"prompts"`
	Summaries    int64 `json:"summaries"`
}

func getCounts(t *testing.T, svc *Service, query string) countsBody {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/counts"+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var out countsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestCountsEndpoint_RealTotalsNotThePageSize(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	assert.Equal(t, countsBody{}, getCounts(t, svc, ""), "an empty database counts zero")

	// More observations than the dashboard's page of 50.
	for i := 0; i < 55; i++ {
		createTestObservation(t, svc.observationStore, "proj_aaaaaa", "Note number "+string(rune('A'+i%26))+string(rune('a'+i/26)), "Distinct narrative "+string(rune('a'+i%26))+string(rune('A'+i/26))+" unique", nil)
	}
	createTestObservation(t, svc.observationStore, "proj_bbbbbb", "Elsewhere", "Another project's note about something else", nil)

	assert.EqualValues(t, 56, getCounts(t, svc, "").Observations, "all projects: more than a page")
	assert.EqualValues(t, 55, getCounts(t, svc, "?project=proj_aaaaaa").Observations)
	assert.EqualValues(t, 1, getCounts(t, svc, "?project=proj_bbbbbb").Observations)
	assert.EqualValues(t, 0, getCounts(t, svc, "?project=proj_nothing").Observations)
}

func TestCountsEndpoint_RefusesABadProject(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, "/api/counts?project=../etc", nil).Code)
}
