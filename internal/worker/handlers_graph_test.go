package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

type graphBody struct {
	Nodes []struct {
		Title   string `json:"title"`
		Project string `json:"project"`
		ID      int64  `json:"id"`
		Degree  int    `json:"degree"`
	} `json:"nodes"`
	Edges []struct {
		Type       string  `json:"type"`
		ID         int64   `json:"id"`
		Source     int64   `json:"source"`
		Target     int64   `json:"target"`
		Confidence float64 `json:"confidence"`
	} `json:"edges"`
	TotalNodes int  `json:"total_nodes"`
	TotalEdges int  `json:"total_edges"`
	Truncated  bool `json:"truncated"`
}

func getGraph(t *testing.T, svc *Service, query string) graphBody {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/graph"+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var out graphBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func storeRelation(t *testing.T, svc *Service, source, target int64, typ models.RelationType, confidence float64) {
	t.Helper()
	require.NoError(t, svc.relationStore.StoreRelations(context.Background(), []*models.ObservationRelation{
		models.NewObservationRelation(source, target, typ, confidence, models.DetectionSourceEmbeddingSimilarity, "test"),
	}))
}

func TestGraphAPI_EmptyThenFilledAndFiltered(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()

	empty := getGraph(t, svc, "")
	assert.Empty(t, empty.Nodes)
	assert.Empty(t, empty.Edges)

	a := addNotes(t, svc, "proj_aaaaaa", 3)
	b := addNotes(t, svc, "proj_bbbbbb", 2)
	storeRelation(t, svc, a[1], a[0], models.RelationRelatesTo, 0.62)
	storeRelation(t, svc, a[2], a[1], models.RelationFixes, 0.85)
	storeRelation(t, svc, b[1], b[0], models.RelationRelatesTo, 0.7)

	all := getGraph(t, svc, "")
	assert.Len(t, all.Nodes, 5)
	assert.Len(t, all.Edges, 3)
	assert.Equal(t, 5, all.TotalNodes)
	assert.False(t, all.Truncated)

	one := getGraph(t, svc, "?project=proj_aaaaaa")
	assert.Len(t, one.Nodes, 3)
	assert.Len(t, one.Edges, 2)

	strong := getGraph(t, svc, "?min_confidence=0.8")
	require.Len(t, strong.Edges, 1)
	assert.Equal(t, "fixes", strong.Edges[0].Type)

	typed := getGraph(t, svc, "?types=relates_to,fixes")
	assert.Len(t, typed.Edges, 3)
	onlyFix := getGraph(t, svc, "?types=fixes")
	assert.Len(t, onlyFix.Edges, 1)

	small := getGraph(t, svc, "?max_nodes=2")
	assert.True(t, small.Truncated)
	assert.LessOrEqual(t, len(small.Nodes), 2)
}

func TestGraphAPI_Refusals(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	for _, q := range []string{"?min_confidence=2", "?min_confidence=x", "?max_nodes=0", "?max_nodes=99999", "?types=bogus", "?types=fixes,bogus", "?project=../etc"} {
		assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, "/api/graph"+q, nil).Code, q)
	}
}

func TestGraphAPI_HidesNotesAPersonSuperseded(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	ids := addNotes(t, svc, "proj_aaaaaa", 3)
	storeRelation(t, svc, ids[1], ids[0], models.RelationRelatesTo, 0.7)
	storeRelation(t, svc, ids[2], ids[0], models.RelationRelatesTo, 0.7)
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, ids[1]).Error)

	g := getGraph(t, svc, "")
	assert.Len(t, g.Edges, 1)
	assert.Len(t, g.Nodes, 2)
}

func TestGraphStatsAPI_RealNumbersAndWhetherItIsBuilding(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	ids := addNotes(t, svc, "proj_aaaaaa", 3)

	stats := func() map[string]any {
		rec := doRequest(t, svc, http.MethodGet, "/api/graph/stats", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	before := stats()
	assert.Equal(t, true, before["enabled"], "enabled means switched on, not that edges exist")
	assert.EqualValues(t, 0, before["edgeCount"])
	assert.EqualValues(t, 3, before["pending"])
	assert.Equal(t, "Building the graph from existing observations.", before["message"])

	storeRelation(t, svc, ids[1], ids[0], models.RelationRelatesTo, 0.7)
	storeRelation(t, svc, ids[2], ids[0], models.RelationRelatesTo, 0.7)
	after := stats()
	assert.EqualValues(t, 2, after["edgeCount"])
	assert.EqualValues(t, 3, after["nodeCount"], "counted, not estimated")
	assert.EqualValues(t, 2, after["maxDegree"])
	assert.NotContains(t, after, "message")
	assert.EqualValues(t, 2, after["edgeTypes"].(map[string]any)["relates_to"])
	cfg := after["config"].(map[string]any)
	assert.InDelta(t, 0.6, cfg["minSimilarity"], 0.0001)
	assert.EqualValues(t, 3, cfg["maxPerNote"])
}

func TestGraphStatsAPI_SwitchedOff(t *testing.T) {
	svc, _, cleanup := graphService(t, func(c *config.Config) { c.GraphEnabled = false })
	defer cleanup()
	rec := doRequest(t, svc, http.MethodGet, "/api/graph/stats", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, false, out["enabled"])
	assert.Contains(t, out["message"], "switched off")
}

func TestRebuildRelationsAPI(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	ids := addNotes(t, svc, "proj_aaaaaa", 3)
	svc.runRelationPass(context.Background())
	require.NotEmpty(t, relationsOf(t, svc))

	rec := doRequest(t, svc, http.MethodPost, "/api/relations/rebuild", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.EqualValues(t, 3, out.Deleted)
	assert.Empty(t, relationsOf(t, svc))
	pending, err := svc.relationStore.CountUncheckedForRelations(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, len(ids), pending, "everything is waiting again")

	svc.runRelationPass(context.Background())
	assert.Len(t, relationsOf(t, svc), 3, "and the next pass builds it again")
}
