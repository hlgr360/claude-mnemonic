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

func TestProjectIdentity_RecordIsAnUpsertAndIgnoresEmptyIdentities(t *testing.T) {
	_, store, cleanup := testObservationStore(t)
	defer cleanup()
	s := NewProjectIdentityStore(store)
	ctx := context.Background()

	require.NoError(t, s.RecordIdentity(ctx, "shop_aaaaaa", "git.example.org/team/shop", "/work/shop"))
	first, err := s.Identities(ctx)
	require.NoError(t, err)
	require.Len(t, first["shop_aaaaaa"], 1)
	seen := first["shop_aaaaaa"][0]

	time.Sleep(5 * time.Millisecond)
	require.NoError(t, s.RecordIdentity(ctx, "shop_aaaaaa", "git.example.org/team/shop", "/work/shop"))
	again, _ := s.Identities(ctx)
	require.Len(t, again["shop_aaaaaa"], 1, "the same identity again is one row")
	assert.Equal(t, seen.FirstSeenEpoch, again["shop_aaaaaa"][0].FirstSeenEpoch)
	assert.Greater(t, again["shop_aaaaaa"][0].LastSeenEpoch, seen.LastSeenEpoch, "and says when it was seen last")

	require.NoError(t, s.RecordIdentity(ctx, "shop_aaaaaa", "git.example.org/team/shop", "/other/clone"))
	require.NoError(t, s.RecordIdentity(ctx, "shop_aaaaaa", "", "/no/remote"))
	require.NoError(t, s.RecordIdentity(ctx, "shop_aaaaaa", "", ""))
	require.NoError(t, s.RecordIdentity(ctx, "", "x/y", "/z"))
	all, _ := s.Identities(ctx)
	assert.Len(t, all["shop_aaaaaa"], 3, "another root or no remote are other identities; an empty one is nothing")
	assert.Len(t, all, 1)
}

func TestProjectIdentity_DismissalsAreOrderIndependentAndCanBeRestored(t *testing.T) {
	_, store, cleanup := testObservationStore(t)
	defer cleanup()
	s := NewProjectIdentityStore(store)
	ctx := context.Background()

	require.NoError(t, s.Dismiss(ctx, "b_222222", "a_111111"))
	require.NoError(t, s.Dismiss(ctx, "a_111111", "b_222222"), "dismissing again is fine")
	d, err := s.Dismissed(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[[2]string]bool{{"a_111111", "b_222222"}: true}, d)
	assert.Error(t, s.Dismiss(ctx, "a_111111", "a_111111"))
	assert.Error(t, s.Dismiss(ctx, "", "a_111111"))

	ok, err := s.Restore(ctx, "b_222222", "a_111111")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, _ = s.Restore(ctx, "b_222222", "a_111111")
	assert.False(t, ok, "nothing left to restore")
	d, _ = s.Dismissed(ctx)
	assert.Empty(t, d)
}

func TestProjectIdentity_NormalizedTitlesOfLiveNotesOnly(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	s := NewProjectIdentityStore(store)
	ctx := context.Background()

	addObservation(t, obs, "shop_aaaaaa", "  Retry   Policy ", models.ScopeProject)
	archived := addObservation(t, obs, "shop_aaaaaa", "Archived", models.ScopeProject)
	superseded := addObservation(t, obs, "shop_aaaaaa", "Superseded", models.ScopeProject)
	addObservation(t, obs, "other_bbbbbb", "Elsewhere", models.ScopeProject)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = ?`, archived).Error)
	require.NoError(t, store.DB.Exec(`UPDATE observations SET is_superseded = 1 WHERE id = ?`, superseded).Error)

	got, err := s.NormalizedTitles(ctx, "shop_aaaaaa", 100)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"retry policy": true}, got)
}

func TestProjectIdentity_MergeMovesIdentitiesAndDismissalsAndDeleteDropsThem(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	s := NewProjectIdentityStore(store)
	admin := NewProjectAdminStore(store)
	ctx := context.Background()

	for _, p := range []string{"shop_aaaaaa", "shop_bbbbbb", "shop_cccccc", "shop_dddddd"} {
		addObservation(t, obs, p, "note of "+p, models.ScopeProject)
	}
	require.NoError(t, s.RecordIdentity(ctx, "shop_aaaaaa", "git.example.org/team/shop", "/work/shop"))
	require.NoError(t, s.RecordIdentity(ctx, "shop_bbbbbb", "git.example.org/team/shop", "/work/shop"), "the same identity seen under both ids")
	require.NoError(t, s.RecordIdentity(ctx, "shop_bbbbbb", "git.example.org/team/shop", "/old/shop"))
	require.NoError(t, s.Dismiss(ctx, "shop_aaaaaa", "shop_bbbbbb"), "the pair that is merged")
	require.NoError(t, s.Dismiss(ctx, "shop_bbbbbb", "shop_cccccc"), "bbbbbb was said to be different from cccccc")
	require.NoError(t, s.Dismiss(ctx, "shop_aaaaaa", "shop_cccccc"), "so was aaaaaa: the pair collapses into one")

	_, err := admin.Merge(ctx, "shop_bbbbbb", "shop_aaaaaa", "merge")
	require.NoError(t, err)

	ids, _ := s.Identities(ctx)
	assert.Empty(t, ids["shop_bbbbbb"], "the merged-away project has none left")
	var roots []string
	for _, i := range ids["shop_aaaaaa"] {
		roots = append(roots, i.RootPath)
	}
	assert.ElementsMatch(t, []string{"/work/shop", "/old/shop"}, roots, "the survivor stands for both clones, without a duplicate")
	d, _ := s.Dismissed(ctx)
	assert.Equal(t, map[[2]string]bool{{"shop_aaaaaa", "shop_cccccc"}: true}, d, "the merged pair's dismissal is gone, the other is the survivor's")

	_, err = admin.Delete(ctx, "shop_cccccc")
	require.NoError(t, err)
	d, _ = s.Dismissed(ctx)
	assert.Empty(t, d, "deleting a project removes the dismissals that mention it")
	_, err = admin.Delete(ctx, "shop_aaaaaa")
	require.NoError(t, err)
	ids, _ = s.Identities(ctx)
	assert.Empty(t, ids)
}
