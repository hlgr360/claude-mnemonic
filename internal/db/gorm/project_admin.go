// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

var (
	// ErrProjectNotFound means the project has no sessions, observations or summaries.
	ErrProjectNotFound = errors.New("project not found")
	// ErrProjectIsAlias means the ID is an alias of another project, not a project itself.
	ErrProjectIsAlias = errors.New("project id is an alias")
	// ErrMergeSelf means a project cannot be merged into itself.
	ErrMergeSelf = errors.New("cannot merge a project into itself")
)

// ProjectStats counts what a project holds, and so what deleting or merging it affects.
type ProjectStats struct {
	Project              string   `json:"project"`
	Aliases              []string `json:"aliases,omitempty"`
	Sessions             int64    `json:"sessions"`
	Observations         int64    `json:"observations"`
	ArchivedObservations int64    `json:"archived_observations"`
	Summaries            int64    `json:"summaries"`
	Prompts              int64    `json:"prompts"`
	Relations            int64    `json:"relations"`
	Conflicts            int64    `json:"conflicts"`
	Vectors              int64    `json:"vectors"`
	Patterns             int64    `json:"patterns"`
}

// ProjectAdminStore removes and merges whole projects.
//
// Everything a project owns is changed in one transaction, including its rows
// in the sqlite-vec "vectors" table, so a failure leaves nothing half-deleted
// and search can never return vectors of a project that no longer exists.
type ProjectAdminStore struct {
	db *gorm.DB
}

// NewProjectAdminStore creates a project admin store.
func NewProjectAdminStore(store *Store) *ProjectAdminStore {
	return &ProjectAdminStore{db: store.DB}
}

// Exists reports whether the project has any sessions, observations or summaries.
func (s *ProjectAdminStore) Exists(ctx context.Context, project string) (bool, error) {
	return existsIn(s.db.WithContext(ctx), project)
}

func existsIn(db *gorm.DB, project string) (bool, error) {
	var n int64
	err := db.Raw(`SELECT
		(SELECT COUNT(*) FROM sdk_sessions WHERE project = ?) +
		(SELECT COUNT(*) FROM observations WHERE project = ?) +
		(SELECT COUNT(*) FROM session_summaries WHERE project = ?)`, project, project, project).Scan(&n).Error
	return n > 0, err
}

// Stats counts what the project holds. The returned stats are valid even for a
// project that does not exist (all zero).
func (s *ProjectAdminStore) Stats(ctx context.Context, project string) (ProjectStats, error) {
	return statsIn(s.db.WithContext(ctx), project)
}

func statsIn(db *gorm.DB, project string) (ProjectStats, error) {
	st := ProjectStats{Project: project}
	obsIDs := `SELECT id FROM observations WHERE project = ?`
	counts := []struct {
		dst  *int64
		sql  string
		args []any
	}{
		{&st.Sessions, `SELECT COUNT(*) FROM sdk_sessions WHERE project = ?`, []any{project}},
		{&st.Observations, `SELECT COUNT(*) FROM observations WHERE project = ? AND (is_archived = 0 OR is_archived IS NULL)`, []any{project}},
		{&st.ArchivedObservations, `SELECT COUNT(*) FROM observations WHERE project = ? AND is_archived = 1`, []any{project}},
		{&st.Summaries, `SELECT COUNT(*) FROM session_summaries WHERE project = ?`, []any{project}},
		{&st.Prompts, `SELECT COUNT(*) FROM user_prompts WHERE claude_session_id IN (SELECT claude_session_id FROM sdk_sessions WHERE project = ?)`, []any{project}},
		{&st.Relations, `SELECT COUNT(*) FROM observation_relations WHERE source_id IN (` + obsIDs + `) OR target_id IN (` + obsIDs + `)`, []any{project, project}},
		{&st.Conflicts, `SELECT COUNT(*) FROM observation_conflicts WHERE newer_obs_id IN (` + obsIDs + `) OR older_obs_id IN (` + obsIDs + `)`, []any{project, project}},
		{&st.Vectors, `SELECT COUNT(*) FROM vectors WHERE project = ?`, []any{project}},
	}
	for _, c := range counts {
		if err := db.Raw(c.sql, c.args...).Scan(c.dst).Error; err != nil {
			return st, fmt.Errorf("count %q: %w", c.sql, err)
		}
	}

	patterns, err := patternsMentioning(db, project)
	if err != nil {
		return st, err
	}
	st.Patterns = int64(len(patterns))

	var aliases []string
	if err := db.Model(&ProjectAlias{}).Where("canonical = ?", project).Order("alias ASC").Pluck("alias", &aliases).Error; err != nil {
		return st, fmt.Errorf("list aliases: %w", err)
	}
	if len(aliases) > 0 {
		st.Aliases = aliases
	}
	return st, nil
}

// patternsMentioning returns the patterns whose project list includes project.
func patternsMentioning(db *gorm.DB, project string) ([]Pattern, error) {
	var candidates []Pattern
	// LIKE narrows the scan; the exact membership test below is authoritative.
	if err := db.Where(`projects LIKE ? ESCAPE '\'`, "%"+escapeLike(project)+"%").Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("find patterns: %w", err)
	}
	var out []Pattern
	for _, p := range candidates {
		for _, name := range p.Projects {
			if name == project {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Delete removes the project and everything it owns, and returns what was removed.
func (s *ProjectAdminStore) Delete(ctx context.Context, project string) (ProjectStats, error) {
	var removed ProjectStats
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkRemovable(tx, project); err != nil {
			return err
		}
		var err error
		if removed, err = statsIn(tx, project); err != nil {
			return err
		}

		obsIDs := `SELECT id FROM observations WHERE project = ?`
		steps := []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM observation_relations WHERE source_id IN (` + obsIDs + `) OR target_id IN (` + obsIDs + `)`, []any{project, project}},
			{`DELETE FROM observation_conflicts WHERE newer_obs_id IN (` + obsIDs + `) OR older_obs_id IN (` + obsIDs + `)`, []any{project, project}},
			{`DELETE FROM user_prompts WHERE claude_session_id IN (SELECT claude_session_id FROM sdk_sessions WHERE project = ?)`, []any{project}},
			{`DELETE FROM session_summaries WHERE project = ?`, []any{project}},
			{`DELETE FROM observations WHERE project = ?`, []any{project}},
			{`DELETE FROM sdk_sessions WHERE project = ?`, []any{project}},
			{`DELETE FROM vectors WHERE project = ?`, []any{project}},
			{`DELETE FROM project_aliases WHERE canonical = ? OR alias = ?`, []any{project, project}},
		}
		for _, st := range steps {
			if err := tx.Exec(st.sql, st.args...).Error; err != nil {
				return fmt.Errorf("delete step %q: %w", st.sql, err)
			}
		}
		return detachPatterns(tx, project, "")
	})
	return removed, err
}

// Merge moves everything the project owns into another project, records an
// alias so the old ID keeps resolving, and returns what was moved. Vectors are
// moved with their embeddings, so nothing is re-embedded.
func (s *ProjectAdminStore) Merge(ctx context.Context, from, into, source string) (ProjectStats, error) {
	var moved ProjectStats
	if from == into {
		return moved, ErrMergeSelf
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkRemovable(tx, from); err != nil {
			return err
		}
		if _, isAlias, err := resolveAliasIn(tx, into); err != nil {
			return err
		} else if isAlias {
			return fmt.Errorf("%w: %s", ErrProjectIsAlias, into)
		}
		if ok, err := existsIn(tx, into); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: %s", ErrProjectNotFound, into)
		}

		var err error
		if moved, err = statsIn(tx, from); err != nil {
			return err
		}

		steps := []struct {
			sql  string
			args []any
		}{
			{`UPDATE sdk_sessions SET project = ? WHERE project = ?`, []any{into, from}},
			{`UPDATE observations SET project = ? WHERE project = ?`, []any{into, from}},
			{`UPDATE session_summaries SET project = ? WHERE project = ?`, []any{into, from}},
			// project is a metadata column of the vec0 table, so the embeddings are not touched.
			{`UPDATE vectors SET project = ? WHERE project = ?`, []any{into, from}},
		}
		for _, st := range steps {
			if err := tx.Exec(st.sql, st.args...).Error; err != nil {
				return fmt.Errorf("merge step %q: %w", st.sql, err)
			}
		}
		if err := detachPatterns(tx, from, into); err != nil {
			return err
		}
		return setAliasIn(tx, from, into, source)
	})
	return moved, err
}

// checkRemovable fails unless project is a real project and not an alias.
func checkRemovable(tx *gorm.DB, project string) error {
	if _, isAlias, err := resolveAliasIn(tx, project); err != nil {
		return err
	} else if isAlias {
		return fmt.Errorf("%w: %s", ErrProjectIsAlias, project)
	}
	ok, err := existsIn(tx, project)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, project)
	}
	return nil
}

// detachPatterns removes project from every pattern that lists it. With a
// replacement the project is renamed in place; with none it is dropped, and a
// pattern left with no projects is deleted together with its vectors.
func detachPatterns(tx *gorm.DB, project, replacement string) error {
	patterns, err := patternsMentioning(tx, project)
	if err != nil {
		return err
	}
	for _, p := range patterns {
		var projects models.JSONStringArray
		seen := map[string]bool{}
		for _, name := range p.Projects {
			if name == project {
				name = replacement
			}
			if name != "" && !seen[name] {
				seen[name] = true
				projects = append(projects, name)
			}
		}

		if len(projects) == 0 {
			if err := tx.Exec(`DELETE FROM patterns WHERE id = ?`, p.ID).Error; err != nil {
				return fmt.Errorf("delete pattern %d: %w", p.ID, err)
			}
			if err := tx.Exec(`DELETE FROM vectors WHERE doc_id LIKE ?`, fmt.Sprintf("pattern_%d_%%", p.ID)).Error; err != nil {
				return fmt.Errorf("delete pattern %d vectors: %w", p.ID, err)
			}
			continue
		}
		if err := tx.Model(&Pattern{}).Where("id = ?", p.ID).Update("projects", projects).Error; err != nil {
			return fmt.Errorf("update pattern %d: %w", p.ID, err)
		}
	}
	return nil
}

// resolveAliasIn is ResolveAlias against an open transaction.
func resolveAliasIn(tx *gorm.DB, id string) (string, bool, error) {
	var a ProjectAlias
	err := tx.Where("alias = ?", id).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return id, false, nil
	}
	if err != nil {
		return id, false, err
	}
	return a.Canonical, true, nil
}

// Snapshot writes a consistent copy of the database to dir and keeps only the
// newest keep snapshots. It returns the new file's path. Take one before any
// destructive project operation.
func (s *Store) Snapshot(ctx context.Context, dir, label string, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	label = sanitizeLabel(label)
	stamp := time.Now().Format("20060102-150405.000")
	dst := filepath.Join(dir, fmt.Sprintf("snapshot-%s-%s.db", stamp, label))
	// VACUUM INTO refuses to overwrite, so two snapshots in one millisecond need distinct names.
	for n := 2; ; n++ {
		if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = filepath.Join(dir, fmt.Sprintf("snapshot-%s-%d-%s.db", stamp, n, label))
	}

	// VACUUM INTO produces a transactionally consistent copy even in WAL mode.
	if _, err := s.sqlDB.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return "", fmt.Errorf("snapshot database: %w", err)
	}
	pruneSnapshots(dir, keep)
	return dst, nil
}

func sanitizeLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "snapshot"
	}
	return b.String()
}

// pruneSnapshots deletes all but the newest keep snapshot files in dir.
func pruneSnapshots(dir string, keep int) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "snapshot-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // timestamps sort lexically
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// DefaultSnapshotDir is where snapshots go: a "backups" folder beside the database.
func (s *Store) DefaultSnapshotDir() string {
	return filepath.Join(filepath.Dir(s.path), "backups")
}
