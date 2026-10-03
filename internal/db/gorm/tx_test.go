//go:build fts5

// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func aliasCount(t *testing.T, s *ProjectAliasStore) int {
	t.Helper()
	rows, err := s.ListAliases(context.Background())
	require.NoError(t, err)
	return len(rows)
}

func TestImmediateTx_CommitsOnSuccess(t *testing.T) {
	s := testAliasStore(t)
	err := immediateTx(context.Background(), s.db, func(tx *gorm.DB) error {
		return tx.Create(&ProjectAlias{Alias: "a_111111", Canonical: "b_222222", Source: "t"}).Error
	})
	require.NoError(t, err)
	assert.Equal(t, 1, aliasCount(t, s))
}

func TestImmediateTx_RollsBackWhenTheFunctionFails(t *testing.T) {
	s := testAliasStore(t)
	boom := errors.New("boom")
	err := immediateTx(context.Background(), s.db, func(tx *gorm.DB) error {
		require.NoError(t, tx.Create(&ProjectAlias{Alias: "a_111111", Canonical: "b_222222", Source: "t"}).Error)
		return boom
	})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 0, aliasCount(t, s), "the write inside the failed transaction is undone")
}

func TestImmediateTx_RollsBackAndRepanicsOnPanic(t *testing.T) {
	s := testAliasStore(t)
	assert.PanicsWithValue(t, "kaboom", func() {
		_ = immediateTx(context.Background(), s.db, func(tx *gorm.DB) error {
			require.NoError(t, tx.Create(&ProjectAlias{Alias: "a_111111", Canonical: "b_222222", Source: "t"}).Error)
			panic("kaboom")
		})
	})
	assert.Equal(t, 0, aliasCount(t, s), "a panic must not leave a half-done transaction")

	// and the connection is usable again afterwards (no stuck transaction)
	require.NoError(t, s.SetAlias(context.Background(), "c_333333", "d_444444", ""))
	assert.Equal(t, 1, aliasCount(t, s))
}

func TestImmediateTx_GormCallsInsideDoNotTryToNestTransactions(t *testing.T) {
	s := testAliasStore(t)
	// Create, Update and Delete each open their own transaction by default; inside
	// immediateTx that would fail with "cannot start a transaction within a transaction".
	err := immediateTx(context.Background(), s.db, func(tx *gorm.DB) error {
		if err := tx.Create(&ProjectAlias{Alias: "a_111111", Canonical: "b_222222", Source: "t"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&ProjectAlias{}).Where("alias = ?", "a_111111").Update("canonical", "c_333333").Error; err != nil {
			return err
		}
		return tx.Where("alias = ?", "nothing").Delete(&ProjectAlias{}).Error
	})
	require.NoError(t, err)
	got, _, err := s.ResolveAlias(context.Background(), "a_111111")
	require.NoError(t, err)
	assert.Equal(t, "c_333333", got)
}
