// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrAliasSelf is returned when an alias would point at itself.
var ErrAliasSelf = errors.New("alias and canonical project must differ")

// ProjectAliasStore manages the alias -> canonical project mapping.
//
// The mapping is kept flat: every alias points directly at a project that is
// not itself an alias, so resolving is a single lookup and cycles cannot form.
type ProjectAliasStore struct {
	db *gorm.DB
}

// NewProjectAliasStore creates a new project alias store.
func NewProjectAliasStore(store *Store) *ProjectAliasStore {
	return &ProjectAliasStore{db: store.DB}
}

// SetAlias records that alias stands for canonical. An existing alias is
// re-pointed. If canonical is itself an alias it is followed first, and any
// alias that already pointed at the new alias is re-pointed too, so the table
// stays flat.
func (s *ProjectAliasStore) SetAlias(ctx context.Context, alias, canonical, source string) error {
	return immediateTx(ctx, s.db, func(tx *gorm.DB) error {
		return setAliasIn(tx, alias, canonical, source)
	})
}

// setAliasIn is SetAlias against an open transaction, so project merges can
// record their alias atomically with the data move.
func setAliasIn(tx *gorm.DB, alias, canonical, source string) error {
	alias, canonical = strings.TrimSpace(alias), strings.TrimSpace(canonical)
	if alias == "" || canonical == "" {
		return errors.New("alias and canonical project are required")
	}
	if source == "" {
		source = "manual"
	}

	var existing ProjectAlias
	err := tx.Where("alias = ?", canonical).First(&existing).Error
	switch {
	case err == nil:
		canonical = existing.Canonical
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return fmt.Errorf("follow canonical alias: %w", err)
	}
	if alias == canonical {
		return ErrAliasSelf
	}

	// Anything that used to resolve through the new alias now resolves to canonical.
	if err := tx.Model(&ProjectAlias{}).Where("canonical = ?", alias).
		Update("canonical", canonical).Error; err != nil {
		return fmt.Errorf("repoint aliases: %w", err)
	}

	row := ProjectAlias{Alias: alias, Canonical: canonical, Source: source}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "alias"}},
		DoUpdates: clause.AssignmentColumns([]string{"canonical", "source"}),
	}).Create(&row).Error
}

// ResolveAlias returns the canonical project for id, or id itself when it is not an alias.
func (s *ProjectAliasStore) ResolveAlias(ctx context.Context, id string) (string, bool, error) {
	var a ProjectAlias
	err := s.db.WithContext(ctx).Where("alias = ?", id).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return id, false, nil
	}
	if err != nil {
		return id, false, err
	}
	return a.Canonical, true, nil
}

// ListAliases returns all aliases ordered by alias name.
func (s *ProjectAliasStore) ListAliases(ctx context.Context) ([]ProjectAlias, error) {
	var out []ProjectAlias
	err := s.db.WithContext(ctx).Order("alias ASC").Find(&out).Error
	return out, err
}

// AliasMap returns every alias as an alias -> canonical map.
func (s *ProjectAliasStore) AliasMap(ctx context.Context) (map[string]string, error) {
	rows, err := s.ListAliases(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Alias] = r.Canonical
	}
	return m, nil
}

// DeleteAlias removes an alias and reports whether it existed.
func (s *ProjectAliasStore) DeleteAlias(ctx context.Context, alias string) (bool, error) {
	res := s.db.WithContext(ctx).Where("alias = ?", alias).Delete(&ProjectAlias{})
	return res.RowsAffected > 0, res.Error
}
