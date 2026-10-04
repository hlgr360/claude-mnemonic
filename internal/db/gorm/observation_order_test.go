//go:build fts5

package gorm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// ids lists the ids of observations in the order they were returned.
func ids(obs []*models.Observation) []int64 {
	out := make([]int64, len(obs))
	for i, o := range obs {
		out[i] = o.ID
	}
	return out
}

// spreadTimes gives the seeded notes distinct creation times in id order: notes stored within one millisecond share a
// timestamp, and the order among equals is then not defined.
func spreadTimes(t *testing.T, store *Store) {
	t.Helper()
	require.NoError(t, store.DB.Exec(`UPDATE observations SET created_at_epoch = 1700000000000 + id * 1000`).Error)
}

func TestParseObservationOrder(t *testing.T) {
	for value, want := range map[string]ObservationOrder{"": OrderByImportance, "importance": OrderByImportance, "date": OrderByDate} {
		got, err := ParseObservationOrder(value)
		require.NoError(t, err, value)
		assert.Equal(t, want, got, value)
	}
	for _, bad := range []string{"newest", "Date", "importance desc", "id"} {
		_, err := ParseObservationOrder(bad)
		assert.Error(t, err, bad)
	}
}

// A note that has just been saved starts at importance 1, behind every older note that has earned a higher score. The
// importance order (the default) leaves it off the first page; the date order puts it first.
func TestObservationOrder_ANewLowScoreNoteIsFirstByDateAndBuriedByImportance(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	seedProject(t, store, obsStore, "proj_aaaaaa", 5, 0, 0) // ids 1..5, saved in this order
	seedProject(t, store, obsStore, "proj_bbbbbb", 1, 0, 0) // id 6, the newest, importance 1
	spreadTimes(t, store)
	for id := int64(1); id <= 5; id++ {
		require.NoError(t, obsStore.UpdateImportanceScore(ctx, id, 1.2))
	}

	// All projects, a first page of 3.
	byImportance, total, err := obsStore.GetAllRecentObservationsPaginated(ctx, 3, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 6, total)
	for _, o := range byImportance {
		assert.NotEqual(t, "proj_bbbbbb note 0", o.Title.String, "the importance order buries the fresh note")
	}

	byDate, _, err := obsStore.GetAllRecentObservationsOrdered(ctx, 3, 0, OrderByDate)
	require.NoError(t, err)
	require.Len(t, byDate, 3)
	assert.Equal(t, "proj_bbbbbb note 0", byDate[0].Title.String, "the date order lists the fresh note first")
	assert.Equal(t, []int64{6, 5, 4}, []int64{byDate[0].ID, byDate[1].ID, byDate[2].ID}, "newest first")
}

func TestObservationOrder_OneProjectByDateAndByImportance(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	seedProject(t, store, obsStore, "proj_aaaaaa", 4, 0, 0) // ids 1..4
	spreadTimes(t, store)
	require.NoError(t, obsStore.UpdateImportanceScore(ctx, 1, 2.0))
	require.NoError(t, obsStore.UpdateImportanceScore(ctx, 2, 1.5))

	imp, _, err := obsStore.GetObservationsByProjectStrictOrdered(ctx, "proj_aaaaaa", 10, 0, OrderByImportance)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 4, 3}, ids(imp), "importance first, the newest first among equals")

	date, _, err := obsStore.GetObservationsByProjectStrictOrdered(ctx, "proj_aaaaaa", 10, 0, OrderByDate)
	require.NoError(t, err)
	assert.Equal(t, []int64{4, 3, 2, 1}, ids(date))

	// Only that project's notes, whatever the order.
	other, _, err := obsStore.GetObservationsByProjectStrictOrdered(ctx, "proj_nothing", 10, 0, OrderByDate)
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestObservationOrder_PagesByDateDoNotOverlapAndCoverEverything(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	seedProject(t, store, obsStore, "proj_aaaaaa", 7, 0, 0)

	var seen []int64
	for offset := 0; offset < 7; offset += 3 {
		page, _, err := obsStore.GetAllRecentObservationsOrdered(ctx, 3, offset, OrderByDate)
		require.NoError(t, err)
		seen = append(seen, ids(page)...)
	}
	assert.Equal(t, []int64{7, 6, 5, 4, 3, 2, 1}, seen)
}

// The existing methods keep their meaning: most important first.
func TestObservationOrder_TheOldMethodsStillOrderByImportance(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	seedProject(t, store, obsStore, "proj_aaaaaa", 3, 0, 0)
	spreadTimes(t, store)
	require.NoError(t, obsStore.UpdateImportanceScore(ctx, 1, 2.0))

	all, _, err := obsStore.GetAllRecentObservationsPaginated(ctx, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 3, 2}, ids(all))
	one, _, err := obsStore.GetObservationsByProjectStrictPaginated(ctx, "proj_aaaaaa", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 3, 2}, ids(one))
}
