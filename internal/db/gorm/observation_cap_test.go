//go:build fts5

package gorm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// storeNotes stores n notes in a project, a few milliseconds apart so their creation times differ, and returns their ids
// oldest first.
func storeNotes(t *testing.T, s *ObservationStore, project string, n int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		id, _, err := s.StoreObservation(context.Background(), "claude-1", project,
			&models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: "Note"}, i, 10)
		require.NoError(t, err)
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}
	return ids
}

func countNotes(t *testing.T, store *Store, project string) (live, archived int64) {
	t.Helper()
	require.NoError(t, store.DB.Model(&Observation{}).Where("project = ? AND COALESCE(is_archived, 0) = 0", project).Count(&live).Error)
	require.NoError(t, store.DB.Model(&Observation{}).Where("project = ? AND is_archived = 1", project).Count(&archived).Error)
	return live, archived
}

func TestCap_ByDefaultThereIsNoCapAndNothingIsRemoved(t *testing.T) {
	s, store, cleanup := testObservationStore(t)
	defer cleanup()

	var mu sync.Mutex
	called := false
	s.cleanupFunc = func(context.Context, []int64) { mu.Lock(); called = true; mu.Unlock() }

	assert.Equal(t, 0, s.MaxObservationsPerProject(), "the default is no cap")
	storeNotes(t, s, "proj", 105)
	time.Sleep(200 * time.Millisecond) // the cleanup worker has had its turn

	live, archived := countNotes(t, store, "proj")
	assert.EqualValues(t, 105, live, "every note stays; the old behaviour kept 100 and deleted the rest")
	assert.EqualValues(t, 0, archived)
	mu.Lock()
	defer mu.Unlock()
	assert.False(t, called, "no cleanup ran, so no vectors were dropped")
}

func TestCap_ACapArchivesTheOldestAndKeepsEverything(t *testing.T) {
	s, store, cleanup := testObservationStore(t)
	defer cleanup()

	var mu sync.Mutex
	var dropped []int64
	s.cleanupFunc = func(_ context.Context, ids []int64) { mu.Lock(); dropped = append(dropped, ids...); mu.Unlock() }
	s.SetMaxObservationsPerProject(100)

	ids := storeNotes(t, s, "proj", 105)
	require.Eventually(t, func() bool { _, a := countNotes(t, store, "proj"); return a == 5 }, 2*time.Second, 20*time.Millisecond)

	live, archived := countNotes(t, store, "proj")
	assert.EqualValues(t, 100, live)
	assert.EqualValues(t, 5, archived)
	var total int64
	require.NoError(t, store.DB.Model(&Observation{}).Where("project = ?", "proj").Count(&total).Error)
	assert.EqualValues(t, 105, total, "nothing was deleted")

	var rows []Observation
	require.NoError(t, store.DB.Where("project = ? AND is_archived = 1", "proj").Find(&rows).Error)
	got := make([]int64, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.ID)
		assert.Equal(t, ArchivedByCapReason, r.ArchivedReason.String, "the reason says why")
	}
	assert.ElementsMatch(t, ids[:5], got, "the five oldest were archived")

	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, ids[:5], dropped, "their vectors are dropped so search cannot return them")
}

func TestArchiveBeyondLimit_CountsOnlyLiveNotesIsRepeatableAndLeavesOtherProjectsAlone(t *testing.T) {
	s, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	mine := storeNotes(t, s, "mine", 6)
	storeNotes(t, s, "other", 6)
	// A note that is already archived, and one that is superseded, do not count towards the cap.
	require.NoError(t, s.ArchiveObservation(ctx, mine[0], "by hand"))
	require.NoError(t, store.DB.Model(&Observation{}).Where("id = ?", mine[1]).Update("is_superseded", 1).Error)

	archived, err := s.ArchiveBeyondLimit(ctx, "mine", 3)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{mine[2]}, archived, "four live notes (ids 2..5), a cap of three: only the oldest live one goes")

	again, err := s.ArchiveBeyondLimit(ctx, "mine", 3)
	require.NoError(t, err)
	assert.Empty(t, again, "a second pass has nothing to do")

	live, _ := countNotes(t, store, "other")
	assert.EqualValues(t, 6, live, "another project is untouched")

	none, err := s.ArchiveBeyondLimit(ctx, "mine", 0)
	require.NoError(t, err)
	assert.Empty(t, none, "a limit of zero means no cap, not 'archive everything'")
}

func TestCap_AnArchivedNoteCanBeRestored(t *testing.T) {
	s, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	ids := storeNotes(t, s, "proj", 4)
	archived, err := s.ArchiveBeyondLimit(ctx, "proj", 2)
	require.NoError(t, err)
	require.Len(t, archived, 2)

	require.NoError(t, s.UnarchiveObservation(ctx, ids[0]))
	live, arch := countNotes(t, store, "proj")
	assert.EqualValues(t, 3, live)
	assert.EqualValues(t, 1, arch)
}

func TestArchivedNotesAreKeptButOutOfSearchListsCountsAndTheVectorRebuild(t *testing.T) {
	s, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	var ids []int64
	for _, title := range []string{"Zebra ledger reconciliation", "Quokka deployment checklist", "Ocelot retry budget"} {
		id, _, err := s.StoreObservation(ctx, "claude-1", "proj",
			&models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: title, Narrative: title + " notes"}, 1, 10)
		require.NoError(t, err)
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}
	archivedID := ids[0] // the zebra note
	require.NoError(t, s.ArchiveObservation(ctx, archivedID, "test"))

	has := func(obs []*models.Observation) bool {
		for _, o := range obs {
			if o.ID == archivedID {
				return true
			}
		}
		return false
	}

	recent, err := s.GetRecentObservations(ctx, "proj", 10)
	require.NoError(t, err)
	assert.False(t, has(recent), "context injection does not see it")

	strict, err := s.GetObservationsByProjectStrict(ctx, "proj", 10)
	require.NoError(t, err)
	assert.False(t, has(strict))

	all, err := s.GetAllRecentObservations(ctx, 10)
	require.NoError(t, err)
	assert.False(t, has(all))

	byID, err := s.GetObservationsByIDs(ctx, ids, "default", 0)
	require.NoError(t, err)
	assert.False(t, has(byID), "a vector hit for it is dropped when it is loaded")
	assert.Len(t, byID, 2)

	ordered, err := s.GetObservationsByIDsPreserveOrder(ctx, ids)
	require.NoError(t, err)
	assert.False(t, has(ordered))

	fts, err := s.SearchObservationsFTS(ctx, "zebra ledger", "proj", 10)
	require.NoError(t, err)
	assert.False(t, has(fts), "full-text search does not find it")
	other, err := s.SearchObservationsFTS(ctx, "quokka deployment", "proj", 10)
	require.NoError(t, err)
	assert.Len(t, other, 1, "and still finds the others")

	n, err := s.GetObservationCount(ctx, "proj")
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the status line counts live notes")

	rebuild, err := s.GetAllObservations(ctx)
	require.NoError(t, err)
	assert.False(t, has(rebuild), "a vector rebuild does not put it back into the index")

	listed, total, err := s.GetAllRecentObservationsOrdered(ctx, 10, 0, OrderByDate, false)
	require.NoError(t, err)
	assert.False(t, has(listed))
	assert.EqualValues(t, 2, total)
	withArchived, total, err := s.GetAllRecentObservationsOrdered(ctx, 10, 0, OrderByDate, true)
	require.NoError(t, err)
	assert.True(t, has(withArchived), "an export asks for them")
	assert.EqualValues(t, 3, total)

	// Kept: the row is still there, and restoring it makes it visible again.
	one, err := s.GetObservationByID(ctx, archivedID)
	require.NoError(t, err)
	require.NotNil(t, one)
	require.NoError(t, s.UnarchiveObservation(ctx, archivedID))
	back, err := s.SearchObservationsFTS(ctx, "zebra ledger", "proj", 10)
	require.NoError(t, err)
	assert.True(t, has(back))
	var rows int64
	require.NoError(t, store.DB.Model(&Observation{}).Where("project = ?", "proj").Count(&rows).Error)
	assert.EqualValues(t, 3, rows)
}

func TestCap_ASnapshotIsTakenBeforeNotesAreArchivedAndOnlyThen(t *testing.T) {
	s, store, cleanup := testObservationStore(t)
	defer cleanup()

	var mu sync.Mutex
	var calls []string
	var liveWhenCalled []int64
	s.SetBeforeArchive(func(_ context.Context, reason string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, reason)
		live, _ := countNotes(t, store, "proj")
		liveWhenCalled = append(liveWhenCalled, live)
	})
	s.SetMaxObservationsPerProject(10)

	storeNotes(t, s, "proj", 10)
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	assert.Empty(t, calls, "at the cap, nothing is archived, so nothing is snapshotted")
	mu.Unlock()

	storeNotes(t, s, "proj", 1)
	require.Eventually(t, func() bool { _, a := countNotes(t, store, "proj"); return a == 1 }, 2*time.Second, 20*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"cap"}, calls, "once, with the reason")
	assert.Equal(t, []int64{11}, liveWhenCalled, "it ran before the archive: all eleven notes were still live")
}
