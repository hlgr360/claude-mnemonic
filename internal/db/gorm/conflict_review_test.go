//go:build fts5

// Package gorm provides GORM-based database operations for claude-mnemonic.
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

type reviewFixture struct {
	obs       *ObservationStore
	conflicts *ConflictStore
	store     *Store
}

var reviewSeq int

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	obs, store, cleanup := testObservationStore(t)
	t.Cleanup(cleanup)
	return &reviewFixture{obs: obs, conflicts: NewConflictStore(store), store: store}
}

// add stores an observation (each a little later than the one before, with its own text) and returns its id.
func (f *reviewFixture) add(t *testing.T, project, title string) int64 {
	t.Helper()
	reviewSeq++
	id, _, err := f.obs.StoreObservation(context.Background(), "sdk-"+project, project,
		&models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: title, Narrative: fmt.Sprintf("narrative %d of %s", reviewSeq, title)}, 1, 1)
	require.NoError(t, err)
	time.Sleep(3 * time.Millisecond)
	return id
}

func (f *reviewFixture) superseded(t *testing.T, id int64) bool {
	t.Helper()
	var n int
	require.NoError(t, f.store.DB.Raw(`SELECT COALESCE(is_superseded, 0) FROM observations WHERE id = ?`, id).Scan(&n).Error)
	return n == 1
}

func (f *reviewFixture) propose(t *testing.T, older, newer int64, confidence string) int64 {
	t.Helper()
	id, _, err := f.conflicts.Propose(context.Background(), Proposal{
		NewerID: newer, OlderID: older, Type: models.ConflictSuperseded, Relation: "supersedes", Confidence: confidence, Reason: "newer replaces older", Proposer: "haiku",
	})
	require.NoError(t, err)
	return id
}

func TestPropose_RecordsTheProposalAndTouchesNothing(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")

	id, created, err := f.conflicts.Propose(ctx, Proposal{
		NewerID: newer, OlderID: older, Type: models.ConflictContradicts, Relation: "contradicts", Confidence: "high", Reason: "they disagree", Proposer: "haiku",
	})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Greater(t, id, int64(0))

	rec, err := f.conflicts.GetConflict(ctx, id)
	require.NoError(t, err)
	c := rec.Conflict
	assert.Equal(t, models.ConflictContradicts, c.ConflictType)
	assert.Equal(t, "contradicts", c.Relation)
	assert.Equal(t, "high", c.Confidence)
	assert.Equal(t, "they disagree", c.Reason)
	assert.Equal(t, "haiku", c.Proposer)
	assert.False(t, c.Resolved)
	assert.Empty(t, c.Decision)
	assert.Equal(t, models.ResolutionManual, c.Resolution)
	assert.Greater(t, c.DetectedAtEpoch, int64(0))
	assert.Equal(t, older, rec.Older.ID)
	assert.Equal(t, newer, rec.Newer.ID)
	assert.False(t, f.superseded(t, older), "a proposal hides nothing")
	assert.False(t, f.superseded(t, newer))
}

func TestPropose_AnUnknownTypeBecomesSuperseded(t *testing.T) {
	f := newReviewFixture(t)
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")
	id, _, err := f.conflicts.Propose(context.Background(), Proposal{NewerID: newer, OlderID: older, Type: "duplicate"})
	require.NoError(t, err)
	rec, _ := f.conflicts.GetConflict(context.Background(), id)
	assert.Equal(t, models.ConflictSuperseded, rec.Conflict.ConflictType)
}

func TestPropose_ThePairIsRecordedOnceInEitherOrderAndADecisionIsRemembered(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")

	first := f.propose(t, older, newer, "high")
	again, created, err := f.conflicts.Propose(ctx, Proposal{NewerID: newer, OlderID: older})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, again)

	_, err = f.conflicts.ResolveProposal(ctx, first, DecisionKeepBoth)
	require.NoError(t, err)
	remembered, created, err := f.conflicts.Propose(ctx, Proposal{NewerID: newer, OlderID: older, Confidence: "high"})
	require.NoError(t, err)
	assert.False(t, created, "a keep-both is never proposed again")
	assert.Equal(t, first, remembered)
	n, _ := f.conflicts.CountOpen(ctx, "")
	assert.Zero(t, n)
}

func TestPropose_Refusals(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	a, b := f.add(t, "p_aaaaaa", "first"), f.add(t, "p_aaaaaa", "second")
	other := f.add(t, "q_bbbbbb", "elsewhere")

	_, _, err := f.conflicts.Propose(ctx, Proposal{NewerID: a, OlderID: a})
	assert.ErrorIs(t, err, ErrInvalidPair, "an observation is not in conflict with itself")
	_, _, err = f.conflicts.Propose(ctx, Proposal{NewerID: b, OlderID: other})
	assert.ErrorIs(t, err, ErrInvalidPair, "observations of different projects")
	_, _, err = f.conflicts.Propose(ctx, Proposal{NewerID: a, OlderID: b})
	assert.ErrorIs(t, err, ErrInvalidPair, "the 'newer' one is older")
	_, _, err = f.conflicts.Propose(ctx, Proposal{NewerID: b, OlderID: 999999})
	assert.ErrorIs(t, err, ErrObservationGone)
}

func TestListConflicts_StatusesOrderProjectAndPaging(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	var ids []int64
	for i, conf := range []string{"low", "high", "medium"} {
		older, newer := f.add(t, "p_aaaaaa", fmt.Sprintf("old %d", i)), f.add(t, "p_aaaaaa", fmt.Sprintf("new %d", i))
		ids = append(ids, f.propose(t, older, newer, conf))
	}
	o2, n2 := f.add(t, "q_bbbbbb", "q old"), f.add(t, "q_bbbbbb", "q new")
	other := f.propose(t, o2, n2, "high")

	open, total, err := f.conflicts.ListConflicts(ctx, ConflictFilter{Project: "p_aaaaaa"})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	var got []string
	for _, r := range open {
		got = append(got, r.Conflict.Confidence)
		assert.NotNil(t, r.Older)
		assert.NotNil(t, r.Newer)
	}
	assert.Equal(t, []string{"high", "medium", "low"}, got, "the most confident first")

	all, total, err := f.conflicts.ListConflicts(ctx, ConflictFilter{})
	require.NoError(t, err)
	assert.Equal(t, 4, total, "every project when none is named")
	assert.Len(t, all, 4)

	page, total, err := f.conflicts.ListConflicts(ctx, ConflictFilter{Limit: 2, Offset: 1})
	require.NoError(t, err)
	assert.Equal(t, 4, total, "the total is not the page")
	assert.Len(t, page, 2)

	_, err = f.conflicts.ResolveProposal(ctx, ids[0], DecisionKeepBoth)
	require.NoError(t, err)
	_, err = f.conflicts.ResolveProposal(ctx, other, DecisionSupersedeOlder)
	require.NoError(t, err)

	resolved, total, err := f.conflicts.ListConflicts(ctx, ConflictFilter{Status: ConflictStatusResolved})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Equal(t, other, resolved[0].Conflict.ID, "the latest decision first")
	still, total, _ := f.conflicts.ListConflicts(ctx, ConflictFilter{Status: ConflictStatusOpen})
	assert.Equal(t, 2, total)
	assert.Len(t, still, 2)
	everything, total, _ := f.conflicts.ListConflicts(ctx, ConflictFilter{Status: ConflictStatusAll})
	assert.Equal(t, 4, total)
	assert.False(t, everything[0].Conflict.Resolved, "open ones come before resolved ones")

	capped, _, _ := f.conflicts.ListConflicts(ctx, ConflictFilter{Limit: 100000})
	assert.LessOrEqual(t, len(capped), 200)
}

func TestConflictsOfDeletedObservationsAreNeitherListedNorCounted(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")
	id := f.propose(t, older, newer, "high")
	require.NoError(t, f.store.DB.Exec(`DELETE FROM observations WHERE id = ?`, older).Error)

	list, total, err := f.conflicts.ListConflicts(ctx, ConflictFilter{})
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.Zero(t, total)
	n, _ := f.conflicts.CountOpen(ctx, "")
	assert.Zero(t, n)
	_, err = f.conflicts.GetConflict(ctx, id)
	assert.ErrorIs(t, err, ErrObservationGone)
	_, err = f.conflicts.ResolveProposal(ctx, id, DecisionKeepBoth)
	assert.ErrorIs(t, err, ErrObservationGone, "nothing to decide about")
}

func TestCountOpen_PerProjectAndAll(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	for _, project := range []string{"p_aaaaaa", "p_aaaaaa", "q_bbbbbb"} {
		f.propose(t, f.add(t, project, "old"), f.add(t, project, "new"), "high")
	}
	all, _ := f.conflicts.CountOpen(ctx, "")
	p, _ := f.conflicts.CountOpen(ctx, "p_aaaaaa")
	q, _ := f.conflicts.CountOpen(ctx, "q_bbbbbb")
	none, _ := f.conflicts.CountOpen(ctx, "nothing_000000")
	assert.Equal(t, []int{3, 2, 1, 0}, []int{all, p, q, none})
}

func TestResolveProposal_SupersedeOlderHidesOlderAndRecordsTheDecision(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")
	id := f.propose(t, older, newer, "high")

	rec, err := f.conflicts.ResolveProposal(ctx, id, DecisionSupersedeOlder)
	require.NoError(t, err)
	c := rec.Conflict
	assert.True(t, c.Resolved)
	assert.Equal(t, DecisionSupersedeOlder, c.Decision)
	assert.Equal(t, models.ResolutionPreferNewer, c.Resolution)
	assert.Equal(t, older, c.SupersededObsID)
	assert.Greater(t, c.ResolvedAtEpoch, int64(0))
	require.NotNil(t, c.ResolvedAt)
	assert.True(t, f.superseded(t, older))
	assert.False(t, f.superseded(t, newer))
	assert.True(t, rec.Older.IsSuperseded)
}

func TestResolveProposal_SupersedeNewerAndKeepBoth(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	o1, n1 := f.add(t, "p_aaaaaa", "old 1"), f.add(t, "p_aaaaaa", "new 1")
	id1 := f.propose(t, o1, n1, "high")
	rec, err := f.conflicts.ResolveProposal(ctx, id1, DecisionSupersedeNewer)
	require.NoError(t, err)
	assert.Equal(t, models.ResolutionPreferOlder, rec.Conflict.Resolution)
	assert.Equal(t, n1, rec.Conflict.SupersededObsID)
	assert.True(t, f.superseded(t, n1))
	assert.False(t, f.superseded(t, o1))

	o2, n2 := f.add(t, "p_aaaaaa", "old 2"), f.add(t, "p_aaaaaa", "new 2")
	id2 := f.propose(t, o2, n2, "high")
	rec, err = f.conflicts.ResolveProposal(ctx, id2, DecisionKeepBoth)
	require.NoError(t, err)
	assert.True(t, rec.Conflict.Resolved)
	assert.Equal(t, DecisionKeepBoth, rec.Conflict.Decision)
	assert.Zero(t, rec.Conflict.SupersededObsID)
	assert.False(t, f.superseded(t, o2))
	assert.False(t, f.superseded(t, n2), "keep both hides nothing")
}

func TestResolveProposal_Refusals(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")
	id := f.propose(t, older, newer, "high")

	_, err := f.conflicts.ResolveProposal(ctx, id, "delete_everything")
	assert.ErrorIs(t, err, ErrBadDecision)
	assert.False(t, f.superseded(t, older), "nothing happened")
	_, err = f.conflicts.ResolveProposal(ctx, 424242, DecisionKeepBoth)
	assert.ErrorIs(t, err, ErrConflictNotFound)

	_, err = f.conflicts.ResolveProposal(ctx, id, DecisionSupersedeOlder)
	require.NoError(t, err)
	_, err = f.conflicts.ResolveProposal(ctx, id, DecisionSupersedeNewer)
	assert.ErrorIs(t, err, ErrConflictResolved, "a decision is not silently replaced")
	assert.False(t, f.superseded(t, newer), "the second attempt hid nothing")
}

func TestUndoProposal_RestoresTheObservationAndReopensTheConflict(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")
	id := f.propose(t, older, newer, "high")
	_, err := f.conflicts.ResolveProposal(ctx, id, DecisionSupersedeOlder)
	require.NoError(t, err)

	rec, err := f.conflicts.UndoProposal(ctx, id)
	require.NoError(t, err)
	c := rec.Conflict
	assert.False(t, c.Resolved)
	assert.Empty(t, c.Decision)
	assert.Zero(t, c.SupersededObsID)
	assert.Zero(t, c.ResolvedAtEpoch)
	assert.Nil(t, c.ResolvedAt)
	assert.Equal(t, models.ResolutionManual, c.Resolution)
	assert.False(t, f.superseded(t, older), "visible again")
	n, _ := f.conflicts.CountOpen(ctx, "")
	assert.Equal(t, 1, n, "and waiting for a decision again")

	_, err = f.conflicts.UndoProposal(ctx, id)
	assert.ErrorIs(t, err, ErrConflictOpen, "nothing to undo")
	_, err = f.conflicts.UndoProposal(ctx, 424242)
	assert.ErrorIs(t, err, ErrConflictNotFound)

	_, err = f.conflicts.ResolveProposal(ctx, id, DecisionSupersedeNewer)
	assert.NoError(t, err, "it can be decided again, differently")
}

func TestUndoProposal_AKeepBothIsReopenedToo(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	id := f.propose(t, f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new"), "low")
	_, _ = f.conflicts.ResolveProposal(ctx, id, DecisionKeepBoth)
	rec, err := f.conflicts.UndoProposal(ctx, id)
	require.NoError(t, err)
	assert.False(t, rec.Conflict.Resolved)
}

func TestUndoProposal_AnObservationHiddenByTwoDecisionsStaysHiddenUntilBothAreUndone(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	old := f.add(t, "p_aaaaaa", "the old one")
	n1, n2 := f.add(t, "p_aaaaaa", "newer one"), f.add(t, "p_aaaaaa", "newest one")
	c1, c2 := f.propose(t, old, n1, "high"), f.propose(t, old, n2, "high")
	_, _ = f.conflicts.ResolveProposal(ctx, c1, DecisionSupersedeOlder)
	_, _ = f.conflicts.ResolveProposal(ctx, c2, DecisionSupersedeOlder)

	_, err := f.conflicts.UndoProposal(ctx, c1)
	require.NoError(t, err)
	assert.True(t, f.superseded(t, old), "the other decision still hides it")
	_, err = f.conflicts.UndoProposal(ctx, c2)
	require.NoError(t, err)
	assert.False(t, f.superseded(t, old))
}

func (f *reviewFixture) backdateDecision(t *testing.T, id int64, daysAgo int) {
	t.Helper()
	require.NoError(t, f.store.DB.Exec(`UPDATE observation_conflicts SET resolved_at_epoch = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -daysAgo).UnixMilli(), id).Error)
}

func (f *reviewFixture) exists(t *testing.T, id int64) bool {
	t.Helper()
	var n int64
	require.NoError(t, f.store.DB.Raw(`SELECT COUNT(*) FROM observations WHERE id = ?`, id).Scan(&n).Error)
	return n == 1
}

func TestCleanupSuperseded_DeletesOnlyWhatAPersonSupersededLongAgoAndOnlyWhenAsked(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()

	oldOne, newOne := f.add(t, "p_aaaaaa", "old one"), f.add(t, "p_aaaaaa", "new one")
	longAgo := f.propose(t, oldOne, newOne, "high")
	_, _ = f.conflicts.ResolveProposal(ctx, longAgo, DecisionSupersedeOlder)
	f.backdateDecision(t, longAgo, 40)

	recentOld, recentNew := f.add(t, "p_aaaaaa", "recent old"), f.add(t, "p_aaaaaa", "recent new")
	recent := f.propose(t, recentOld, recentNew, "high")
	_, _ = f.conflicts.ResolveProposal(ctx, recent, DecisionSupersedeOlder)
	f.backdateDecision(t, recent, 5)

	keepOld, keepNew := f.add(t, "p_aaaaaa", "keep old"), f.add(t, "p_aaaaaa", "keep new")
	kept := f.propose(t, keepOld, keepNew, "high")
	_, _ = f.conflicts.ResolveProposal(ctx, kept, DecisionKeepBoth)
	f.backdateDecision(t, kept, 400)

	flagOnly := f.add(t, "p_aaaaaa", "flag only")
	require.NoError(t, f.store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, flagOnly).Error)

	none, err := f.conflicts.CleanupSuperseded(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, none, "zero keeps everything for ever")
	assert.True(t, f.exists(t, oldOne))

	deleted, err := f.conflicts.CleanupSuperseded(ctx, 30)
	require.NoError(t, err)
	assert.Equal(t, []int64{oldOne}, deleted)
	assert.False(t, f.exists(t, oldOne), "superseded by a decision 40 days ago")
	assert.True(t, f.exists(t, newOne), "the survivor stays")
	assert.True(t, f.exists(t, recentOld), "decided 5 days ago")
	assert.True(t, f.exists(t, keepOld) && f.exists(t, keepNew), "keep both deletes nothing, however old")
	assert.True(t, f.exists(t, flagOnly), "a flag without a decision is not ours to delete")
	_, err = f.conflicts.GetConflict(ctx, longAgo)
	assert.Error(t, err, "its conflict went with it")
}

func TestCleanupSuperseded_TheClockStartsAtTheDecisionNotAtTheProposal(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")
	id := f.propose(t, older, newer, "high")
	require.NoError(t, f.store.DB.Exec(`UPDATE observation_conflicts SET detected_at_epoch = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -200).UnixMilli(), id).Error)

	_, err := f.conflicts.ResolveProposal(ctx, id, DecisionSupersedeOlder)
	require.NoError(t, err)
	deleted, err := f.conflicts.CleanupSuperseded(ctx, 30)
	require.NoError(t, err)
	assert.Empty(t, deleted, "proposed long ago, decided just now: kept")
	assert.True(t, f.exists(t, older))
}

func TestCleanupSuperseded_AnUndoneDecisionIsNeverDeleted(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	older, newer := f.add(t, "p_aaaaaa", "old"), f.add(t, "p_aaaaaa", "new")
	id := f.propose(t, older, newer, "high")
	_, _ = f.conflicts.ResolveProposal(ctx, id, DecisionSupersedeOlder)
	f.backdateDecision(t, id, 90)
	_, err := f.conflicts.UndoProposal(ctx, id)
	require.NoError(t, err)

	deleted, err := f.conflicts.CleanupSuperseded(ctx, 30)
	require.NoError(t, err)
	assert.Empty(t, deleted)
	assert.True(t, f.exists(t, older))
}

func TestUncheckedObservations_LiveProjectOnesNewestFirstUntilMarked(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	first := f.add(t, "p_aaaaaa", "first")
	archived := f.add(t, "p_aaaaaa", "archived")
	hidden := f.add(t, "p_aaaaaa", "hidden")
	last := f.add(t, "p_aaaaaa", "last")
	f.add(t, "q_bbbbbb", "other project")
	require.NoError(t, f.store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = ?`, archived).Error)
	require.NoError(t, f.store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, hidden).Error)

	got, err := f.conflicts.UncheckedObservations(ctx, "p_aaaaaa", 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{last, first}, []int64{got[0].ID, got[1].ID})
	assert.Len(t, got, 2)

	require.NoError(t, f.conflicts.MarkChecked(ctx, last, 1))
	got, _ = f.conflicts.UncheckedObservations(ctx, "p_aaaaaa", 10)
	require.Len(t, got, 1)
	assert.Equal(t, first, got[0].ID)
	require.NoError(t, f.conflicts.MarkChecked(ctx, last, 2), "marking twice is fine")

	limited, _ := f.conflicts.UncheckedObservations(ctx, "q_bbbbbb", 0)
	assert.Empty(t, limited, "a limit of zero asks for nothing")
}

func TestProjectsWithUncheckedObservations_MostRecentlyActiveFirst(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	a := f.add(t, "a_111111", "a")
	f.add(t, "b_222222", "b")
	f.add(t, "a_111111", "a again")

	projects, err := f.conflicts.ProjectsWithUncheckedObservations(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"a_111111", "b_222222"}, projects)

	require.NoError(t, f.conflicts.MarkChecked(ctx, a, 0))
	require.NoError(t, f.store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE project = 'b_222222'`).Error)
	projects, _ = f.conflicts.ProjectsWithUncheckedObservations(ctx)
	assert.Equal(t, []string{"a_111111"}, projects, "a project whose observations are all checked or archived is done")
}

func TestMigration_ConflictReviewColumnsAndTableExist(t *testing.T) {
	f := newReviewFixture(t)
	var cols []struct{ Name string }
	require.NoError(t, f.store.DB.Raw(`SELECT name FROM pragma_table_info('observation_conflicts')`).Scan(&cols).Error)
	have := map[string]bool{}
	for _, c := range cols {
		have[c.Name] = true
	}
	for _, want := range []string{"relation", "confidence", "proposer", "decision", "superseded_obs_id", "resolved_at_epoch"} {
		assert.True(t, have[want], want)
	}
	var n int64
	require.NoError(t, f.store.DB.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'conflict_checks'`).Scan(&n).Error)
	assert.Equal(t, int64(1), n)
}
