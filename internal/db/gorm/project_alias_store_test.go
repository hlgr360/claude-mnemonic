//go:build fts5

// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

func testAliasStore(t *testing.T) *ProjectAliasStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "gorm_alias_test_*")
	require.NoError(t, err)

	store, err := NewStore(Config{Path: filepath.Join(tmpDir, "test.db"), MaxConns: 4, LogLevel: logger.Silent})
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		os.RemoveAll(tmpDir)
	})
	return NewProjectAliasStore(store)
}

func TestProjectAliasStore_SetAndResolve(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetAlias(ctx, "wt-frag_aaaaaa", "repo_bbbbbb", "merge"))

	got, isAlias, err := s.ResolveAlias(ctx, "wt-frag_aaaaaa")
	require.NoError(t, err)
	assert.True(t, isAlias)
	assert.Equal(t, "repo_bbbbbb", got)
}

func TestProjectAliasStore_ResolveUnknownReturnsInput(t *testing.T) {
	s := testAliasStore(t)

	got, isAlias, err := s.ResolveAlias(context.Background(), "repo_bbbbbb")
	require.NoError(t, err)
	assert.False(t, isAlias)
	assert.Equal(t, "repo_bbbbbb", got)
}

func TestProjectAliasStore_RejectsSelfAndEmpty(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	assert.ErrorIs(t, s.SetAlias(ctx, "a_111111", "a_111111", ""), ErrAliasSelf)
	assert.Error(t, s.SetAlias(ctx, "", "b_222222", ""))
	assert.Error(t, s.SetAlias(ctx, "a_111111", "  ", ""))
}

func TestProjectAliasStore_TrimsWhitespace(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetAlias(ctx, "  a_111111 ", " b_222222\n", ""))
	got, isAlias, err := s.ResolveAlias(ctx, "a_111111")
	require.NoError(t, err)
	assert.True(t, isAlias)
	assert.Equal(t, "b_222222", got)
}

func TestProjectAliasStore_DefaultSource(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetAlias(ctx, "a_111111", "b_222222", ""))
	rows, err := s.ListAliases(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "manual", rows[0].Source)
	assert.NotEmpty(t, rows[0].CreatedAt)
	assert.NotZero(t, rows[0].CreatedAtEpoch)
}

func TestProjectAliasStore_ReSettingRepointsAlias(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetAlias(ctx, "a_111111", "b_222222", "manual"))
	require.NoError(t, s.SetAlias(ctx, "a_111111", "c_333333", "merge"))

	got, _, err := s.ResolveAlias(ctx, "a_111111")
	require.NoError(t, err)
	assert.Equal(t, "c_333333", got)
	rows, _ := s.ListAliases(ctx)
	assert.Len(t, rows, 1, "re-pointing must not create a duplicate row")
	assert.Equal(t, "merge", rows[0].Source)
}

func TestProjectAliasStore_CanonicalThatIsAnAliasIsFollowed(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetAlias(ctx, "b_222222", "c_333333", ""))
	require.NoError(t, s.SetAlias(ctx, "a_111111", "b_222222", ""))

	got, _, err := s.ResolveAlias(ctx, "a_111111")
	require.NoError(t, err)
	assert.Equal(t, "c_333333", got, "alias must point at the final project, not at another alias")
}

func TestProjectAliasStore_MakingAProjectAnAliasFlattensExistingAliases(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetAlias(ctx, "a_111111", "b_222222", ""))
	// b_222222 itself becomes an alias of c_333333: a_111111 must follow.
	require.NoError(t, s.SetAlias(ctx, "b_222222", "c_333333", ""))

	got, _, err := s.ResolveAlias(ctx, "a_111111")
	require.NoError(t, err)
	assert.Equal(t, "c_333333", got)
}

func TestProjectAliasStore_CycleIsRejected(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetAlias(ctx, "a_111111", "b_222222", ""))
	assert.ErrorIs(t, s.SetAlias(ctx, "b_222222", "a_111111", ""), ErrAliasSelf)

	got, isAlias, err := s.ResolveAlias(ctx, "b_222222")
	require.NoError(t, err)
	assert.False(t, isAlias, "rejected alias must not have been written")
	assert.Equal(t, "b_222222", got)
}

func TestProjectAliasStore_ListAndMapAndDelete(t *testing.T) {
	s := testAliasStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetAlias(ctx, "z_999999", "p_000001", ""))
	require.NoError(t, s.SetAlias(ctx, "a_111111", "p_000001", ""))

	rows, err := s.ListAliases(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "a_111111", rows[0].Alias, "ordered by alias")

	m, err := s.AliasMap(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"z_999999": "p_000001", "a_111111": "p_000001"}, m)

	deleted, err := s.DeleteAlias(ctx, "a_111111")
	require.NoError(t, err)
	assert.True(t, deleted)
	deleted, err = s.DeleteAlias(ctx, "a_111111")
	require.NoError(t, err)
	assert.False(t, deleted, "deleting a missing alias reports false")
}
