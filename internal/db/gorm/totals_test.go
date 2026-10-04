//go:build fts5

package gorm

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// seedProject gives a project a session with n observations, p prompts and s summaries.
func seedProject(t *testing.T, store *Store, obsStore *ObservationStore, project string, n, p, s int) {
	t.Helper()
	ctx := context.Background()
	sessionStore := NewSessionStore(store)
	claudeID := "claude-" + project
	sid, err := sessionStore.CreateSDKSession(ctx, claudeID, project, "")
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		_, _, err := obsStore.StoreObservation(ctx, claudeID, project, &models.ParsedObservation{
			Type: models.ObsTypeDiscovery, Title: fmt.Sprintf("%s note %d", project, i), Narrative: fmt.Sprintf("distinct text %s %d", project, i),
		}, int(sid), 1)
		require.NoError(t, err)
	}
	promptStore := NewPromptStore(store, nil)
	for i := 0; i < p; i++ {
		_, err := promptStore.SaveUserPromptWithMatches(ctx, claudeID, i+1, fmt.Sprintf("prompt %d of %s", i, project), 0)
		require.NoError(t, err)
	}
	summaryStore := NewSummaryStore(store)
	for i := 0; i < s; i++ {
		_, _, err := summaryStore.StoreSummary(ctx, claudeID, project, &models.ParsedSummary{Request: fmt.Sprintf("request %d", i)}, i+1, 1)
		require.NoError(t, err)
	}
}

func TestTotals_CountsWhatTheListsHoldForOneProjectOrAll(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	seedProject(t, store, obsStore, "proj_aaaaaa", 5, 3, 2)
	seedProject(t, store, obsStore, "proj_bbbbbb", 2, 1, 4)

	all, err := store.Totals(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, Totals{Observations: 7, Prompts: 4, Summaries: 6}, all)

	a, err := store.Totals(ctx, "proj_aaaaaa")
	require.NoError(t, err)
	assert.Equal(t, Totals{Observations: 5, Prompts: 3, Summaries: 2}, a)

	none, err := store.Totals(ctx, "proj_nothing")
	require.NoError(t, err)
	assert.Equal(t, Totals{}, none)
}

func TestTotals_AgreeWithTheListQueries(t *testing.T) {
	obsStore, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	seedProject(t, store, obsStore, "proj_aaaaaa", 4, 3, 2)
	seedProject(t, store, obsStore, "proj_bbbbbb", 2, 2, 1)
	// A superseded and an archived observation are still in the feed, so they are still counted.
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = 1`).Error)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = 2`).Error)

	for _, project := range []string{"", "proj_aaaaaa", "proj_bbbbbb"} {
		var listedObs int64
		if project == "" {
			_, listedObs, _ = obsStore.GetAllRecentObservationsPaginated(ctx, 1, 0)
		} else {
			_, listedObs, _ = obsStore.GetObservationsByProjectStrictPaginated(ctx, project, 1, 0)
		}
		var listedPrompts int
		if project == "" {
			rows, err := NewPromptStore(store, nil).GetAllRecentUserPrompts(ctx, 1000)
			require.NoError(t, err)
			listedPrompts = len(rows)
		} else {
			rows, err := NewPromptStore(store, nil).GetRecentUserPromptsByProject(ctx, project, 1000)
			require.NoError(t, err)
			listedPrompts = len(rows)
		}
		var listedSummaries int
		if project == "" {
			rows, err := NewSummaryStore(store).GetAllRecentSummaries(ctx, 1000)
			require.NoError(t, err)
			listedSummaries = len(rows)
		} else {
			rows, err := NewSummaryStore(store).GetRecentSummaries(ctx, project, 1000)
			require.NoError(t, err)
			listedSummaries = len(rows)
		}

		got, err := store.Totals(ctx, project)
		require.NoError(t, err)
		assert.Equal(t, listedObs, got.Observations, "observations, project %q", project)
		assert.EqualValues(t, listedPrompts, got.Prompts, "prompts, project %q", project)
		assert.EqualValues(t, listedSummaries, got.Summaries, "summaries, project %q", project)
	}
}
