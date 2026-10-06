package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func obsAt(id int64, session string, t time.Time) *models.Observation {
	return &models.Observation{
		ID: id, SDKSessionID: session, Type: models.ObsTypeDiscovery, CreatedAtEpoch: t.UnixMilli(),
		Title: sql.NullString{String: fmt.Sprintf("note %d", id), Valid: true},
	}
}

func idsOfGroup(g rollupGroup) []int64 { return g.ids() }

func TestGroupForRollup(t *testing.T) {
	jan := func(d int) time.Time { return time.Date(2026, 1, d, 10, 0, 0, 0, time.UTC) }
	feb := func(d int) time.Time { return time.Date(2026, 2, d, 10, 0, 0, 0, time.UTC) }

	t.Run("one group per month, oldest first, small months left out", func(t *testing.T) {
		var notes []*models.Observation
		for i := 0; i < 5; i++ {
			notes = append(notes, obsAt(int64(i+1), "s", jan(i+1)))
		}
		for i := 0; i < 3; i++ { // too few for February
			notes = append(notes, obsAt(int64(10+i), "s", feb(i+1)))
		}
		got := groupForRollup(notes, 4, 40)
		require.Len(t, got, 1)
		assert.Equal(t, "2026-01", got[0].Label)
		assert.Equal(t, []int64{1, 2, 3, 4, 5}, idsOfGroup(got[0]))
	})

	t.Run("a month is split by session when it is too big, and sessions stay together", func(t *testing.T) {
		var notes []*models.Observation
		id := int64(1)
		for _, sess := range []struct {
			name string
			n    int
		}{{"a", 4}, {"b", 4}, {"c", 4}} {
			for i := 0; i < sess.n; i++ {
				notes = append(notes, obsAt(id, sess.name, jan(int(id))))
				id++
			}
		}
		got := groupForRollup(notes, 2, 8)
		require.Len(t, got, 2)
		assert.Equal(t, "2026-01 (part 1)", got[0].Label)
		assert.Equal(t, []int64{1, 2, 3, 4, 5, 6, 7, 8}, idsOfGroup(got[0]), "sessions a and b")
		assert.Equal(t, "2026-01 (part 2)", got[1].Label)
		assert.Equal(t, []int64{9, 10, 11, 12}, idsOfGroup(got[1]), "session c")
	})

	t.Run("a session bigger than the limit is cut in order", func(t *testing.T) {
		var notes []*models.Observation
		for i := 1; i <= 7; i++ {
			notes = append(notes, obsAt(int64(i), "only", jan(i)))
		}
		got := groupForRollup(notes, 2, 3)
		require.Len(t, got, 2, "3 + 3, and the 1 left over is too small")
		assert.Equal(t, []int64{1, 2, 3}, idsOfGroup(got[0]))
		assert.Equal(t, []int64{4, 5, 6}, idsOfGroup(got[1]))
	})

	t.Run("it is deterministic", func(t *testing.T) {
		var notes []*models.Observation
		for i := 1; i <= 30; i++ {
			notes = append(notes, obsAt(int64(i), fmt.Sprintf("s%d", i%4), jan(i)))
		}
		first := groupForRollup(notes, 2, 10)
		for i := 0; i < 5; i++ {
			again := groupForRollup(notes, 2, 10)
			require.Len(t, again, len(first))
			for j := range first {
				assert.Equal(t, first[j].Label, again[j].Label)
				assert.Equal(t, idsOfGroup(first[j]), idsOfGroup(again[j]))
			}
		}
	})

	t.Run("nothing in, nothing out", func(t *testing.T) {
		assert.Empty(t, groupForRollup(nil, 2, 10))
	})
}

func TestRollupObservation(t *testing.T) {
	g := rollupGroup{Label: "2026-01", Notes: []*models.Observation{
		{ID: 1, Type: models.ObsTypeBugfix, CreatedAtEpoch: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC).UnixMilli(), Concepts: []string{"cache", "rounding"}},
		{ID: 2, Type: models.ObsTypeBugfix, CreatedAtEpoch: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC).UnixMilli(), Concepts: []string{"cache", gorm.RollupConcept}},
		{ID: 3, Type: models.ObsTypeChange, CreatedAtEpoch: time.Date(2026, 1, 21, 0, 0, 0, 0, time.UTC).UnixMilli(), Concepts: []string{"best-practice"}},
	}}
	o := rollupObservation(g, &sdk.RollupResult{Title: "Cache fixes", Body: "- a point [#1]."})
	assert.Equal(t, "Roll-up: Cache fixes", o.Title)
	assert.Equal(t, models.ObsTypeBugfix, o.Type, "the most common type")
	assert.Equal(t, models.ScopeProject, o.Scope, "a roll-up never leaves its project, whatever concepts its sources had")
	assert.Equal(t, gorm.RollupConcept, o.Concepts[0])
	assert.Equal(t, 1, countOf(o.Concepts, gorm.RollupConcept), "the marker once")
	assert.Contains(t, o.Concepts, "cache")
	assert.Contains(t, o.Narrative, "- a point [#1].")
	assert.Contains(t, o.Narrative, "Rolled up from 3 notes (2026-01-03 to 2026-01-21)")
	assert.Contains(t, o.Narrative, "archived, not deleted")

	untitled := rollupObservation(g, &sdk.RollupResult{Body: "- a point."})
	assert.Equal(t, "Roll-up: notes of 2026-01", untitled.Title)
}

func countOf(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

// ---- the flow, with a fake writer and the real stores ----

type fakeRollupWriter struct {
	err    error
	calls  [][]int64
	inputs []sdk.RollupInput
	mu     sync.Mutex
}

func (f *fakeRollupWriter) write(_ context.Context, in sdk.RollupInput) (*sdk.RollupResult, error) {
	var ids []int64
	var cites []string
	for _, o := range in.Observations {
		ids = append(ids, o.ID)
		cites = append(cites, fmt.Sprintf("#%d", o.ID))
	}
	f.mu.Lock()
	f.calls = append(f.calls, ids)
	f.inputs = append(f.inputs, in)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &sdk.RollupResult{Title: "What happened", Body: "- The notes were condensed [" + strings.Join(cites[:1], "") + "]."}, nil
}

func rollupService(t *testing.T, mutate func(*config.Config)) (*Service, *fakeRollupWriter, func()) {
	t.Helper()
	svc, cleanup := testService(t)
	cfg := *config.Default()
	cfg.RollupEnabled = true
	cfg.RollupMinAgeDays = 60
	cfg.RollupMinGroupSize = 5
	cfg.RollupKeepNewest = 0
	cfg.RollupMaxGroupsPerRun = 3
	if mutate != nil {
		mutate(&cfg)
	}
	svc.config = &cfg
	w := &fakeRollupWriter{}
	svc.rollupWriter = w.write
	return svc, w, cleanup
}

// oldMonth is the middle of a month that is long enough ago to be old, whatever today is.
func oldMonth() time.Time {
	m := time.Now().UTC().AddDate(0, -5, 0)
	return time.Date(m.Year(), m.Month(), 10, 12, 0, 0, 0, time.UTC)
}

// addOldNotes stores n distinct notes dated a few minutes apart in the old month and returns their ids.
func addOldNotes(t *testing.T, svc *Service, project string, n int, typ models.ObservationType) []int64 {
	t.Helper()
	var ids []int64
	base := oldMonth()
	for i := 0; i < n; i++ {
		seq := observationSeq.Add(1)
		id, _, err := svc.observationStore.StoreObservation(context.Background(), "sdk-"+project, project,
			&models.ParsedObservation{Type: typ, Title: fmt.Sprintf("old note %d", seq), Narrative: fmt.Sprintf("something different %d in %s", seq, project)}, 1, 1)
		require.NoError(t, err)
		require.NoError(t, svc.observationStore.SetObservationCreated(context.Background(), id, base.Add(time.Duration(i)*time.Minute).UnixMilli()))
		ids = append(ids, id)
	}
	return ids
}

func liveIDs(t *testing.T, svc *Service, project string) map[int64]*models.Observation {
	t.Helper()
	obs, err := svc.observationStore.GetRecentObservations(context.Background(), project, 1000)
	require.NoError(t, err)
	out := map[int64]*models.Observation{}
	for _, o := range obs {
		out[o.ID] = o
	}
	return out
}

func TestRollupProject_DryRunWritesNothing(t *testing.T) {
	svc, w, cleanup := rollupService(t, nil)
	defer cleanup()
	ids := addOldNotes(t, svc, "shop_aaaaaa", 8, models.ObsTypeDiscovery)
	failures := 0

	rep, err := svc.rollupProject(context.Background(), "shop_aaaaaa", 0, true, &failures)
	require.NoError(t, err)
	assert.True(t, rep.DryRun)
	assert.Equal(t, 8, rep.Candidates)
	require.Len(t, rep.Groups, 1)
	assert.Equal(t, ids, rep.Groups[0].IDs)
	assert.Empty(t, w.calls, "a preview never asks the model")
	assert.Len(t, liveIDs(t, svc, "shop_aaaaaa"), 8, "and archives nothing")
	folds, err := gorm.NewObservationFoldStore(svc.store).List(context.Background(), "", "", true, 0)
	require.NoError(t, err)
	assert.Empty(t, folds)
}

func TestRollupProject_CondensesArchivesAndRecords(t *testing.T) {
	svc, w, cleanup := rollupService(t, nil)
	defer cleanup()
	ctx := context.Background()
	old := addOldNotes(t, svc, "shop_aaaaaa", 8, models.ObsTypeBugfix)
	decision := addOldNotes(t, svc, "shop_aaaaaa", 1, models.ObsTypeDecision)[0]
	other := addOldNotes(t, svc, "other_bbbbbb", 8, models.ObsTypeDiscovery)
	addObs(t, svc, "shop_aaaaaa", 3) // recent: not old enough
	failures := 0

	rep, err := svc.rollupProject(ctx, "shop_aaaaaa", 0, false, &failures)
	require.NoError(t, err)
	require.Len(t, rep.Groups, 1)
	g := rep.Groups[0]
	require.Empty(t, g.Error)
	assert.Equal(t, 8, g.Archived)
	require.NotZero(t, g.RollupID)
	require.NotZero(t, g.FoldID)

	require.Len(t, w.calls, 1)
	assert.Equal(t, old, w.calls[0], "the model got the old notes, not the decision, the recent ones or another project's")

	live := liveIDs(t, svc, "shop_aaaaaa")
	for _, id := range old {
		assert.NotContains(t, live, id, "the originals are archived")
	}
	assert.Contains(t, live, decision, "a decision is never rolled up")
	rollup := live[g.RollupID]
	require.NotNil(t, rollup, "the roll-up is a live note")
	assert.Contains(t, rollup.Concepts, gorm.RollupConcept)
	assert.Equal(t, models.ScopeProject, rollup.Scope)
	assert.Equal(t, models.ObsTypeBugfix, rollup.Type)
	assert.Equal(t, "Roll-up: What happened", rollup.Title.String)

	// Dated at the newest note it condenses (no cap is set), so it does not look like new work.
	newestSource := oldMonth().Add(7 * time.Minute).UnixMilli()
	assert.Equal(t, newestSource, rollup.CreatedAtEpoch)

	// The originals are kept, archived with a link to the roll-up, and the other project is untouched.
	var archivedReason string
	require.NoError(t, svc.store.DB.Raw("SELECT archived_reason FROM observations WHERE id = ?", old[0]).Scan(&archivedReason).Error)
	assert.Equal(t, fmt.Sprintf("rolled-up into #%d", g.RollupID), archivedReason)
	assert.Len(t, liveIDs(t, svc, "other_bbbbbb"), len(other))

	fold, err := gorm.NewObservationFoldStore(svc.store).Get(ctx, g.FoldID)
	require.NoError(t, err)
	require.NotNil(t, fold)
	assert.Equal(t, gorm.FoldRollup, fold.Kind)
	assert.Equal(t, g.RollupID, fold.SurvivorID)
	assert.ElementsMatch(t, old, fold.Sources())

	// A second run finds nothing left to roll up: the roll-up itself is protected, the rest is protected or too new.
	rep, err = svc.rollupProject(ctx, "shop_aaaaaa", 0, false, &failures)
	require.NoError(t, err)
	assert.Empty(t, rep.Groups)
	assert.Len(t, w.calls, 1)
}

func TestRollupProject_WithACapTheRollupKeepsTodaysDate(t *testing.T) {
	svc, _, cleanup := rollupService(t, func(c *config.Config) { c.MaxObservationsPerProject = 500 })
	defer cleanup()
	addOldNotes(t, svc, "shop_aaaaaa", 6, models.ObsTypeDiscovery)
	failures := 0
	before := time.Now().Add(-time.Minute).UnixMilli()

	rep, err := svc.rollupProject(context.Background(), "shop_aaaaaa", 0, false, &failures)
	require.NoError(t, err)
	require.Len(t, rep.Groups, 1)
	rollup := liveIDs(t, svc, "shop_aaaaaa")[rep.Groups[0].RollupID]
	require.NotNil(t, rollup)
	assert.Greater(t, rollup.CreatedAtEpoch, before, "the cap archives the oldest notes first, so a backdated roll-up could be its first victim")
}

func TestRollupProject_NoModelMeansNothingChanges(t *testing.T) {
	svc, w, cleanup := rollupService(t, nil)
	defer cleanup()
	ctx := context.Background()
	old := addOldNotes(t, svc, "shop_aaaaaa", 8, models.ObsTypeDiscovery)
	w.err = errors.New("claude: not found")
	failures := 0

	rep, err := svc.rollupProject(ctx, "shop_aaaaaa", 0, false, &failures)
	require.NoError(t, err)
	require.Len(t, rep.Groups, 1)
	assert.Contains(t, rep.Groups[0].Error, "not found")
	assert.Zero(t, rep.Groups[0].Archived)
	assert.Equal(t, 1, failures)

	live := liveIDs(t, svc, "shop_aaaaaa")
	assert.Len(t, live, len(old), "every note is still live, and no roll-up was stored")
	for _, o := range live {
		assert.NotContains(t, o.Concepts, gorm.RollupConcept)
	}
	folds, err := gorm.NewObservationFoldStore(svc.store).List(ctx, "", "", true, 0)
	require.NoError(t, err)
	assert.Empty(t, folds)

	// With nothing that can write at all, the request says so.
	svc.rollupWriter = nil
	svc.processor = nil
	_, err = svc.rollupProject(ctx, "shop_aaaaaa", 0, false, &failures)
	assert.ErrorIs(t, err, ErrRollupUnavailable)
}

func TestRunRollupPass_BudgetAndStopsAfterRepeatedFailures(t *testing.T) {
	t.Run("at most the configured number of groups in all", func(t *testing.T) {
		svc, w, cleanup := rollupService(t, func(c *config.Config) { c.RollupMaxGroupsPerRun = 2 })
		defer cleanup()
		for _, p := range []string{"a_aaaaaa", "b_bbbbbb", "c_cccccc"} {
			addOldNotes(t, svc, p, 6, models.ObsTypeDiscovery)
		}
		assert.Equal(t, 2, svc.runRollupPass(context.Background()))
		assert.Len(t, w.calls, 2)
	})

	t.Run("a model that keeps failing is not asked again and again", func(t *testing.T) {
		svc, w, cleanup := rollupService(t, func(c *config.Config) { c.RollupMaxGroupsPerRun = 10 })
		defer cleanup()
		w.err = errors.New("down")
		for _, p := range []string{"a_aaaaaa", "b_bbbbbb", "c_cccccc", "d_dddddd"} {
			addOldNotes(t, svc, p, 6, models.ObsTypeDiscovery)
		}
		assert.Equal(t, 0, svc.runRollupPass(context.Background()))
		assert.Len(t, w.calls, rollupMaxFailuresInARow)
	})

	t.Run("no model at all: logged, nothing changes", func(t *testing.T) {
		svc, _, cleanup := rollupService(t, nil)
		defer cleanup()
		svc.rollupWriter, svc.processor = nil, nil
		addOldNotes(t, svc, "a_aaaaaa", 6, models.ObsTypeDiscovery)
		assert.Equal(t, 0, svc.runRollupPass(context.Background()))
		assert.Len(t, liveIDs(t, svc, "a_aaaaaa"), 6)
	})
}

func TestRestoreFold_Rollup(t *testing.T) {
	svc, _, cleanup := rollupService(t, nil)
	defer cleanup()
	ctx := context.Background()
	old := addOldNotes(t, svc, "shop_aaaaaa", 8, models.ObsTypeDiscovery)
	failures := 0
	rep, err := svc.rollupProject(ctx, "shop_aaaaaa", 0, false, &failures)
	require.NoError(t, err)
	require.Len(t, rep.Groups, 1)
	g := rep.Groups[0]

	// A person archived one of the originals again, for their own reason, before restoring.
	require.NoError(t, svc.observationStore.UnarchiveObservation(ctx, old[0]))
	require.NoError(t, svc.observationStore.ArchiveObservation(ctx, old[0], "my own reason"))

	res, err := svc.restoreFold(ctx, g.FoldID)
	require.NoError(t, err)
	assert.Equal(t, gorm.FoldRollup, res.Kind)
	assert.True(t, res.SurvivorArchived)
	assert.ElementsMatch(t, old[1:], res.Restored)
	assert.Equal(t, []int64{old[0]}, res.Kept, "a note archived for another reason stays archived")

	live := liveIDs(t, svc, "shop_aaaaaa")
	assert.NotContains(t, live, g.RollupID, "the roll-up is archived again")
	for _, id := range old[1:] {
		assert.Contains(t, live, id)
	}
	assert.NotContains(t, live, old[0])

	_, err = svc.restoreFold(ctx, g.FoldID)
	assert.ErrorIs(t, err, ErrFoldUndone)
	_, err = svc.restoreFold(ctx, 99999)
	assert.ErrorIs(t, err, ErrFoldNotFound)

	// A person restored them on purpose: they are left alone for a while, so the next pass does not fold them again.
	again, err := svc.rollupProject(ctx, "shop_aaaaaa", 0, true, &failures)
	require.NoError(t, err)
	assert.Equal(t, 0, again.Candidates, "restored notes are not offered for the restore grace period")
	assert.Empty(t, again.Groups)

	// After the grace period they qualify again.
	longAgo := time.Now().Add(-(rollupRestoreGraceDays + 1) * 24 * time.Hour).UnixMilli()
	require.NoError(t, svc.store.DB.Exec("UPDATE observation_folds SET undone_at_epoch = ?", longAgo).Error)
	later, err := svc.rollupProject(ctx, "shop_aaaaaa", 0, true, &failures)
	require.NoError(t, err)
	assert.Equal(t, 7, later.Candidates, "the seven notes that came back qualify again")
}

func TestRollupHandlers(t *testing.T) {
	svc, _, cleanup := rollupService(t, nil)
	defer cleanup()
	addOldNotes(t, svc, "shop_aaaaaa", 8, models.ObsTypeDiscovery)

	t.Run("a preview, then the roll-up, then the list, then the restore", func(t *testing.T) {
		rec := doRequest(t, svc, http.MethodPost, "/api/projects/shop_aaaaaa/rollup", map[string]any{"dry_run": true})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var preview RollupReport
		decode(t, rec, &preview)
		assert.True(t, preview.DryRun)
		require.Len(t, preview.Groups, 1)

		rec = doRequest(t, svc, http.MethodPost, "/api/projects/shop_aaaaaa/rollup", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var done RollupReport
		decode(t, rec, &done)
		require.Len(t, done.Groups, 1)
		require.Empty(t, done.Groups[0].Error)

		rec = doRequest(t, svc, http.MethodGet, "/api/folds?project=shop_aaaaaa", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var list struct{ Folds []FoldItem }
		decode(t, rec, &list)
		require.Len(t, list.Folds, 1)
		assert.Equal(t, "Roll-up: What happened", list.Folds[0].SurvivorTitle)
		assert.Len(t, list.Folds[0].Sources, 8)

		rec = doRequest(t, svc, http.MethodPost, fmt.Sprintf("/api/folds/%d/restore", list.Folds[0].ID), nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = doRequest(t, svc, http.MethodPost, fmt.Sprintf("/api/folds/%d/restore", list.Folds[0].ID), nil)
		assert.Equal(t, http.StatusConflict, rec.Code, "a second restore is refused")

		rec = doRequest(t, svc, http.MethodGet, "/api/folds?project=shop_aaaaaa", nil)
		decode(t, rec, &list)
		assert.Empty(t, list.Folds, "an undone fold is not listed")
		rec = doRequest(t, svc, http.MethodGet, "/api/folds?project=shop_aaaaaa&include_undone=true", nil)
		decode(t, rec, &list)
		assert.Len(t, list.Folds, 1)
	})

	t.Run("bad input", func(t *testing.T) {
		assert.Equal(t, http.StatusUnprocessableEntity, doRequest(t, svc, http.MethodPost, "/api/projects/nothing_cccccc/rollup", nil).Code)
		assert.Equal(t, http.StatusNotFound, doRequest(t, svc, http.MethodPost, "/api/folds/424242/restore", nil).Code)
		assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPost, "/api/folds/abc/restore", nil).Code)
		assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, "/api/folds?kind=nonsense", nil).Code)
	})
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), into), rec.Body.String())
}

func TestRollupProject_ACallerThatGivesUpHalfWayDoesNotLeaveNotesArchivedWithoutARecord(t *testing.T) {
	svc, _, cleanup := rollupService(t, nil)
	defer cleanup()
	old := addOldNotes(t, svc, "shop_aaaaaa", 8, models.ObsTypeDiscovery)

	// The caller's context ends the moment the model has answered, before the notes are stored and archived.
	ctx, cancel := context.WithCancel(context.Background())
	inner := &fakeRollupWriter{}
	svc.rollupWriter = func(c context.Context, in sdk.RollupInput) (*sdk.RollupResult, error) {
		res, err := inner.write(c, in)
		cancel()
		return res, err
	}
	failures := 0
	rep, err := svc.rollupProject(ctx, "shop_aaaaaa", 0, false, &failures)
	require.NoError(t, err)
	require.Len(t, rep.Groups, 1)
	require.Empty(t, rep.Groups[0].Error, "the group is committed even though the caller has gone")

	live := liveIDs(t, svc, "shop_aaaaaa")
	assert.Contains(t, live, rep.Groups[0].RollupID)
	for _, id := range old {
		assert.NotContains(t, live, id)
	}
	fold, err := gorm.NewObservationFoldStore(svc.store).Get(context.Background(), rep.Groups[0].FoldID)
	require.NoError(t, err)
	require.NotNil(t, fold, "and it has the record that restores it")
}

func TestObservationsListArchivedOnly(t *testing.T) {
	svc, _, cleanup := rollupService(t, nil)
	defer cleanup()
	ctx := context.Background()
	old := addOldNotes(t, svc, "shop_aaaaaa", 8, models.ObsTypeDiscovery)
	addObs(t, svc, "shop_aaaaaa", 2)
	failures := 0
	rep, err := svc.rollupProject(ctx, "shop_aaaaaa", 0, false, &failures)
	require.NoError(t, err)
	require.Len(t, rep.Groups, 1)

	rec := doRequest(t, svc, http.MethodGet, "/api/observations?project=shop_aaaaaa&archived_only=true", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var archived struct {
		Observations []map[string]any `json:"observations"`
		Total        int              `json:"total"`
	}
	decode(t, rec, &archived)
	assert.Equal(t, 8, archived.Total)
	require.Len(t, archived.Observations, 8)
	ids := map[float64]bool{}
	for _, o := range archived.Observations {
		ids[o["id"].(float64)] = true
		assert.Equal(t, true, o["is_archived"])
		assert.Equal(t, fmt.Sprintf("rolled-up into #%d", rep.Groups[0].RollupID), o["archived_reason"])
	}
	for _, id := range old {
		assert.True(t, ids[float64(id)])
	}

	rec = doRequest(t, svc, http.MethodGet, "/api/observations?project=shop_aaaaaa", nil)
	var live struct {
		Observations []map[string]any `json:"observations"`
	}
	decode(t, rec, &live)
	assert.Len(t, live.Observations, 3, "the usual list is the two recent notes and the roll-up")
	for _, o := range live.Observations {
		assert.NotContains(t, o, "is_archived")
	}
}
