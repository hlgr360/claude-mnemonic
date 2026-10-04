package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

type connectionsBody struct {
	Observation struct {
		Title   string `json:"title"`
		Project string `json:"project"`
		ID      int64  `json:"id"`
	} `json:"observation"`
	Connections []struct {
		Relation struct {
			RelationType string  `json:"relation_type"`
			Reason       string  `json:"reason"`
			Confidence   float64 `json:"confidence"`
		} `json:"relation"`
		Direction string `json:"direction"`
		Note      struct {
			Title   string `json:"title"`
			Project string `json:"project"`
			ID      int64  `json:"id"`
		} `json:"note"`
	} `json:"connections"`
	Total int `json:"total"`
}

func getConnections(t *testing.T, svc *Service, id int64, query string) connectionsBody {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, fmt.Sprintf("/api/observations/%d/connections%s", id, query), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var out connectionsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestConnectionsAPI_AnswersWithTheNoteTheRelationAndTheOtherEnd(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	n := addNotes(t, svc, "proj_aaaaaa", 4)
	storeRelation(t, svc, n[2], n[0], models.RelationFixes, 0.9)
	storeRelation(t, svc, n[3], n[2], models.RelationRelatesTo, 0.7)
	storeRelation(t, svc, n[2], n[1], models.RelationEvolvesFrom, 0.5)

	got := getConnections(t, svc, n[2], "")
	assert.Equal(t, n[2], got.Observation.ID, "it says which note it is about")
	assert.Equal(t, "proj_aaaaaa", got.Observation.Project)
	assert.Equal(t, 3, got.Total)
	require.Len(t, got.Connections, 3)
	assert.Equal(t, n[0], got.Connections[0].Note.ID)
	assert.Equal(t, "fixes", got.Connections[0].Relation.RelationType)
	assert.Equal(t, "older", got.Connections[0].Direction)
	assert.Equal(t, "test", got.Connections[0].Relation.Reason)
	assert.Equal(t, "newer", got.Connections[1].Direction)

	assert.Len(t, getConnections(t, svc, n[2], "?direction=newer").Connections, 1)
	assert.Len(t, getConnections(t, svc, n[2], "?types=fixes,relates_to").Connections, 2)
	assert.Len(t, getConnections(t, svc, n[2], "?min_confidence=0.6").Connections, 2)
	limited := getConnections(t, svc, n[2], "?limit=1")
	assert.Len(t, limited.Connections, 1)
	assert.Equal(t, 3, limited.Total)

	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, n[0]).Error)
	assert.Equal(t, 2, getConnections(t, svc, n[2], "").Total, "a superseded note is not offered")
}

func TestConnectionsAPI_RefusesWhatItCannotAnswer(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	n := addNotes(t, svc, "proj_aaaaaa", 1)
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/observations/999999/connections", http.StatusNotFound},
		{"/api/observations/abc/connections", http.StatusBadRequest},
		{fmt.Sprintf("/api/observations/%d/connections?direction=sideways", n[0]), http.StatusBadRequest},
		{fmt.Sprintf("/api/observations/%d/connections?types=fixes,nonsense", n[0]), http.StatusBadRequest},
		{fmt.Sprintf("/api/observations/%d/connections?min_confidence=2", n[0]), http.StatusBadRequest},
		{fmt.Sprintf("/api/observations/%d/connections?limit=0", n[0]), http.StatusBadRequest},
		{fmt.Sprintf("/api/observations/%d/connections?limit=101", n[0]), http.StatusBadRequest},
	} {
		assert.Equal(t, tc.code, doRequest(t, svc, http.MethodGet, tc.path, nil).Code, tc.path)
	}
	assert.Contains(t, doRequest(t, svc, http.MethodGet, fmt.Sprintf("/api/observations/%d/connections?types=nonsense", n[0]), nil).Body.String(), "relates_to",
		"the error lists the valid types")
}

type relationTypesBody struct {
	Types []struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Count       int    `json:"count"`
		Automatic   bool   `json:"automatic"`
	} `json:"types"`
	Total int `json:"total"`
}

func getRelationTypes(t *testing.T, svc *Service, query string) relationTypesBody {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/relations/types"+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out relationTypesBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestRelationTypesAPI_ListsEveryTypeWithMeaningAndCount(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	a := addNotes(t, svc, "proj_aaaaaa", 3)
	b := addNotes(t, svc, "proj_bbbbbb", 2)
	storeRelation(t, svc, a[1], a[0], models.RelationRelatesTo, 0.7)
	storeRelation(t, svc, a[2], a[1], models.RelationFixes, 0.8)
	storeRelation(t, svc, b[1], b[0], models.RelationRelatesTo, 0.7)

	all := getRelationTypes(t, svc, "")
	require.Len(t, all.Types, len(models.AllRelationTypes))
	counts := map[string]int{}
	for _, ty := range all.Types {
		assert.NotEmpty(t, ty.Description, ty.Type)
		counts[ty.Type] = ty.Count
		assert.Equal(t, ty.Type != "supersedes" && ty.Type != "causes", ty.Automatic, ty.Type)
	}
	assert.Equal(t, 2, counts["relates_to"])
	assert.Equal(t, 1, counts["fixes"])
	assert.Zero(t, counts["supersedes"])
	assert.Equal(t, 3, all.Total)

	assert.Equal(t, 1, getRelationTypes(t, svc, "?project=proj_bbbbbb").Total)
	assert.Equal(t, 2, getRelationTypes(t, svc, fmt.Sprintf("?observation_id=%d", a[1])).Total)
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, "/api/relations/types?observation_id=x", nil).Code)
}

func TestRelatedAPI_HonoursLimitAndTypesAndLeavesOutSupersededNotes(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	n := addNotes(t, svc, "proj_aaaaaa", 5)
	storeRelation(t, svc, n[0], n[1], models.RelationRelatesTo, 0.9)
	storeRelation(t, svc, n[0], n[2], models.RelationFixes, 0.8)
	storeRelation(t, svc, n[0], n[3], models.RelationRelatesTo, 0.7)
	storeRelation(t, svc, n[0], n[4], models.RelationRelatesTo, 0.6)
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, n[4]).Error)

	related := func(query string) []struct {
		ID int64 `json:"id"`
	} {
		rec := doRequest(t, svc, http.MethodGet, fmt.Sprintf("/api/observations/%d/related%s", n[0], query), nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out []struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	assert.Len(t, related(""), 3, "the superseded note is not related any more")
	assert.Len(t, related("?limit=2"), 2, "limit is honoured")
	only := related("?types=fixes")
	require.Len(t, only, 1)
	assert.Equal(t, n[2], only[0].ID)
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, fmt.Sprintf("/api/observations/%d/related?types=bogus", n[0]), nil).Code)
}

func TestRelationGraphAPI_ReadsMaxDepthAndFiltersByTypeAndConfidence(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	n := addNotes(t, svc, "proj_aaaaaa", 4)
	storeRelation(t, svc, n[1], n[0], models.RelationRelatesTo, 0.9)
	storeRelation(t, svc, n[2], n[1], models.RelationFixes, 0.8)
	storeRelation(t, svc, n[3], n[2], models.RelationRelatesTo, 0.5)

	count := func(query string) int {
		rec := doRequest(t, svc, http.MethodGet, fmt.Sprintf("/api/observations/%d/graph%s", n[0], query), nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct {
			Relations []json.RawMessage `json:"relations"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return len(out.Relations)
	}
	assert.Equal(t, 2, count(""), "the default reaches two hops: the note's relation and the next one")
	assert.Equal(t, 1, count("?max_depth=1"), "the MCP tool sends max_depth")
	assert.Equal(t, 1, count("?depth=1"))
	assert.Equal(t, 3, count("?max_depth=3"))
	assert.Equal(t, 2, count("?max_depth=3&types=relates_to"))
	assert.Equal(t, 2, count("?max_depth=3&min_confidence=0.7"))
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, fmt.Sprintf("/api/observations/%d/graph?types=bogus", n[0]), nil).Code)
}
