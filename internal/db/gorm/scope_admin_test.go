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

func storeWith(t *testing.T, obs *ObservationStore, title string, p *models.ParsedObservation) int64 {
	t.Helper()
	p.Type, p.Title, p.Narrative = models.ObsTypeDiscovery, title, "narrative of "+title
	id, _, err := obs.StoreObservation(context.Background(), "sdk-p", "p_aaaaaa", p, 1, 1)
	require.NoError(t, err)
	return id
}

func scopeOf(t *testing.T, store *Store, id int64) (scope, source string) {
	t.Helper()
	var row struct{ Scope, ScopeSource string }
	require.NoError(t, store.DB.Raw(`SELECT scope, scope_source FROM observations WHERE id = ?`, id).Scan(&row).Error)
	return row.Scope, row.ScopeSource
}

func vectorScope(t *testing.T, store *Store, docID string) string {
	t.Helper()
	var s string
	require.NoError(t, store.DB.Raw(`SELECT scope FROM vectors WHERE doc_id = ?`, docID).Scan(&s).Error)
	return s
}

func TestStoreObservation_TheRuleDecidesTheScopeAndRemembersWhoDecided(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()

	general := storeWith(t, obs, "general", &models.ParsedObservation{Concepts: []string{"best-practice", "workflow"}})
	touched := storeWith(t, obs, "touched", &models.ParsedObservation{Concepts: []string{"best-practice"}, FilesModified: []string{"a.go"}})
	common := storeWith(t, obs, "common", &models.ParsedObservation{Concepts: []string{"architecture", "testing", "workflow"}})
	chosen := storeWith(t, obs, "chosen", &models.ParsedObservation{Concepts: []string{"architecture"}, Scope: models.ScopeGlobal})

	for id, want := range map[int64][2]string{
		general: {"global", "auto"}, touched: {"project", "auto"}, common: {"project", "auto"}, chosen: {"global", "explicit"},
	} {
		scope, source := scopeOf(t, store, id)
		assert.Equal(t, want, [2]string{scope, source}, "observation %d", id)
	}
}

func TestUpdateObservation_ChoosingAScopeMakesItExplicit(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	id := storeWith(t, obs, "a note", &models.ParsedObservation{})
	scope := "global"
	_, err := obs.UpdateObservation(context.Background(), id, &ObservationUpdate{Scope: &scope})
	require.NoError(t, err)
	got, source := scopeOf(t, store, id)
	assert.Equal(t, "global", got)
	assert.Equal(t, "explicit", source)

	title := "retitled"
	id2 := storeWith(t, obs, "another", &models.ParsedObservation{})
	_, err = obs.UpdateObservation(context.Background(), id2, &ObservationUpdate{Title: &title})
	require.NoError(t, err)
	_, source = scopeOf(t, store, id2)
	assert.Equal(t, "auto", source, "an edit that does not touch the scope leaves who decided it as it was")
}

func TestBackfillScopeSource_RememberedNotesAreExplicitTheRestAuto(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	remembered := storeWith(t, obs, "remembered", &models.ParsedObservation{Facts: []string{"Saved explicitly via claude-ai"}})
	extracted := storeWith(t, obs, "extracted", &models.ParsedObservation{Facts: []string{"something it learned"}})
	require.NoError(t, store.DB.Exec(`UPDATE observations SET scope_source = ''`).Error)

	require.NoError(t, backfillScopeSource(store.DB))
	_, source := scopeOf(t, store, remembered)
	assert.Equal(t, "explicit", source)
	_, source = scopeOf(t, store, extracted)
	assert.Equal(t, "auto", source)
}

// legacy makes an observation look like one saved by the old rule: global, decided by the rule.
func legacy(t *testing.T, store *Store, id int64, scope string) {
	t.Helper()
	require.NoError(t, store.DB.Exec(`UPDATE observations SET scope = ?, scope_source = 'auto' WHERE id = ?`, scope, id).Error)
}

func TestPlanRescope_ListsWhatTheRuleWouldChangeAndLeavesChosenScopesAlone(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	tooGlobal := storeWith(t, obs, "tagged only architecture", &models.ParsedObservation{Concepts: []string{"architecture", "testing"}})
	legacy(t, store, tooGlobal, "global")
	general := storeWith(t, obs, "a real lesson", &models.ParsedObservation{Concepts: []string{"best-practice"}})
	legacy(t, store, general, "project")
	fine := storeWith(t, obs, "stays global", &models.ParsedObservation{Concepts: []string{"anti-pattern"}})
	chosen := storeWith(t, obs, "chosen by a person", &models.ParsedObservation{Concepts: []string{"architecture"}, Scope: models.ScopeGlobal})
	storeWith(t, obs, "plainly project", &models.ParsedObservation{})

	plan, err := obs.PlanRescope(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, plan.Total)
	assert.Equal(t, 1, plan.Protected, "the explicit one is never in the plan")
	assert.Equal(t, 2, plan.Unchanged)
	assert.Equal(t, 1, plan.ToProject)
	assert.Equal(t, 1, plan.ToGlobal)
	require.Len(t, plan.Changes, 2)
	byID := map[int64]ScopeChange{}
	for _, c := range plan.Changes {
		byID[c.ID] = c
	}
	assert.Equal(t, ScopeChange{ID: tooGlobal, Project: "p_aaaaaa", Title: "tagged only architecture", From: models.ScopeGlobal, To: models.ScopeProject}, byID[tooGlobal])
	assert.Equal(t, models.ScopeGlobal, byID[general].To)
	assert.NotContains(t, byID, fine)
	assert.NotContains(t, byID, chosen)

	again, _ := obs.PlanRescope(ctx)
	assert.Equal(t, plan.Token(), again.Token(), "the token of an unchanged archive is stable")
	require.NoError(t, store.DB.Exec(`UPDATE observations SET scope = 'project' WHERE id = ?`, tooGlobal).Error)
	changed, _ := obs.PlanRescope(ctx)
	assert.NotEqual(t, plan.Token(), changed.Token(), "and it changes when what the plan would do changes")
}

func TestPlanRescope_AnEmptyArchiveHasNothingToDo(t *testing.T) {
	obs, _, cleanup := testObservationStore(t)
	defer cleanup()
	plan, err := obs.PlanRescope(context.Background())
	require.NoError(t, err)
	assert.Zero(t, plan.Total)
	assert.NotNil(t, plan.Changes, "an empty list, not null")
	assert.Empty(t, plan.Changes)
}

func TestApplyRescope_ChangesTheScopeAndTheVectorsAndSkipsWhatAPersonFixedSince(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()

	a := storeWith(t, obs, "to project", &models.ParsedObservation{Concepts: []string{"architecture"}})
	legacy(t, store, a, "global")
	b := storeWith(t, obs, "to global", &models.ParsedObservation{Concepts: []string{"best-practice"}})
	legacy(t, store, b, "project")
	c := storeWith(t, obs, "fixed after the preview", &models.ParsedObservation{Concepts: []string{"architecture"}})
	legacy(t, store, c, "global")
	for _, id := range []int64{a, b, c} {
		scope, _ := scopeOf(t, store, id)
		require.NoError(t, store.DB.Exec(
			`INSERT INTO vectors (doc_id, embedding, sqlite_id, doc_type, field_type, project, scope, model_version) VALUES (?, ?, ?, 'observation', 'narrative', 'p_aaaaaa', ?, 'test')`,
			fmt.Sprintf("obs_%d_narrative", id), vecBlob(), id, scope).Error)
	}

	plan, err := obs.PlanRescope(ctx)
	require.NoError(t, err)
	require.Len(t, plan.Changes, 3)
	// A person pins c's scope after the preview.
	require.NoError(t, store.DB.Exec(`UPDATE observations SET scope_source = 'explicit' WHERE id = ?`, c).Error)

	changed, err := obs.ApplyRescope(ctx, plan)
	require.NoError(t, err)
	assert.Equal(t, 2, changed)

	scope, source := scopeOf(t, store, a)
	assert.Equal(t, [2]string{"project", "auto"}, [2]string{scope, source})
	scope, _ = scopeOf(t, store, b)
	assert.Equal(t, "global", scope)
	scope, source = scopeOf(t, store, c)
	assert.Equal(t, [2]string{"global", "explicit"}, [2]string{scope, source}, "left exactly as the person set it")

	assert.Equal(t, "project", vectorScope(t, store, fmt.Sprintf("obs_%d_narrative", a)), "the vectors follow the notes")
	assert.Equal(t, "global", vectorScope(t, store, fmt.Sprintf("obs_%d_narrative", b)))
	assert.Equal(t, "global", vectorScope(t, store, fmt.Sprintf("obs_%d_narrative", c)), "and a skipped note's vectors are not touched")

	after, _ := obs.PlanRescope(ctx)
	assert.Empty(t, after.Changes, "nothing is left to do")
}

func TestApplyRescope_ABigPlanIsAppliedInChunks(t *testing.T) {
	obs, store, cleanup := testObservationStore(t)
	defer cleanup()
	ctx := context.Background()
	for i := 0; i < 950; i++ {
		require.NoError(t, store.DB.Exec(
			`INSERT INTO observations (sdk_session_id, project, type, title, concepts, scope, scope_source, created_at, created_at_epoch) VALUES ('s','p_aaaaaa','discovery',?, '["architecture"]','global','auto','2026-01-01T00:00:00Z',?)`,
			fmt.Sprintf("n%d", i), int64(1000+i)).Error)
	}
	plan, err := obs.PlanRescope(ctx)
	require.NoError(t, err)
	require.Len(t, plan.Changes, 950)
	changed, err := obs.ApplyRescope(ctx, plan)
	require.NoError(t, err)
	assert.Equal(t, 950, changed)
	var globals int64
	require.NoError(t, store.DB.Raw(`SELECT COUNT(*) FROM observations WHERE scope = 'global'`).Scan(&globals).Error)
	assert.Zero(t, globals)
}
