//go:build fts5

package gorm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func connectionIDs(cs []Connection) []int64 {
	var ids []int64
	for _, c := range cs {
		ids = append(ids, c.Note.ID)
	}
	return ids
}

func TestConnections_DirectionTypeConfidenceAndOrder(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	ctx := context.Background()
	n := graphFixture(t, store, obsStore, "proj_aaaaaa", 5) // n[0] oldest
	// n[2] is the note asked about: it fixes n[0] and evolves from n[1]; n[3] relates to it and n[4] depends on it.
	relate(t, rs, n[2], n[0], models.RelationFixes, 0.9)
	relate(t, rs, n[2], n[1], models.RelationEvolvesFrom, 0.5)
	relate(t, rs, n[3], n[2], models.RelationRelatesTo, 0.7)
	relate(t, rs, n[4], n[2], models.RelationDependsOn, 0.6)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET concepts = '["queues","retries"]' WHERE id = ?`, n[0]).Error)

	all, total, err := rs.Connections(ctx, n[2], ConnectionFilter{})
	require.NoError(t, err)
	assert.Equal(t, 4, total)
	assert.Equal(t, []int64{n[0], n[3], n[4], n[1]}, connectionIDs(all), "the most certain relation first")
	first := all[0]
	assert.Equal(t, models.RelationFixes, first.Relation.RelationType)
	assert.InDelta(t, 0.9, first.Relation.Confidence, 1e-9)
	assert.Equal(t, "test", first.Relation.Reason)
	assert.Equal(t, DirectionOlder, first.Direction, "the note asked about is the source: the other one is older")
	assert.Equal(t, "proj_aaaaaa note 0", first.Note.Title)
	assert.Equal(t, "proj_aaaaaa", first.Note.Project)
	assert.Equal(t, []string{"queues", "retries"}, first.Note.Concepts)
	assert.NotZero(t, first.Note.CreatedAtEpoch)
	assert.Equal(t, DirectionNewer, all[1].Direction)

	older, total, err := rs.Connections(ctx, n[2], ConnectionFilter{Direction: DirectionOlder})
	require.NoError(t, err)
	assert.Equal(t, []int64{n[0], n[1]}, connectionIDs(older))
	assert.Equal(t, 2, total)
	newer, _, err := rs.Connections(ctx, n[2], ConnectionFilter{Direction: DirectionNewer})
	require.NoError(t, err)
	assert.Equal(t, []int64{n[3], n[4]}, connectionIDs(newer))

	typed, _, err := rs.Connections(ctx, n[2], ConnectionFilter{Types: []models.RelationType{models.RelationFixes, models.RelationDependsOn}})
	require.NoError(t, err)
	assert.Equal(t, []int64{n[0], n[4]}, connectionIDs(typed))

	sure, total, err := rs.Connections(ctx, n[2], ConnectionFilter{MinConfidence: 0.65})
	require.NoError(t, err)
	assert.Equal(t, []int64{n[0], n[3]}, connectionIDs(sure))
	assert.Equal(t, 2, total)

	limited, total, err := rs.Connections(ctx, n[2], ConnectionFilter{Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, []int64{n[0]}, connectionIDs(limited))
	assert.Equal(t, 4, total, "the total is not limited")

	none, total, err := rs.Connections(ctx, n[0]+1000, ConnectionFilter{})
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.Zero(t, total)
	assert.NotNil(t, none, "an empty answer is a list, not null")
}

func TestConnections_LeaveOutSupersededAndArchivedNotes(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	ctx := context.Background()
	n := graphFixture(t, store, obsStore, "proj_aaaaaa", 4)
	relate(t, rs, n[1], n[0], models.RelationRelatesTo, 0.7)
	relate(t, rs, n[2], n[1], models.RelationRelatesTo, 0.7)
	relate(t, rs, n[3], n[1], models.RelationRelatesTo, 0.7)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, n[2]).Error)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = ?`, n[3]).Error)

	got, total, err := rs.Connections(ctx, n[1], ConnectionFilter{})
	require.NoError(t, err)
	assert.Equal(t, []int64{n[0]}, connectionIDs(got), "a note a person superseded or that is archived is not offered")
	assert.Equal(t, 1, total)

	details, err := rs.GetRelationsWithDetails(ctx, n[1])
	require.NoError(t, err)
	assert.Len(t, details, 1, "the relations listing leaves them out too")
}

func TestRelationTypeCounts_WholeGraphProjectAndNote(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	rs := NewRelationStore(store)
	ctx := context.Background()
	a := graphFixture(t, store, obsStore, "proj_aaaaaa", 3)
	b := graphFixture(t, store, obsStore, "proj_bbbbbb", 2)
	relate(t, rs, a[1], a[0], models.RelationRelatesTo, 0.7)
	relate(t, rs, a[2], a[1], models.RelationFixes, 0.8)
	relate(t, rs, a[2], a[0], models.RelationRelatesTo, 0.6)
	relate(t, rs, b[1], b[0], models.RelationRelatesTo, 0.7)

	byType := func(counts []RelationTypeCount) map[models.RelationType]int {
		m := map[models.RelationType]int{}
		for _, c := range counts {
			m[c.Type] = c.Count
		}
		return m
	}

	all, err := rs.RelationTypeCounts(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, all, len(models.AllRelationTypes), "every type is listed")
	assert.Equal(t, 3, byType(all)[models.RelationRelatesTo])
	assert.Equal(t, 1, byType(all)[models.RelationFixes])
	assert.Zero(t, byType(all)[models.RelationSupersedes])

	project, err := rs.RelationTypeCounts(ctx, "proj_bbbbbb", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, byType(project)[models.RelationRelatesTo])
	assert.Zero(t, byType(project)[models.RelationFixes])

	note, err := rs.RelationTypeCounts(ctx, "", a[1])
	require.NoError(t, err)
	assert.Equal(t, 1, byType(note)[models.RelationRelatesTo], "around one note: only its own relations")
	assert.Equal(t, 1, byType(note)[models.RelationFixes])
}

func TestRelationTypeInfos_DescribeEveryTypeAndMarkTheAutomaticOnes(t *testing.T) {
	seen := map[models.RelationType]bool{}
	for _, info := range models.RelationTypeInfos {
		assert.NotEmpty(t, info.Description, info.Type)
		seen[info.Type] = true
	}
	for _, typ := range models.AllRelationTypes {
		assert.True(t, seen[typ], "%s has a description", typ)
	}
	auto := map[models.RelationType]bool{}
	for _, info := range models.RelationTypeInfos {
		auto[info.Type] = info.Automatic
	}
	assert.False(t, auto[models.RelationSupersedes], "replacing a note is a person's decision, never automatic")
	assert.False(t, auto[models.RelationCauses])
	assert.True(t, auto[models.RelationRelatesTo])
}
