//go:build fts5

// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	doomed = "doomed_aaaaaa"
	keeper = "keeper_bbbbbb"
)

// adminFixture is a store seeded with two projects that touch every table.
type adminFixture struct {
	store    *Store
	admin    *ProjectAdminStore
	aliases  *ProjectAliasStore
	obsStore *ObservationStore
	ctx      context.Context
	// observation IDs
	doomedObs [2]int64
	keeperObs int64
	// pattern IDs
	sharedPattern, doomedOnlyPattern int64
}

func vecBlob() []byte { return make([]byte, 384*4) }

func (f *adminFixture) addVector(t *testing.T, docID string, sqliteID int64, docType, project string) {
	t.Helper()
	require.NoError(t, f.store.DB.Exec(
		`INSERT INTO vectors (doc_id, embedding, sqlite_id, doc_type, field_type, project, scope, model_version)
		 VALUES (?, ?, ?, ?, 'narrative', ?, 'project', 'test')`, docID, vecBlob(), sqliteID, docType, project).Error)
}

func (f *adminFixture) embeddingOf(t *testing.T, docID string) []byte {
	t.Helper()
	var blob []byte
	require.NoError(t, f.store.DB.Raw(`SELECT embedding FROM vectors WHERE doc_id = ?`, docID).Row().Scan(&blob))
	return blob
}

func (f *adminFixture) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.store.DB.Raw(query, args...).Scan(&n).Error)
	return n
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	obsStore, store, cleanup := testObservationStore(t)
	t.Cleanup(cleanup)
	f := &adminFixture{store: store, admin: NewProjectAdminStore(store), aliases: NewProjectAliasStore(store), obsStore: obsStore, ctx: context.Background()}
	sessions := NewSessionStore(store)

	// doomed: 2 explicit sessions + 1 auto-created by the observations = 3 sessions.
	for _, id := range []string{"claude-d-1", "claude-d-2"} {
		_, err := sessions.CreateSDKSession(f.ctx, id, doomed, "prompt")
		require.NoError(t, err)
	}
	for i := range f.doomedObs {
		obs := &models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: fmt.Sprintf("doomed finding %d", i), Narrative: "zebra migration pattern in doomed"}
		id, _, err := obsStore.StoreObservation(f.ctx, "sdk-doomed", doomed, obs, 1, 10)
		require.NoError(t, err)
		f.doomedObs[i] = id
	}
	// keeper: 1 observation (auto session).
	kid, _, err := obsStore.StoreObservation(f.ctx, "sdk-keeper", keeper,
		&models.ParsedObservation{Type: models.ObsTypeDiscovery, Title: "keeper finding", Narrative: "giraffe habitat notes in keeper"}, 1, 10)
	require.NoError(t, err)
	f.keeperObs = kid

	// summaries and prompts
	require.NoError(t, store.DB.Create(&SessionSummary{SDKSessionID: "sdk-doomed", Project: doomed, Completed: sql.NullString{String: "done", Valid: true}}).Error)
	require.NoError(t, store.DB.Create(&SessionSummary{SDKSessionID: "sdk-keeper", Project: keeper}).Error)
	for n := 1; n <= 2; n++ {
		require.NoError(t, store.DB.Create(&UserPrompt{ClaudeSessionID: "claude-d-1", PromptText: fmt.Sprintf("doomed prompt %d", n), PromptNumber: n}).Error)
	}
	require.NoError(t, store.DB.Create(&UserPrompt{ClaudeSessionID: "sdk-keeper", PromptText: "keeper prompt", PromptNumber: 1}).Error)

	// relations: one inside doomed, one across projects
	for _, r := range []ObservationRelation{
		{SourceID: f.doomedObs[0], TargetID: f.doomedObs[1], RelationType: models.RelationRelatesTo, DetectionSource: models.DetectionSourceConceptOverlap},
		{SourceID: f.keeperObs, TargetID: f.doomedObs[0], RelationType: models.RelationDependsOn, DetectionSource: models.DetectionSourceConceptOverlap},
		{SourceID: kid, TargetID: kid + 1000, RelationType: models.RelationRelatesTo, DetectionSource: models.DetectionSourceConceptOverlap}, // unrelated to doomed
	} {
		require.NoError(t, store.DB.Create(&r).Error)
	}
	require.NoError(t, store.DB.Create(&ObservationConflict{NewerObsID: f.keeperObs, OlderObsID: f.doomedObs[1],
		ConflictType: models.ConflictContradicts, Resolution: models.ResolutionManual}).Error)

	// patterns: one shared between both projects, one only in doomed
	shared := Pattern{Name: "shared", Type: models.PatternTypeBestPractice, Projects: models.JSONStringArray{doomed, keeper},
		ObservationIDs: models.JSONInt64Array{f.doomedObs[0], kid}}
	only := Pattern{Name: "doomed only", Type: models.PatternTypeBug, Projects: models.JSONStringArray{doomed}}
	require.NoError(t, store.DB.Create(&shared).Error)
	require.NoError(t, store.DB.Create(&only).Error)
	f.sharedPattern, f.doomedOnlyPattern = shared.ID, only.ID

	// vectors
	f.addVector(t, fmt.Sprintf("obs_%d_narrative", f.doomedObs[0]), f.doomedObs[0], "observation", doomed)
	f.addVector(t, fmt.Sprintf("obs_%d_fact_0", f.doomedObs[0]), f.doomedObs[0], "observation", doomed)
	f.addVector(t, fmt.Sprintf("obs_%d_narrative", f.doomedObs[1]), f.doomedObs[1], "observation", doomed)
	f.addVector(t, fmt.Sprintf("obs_%d_narrative", kid), kid, "observation", keeper)
	f.addVector(t, fmt.Sprintf("pattern_%d_name", f.sharedPattern), f.sharedPattern, "pattern", "")
	f.addVector(t, fmt.Sprintf("pattern_%d_name", f.doomedOnlyPattern), f.doomedOnlyPattern, "pattern", "")

	require.NoError(t, f.aliases.SetAlias(f.ctx, "old-doomed_ffffff", doomed, "manual"))
	return f
}

func TestProjectAdmin_Stats(t *testing.T) {
	f := newAdminFixture(t)

	got, err := f.admin.Stats(f.ctx, doomed)
	require.NoError(t, err)
	assert.Equal(t, ProjectStats{
		Project: doomed, Aliases: []string{"old-doomed_ffffff"},
		Sessions: 3, Observations: 2, ArchivedObservations: 0, Summaries: 1, Prompts: 2,
		Relations: 2, Conflicts: 1, Vectors: 3, Patterns: 2,
	}, got)

	empty, err := f.admin.Stats(f.ctx, "nothing_000000")
	require.NoError(t, err)
	assert.Equal(t, ProjectStats{Project: "nothing_000000"}, empty)
}

func TestProjectAdmin_StatsCountsArchivedSeparately(t *testing.T) {
	f := newAdminFixture(t)
	require.NoError(t, f.store.DB.Exec(`UPDATE observations SET is_archived = 1 WHERE id = ?`, f.doomedObs[1]).Error)

	got, err := f.admin.Stats(f.ctx, doomed)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.Observations)
	assert.Equal(t, int64(1), got.ArchivedObservations)
}

func TestProjectAdmin_Exists(t *testing.T) {
	f := newAdminFixture(t)
	ok, err := f.admin.Exists(f.ctx, doomed)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = f.admin.Exists(f.ctx, "nothing_000000")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestProjectAdmin_DeleteRemovesEverythingTheProjectOwns(t *testing.T) {
	f := newAdminFixture(t)
	before, err := f.admin.Stats(f.ctx, doomed)
	require.NoError(t, err)
	keeperBefore, err := f.admin.Stats(f.ctx, keeper)
	require.NoError(t, err)

	removed, err := f.admin.Delete(f.ctx, doomed)
	require.NoError(t, err)
	assert.Equal(t, before, removed, "reports exactly what it removed")

	d := f.count
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM sdk_sessions WHERE project = ?`, doomed))
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM observations WHERE project = ?`, doomed))
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM session_summaries WHERE project = ?`, doomed))
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM user_prompts WHERE claude_session_id = 'claude-d-1'`))
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM vectors WHERE project = ?`, doomed), "no vector of a deleted project may survive in search")
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM project_aliases WHERE canonical = ?`, doomed), "aliases of a deleted project are removed")

	assert.Zero(t, d(t, `SELECT COUNT(*) FROM observation_relations WHERE source_id IN (?, ?) OR target_id IN (?, ?)`,
		f.doomedObs[0], f.doomedObs[1], f.doomedObs[0], f.doomedObs[1]), "relations, including cross-project ones")
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM observation_conflicts`))
	assert.Equal(t, int64(1), d(t, `SELECT COUNT(*) FROM observation_relations`), "the unrelated relation is kept")

	// full-text index follows the observations
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM observations_fts WHERE observations_fts MATCH 'zebra'`))
	assert.Equal(t, int64(1), d(t, `SELECT COUNT(*) FROM observations_fts WHERE observations_fts MATCH 'giraffe'`))

	// the other project is untouched
	keeperAfter, err := f.admin.Stats(f.ctx, keeper)
	require.NoError(t, err)
	keeperBefore.Patterns, keeperBefore.Relations, keeperBefore.Conflicts = keeperAfter.Patterns, keeperAfter.Relations, keeperAfter.Conflicts
	assert.Equal(t, keeperBefore, keeperAfter)
	assert.Equal(t, int64(1), keeperAfter.Observations)
	assert.Equal(t, int64(1), keeperAfter.Vectors)
}

func TestProjectAdmin_DeleteHandlesSharedAndExclusivePatterns(t *testing.T) {
	f := newAdminFixture(t)
	_, err := f.admin.Delete(f.ctx, doomed)
	require.NoError(t, err)

	var shared Pattern
	require.NoError(t, f.store.DB.First(&shared, f.sharedPattern).Error)
	assert.Equal(t, models.JSONStringArray{keeper}, shared.Projects, "a shared pattern loses only the deleted project")

	assert.Zero(t, f.count(t, `SELECT COUNT(*) FROM patterns WHERE id = ?`, f.doomedOnlyPattern), "a pattern only about the deleted project goes with it")
	assert.Zero(t, f.count(t, `SELECT COUNT(*) FROM vectors WHERE doc_id LIKE ?`, fmt.Sprintf("pattern_%d_%%", f.doomedOnlyPattern)))
	assert.Equal(t, int64(1), f.count(t, `SELECT COUNT(*) FROM vectors WHERE doc_id LIKE ?`, fmt.Sprintf("pattern_%d_%%", f.sharedPattern)))
}

func TestProjectAdmin_DeleteRefusesMissingAndAliasProjects(t *testing.T) {
	f := newAdminFixture(t)

	_, err := f.admin.Delete(f.ctx, "nothing_000000")
	assert.ErrorIs(t, err, ErrProjectNotFound)

	_, err = f.admin.Delete(f.ctx, "old-doomed_ffffff")
	assert.ErrorIs(t, err, ErrProjectIsAlias, "an alias is removed as an alias, never as data")

	stats, _ := f.admin.Stats(f.ctx, doomed)
	assert.Equal(t, int64(2), stats.Observations, "nothing was deleted")
}

func TestProjectAdmin_DeleteIsAtomic(t *testing.T) {
	f := newAdminFixture(t)
	// Make the last data step fail; everything done before it must roll back.
	require.NoError(t, f.store.DB.Exec(`CREATE TRIGGER fail_session_delete BEFORE DELETE ON sdk_sessions
		BEGIN SELECT RAISE(ABORT, 'boom'); END`).Error)
	before, err := f.admin.Stats(f.ctx, doomed)
	require.NoError(t, err)

	_, err = f.admin.Delete(f.ctx, doomed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")

	after, err := f.admin.Stats(f.ctx, doomed)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a failed delete leaves every table, vectors included, exactly as it was")
}

func TestProjectAdmin_MergeMovesEverythingAndRecordsTheAlias(t *testing.T) {
	f := newAdminFixture(t)
	before, err := f.admin.Stats(f.ctx, doomed)
	require.NoError(t, err)

	docID := fmt.Sprintf("obs_%d_narrative", f.doomedObs[0])
	// Give the vector a recognisable value so "moved as is" is a real check, not all zeros.
	marked := vecBlob()
	marked[0], marked[7], marked[len(marked)-1] = 1, 2, 3
	require.NoError(t, f.store.DB.Exec(`UPDATE vectors SET embedding = ? WHERE doc_id = ?`, marked, docID).Error)
	embeddingBefore := f.embeddingOf(t, docID)
	require.Equal(t, marked, embeddingBefore)

	moved, err := f.admin.Merge(f.ctx, doomed, keeper, "merge")
	require.NoError(t, err)
	assert.Equal(t, before, moved, "reports what moved")

	d := f.count
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM sdk_sessions WHERE project = ?`, doomed))
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM observations WHERE project = ?`, doomed))
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM session_summaries WHERE project = ?`, doomed))
	assert.Zero(t, d(t, `SELECT COUNT(*) FROM vectors WHERE project = ?`, doomed))
	assert.Equal(t, int64(3), d(t, `SELECT COUNT(*) FROM observations WHERE project = ?`, keeper), "2 moved + 1 own")
	assert.Equal(t, int64(4), d(t, `SELECT COUNT(*) FROM vectors WHERE project = ?`, keeper), "3 moved + 1 own, each exactly once")
	assert.Equal(t, int64(6), d(t, `SELECT COUNT(*) FROM vectors`), "no vector was duplicated or lost (4 observation docs + 2 pattern docs)")

	assert.Equal(t, embeddingBefore, f.embeddingOf(t, docID), "embeddings move as they are, nothing is re-embedded")

	assert.Equal(t, int64(2), d(t, `SELECT COUNT(*) FROM observations_fts WHERE observations_fts MATCH 'zebra'`), "FTS keeps both moved observations searchable")

	// prompts stay linked through their sessions
	stats, err := f.admin.Stats(f.ctx, keeper)
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.Observations)
	assert.Equal(t, int64(3), stats.Prompts)
	assert.Equal(t, int64(3), d(t, `SELECT COUNT(*) FROM user_prompts`))

	// alias recorded; the old alias follows to the surviving project
	canonical, isAlias, err := f.aliases.ResolveAlias(f.ctx, doomed)
	require.NoError(t, err)
	assert.True(t, isAlias)
	assert.Equal(t, keeper, canonical)
	canonical, _, _ = f.aliases.ResolveAlias(f.ctx, "old-doomed_ffffff")
	assert.Equal(t, keeper, canonical, "aliases that pointed at the merged project are flattened onto the survivor")
	rows, _ := f.aliases.ListAliases(f.ctx)
	for _, r := range rows {
		if r.Alias == doomed {
			assert.Equal(t, "merge", r.Source)
		}
	}
}

func TestProjectAdmin_MergeRenamesProjectInPatternsWithoutDuplicates(t *testing.T) {
	f := newAdminFixture(t)
	_, err := f.admin.Merge(f.ctx, doomed, keeper, "merge")
	require.NoError(t, err)

	var shared, only Pattern
	require.NoError(t, f.store.DB.First(&shared, f.sharedPattern).Error)
	require.NoError(t, f.store.DB.First(&only, f.doomedOnlyPattern).Error)
	assert.Equal(t, models.JSONStringArray{keeper}, shared.Projects, "[doomed, keeper] collapses to [keeper]")
	assert.Equal(t, models.JSONStringArray{keeper}, only.Projects, "[doomed] becomes [keeper]")
}

func TestProjectAdmin_MergeValidation(t *testing.T) {
	f := newAdminFixture(t)
	before, _ := f.admin.Stats(f.ctx, doomed)

	_, err := f.admin.Merge(f.ctx, doomed, doomed, "")
	assert.ErrorIs(t, err, ErrMergeSelf)

	_, err = f.admin.Merge(f.ctx, "nothing_000000", keeper, "")
	assert.ErrorIs(t, err, ErrProjectNotFound, "source must exist")

	_, err = f.admin.Merge(f.ctx, doomed, "nothing_000000", "")
	assert.ErrorIs(t, err, ErrProjectNotFound, "target must exist: a typo must not silently create a project")

	_, err = f.admin.Merge(f.ctx, "old-doomed_ffffff", keeper, "")
	assert.ErrorIs(t, err, ErrProjectIsAlias, "an alias cannot be the source")

	_, err = f.admin.Merge(f.ctx, keeper, "old-doomed_ffffff", "")
	assert.ErrorIs(t, err, ErrProjectIsAlias, "an alias cannot be the target")

	after, _ := f.admin.Stats(f.ctx, doomed)
	assert.Equal(t, before, after, "every refused merge changed nothing")
}

func TestProjectAdmin_MergeIsAtomic(t *testing.T) {
	f := newAdminFixture(t)
	// Fail on the last step (alias creation) by making the alias table reject inserts.
	require.NoError(t, f.store.DB.Exec(`CREATE TRIGGER fail_alias_insert BEFORE INSERT ON project_aliases
		BEGIN SELECT RAISE(ABORT, 'boom'); END`).Error)
	before, _ := f.admin.Stats(f.ctx, doomed)

	_, err := f.admin.Merge(f.ctx, doomed, keeper, "merge")
	require.Error(t, err)

	after, err := f.admin.Stats(f.ctx, doomed)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a failed merge moves nothing")
	assert.Equal(t, int64(3), f.count(t, `SELECT COUNT(*) FROM vectors WHERE project = ?`, doomed))
}

func TestPatternsMentioning_MatchesWholeProjectNamesOnly(t *testing.T) {
	f := newAdminFixture(t)
	// LIKE wildcards in a project name must not widen the match.
	require.NoError(t, f.store.DB.Create(&Pattern{Name: "x", Type: models.PatternTypeBug, Projects: models.JSONStringArray{"axb_111111"}}).Error)

	got, err := patternsMentioning(f.store.DB, "a_b_111111")
	require.NoError(t, err)
	assert.Empty(t, got, `"_" is a LIKE wildcard; it must not match "x"`)

	got, err = patternsMentioning(f.store.DB, "axb_111111")
	require.NoError(t, err)
	assert.Len(t, got, 1)

	got, err = patternsMentioning(f.store.DB, "axb")
	require.NoError(t, err)
	assert.Empty(t, got, "a substring of a project name is not the project")
}

func TestStore_SnapshotProducesAnOpenableCopyAndPrunesOldOnes(t *testing.T) {
	f := newAdminFixture(t)
	dir := filepath.Join(t.TempDir(), "backups")

	var paths []string
	for i := 0; i < 5; i++ {
		p, err := f.store.Snapshot(f.ctx, dir, "before delete/doomed", 3)
		require.NoError(t, err)
		paths = append(paths, p)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 3, "only the newest 3 are kept")
	for _, old := range paths[:2] {
		_, err := os.Stat(old)
		assert.True(t, os.IsNotExist(err), "oldest snapshots are pruned: %s", old)
	}
	assert.True(t, strings.Contains(filepath.Base(paths[4]), "before-delete-doomed"), "label is sanitised into the name: %s", paths[4])

	// the copy is a complete, working database
	snap, err := NewStore(Config{Path: paths[4], MaxConns: 1})
	require.NoError(t, err)
	defer snap.Close()
	var n int64
	require.NoError(t, snap.DB.Raw(`SELECT COUNT(*) FROM observations`).Scan(&n).Error)
	assert.Equal(t, int64(3), n)
	require.NoError(t, snap.DB.Raw(`SELECT COUNT(*) FROM vectors`).Scan(&n).Error)
	assert.Equal(t, int64(6), n, "vectors are included in the snapshot")
}

func TestStore_SnapshotDirDefaultsBesideTheDatabase(t *testing.T) {
	f := newAdminFixture(t)
	assert.Equal(t, filepath.Join(filepath.Dir(f.store.path), "backups"), f.store.DefaultSnapshotDir())
}

func TestSanitizeLabelAndPrune(t *testing.T) {
	assert.Equal(t, "before-delete-doomed_1", sanitizeLabel("before delete/doomed_1"))
	assert.Equal(t, "snapshot", sanitizeLabel(""))
	assert.Equal(t, "-------", sanitizeLabel("../../"+"é"), "every unsafe rune becomes one dash")

	dir := t.TempDir()
	for _, n := range []string{"snapshot-1.db", "snapshot-2.db", "notes.txt", "other.db"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), nil, 0o600))
	}
	pruneSnapshots(dir, 1)
	_, e1 := os.Stat(filepath.Join(dir, "snapshot-1.db"))
	_, e2 := os.Stat(filepath.Join(dir, "snapshot-2.db"))
	_, e3 := os.Stat(filepath.Join(dir, "notes.txt"))
	_, e4 := os.Stat(filepath.Join(dir, "other.db"))
	assert.True(t, os.IsNotExist(e1))
	assert.NoError(t, e2)
	assert.NoError(t, e3, "only snapshot files are ever pruned")
	assert.NoError(t, e4)
}

// holdWriteLock makes a second connection take the database write lock now and
// commit after d, the way the worker's asynchronous vector sync does.
func (f *adminFixture) holdWriteLock(t *testing.T, d time.Duration) {
	t.Helper()
	conn, err := f.store.sqlDB.Conn(f.ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(f.ctx, `BEGIN IMMEDIATE`)
	require.NoError(t, err)
	_, err = conn.ExecContext(f.ctx, `UPDATE observations SET importance_score = importance_score WHERE id = ?`, f.keeperObs)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(d)
		_, _ = conn.ExecContext(f.ctx, `COMMIT`)
		_ = conn.Close()
	}()
	t.Cleanup(func() { <-done })
}

// A transaction that reads first and then writes fails at once with SQLITE_BUSY_SNAPSHOT when
// another connection commits in between, and the busy timeout does not apply. The project
// operations therefore take the write lock up front and wait their turn instead.
func TestProjectAdmin_WritesWaitForAConcurrentWriterInsteadOfFailing(t *testing.T) {
	t.Run("alias", func(t *testing.T) {
		f := newAdminFixture(t)
		f.holdWriteLock(t, 300*time.Millisecond)
		require.NoError(t, f.aliases.SetAlias(f.ctx, "x_111111", keeper, "manual"))
		got, isAlias, err := f.aliases.ResolveAlias(f.ctx, "x_111111")
		require.NoError(t, err)
		assert.True(t, isAlias)
		assert.Equal(t, keeper, got)
	})
	t.Run("merge", func(t *testing.T) {
		f := newAdminFixture(t)
		f.holdWriteLock(t, 300*time.Millisecond)
		_, err := f.admin.Merge(f.ctx, doomed, keeper, "merge")
		require.NoError(t, err)
		assert.Zero(t, f.count(t, `SELECT COUNT(*) FROM observations WHERE project = ?`, doomed))
	})
	t.Run("delete", func(t *testing.T) {
		f := newAdminFixture(t)
		f.holdWriteLock(t, 300*time.Millisecond)
		_, err := f.admin.Delete(f.ctx, doomed)
		require.NoError(t, err)
		assert.Zero(t, f.count(t, `SELECT COUNT(*) FROM observations WHERE project = ?`, doomed))
	})
}
