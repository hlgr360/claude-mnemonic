//go:build fts5

// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func TestSessionStore_ProjectSummaries(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	sessions := NewSessionStore(store)

	// projA: two sessions, no observations.
	_, err := sessions.CreateSDKSession(ctx, "claude-a1", "projA", "p")
	require.NoError(t, err)
	_, err = sessions.CreateSDKSession(ctx, "claude-a2", "projA", "p")
	require.NoError(t, err)

	// projB: one observation (its session is auto-created), then the session row is removed
	// so the project only survives through its observation.
	obs := &models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: "t", Narrative: "n"}
	_, _, err = obsStore.StoreObservation(ctx, "sdk-b", "projB", obs, 1, 10)
	require.NoError(t, err)
	require.NoError(t, store.DB.Exec("DELETE FROM sdk_sessions WHERE project = ?", "projB").Error)

	got, err := sessions.ProjectSummaries(ctx)
	require.NoError(t, err)

	byProject := map[string]ProjectSummary{}
	for _, p := range got {
		byProject[p.Project] = p
	}
	require.Len(t, byProject, 2)

	assert.Equal(t, int64(2), byProject["projA"].Sessions)
	assert.Equal(t, int64(0), byProject["projA"].Observations)
	assert.NotZero(t, byProject["projA"].LastActiveEpoch)

	assert.Equal(t, int64(0), byProject["projB"].Sessions)
	assert.Equal(t, int64(1), byProject["projB"].Observations)
	assert.NotZero(t, byProject["projB"].LastActiveEpoch)

	names, err := sessions.GetAllProjects(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"projA"}, names, "GetAllProjects only sees sessions; summaries also see observation-only projects")
}

func TestSessionStore_ProjectSummaries_ExcludesArchivedObservationsAndEmptyProjects(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	sessions := NewSessionStore(store)

	obs := &models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: "t", Narrative: "n"}
	id, _, err := obsStore.StoreObservation(ctx, "sdk-c", "projC", obs, 1, 10)
	require.NoError(t, err)
	require.NoError(t, store.DB.Exec("DELETE FROM sdk_sessions WHERE project = ?", "projC").Error)
	require.NoError(t, store.DB.Exec("UPDATE observations SET is_archived = 1 WHERE id = ?", id).Error)
	_, err = sessions.CreateSDKSession(ctx, "claude-blank", "", "p")
	require.NoError(t, err)

	got, err := sessions.ProjectSummaries(ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "archived-only and blank-named projects are not listed")
}

func TestSessionStore_ProjectSummaries_EmptyStore(t *testing.T) {
	_, store, cleanup := testObservationStore(t)
	defer cleanup()

	got, err := NewSessionStore(store).ProjectSummaries(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSessionStore_ProjectSampleTitles(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	sessions := NewSessionStore(store)

	add := func(project, title string, importance float64, archived bool) int64 {
		obs := &models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: title, Narrative: "n"}
		id, _, err := obsStore.StoreObservation(ctx, "sdk-"+project, project, obs, 1, 1)
		require.NoError(t, err)
		require.NoError(t, store.DB.Exec(`UPDATE observations SET importance_score = ?, is_archived = ? WHERE id = ?`, importance, archived, id).Error)
		return id
	}
	add("a_111111", "least important", 0.5, false)
	add("a_111111", "most important", 3.0, false)
	add("a_111111", "middle", 1.5, false)
	add("a_111111", "archived but important", 9.0, true)
	add("b_222222", "only one", 1.0, false)
	add("c_333333", "not asked for", 1.0, false)

	got, err := sessions.ProjectSampleTitles(ctx, []string{"a_111111", "b_222222", "nothing_000000"}, 2)
	require.NoError(t, err)

	assert.Equal(t, []string{"most important", "middle"}, got["a_111111"], "top two by importance, archived ones skipped")
	assert.Equal(t, []string{"only one"}, got["b_222222"])
	assert.NotContains(t, got, "c_333333", "only the requested projects")
	assert.NotContains(t, got, "nothing_000000")

	empty, err := sessions.ProjectSampleTitles(ctx, nil, 2)
	require.NoError(t, err)
	assert.Empty(t, empty)
	zero, err := sessions.ProjectSampleTitles(ctx, []string{"a_111111"}, 0)
	require.NoError(t, err)
	assert.Empty(t, zero)
}
