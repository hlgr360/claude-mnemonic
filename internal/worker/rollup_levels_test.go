package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func TestRollupPressureFor(t *testing.T) {
	t.Run("without a target the fixed age applies, whatever the size", func(t *testing.T) {
		cfg := &config.Config{RollupMinAgeDays: 45}
		for _, live := range []int{0, 10, 100000} {
			assert.Equal(t, rollupPressure{AgeDays: 45, Factor: 1}, rollupPressureFor(cfg, live))
		}
		assert.Equal(t, 60, rollupPressureFor(&config.Config{}, 5).AgeDays, "an unset age is the default of 60")
	})
	t.Run("with a target the ladder is 90, 60 and 30 days", func(t *testing.T) {
		cfg := &config.Config{RollupTargetLiveNotes: 100, RollupMinAgeDays: 7}
		cases := []struct {
			name    string
			comment string
			live    int
			age     int
			factor  int
		}{
			{"calm", "an empty project", 0, 90, 1},
			{"calm", "exactly at the target is still calm", 100, 90, 1},
			{"over target", "one over", 101, 60, 2},
			{"over target", "exactly twice the target is still over, not far over", 200, 60, 2},
			{"far over target", "more than twice", 201, 30, 3},
			{"far over target", "far more", 5000, 30, 3},
		}
		for _, c := range cases {
			got := rollupPressureFor(cfg, c.live)
			assert.Equal(t, rollupPressure{Name: c.name, AgeDays: c.age, Factor: c.factor}, got, "%d live notes: %s", c.live, c.comment)
		}
	})
}

func TestQuarterHelpers(t *testing.T) {
	y, q := quarterOf(time.Date(2026, 3, 31, 23, 59, 0, 0, time.UTC))
	assert.Equal(t, [2]int{2026, 1}, [2]int{y, q})
	y, q = quarterOf(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, [2]int{2026, 2}, [2]int{y, q})
	y, q = quarterOf(time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, [2]int{2026, 4}, [2]int{y, q})
	assert.Equal(t, "2026-Q3", quarterLabel(2026, 3))
	assert.Equal(t, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), quarterEnd(2026, 1))
	assert.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), quarterEnd(2026, 4), "the end of the last quarter is the next year")

	march := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC).UnixMilli()
	july := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	y, q = rollupQuarter("2026-05", july)
	assert.Equal(t, [2]int{2026, 2}, [2]int{y, q}, "the month in the label wins over the roll-up's own date")
	y, q = rollupQuarter("2026-03 (part 2)", july)
	assert.Equal(t, [2]int{2026, 1}, [2]int{y, q})
	y, q = rollupQuarter("", march)
	assert.Equal(t, [2]int{2026, 1}, [2]int{y, q}, "no label: the date")
	y, q = rollupQuarter("nonsense", march)
	assert.Equal(t, [2]int{2026, 1}, [2]int{y, q})
	y, q = rollupQuarter("2026-13", march)
	assert.Equal(t, [2]int{2026, 1}, [2]int{y, q}, "an impossible month is not believed")
}

func TestGroupQuarters(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	at := func(y int, m time.Month, d int) int64 { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC).UnixMilli() }
	ru := func(id, created int64) *models.Observation {
		return &models.Observation{ID: id, CreatedAtEpoch: created}
	}
	labels := map[int64]string{1: "2026-01", 2: "2026-02", 3: "2026-03", 4: "2026-04", 5: "2026-05", 6: "2026-07", 7: "2026-08"}
	rollups := []*models.Observation{
		ru(1, at(2026, 1, 28)), ru(2, at(2026, 2, 27)), ru(3, at(2026, 3, 30)), // Q1: ended 2026-04-01, settled on 2026-06-30
		ru(4, at(2026, 4, 29)),                         // Q2 with a single roll-up
		ru(5, at(2026, 5, 29)),                         // Q2 again: two, but Q2 ended on 2026-07-01 and settles on 2026-09-29: settled by now
		ru(6, at(2026, 7, 30)), ru(7, at(2026, 8, 30)), // Q3 ended 2026-10-01: not settled
	}
	got := groupQuarters(rollups, labels, now, 90, 2)
	require.Len(t, got, 2, "Q1 and Q2 are done; Q3 ended too recently")
	assert.Equal(t, "2026-Q1", got[0].Label)
	assert.Equal(t, sdk.LevelQuarter, got[0].Level)
	assert.Equal(t, []int64{1, 2, 3}, got[0].ids(), "its three monthly roll-ups, oldest first")
	assert.Equal(t, "2026-Q2", got[1].Label)
	assert.Equal(t, []int64{4, 5}, got[1].ids())

	assert.Len(t, groupQuarters(rollups, labels, now, 90, 3), 1, "a quarter needs the minimum number of roll-ups: only Q1 has three")
	assert.Len(t, groupQuarters(rollups, labels, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), 90, 2), 1, "Q2 has not settled yet the day before")
	assert.Empty(t, groupQuarters(nil, nil, now, 90, 2))

	// The roll-ups kept today's date because a cap is set: the labels still say which month they stand for.
	today := now.UnixMilli()
	capped := []*models.Observation{ru(1, today), ru(2, today)}
	got = groupQuarters(capped, labels, now, 90, 2)
	require.Len(t, got, 1)
	assert.Equal(t, "2026-Q1", got[0].Label)
}

// settledQuarter returns the latest calendar quarter that ended more than quarterSettleDays ago.
func settledQuarter(now time.Time) (int, int) {
	y, q := quarterOf(now)
	for quarterEnd(y, q).Add(quarterSettleDays * 24 * time.Hour).After(now) {
		q--
		if q == 0 {
			y, q = y-1, 4
		}
	}
	return y, q
}

// addNotesAt stores n distinct automatic-scope notes a few minutes apart starting at base.
func addNotesAt(t *testing.T, svc *Service, project string, n int, base time.Time) []int64 {
	t.Helper()
	var ids []int64
	for i := 0; i < n; i++ {
		seq := observationSeq.Add(1)
		id, _, err := svc.observationStore.StoreObservation(context.Background(), "sdk-"+project, project,
			&models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: fmt.Sprintf("dated note %d", seq), Narrative: fmt.Sprintf("something different %d in %s", seq, project)}, 1, 1)
		require.NoError(t, err)
		require.NoError(t, svc.observationStore.SetObservationCreated(context.Background(), id, base.Add(time.Duration(i)*time.Minute).UnixMilli()))
		ids = append(ids, id)
	}
	return ids
}

func TestQuarterRollup_MonthsThenAQuarterThenRestore(t *testing.T) {
	svc, w, cleanup := rollupService(t, nil)
	defer cleanup()
	ctx := context.Background()
	p := "shop_aaaaaa"
	y, q := settledQuarter(time.Now())
	first := time.Date(y, time.Month((q-1)*3+1), 12, 12, 0, 0, 0, time.UTC)
	second := first.AddDate(0, 1, 0)
	third := first.AddDate(0, 2, 0)
	m1 := addNotesAt(t, svc, p, 8, first)
	m2 := addNotesAt(t, svc, p, 8, second)
	m3 := addNotesAt(t, svc, p, 8, third)
	label := quarterLabel(y, q)
	failures := 0

	// 1. Months: three groups of raw notes, no quarter yet (there are no monthly roll-ups to condense).
	rep, err := svc.rollupProject(ctx, p, 0, false, &failures)
	require.NoError(t, err)
	require.Len(t, rep.Groups, 3)
	for _, g := range rep.Groups {
		assert.Equal(t, sdk.LevelMonth, g.Level)
		require.Empty(t, g.Error)
	}
	monthRollups := []int64{rep.Groups[0].RollupID, rep.Groups[1].RollupID, rep.Groups[2].RollupID}
	require.Len(t, w.inputs, 3)
	assert.Equal(t, sdk.LevelMonth, w.inputs[0].Level)

	// 2. The quarter: a preview first, then the quarter note.
	pre, err := svc.rollupProject(ctx, p, 0, true, &failures)
	require.NoError(t, err)
	require.Len(t, pre.Groups, 1)
	assert.Equal(t, sdk.LevelQuarter, pre.Groups[0].Level)
	assert.Equal(t, label, pre.Groups[0].Label)
	assert.ElementsMatch(t, monthRollups, pre.Groups[0].IDs, "the quarter is made of the three monthly roll-ups")
	assert.Len(t, w.inputs, 3, "a preview asks no model")

	rep, err = svc.rollupProject(ctx, p, 0, false, &failures)
	require.NoError(t, err)
	require.Len(t, rep.Groups, 1)
	require.Empty(t, rep.Groups[0].Error)
	quarterID := rep.Groups[0].RollupID
	require.Len(t, w.inputs, 4)
	assert.Equal(t, sdk.LevelQuarter, w.inputs[3].Level, "the model was asked for a quarter")
	assert.Equal(t, label, w.inputs[3].Period)
	assert.Len(t, w.inputs[3].Observations, 3)

	live := liveIDs(t, svc, p)
	quarter := live[quarterID]
	require.NotNil(t, quarter, "the quarter note is live")
	assert.Contains(t, quarter.Concepts, gorm.RollupConcept)
	assert.Contains(t, quarter.Concepts, gorm.QuarterConcept)
	assert.Contains(t, quarter.Title.String, "Quarter "+label)
	assert.Contains(t, quarter.Narrative.String, "final record")
	assert.Equal(t, models.ScopeProject, quarter.Scope)
	for _, id := range monthRollups {
		assert.NotContains(t, live, id, "the monthly roll-ups are archived into the quarter")
	}
	for _, ids := range [][]int64{m1, m2, m3} {
		for _, id := range ids {
			assert.NotContains(t, live, id, "and the raw notes stay archived under their months")
		}
	}
	var reason string
	require.NoError(t, svc.store.DB.Raw("SELECT archived_reason FROM observations WHERE id = ?", monthRollups[0]).Scan(&reason).Error)
	assert.Equal(t, fmt.Sprintf("rolled-up into #%d", quarterID), reason)
	folds, err := gorm.NewObservationFoldStore(svc.store).List(ctx, p, gorm.FoldRollup, false, 0)
	require.NoError(t, err)
	var quarterFold *gorm.ObservationFold
	for i := range folds {
		if folds[i].SurvivorID == quarterID {
			quarterFold = &folds[i]
		}
	}
	require.NotNil(t, quarterFold)
	assert.Equal(t, label, quarterFold.Note)

	// 3. The quarter is the final record: nothing more is offered, not even with a lower age.
	again, err := svc.rollupProject(ctx, p, 0, true, &failures)
	require.NoError(t, err)
	assert.Empty(t, again.Groups)

	// 4. Restoring the quarter brings the monthly roll-ups back and archives the quarter note; they are then left alone for
	// a while rather than condensed again at once.
	res, err := svc.restoreFold(ctx, quarterFold.ID)
	require.NoError(t, err)
	assert.True(t, res.SurvivorArchived)
	assert.ElementsMatch(t, monthRollups, res.Restored)
	live = liveIDs(t, svc, p)
	assert.NotContains(t, live, quarterID)
	for _, id := range monthRollups {
		assert.Contains(t, live, id)
	}
	after, err := svc.rollupProject(ctx, p, 0, true, &failures)
	require.NoError(t, err)
	assert.Empty(t, after.Groups, "restored on purpose: not condensed again straight away")
}

func TestQuarterRollup_OffAndNeedsTwo(t *testing.T) {
	y, q := settledQuarter(time.Now())
	first := time.Date(y, time.Month((q-1)*3+1), 12, 12, 0, 0, 0, time.UTC)

	t.Run("a single monthly roll-up is not a quarter", func(t *testing.T) {
		svc, _, cleanup := rollupService(t, nil)
		defer cleanup()
		addNotesAt(t, svc, "a_aaaaaa", 8, first)
		failures := 0
		rep, err := svc.rollupProject(context.Background(), "a_aaaaaa", 0, false, &failures)
		require.NoError(t, err)
		require.Len(t, rep.Groups, 1)
		again, err := svc.rollupProject(context.Background(), "a_aaaaaa", 0, true, &failures)
		require.NoError(t, err)
		assert.Empty(t, again.Groups)
	})
	t.Run("with quarters off the monthly roll-ups stay", func(t *testing.T) {
		svc, _, cleanup := rollupService(t, func(c *config.Config) { c.RollupQuartersEnabled = false })
		defer cleanup()
		addNotesAt(t, svc, "a_aaaaaa", 8, first)
		addNotesAt(t, svc, "a_aaaaaa", 8, first.AddDate(0, 1, 0))
		failures := 0
		rep, err := svc.rollupProject(context.Background(), "a_aaaaaa", 0, false, &failures)
		require.NoError(t, err)
		require.Len(t, rep.Groups, 2)
		again, err := svc.rollupProject(context.Background(), "a_aaaaaa", 0, true, &failures)
		require.NoError(t, err)
		assert.Empty(t, again.Groups, "two monthly roll-ups of a settled quarter, and no quarter note while it is off")
	})
}

func TestRollupLadder_AgeFollowsHowFarOverTheTargetAProjectIs(t *testing.T) {
	// Ten notes that are 45 days old: older than the far-over step (30 days), not older than the over step (60 days).
	notes45 := func(svc *Service, project string) {
		addNotesAt(t, svc, project, 10, time.Now().Add(-45*24*time.Hour).Truncate(time.Hour))
	}
	run := func(target int) *RollupReport {
		svc, _, cleanup := rollupService(t, func(c *config.Config) { c.RollupTargetLiveNotes = target })
		defer cleanup()
		notes45(svc, "a_aaaaaa")
		failures := 0
		rep, err := svc.rollupProject(context.Background(), "a_aaaaaa", 0, true, &failures)
		require.NoError(t, err)
		return rep
	}

	calm := run(20) // 10 live notes, target 20
	assert.Equal(t, "calm", calm.Pressure)
	assert.Equal(t, 90, calm.AgeDays)
	assert.Equal(t, 10, calm.Live)
	assert.Equal(t, 20, calm.Target)
	assert.Empty(t, calm.Groups, "45 days is not old enough while the project is under its target")

	over := run(6) // 10 live notes, target 6 -> over (6 < 10 <= 12)
	assert.Equal(t, "over target", over.Pressure)
	assert.Equal(t, 60, over.AgeDays)
	assert.Empty(t, over.Groups, "45 days is still not old enough at the 60-day step")

	far := run(4) // 10 live notes, target 4 -> far over (10 > 8)
	assert.Equal(t, "far over target", far.Pressure)
	assert.Equal(t, 30, far.AgeDays)
	require.Len(t, far.Groups, 1, "45 days is old enough once the project is far over its target")
	assert.Equal(t, 10, far.Candidates)

	none := run(0) // no target: the fixed 60 days
	assert.Equal(t, "", none.Pressure)
	assert.Equal(t, 60, none.AgeDays)
	assert.Empty(t, none.Groups)
}

func TestRunRollupPass_PressuredProjectsGoFirstAndGetALargerBudget(t *testing.T) {
	svc, w, cleanup := rollupService(t, func(c *config.Config) {
		c.RollupTargetLiveNotes = 4
		c.RollupMaxGroupsPerRun = 1 // the base budget is one group
	})
	defer cleanup()
	old := time.Now().Add(-120 * 24 * time.Hour)
	// Four projects of ten old notes each: all far over the target of 4 (factor 3), so the pass may write 3 groups.
	for _, p := range []string{"a_aaaaaa", "b_bbbbbb", "c_cccccc", "d_dddddd"} {
		addNotesAt(t, svc, p, 10, old)
	}
	// A calm project (3 notes, target 4) with nothing to do.
	addNotesAt(t, svc, "e_eeeeee", 3, old)
	assert.Equal(t, 3, svc.runRollupPass(context.Background()), "the pass writes as many groups as the most pressed project is allowed: 1 x 3")
	assert.Len(t, w.calls, 3)

	t.Run("without a target the base budget stays", func(t *testing.T) {
		svc2, w2, cleanup2 := rollupService(t, func(c *config.Config) { c.RollupMaxGroupsPerRun = 1 })
		defer cleanup2()
		for _, p := range []string{"a_aaaaaa", "b_bbbbbb"} {
			addNotesAt(t, svc2, p, 10, old)
		}
		assert.Equal(t, 1, svc2.runRollupPass(context.Background()))
		assert.Len(t, w2.calls, 1)
	})
}
