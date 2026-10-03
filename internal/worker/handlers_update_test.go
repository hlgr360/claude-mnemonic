package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleUpdateObservation_EditsAndReturnsTheObservation(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	id := createTestObservation(t, svc.observationStore, "proj_aaaaaa", "Old title", "Old narrative", []string{"cache"})

	rec := doRequest(t, svc, http.MethodPut, fmt.Sprintf("/api/observations/%d", id),
		map[string]any{"title": "New title", "facts": []string{"one", "two"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Observation struct {
			Title     string   `json:"title"`
			Narrative string   `json:"narrative"`
			Facts     []string `json:"facts"`
		} `json:"observation"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "New title", resp.Observation.Title)
	assert.Equal(t, "Old narrative", resp.Observation.Narrative)
	assert.Equal(t, []string{"one", "two"}, resp.Observation.Facts)

	rec = doRequest(t, svc, http.MethodGet, fmt.Sprintf("/api/observations/%d", id), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "New title", "the edit is stored")
}

func TestHandleUpdateObservation_Refusals(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	id := createTestObservation(t, svc.observationStore, "proj_aaaaaa", "A title", "A narrative", nil)

	assert.Equal(t, http.StatusNotFound, doRequest(t, svc, http.MethodPut, "/api/observations/999999", map[string]any{"title": "x"}).Code)
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPut, fmt.Sprintf("/api/observations/%d", id), map[string]any{"scope": "galaxy"}).Code)
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPut, "/api/observations/abc", map[string]any{"title": "x"}).Code)
}
