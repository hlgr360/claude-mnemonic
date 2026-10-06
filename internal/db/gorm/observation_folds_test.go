//go:build fts5

package gorm

import (
	"context"
	"fmt"
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

	id, err := folds.Record(ctx, "p", FoldRollup, 42, []int64{1, 2, 3}, "March", "")
	require.NoError(t, err)
	other, err := folds.Record(ctx, "q", FoldConsolidation, 50, []int64{4}, "", `{"facts":["x"]}`)
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

	_, err = folds.Record(ctx, "p", FoldRollup, 1, nil, "", "")
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

func TestConsolidationCandidates(t *testing.T) {
	s, _, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	plain1 := foldNote(t, s, "p", "plain one", models.ObsTypeDiscovery, "", 5)
	plain2 := foldNote(t, s, "p", "plain two", models.ObsTypeBugfix, "", 3)
	foldNote(t, s, "p", "decision", models.ObsTypeDecision, "", 3)
	foldNote(t, s, "p", "saved on purpose", models.ObsTypeDiscovery, models.ScopeProject, 3)
	foldNote(t, s, "p", "global", models.ObsTypeDiscovery, models.ScopeGlobal, 3)
	rated := foldNote(t, s, "p", "rated", models.ObsTypeDiscovery, "", 3)
	require.NoError(t, s.UpdateObservationFeedback(ctx, rated, 1))
	archived := foldNote(t, s, "p", "archived", models.ObsTypeDiscovery, "", 3)
	require.NoError(t, s.ArchiveObservation(ctx, archived, "x"))
	superseded := foldNote(t, s, "p", "superseded", models.ObsTypeDiscovery, "", 3)
	require.NoError(t, s.MarkAsSuperseded(ctx, superseded))
	foldNote(t, s, "q", "other project", models.ObsTypeDiscovery, "", 3)

	got, err := s.ConsolidationCandidates(ctx, "p", 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{plain2, plain1}, idsOf(got), "live, unprotected notes of the project, newest first")

	limited, err := s.ConsolidationCandidates(ctx, "p", 1)
	require.NoError(t, err)
	assert.Equal(t, []int64{plain2}, idsOf(limited), "bounded to the newest")
}

func TestFoldDetailIsKept(t *testing.T) {
	_, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	folds := NewObservationFoldStore(store)
	id, err := folds.Record(ctx, "p", FoldConsolidation, 1, []int64{2}, "", `{"facts":["x"],"relations":[7]}`)
	require.NoError(t, err)
	got, err := folds.Get(ctx, id)
	require.NoError(t, err)
	assert.JSONEq(t, `{"facts":["x"],"relations":[7]}`, got.Detail)
}

func TestListArchivedObservations(t *testing.T) {
	s, _, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	live := foldNote(t, s, "p", "live", models.ObsTypeDiscovery, "", 5)
	first := foldNote(t, s, "p", "archived first", models.ObsTypeDiscovery, "", 5)
	second := foldNote(t, s, "p", "archived second", models.ObsTypeBugfix, "", 5)
	otherProject := foldNote(t, s, "q", "archived elsewhere", models.ObsTypeDiscovery, "", 5)
	require.NoError(t, s.ArchiveObservation(ctx, first, "my reason"))
	time.Sleep(3 * time.Millisecond)
	require.NoError(t, s.ArchiveObservation(ctx, second, FoldReason(FoldRollup, 99)))
	require.NoError(t, s.ArchiveObservation(ctx, otherProject, "x"))

	got, total, err := s.ListArchivedObservations(ctx, "p", 10, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Equal(t, []int64{second, first}, idsOf(got), "the most recently archived first; live notes and other projects left out")
	assert.True(t, got[0].IsArchived)
	assert.Equal(t, "rolled-up into #99", got[0].ArchivedReason)
	assert.Equal(t, "my reason", got[1].ArchivedReason)

	all, total, err := s.ListArchivedObservations(ctx, "", 10, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, all, 3)

	page, total, err := s.ListArchivedObservations(ctx, "p", 1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "the total is of all archived notes, not of the page")
	assert.Equal(t, []int64{first}, idsOf(page))

	liveOne, err := s.GetObservationByID(ctx, live)
	require.NoError(t, err)
	assert.False(t, liveOne.IsArchived)
	assert.Empty(t, liveOne.ArchivedReason)
}

// quarterNote stores a quarter note (a roll-up that is also marked as a quarter) dated daysAgo days back.
func quarterNote(t *testing.T, s *ObservationStore, project, title string, daysAgo int) int64 {
	t.Helper()
	id, _, err := s.StoreObservation(context.Background(), "sess-"+project, project, &models.ParsedObservation{
		Type: models.ObsTypeDiscovery, Title: title, Narrative: title + " narrative", Scope: models.ScopeProject,
		Concepts: []string{RollupConcept, QuarterConcept}}, 1, 1)
	require.NoError(t, err)
	require.NoError(t, s.SetObservationCreated(context.Background(), id, time.Now().UnixMilli()-int64(daysAgo)*dayMs))
	return id
}

func TestAQuarterNoteIsExemptFromTheCapAndTheAgeArchive(t *testing.T) {
	s, _, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	quarter := quarterNote(t, s, "p", "Q1 record", 400) // the oldest note of all
	var plain []int64
	for i := 0; i < 5; i++ {
		plain = append(plain, foldNote(t, s, "p", fmt.Sprintf("plain %d", i), models.ObsTypeDiscovery, "", 300-i*10))
	}

	archived, err := s.ArchiveBeyondLimit(ctx, "p", 2)
	require.NoError(t, err)
	assert.ElementsMatch(t, plain[:3], archived, "the cap archives the oldest plain notes beyond the newest two")
	assert.NotContains(t, archived, quarter, "the oldest note of all is a quarter: it neither counts nor is archived")

	live, err := s.GetObservationByID(ctx, quarter)
	require.NoError(t, err)
	assert.False(t, live.IsArchived)

	// The age-based archive (maxAgeDays 100 reaches everything here) leaves it too.
	byAge, err := s.ArchiveOldObservations(ctx, "p", 100, "age")
	require.NoError(t, err)
	assert.NotContains(t, byAge, quarter)
	assert.Len(t, byAge, 2, "the two plain notes the cap kept")
	live, err = s.GetObservationByID(ctx, quarter)
	require.NoError(t, err)
	assert.False(t, live.IsArchived, "still live after both rules")
}

func TestMonthlyRollupsAndRollupLabels(t *testing.T) {
	s, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	folds := NewObservationFoldStore(store)

	mkRollup := func(title string, concepts []string, daysAgo int) int64 {
		id, _, err := s.StoreObservation(ctx, "sess", "p", &models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: title,
			Narrative: title + " narrative", Scope: models.ScopeProject, Concepts: concepts}, 1, 1)
		require.NoError(t, err)
		require.NoError(t, s.SetObservationCreated(ctx, id, time.Now().UnixMilli()-int64(daysAgo)*dayMs))
		return id
	}
	march := mkRollup("March roll-up", []string{RollupConcept}, 200)
	april := mkRollup("April roll-up", []string{RollupConcept}, 170)
	quarter := mkRollup("Quarter", []string{RollupConcept, QuarterConcept}, 100)
	archivedMonth := mkRollup("Archived month", []string{RollupConcept}, 220)
	require.NoError(t, s.ArchiveObservation(ctx, archivedMonth, "x"))
	plain := foldNote(t, s, "p", "plain", models.ObsTypeDiscovery, "", 150)
	other, _, err := s.StoreObservation(ctx, "sess", "q", &models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: "other", Concepts: []string{RollupConcept}}, 1, 1)
	require.NoError(t, err)

	got, err := s.MonthlyRollups(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, []int64{march, april}, idsOf(got), "live monthly roll-ups of the project, oldest first: not the quarter, an archived one, a plain note or another project's")
	assert.NotContains(t, idsOf(got), plain)
	assert.NotContains(t, idsOf(got), other)
	assert.NotContains(t, idsOf(got), quarter)

	f1, err := folds.Record(ctx, "p", FoldRollup, march, []int64{1, 2}, "2026-03", "")
	require.NoError(t, err)
	_, err = folds.Record(ctx, "p", FoldRollup, april, []int64{3}, "2026-04 (part 2)", "")
	require.NoError(t, err)
	_, err = folds.Record(ctx, "p", FoldConsolidation, plain, []int64{4}, "ignored", "")
	require.NoError(t, err)
	labels, err := folds.RollupLabels(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, map[int64]string{march: "2026-03", april: "2026-04 (part 2)"}, labels, "roll-up folds only")

	_, err = folds.MarkUndone(ctx, f1)
	require.NoError(t, err)
	labels, err = folds.RollupLabels(ctx, "p")
	require.NoError(t, err)
	assert.NotContains(t, labels, march, "an undone fold no longer names its roll-up")
}
