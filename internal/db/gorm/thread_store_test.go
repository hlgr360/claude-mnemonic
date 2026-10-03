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

func newThreadFixture(t *testing.T) (*SummaryStore, *Store, *ObservationStore) {
	t.Helper()
	obs, store, cleanup := testObservationStore(t)
	t.Cleanup(cleanup)
	return NewSummaryStore(store), store, obs
}

func note(title, goal, progress string) *models.ParsedSummary {
	return &models.ParsedSummary{Request: title, Notes: goal, Investigated: progress}
}

func TestUpsertThreadSummary_CreatesThenUpdatesInPlace(t *testing.T) {
	s, store, _ := newThreadFixture(t)
	ctx := context.Background()

	id1, created, err := s.UpsertThreadSummary(ctx, "thread-p_aaaaaa-overlay", "p_aaaaaa", note("Overlay", "ship it", "step 1 done"))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Greater(t, id1, int64(0))

	time.Sleep(5 * time.Millisecond)
	update := note("Overlay", "ship it", "steps 1 and 2 done")
	update.Learned = "chose names over ids"
	update.NextSteps = "write docs"
	id2, created, err := s.UpsertThreadSummary(ctx, "thread-p_aaaaaa-overlay", "p_aaaaaa", update)
	require.NoError(t, err)
	assert.False(t, created, "the same thread is updated, not duplicated")
	assert.Equal(t, id1, id2, "the row keeps its id (and so its search documents)")

	var n int64
	require.NoError(t, store.DB.Raw(`SELECT COUNT(*) FROM session_summaries WHERE project = ?`, "p_aaaaaa").Scan(&n).Error)
	assert.Equal(t, int64(1), n)

	got, err := s.GetThreadSummaries(ctx, "p_aaaaaa", 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "steps 1 and 2 done", got[0].Investigated.String)
	assert.Equal(t, "chose names over ids", got[0].Learned.String)
	assert.Equal(t, "write docs", got[0].NextSteps.String)
}

func TestUpsertThreadSummary_UpdateClearsFieldsThatWereLeftOut(t *testing.T) {
	s, _, _ := newThreadFixture(t)
	ctx := context.Background()
	first := note("T", "g", "p")
	first.NextSteps = "old next step"
	_, _, err := s.UpsertThreadSummary(ctx, "thread-p-t", "p_aaaaaa", first)
	require.NoError(t, err)
	_, _, err = s.UpsertThreadSummary(ctx, "thread-p-t", "p_aaaaaa", note("T", "g", "p2"))
	require.NoError(t, err)

	got, _ := s.GetThreadSummaries(ctx, "p_aaaaaa", 5)
	require.Len(t, got, 1)
	assert.False(t, got[0].NextSteps.Valid, "a note is the current state: an open item that was resolved is gone, not left behind")
}

func TestUpsertThreadSummary_ThreadsAndProjectsAreSeparate(t *testing.T) {
	s, _, _ := newThreadFixture(t)
	ctx := context.Background()
	for _, c := range []struct{ sdk, project, title string }{
		{"thread-a-one", "a_111111", "One"}, {"thread-a-two", "a_111111", "Two"}, {"thread-b-one", "b_222222", "One"},
	} {
		_, created, err := s.UpsertThreadSummary(ctx, c.sdk, c.project, note(c.title, "g", "p"))
		require.NoError(t, err)
		assert.True(t, created, c.sdk)
		time.Sleep(3 * time.Millisecond)
	}
	a, _ := s.GetThreadSummaries(ctx, "a_111111", 10)
	b, _ := s.GetThreadSummaries(ctx, "b_222222", 10)
	assert.Len(t, a, 2)
	assert.Len(t, b, 1, "a thread with the same title in another project is its own note")
}

func TestGetThreadSummaries_NewestFirstLimitedAndOnlyThreads(t *testing.T) {
	s, store, _ := newThreadFixture(t)
	ctx := context.Background()
	for _, name := range []string{"old", "middle", "new"} {
		_, _, err := s.UpsertThreadSummary(ctx, "thread-p-"+name, "p_aaaaaa", note(name, "g", "p"))
		require.NoError(t, err)
		time.Sleep(4 * time.Millisecond)
	}
	// an end-of-session summary written by Claude Code is not a thread note
	_, _, err := s.StoreSummary(ctx, "claude-session-1", "p_aaaaaa", &models.ParsedSummary{Request: "from claude code"}, 1, 0)
	require.NoError(t, err)
	_ = store

	got, err := s.GetThreadSummaries(ctx, "p_aaaaaa", 2)
	require.NoError(t, err)
	require.Len(t, got, 2, "limit applies")
	assert.Equal(t, "new", got[0].Request.String)
	assert.Equal(t, "middle", got[1].Request.String)

	all, _ := s.GetThreadSummaries(ctx, "p_aaaaaa", 10)
	for _, g := range all {
		assert.NotEqual(t, "from claude code", g.Request.String)
	}

	def, _ := s.GetThreadSummaries(ctx, "p_aaaaaa", 0)
	assert.Len(t, def, 3, "a zero limit falls back to a default")
}

func TestUpdatingAThreadMovesItToTheFront(t *testing.T) {
	s, _, _ := newThreadFixture(t)
	ctx := context.Background()
	for _, n := range []string{"first", "second"} {
		_, _, _ = s.UpsertThreadSummary(ctx, "thread-p-"+n, "p_aaaaaa", note(n, "g", "p"))
		time.Sleep(4 * time.Millisecond)
	}
	time.Sleep(4 * time.Millisecond)
	_, _, err := s.UpsertThreadSummary(ctx, "thread-p-first", "p_aaaaaa", note("first", "g", "worked on again"))
	require.NoError(t, err)

	got, _ := s.GetThreadSummaries(ctx, "p_aaaaaa", 5)
	require.Len(t, got, 2)
	assert.Equal(t, "first", got[0].Request.String, "the thread worked on last is first")
}

func TestUpsertThreadSummary_WaitsForAConcurrentWriter(t *testing.T) {
	s, store, _ := newThreadFixture(t)
	ctx := context.Background()
	_, _, err := s.UpsertThreadSummary(ctx, "thread-p-t", "p_aaaaaa", note("T", "g", "p"))
	require.NoError(t, err)

	conn, err := store.sqlDB.Conn(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `UPDATE session_summaries SET notes = notes`)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(300 * time.Millisecond)
		_, _ = conn.ExecContext(ctx, `COMMIT`)
		_ = conn.Close()
	}()
	defer func() { <-done }()

	// the vector sync writes concurrently in the worker; a checkpoint right then must still succeed
	_, created, err := s.UpsertThreadSummary(ctx, "thread-p-t", "p_aaaaaa", note("T", "g", "updated while another writer held the lock"))
	require.NoError(t, err)
	assert.False(t, created)
}

func TestRecentDecisions(t *testing.T) {
	s, store, obsStore := newThreadFixture(t)
	ctx := context.Background()
	add := func(project, title string, typ models.ObservationType) int64 {
		id, _, err := obsStore.StoreObservation(ctx, "sdk-"+project, project,
			&models.ParsedObservation{Type: typ, Title: title, Subtitle: "because " + title, Narrative: "n"}, 1, 1)
		require.NoError(t, err)
		time.Sleep(3 * time.Millisecond)
		return id
	}
	add("p_aaaaaa", "older decision", models.ObsTypeDecision)
	add("p_aaaaaa", "a discovery", models.ObsTypeDiscovery)
	archived := add("p_aaaaaa", "archived decision", models.ObsTypeDecision)
	superseded := add("p_aaaaaa", "superseded decision", models.ObsTypeDecision)
	add("p_aaaaaa", "newest decision", models.ObsTypeDecision)
	add("q_bbbbbb", "other project decision", models.ObsTypeDecision)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = ?`, archived).Error)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, superseded).Error)

	got, err := s.RecentDecisions(ctx, "p_aaaaaa", 10)
	require.NoError(t, err)
	var titles []string
	for _, d := range got {
		titles = append(titles, d.Title)
	}
	assert.Equal(t, []string{"newest decision", "older decision"}, titles, "only live decisions of this project, newest first")
	assert.Equal(t, "because newest decision", got[0].Subtitle)

	one, _ := s.RecentDecisions(ctx, "p_aaaaaa", 1)
	assert.Len(t, one, 1)
	none, err := s.RecentDecisions(ctx, "nothing_000000", 5)
	require.NoError(t, err)
	assert.Empty(t, none)
}
