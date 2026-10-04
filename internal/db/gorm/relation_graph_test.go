//go:build fts5

package gorm

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// graphFixture stores n observations in a project and returns their ids, oldest first.
func graphFixture(t *testing.T, store *Store, obsStore *ObservationStore, project string, n int) []int64 {
	t.Helper()
	ctx := context.Background()
	sid, err := NewSessionStore(store).CreateSDKSession(ctx, "claude-"+project, project, "")
	require.NoError(t, err)
	var ids []int64
	for i := 0; i < n; i++ {
		id, _, err := obsStore.StoreObservation(ctx, "claude-"+project, project, &models.ParsedObservation{
			Type: models.ObsTypeDiscovery, Title: fmt.Sprintf("%s note %d", project, i), Narrative: fmt.Sprintf("distinct narrative %s %d", project, i),
		}, int(sid), 1)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	return ids
}

func relate(t *testing.T, rs *RelationStore, source, target int64, typ models.RelationType, confidence float64) {
	t.Helper()
	require.NoError(t, rs.StoreRelations(context.Background(), []*models.ObservationRelation{
		models.NewObservationRelation(source, target, typ, confidence, models.DetectionSourceEmbeddingSimilarity, "test"),
	}))
}

func nodeIDs(g *GraphData) []int64 {
	var ids []int64
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

func TestGraph_NodesAreTheObservationsThatHaveRelations(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	ids := graphFixture(t, store, obsStore, "proj_aaaaaa", 4)
	relate(t, rs, ids[1], ids[0], models.RelationRelatesTo, 0.7)
	relate(t, rs, ids[2], ids[1], models.RelationFixes, 0.8)

	g, err := rs.Graph(context.Background(), GraphFilter{})
	require.NoError(t, err)
	assert.ElementsMatch(t, ids[:3], nodeIDs(g), "the unrelated fourth note is not in the graph")
	assert.Len(t, g.Edges, 2)
	assert.Equal(t, 3, g.TotalNodes)
	assert.Equal(t, 2, g.TotalEdges)
	assert.False(t, g.Truncated)
	byID := map[int64]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	assert.Equal(t, 2, byID[ids[1]].Degree, "the middle note has both relations")
	assert.Equal(t, 1, byID[ids[0]].Degree)
	assert.Equal(t, "proj_aaaaaa note 1", byID[ids[1]].Title)
	assert.Equal(t, "proj_aaaaaa", byID[ids[1]].Project)
}

func TestGraph_Filters(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	a := graphFixture(t, store, obsStore, "proj_aaaaaa", 3)
	b := graphFixture(t, store, obsStore, "proj_bbbbbb", 2)
	relate(t, rs, a[1], a[0], models.RelationRelatesTo, 0.62)
	relate(t, rs, a[2], a[1], models.RelationFixes, 0.85)
	relate(t, rs, b[1], b[0], models.RelationRelatesTo, 0.7)
	ctx := context.Background()

	g, _ := rs.Graph(ctx, GraphFilter{Project: "proj_aaaaaa"})
	assert.ElementsMatch(t, a, nodeIDs(g), "one project")

	g, _ = rs.Graph(ctx, GraphFilter{MinConfidence: 0.8})
	assert.Len(t, g.Edges, 1, "a confidence floor")
	assert.Equal(t, models.RelationFixes, g.Edges[0].Type)

	g, _ = rs.Graph(ctx, GraphFilter{Types: []models.RelationType{models.RelationRelatesTo}})
	assert.Len(t, g.Edges, 2, "a type filter")

	g, _ = rs.Graph(ctx, GraphFilter{Project: "proj_nothing"})
	assert.Empty(t, g.Nodes)
	assert.Empty(t, g.Edges)
	assert.NotNil(t, g.Nodes, "an empty graph is an empty list, not null")
}

func TestGraph_LeavesOutSupersededArchivedAndDeletedObservations(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	ids := graphFixture(t, store, obsStore, "proj_aaaaaa", 5)
	relate(t, rs, ids[1], ids[0], models.RelationRelatesTo, 0.7)
	relate(t, rs, ids[2], ids[0], models.RelationRelatesTo, 0.7)
	relate(t, rs, ids[3], ids[0], models.RelationRelatesTo, 0.7)
	relate(t, rs, ids[4], ids[0], models.RelationRelatesTo, 0.7)
	ctx := context.Background()
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, ids[1]).Error)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = ?`, ids[2]).Error)
	require.NoError(t, store.DB.Exec(`DELETE FROM observations WHERE id = ?`, ids[3]).Error)

	g, err := rs.Graph(ctx, GraphFilter{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{ids[0], ids[4]}, nodeIDs(g), "only the live pair is left")
	assert.Len(t, g.Edges, 1)

	st, err := rs.GraphStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 1, st.Edges)
	assert.Equal(t, 2, st.Nodes)

	// Undoing the decision brings the relation back.
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_superseded = 0 WHERE id = ?`, ids[1]).Error)
	g, _ = rs.Graph(ctx, GraphFilter{})
	assert.Len(t, g.Edges, 2)
}

func TestGraph_TooManyNodesKeepsTheBestConnected(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	ids := graphFixture(t, store, obsStore, "proj_aaaaaa", 7)
	// a hub (ids[0]) related to five notes, and a separate pair
	for _, i := range []int{1, 2, 3, 4, 5} {
		relate(t, rs, ids[i], ids[0], models.RelationRelatesTo, 0.7)
	}
	relate(t, rs, ids[6], ids[5], models.RelationRelatesTo, 0.7)

	g, err := rs.Graph(context.Background(), GraphFilter{MaxNodes: 3})
	require.NoError(t, err)
	assert.True(t, g.Truncated)
	assert.Equal(t, 7, g.TotalNodes)
	assert.Equal(t, 6, g.TotalEdges)
	assert.Len(t, g.Nodes, 3)
	assert.Contains(t, nodeIDs(g), ids[0], "the hub is kept")
	for _, e := range g.Edges {
		assert.Contains(t, nodeIDs(g), e.Source, "no edge leaves the node set")
		assert.Contains(t, nodeIDs(g), e.Target)
	}
}

func TestGraphStats_RealNumbers(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	ctx := context.Background()

	empty, err := rs.GraphStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 0, empty.Edges)
	assert.Equal(t, 0, empty.Nodes)
	assert.Len(t, empty.EdgeTypes, len(models.AllRelationTypes), "every type is listed, with zero")

	ids := graphFixture(t, store, obsStore, "proj_aaaaaa", 4)
	relate(t, rs, ids[1], ids[0], models.RelationRelatesTo, 0.7)
	relate(t, rs, ids[2], ids[0], models.RelationRelatesTo, 0.7)
	relate(t, rs, ids[3], ids[0], models.RelationFixes, 0.7)
	st, err := rs.GraphStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 3, st.Edges)
	assert.Equal(t, 4, st.Nodes, "counted, not estimated from the edges")
	assert.Equal(t, 3, st.MaxDegree)
	assert.Equal(t, 1, st.MinDegree)
	assert.InDelta(t, 1.5, st.AvgDegree, 0.001)
	assert.InDelta(t, 1.0, st.MedianDegree, 0.001)
	assert.Equal(t, 2, st.EdgeTypes[models.RelationRelatesTo])
	assert.Equal(t, 1, st.EdgeTypes[models.RelationFixes])

	none, err := rs.GraphStats(ctx, "proj_other")
	require.NoError(t, err)
	assert.Equal(t, 0, none.Edges)
}

func TestRelationChecks_UncheckedMarkAndReset(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	ctx := context.Background()
	ids := graphFixture(t, store, obsStore, "proj_aaaaaa", 3)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, ids[0]).Error)

	pending, err := rs.CountUncheckedForRelations(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, pending, "a superseded note is not looked at")
	batch, err := rs.UncheckedForRelations(ctx, 10)
	require.NoError(t, err)
	require.Len(t, batch, 2)
	assert.Equal(t, ids[2], batch[0].ID, "newest first")

	require.NoError(t, rs.MarkRelationChecked(ctx, ids[2], 1))
	require.NoError(t, rs.MarkRelationChecked(ctx, ids[2], 2), "marking again is fine")
	batch, _ = rs.UncheckedForRelations(ctx, 1)
	require.Len(t, batch, 1, "the limit applies")
	assert.Equal(t, ids[1], batch[0].ID)

	relate(t, rs, ids[2], ids[1], models.RelationRelatesTo, 0.7)
	deleted, err := rs.ResetRelations(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)
	pending, _ = rs.CountUncheckedForRelations(ctx)
	assert.EqualValues(t, 2, pending, "after a reset every live observation is waiting again")
	total, _ := rs.GetTotalRelationCount(ctx)
	assert.Zero(t, total)
}

func TestRelationChecksTableIsMigrated(t *testing.T) {
	_, store, cleanup := testObservationStore(t)
	defer cleanup()
	assert.True(t, store.DB.Migrator().HasTable(&RelationCheck{}))
}
