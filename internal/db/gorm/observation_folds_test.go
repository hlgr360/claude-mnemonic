//go:build fts5

package gorm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const dayMs = int64(24 * 60 * 60 * 1000)

// foldNote stores a note and dates it daysAgo days back.
func foldNote(t *testing.T, s *ObservationStore, project, title string, typ models.ObservationType, scope models.ObservationScope, daysAgo int) int64 {
	t.Helper()
	id, _, err := s.StoreObservation(context.Background(), "sess-"+project, project,
		&models.ParsedObservation{Type: typ, Title: title, Narrative: title + " narrative", Scope: scope}, 1, 1)
	require.NoError(t, err)
	require.NoError(t, s.SetObservationCreated(context.Background(), id, time.Now().UnixMilli()-int64(daysAgo)*dayMs))
	return id
}

func idsOf(obs []*models.Observation) []int64 {
	out := make([]int64, 0, len(obs))
	for _, o := range obs {
		out = append(out, o.ID)
	}
	return out
}

func TestRollupCandidates_ProtectionsAgeAndNewest(t *testing.T) {
	s, _, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	cutoff := time.Now().UnixMilli() - 60*dayMs

	// Old and plain: the candidates (oldest first).
	a := foldNote(t, s, "p", "old a", models.ObsTypeDiscovery, "", 200)
	b := foldNote(t, s, "p", "old b", models.ObsTypeBugfix, "", 150)
	c := foldNote(t, s, "p", "old c", models.ObsTypeChange, "", 100)
	// Old but protected.
	decision := foldNote(t, s, "p", "decision", models.ObsTypeDecision, "", 120)
	rated := foldNote(t, s, "p", "rated", models.ObsTypeDiscovery, "", 120)
	require.NoError(t, s.UpdateObservationFeedback(ctx, rated, 1))
	handScoped := foldNote(t, s, "p", "hand scoped", models.ObsTypeDiscovery, models.ScopeProject, 120) // a scope given is explicit
	global := foldNote(t, s, "p", "global", models.ObsTypeDiscovery, models.ScopeGlobal, 120)
	// Old but not live.
	superseded := foldNote(t, s, "p", "superseded", models.ObsTypeDiscovery, "", 120)
	require.NoError(t, s.MarkAsSuperseded(ctx, superseded))
	archivedByPerson := foldNote(t, s, "p", "archived by a person", models.ObsTypeDiscovery, "", 120)
	require.NoError(t, s.ArchiveObservation(ctx, archivedByPerson, "not useful"))
	// Archived by the cap: hidden, and a roll-up may bring it back into search.
	capped := foldNote(t, s, "p", "archived by the cap", models.ObsTypeDiscovery, "", 20)
	require.NoError(t, s.ArchiveObservation(ctx, capped, ArchivedByCapReason))
	// Recent, and another project.
	recent := foldNote(t, s, "p", "recent", models.ObsTypeDiscovery, "", 5)
	other := foldNote(t, s, "q", "other project", models.ObsTypeDiscovery, "", 200)
	// A roll-up is never rolled up again.
	rollup, _, err := s.StoreObservation(ctx, "sess-p", "p", &models.ParsedObservation{
		Type: models.ObsTypeDiscovery, Title: "a roll-up", Concepts: []string{RollupConcept}}, 1, 1)
	require.NoError(t, err)
	require.NoError(t, s.SetObservationCreated(ctx, rollup, time.Now().UnixMilli()-300*dayMs))

	got, err := s.RollupCandidates(ctx, "p", cutoff, 0)
	require.NoError(t, err)
	// The oldest note of a roll-up's range is 300 days back, but the roll-up itself is protected.
	assert.Equal(t, []int64{a, b, c, capped}, idsOf(got), "old plain notes and the cap's, oldest first")
	for _, id := range []int64{decision, rated, handScoped, global, superseded, archivedByPerson, recent, other, rollup} {
		assert.NotContains(t, idsOf(got), id)
	}

	t.Run("the newest notes stay out, whatever their age", func(t *testing.T) {
		// Of the live notes of the project, the newest two are the recent one and the capped... (live only): recent, c.
		kept, err := s.RollupCandidates(ctx, "p", time.Now().UnixMilli()+dayMs, 2)
		require.NoError(t, err)
		assert.NotContains(t, idsOf(kept), recent)
		assert.Contains(t, idsOf(kept), a)
	})
}

func TestProtectedFromFolding(t *testing.T) {
	s, _, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	plain := foldNote(t, s, "p", "plain", models.ObsTypeDiscovery, "", 1)
	decision := foldNote(t, s, "p", "decision", models.ObsTypeDecision, "", 1)
	rated := foldNote(t, s, "p", "rated", models.ObsTypeDiscovery, "", 1)
	require.NoError(t, s.UpdateObservationFeedback(ctx, rated, -1))

	got, err := s.ProtectedFromFolding(ctx, []int64{plain, decision, rated, 9999})
	require.NoError(t, err)
	assert.Equal(t, map[int64]bool{decision: true, rated: true}, got)

	none, err := s.ProtectedFromFolding(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestArchiveIntoAndUnarchiveWithReason(t *testing.T) {
	s, _, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	a := foldNote(t, s, "p", "a", models.ObsTypeDiscovery, "", 90)
	b := foldNote(t, s, "p", "b", models.ObsTypeDiscovery, "", 90)
	byPerson := foldNote(t, s, "p", "c", models.ObsTypeDiscovery, "", 90)
	alreadyFolded := foldNote(t, s, "p", "d", models.ObsTypeDiscovery, "", 90)
	require.NoError(t, s.ArchiveObservation(ctx, alreadyFolded, FoldReason(FoldRollup, 7)))

	reason := FoldReason(FoldRollup, 42)
	assert.Equal(t, "rolled-up into #42", reason)
	assert.Equal(t, "consolidated into #42", FoldReason(FoldConsolidation, 42))

	done, err := s.ArchiveInto(ctx, []int64{a, b, byPerson, alreadyFolded}, reason)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{a, b, byPerson}, done, "a note that is already folded elsewhere is left alone")

	live, err := s.GetObservationCount(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, 0, live, "folded notes are hidden")

	// A person archived one of them again for another reason in the meantime.
	require.NoError(t, s.ArchiveObservation(ctx, byPerson, "my own reason"))

	back, err := s.UnarchiveWithReason(ctx, []int64{a, b, byPerson, alreadyFolded}, reason)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{a, b}, back, "only notes archived for exactly this fold come back")
	live, err = s.GetObservationCount(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, 2, live)

	none, err := s.ArchiveInto(ctx, nil, reason)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestObservationFoldStore(t *testing.T) {
	_, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	folds := NewObservationFoldStore(store)

	id, err := folds.Record(ctx, "p", FoldRollup, 42, []int64{1, 2, 3}, "March")
	require.NoError(t, err)
	other, err := folds.Record(ctx, "q", FoldConsolidation, 50, []int64{4}, "")
	require.NoError(t, err)

	got, err := folds.Get(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []int64{1, 2, 3}, got.Sources())
	assert.Equal(t, int64(42), got.SurvivorID)
	assert.False(t, got.Undone())

	missing, err := folds.Get(ctx, 9999)
	require.NoError(t, err)
	assert.Nil(t, missing)

	_, err = folds.Record(ctx, "p", FoldRollup, 1, nil, "")
	assert.Error(t, err, "a fold with no sources is refused")

	all, err := folds.List(ctx, "", "", false, 0)
	require.NoError(t, err)
	assert.Len(t, all, 2)
	assert.Equal(t, other, all[0].ID, "newest first")
	onlyP, err := folds.List(ctx, "p", "", false, 0)
	require.NoError(t, err)
	assert.Len(t, onlyP, 1)
	onlyConsolidations, err := folds.List(ctx, "", FoldConsolidation, false, 0)
	require.NoError(t, err)
	assert.Len(t, onlyConsolidations, 1)

	ok, err := folds.MarkUndone(ctx, id)
	require.NoError(t, err)
	assert.True(t, ok)
	again, err := folds.MarkUndone(ctx, id)
	require.NoError(t, err)
	assert.False(t, again, "a fold is undone once")

	live, err := folds.List(ctx, "p", "", false, 0)
	require.NoError(t, err)
	assert.Empty(t, live, "an undone fold is not listed unless asked for")
	withUndone, err := folds.List(ctx, "p", "", true, 0)
	require.NoError(t, err)
	require.Len(t, withUndone, 1)
	assert.True(t, withUndone[0].Undone())
}
