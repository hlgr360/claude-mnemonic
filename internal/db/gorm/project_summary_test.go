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
