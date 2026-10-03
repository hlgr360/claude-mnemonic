//go:build fts5

// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func addObservation(t *testing.T, obs *ObservationStore, project, title string, scope models.ObservationScope) int64 {
	t.Helper()
	id, _, err := obs.StoreObservation(context.Background(), "sdk-"+project, project,
		&models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: title, Narrative: "narrative of " + title, Scope: scope}, 1, 1)
	require.NoError(t, err)
	time.Sleep(3 * time.Millisecond)
	return id
}

func TestBrief_UpsertReplacesInPlaceAndGetReturnsIt(t *testing.T) {
	_, store, cleanup := testObservationStore(t)
	defer cleanup()
	s := NewSummaryStore(store)
	ctx := context.Background()

	got, err := s.GetBrief(ctx, "p_aaaaaa")
	require.NoError(t, err)
	assert.Nil(t, got, "a project without a brief has none")

	id1, created, err := s.UpsertBrief(ctx, "p_aaaaaa", "first text", "10 of 10 observations")
	require.NoError(t, err)
	assert.True(t, created)
	time.Sleep(5 * time.Millisecond)
	id2, created, err := s.UpsertBrief(ctx, "p_aaaaaa", "second text", "12 of 12 observations")
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, id1, id2, "one row per project, replaced in place")

	got, err = s.GetBrief(ctx, "p_aaaaaa")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "second text", got.Text)
	assert.Equal(t, "12 of 12 observations", got.Source)
	assert.Equal(t, id1, got.ID)
	assert.Greater(t, got.GeneratedEpoch, int64(0))

	var n int64
	require.NoError(t, store.DB.Raw(`SELECT COUNT(*) FROM session_summaries WHERE project = ?`, "p_aaaaaa").Scan(&n).Error)
	assert.Equal(t, int64(1), n)
}

func TestBrief_ProjectsAreSeparateAndNotMistakenForThreadNotes(t *testing.T) {
	_, store, cleanup := testObservationStore(t)
	defer cleanup()
	s := NewSummaryStore(store)
	ctx := context.Background()

	_, _, err := s.UpsertBrief(ctx, "a_111111", "brief of a", "src")
	require.NoError(t, err)
	_, _, err = s.UpsertBrief(ctx, "b_222222", "brief of b", "src")
	require.NoError(t, err)
	_, _, err = s.UpsertThreadSummary(ctx, ThreadSessionPrefix+"a_111111-work", "a_111111", &models.ParsedSummary{Request: "Work", Notes: "goal"})
	require.NoError(t, err)

	a, _ := s.GetBrief(ctx, "a_111111")
	b, _ := s.GetBrief(ctx, "b_222222")
	assert.Equal(t, "brief of a", a.Text)
	assert.Equal(t, "brief of b", b.Text)

	threads, err := s.GetThreadSummaries(ctx, "a_111111", 10)
	require.NoError(t, err)
	require.Len(t, threads, 1, "the brief is not a thread note")
	assert.Equal(t, "Work", threads[0].Request.String)
	assert.Equal(t, "brief-a_111111", BriefSessionID("a_111111"))
}

func TestBriefInputs_OnlyLiveProjectObservationsOldestFirstWithTheTotal(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	first := addObservation(t, obs, "p_aaaaaa", "first", models.ScopeProject)
	archived := addObservation(t, obs, "p_aaaaaa", "archived", models.ScopeProject)
	superseded := addObservation(t, obs, "p_aaaaaa", "superseded", models.ScopeProject)
	last := addObservation(t, obs, "p_aaaaaa", "last", models.ScopeProject)
	addObservation(t, obs, "p_aaaaaa", "global one", models.ScopeGlobal)
	addObservation(t, obs, "q_bbbbbb", "other project", models.ScopeProject)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = ?`, archived).Error)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, superseded).Error)

	got, total, err := obs.BriefInputs(ctx, "p_aaaaaa", 50)
	require.NoError(t, err)
	var ids []int64
	for _, o := range got {
		ids = append(ids, o.ID)
	}
	assert.Equal(t, []int64{first, last}, ids, "archived, superseded, global and other projects' observations are left out; oldest first")
	assert.Equal(t, 2, total)
}

func TestBriefInputs_LimitKeepsTheMostImportantAndStillReadsOldestFirst(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	var ids []int64
	for _, title := range []string{"one", "two", "three", "four", "five"} {
		ids = append(ids, addObservation(t, obs, "p_aaaaaa", title, models.ScopeProject))
	}
	require.NoError(t, store.DB.Exec(`UPDATE observations SET importance_score = 5 WHERE id IN (?, ?)`, ids[1], ids[3]).Error)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET importance_score = 0.1 WHERE id IN (?, ?, ?)`, ids[0], ids[2], ids[4]).Error)

	got, total, err := obs.BriefInputs(ctx, "p_aaaaaa", 2)
	require.NoError(t, err)
	assert.Equal(t, 5, total, "the total is not limited")
	require.Len(t, got, 2)
	assert.Equal(t, []int64{ids[1], ids[3]}, []int64{got[0].ID, got[1].ID}, "the two important ones, in the order they happened")

	none, total, err := obs.BriefInputs(ctx, "nothing_000000", 10)
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.Zero(t, total)
}

func TestCountLiveObservationsSince(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	addObservation(t, obs, "p_aaaaaa", "old", models.ScopeProject)
	time.Sleep(10 * time.Millisecond)
	cutoff := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)
	a := addObservation(t, obs, "p_aaaaaa", "new a", models.ScopeProject)
	addObservation(t, obs, "p_aaaaaa", "new b", models.ScopeProject)
	addObservation(t, obs, "p_aaaaaa", "new global", models.ScopeGlobal)
	addObservation(t, obs, "q_bbbbbb", "new elsewhere", models.ScopeProject)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = ?`, a).Error)

	n, err := obs.CountLiveObservationsSince(ctx, "p_aaaaaa", cutoff)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the live, project-scoped, newer observation of this project")

	all, err := obs.CountLiveObservationsSince(ctx, "p_aaaaaa", 0)
	require.NoError(t, err)
	assert.Equal(t, 2, all)
}
