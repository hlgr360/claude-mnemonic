package worker

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func note(id int64, importance float64, created int64) *models.Observation {
	return &models.Observation{ID: id, ImportanceScore: importance, CreatedAtEpoch: created, Title: sql.NullString{String: fmt.Sprintf("n%d", id), Valid: true}}
}

func TestChooseSurvivor(t *testing.T) {
	a, b, c := note(1, 1.0, 100), note(2, 2.0, 50), note(3, 2.0, 200)
	assert.Equal(t, int64(3), chooseSurvivor([]*models.Observation{a, b, c}, nil).ID, "highest importance, then the newest")
	assert.Equal(t, int64(2), chooseSurvivor([]*models.Observation{a, b}, nil).ID, "importance beats age")
	assert.Equal(t, int64(1), chooseSurvivor([]*models.Observation{a, b, c}, map[int64]bool{1: true}).ID, "a protected note survives over a better scored one")
	assert.Equal(t, int64(3), chooseSurvivor([]*models.Observation{a, b, c}, map[int64]bool{1: true, 3: true}).ID, "among protected ones the same order")
	assert.Equal(t, int64(4), chooseSurvivor([]*models.Observation{note(5, 1, 10), note(4, 1, 10)}, nil).ID, "a tie goes to the lower id")
	assert.Equal(t, int64(1), chooseSurvivor([]*models.Observation{a}, nil).ID)
}

func TestNewStringsAndRemoveStrings(t *testing.T) {
	assert.Equal(t, []string{"c", "D"}, newStrings([]string{"a", "B"}, []string{"A", " b ", "c", "C", "D", ""}), "without case or surrounding space, each once")
	assert.Empty(t, newStrings([]string{"a"}, nil))
	assert.Equal(t, []string{"a", "c"}, removeStrings([]string{"a", "b", "c"}, []string{"b", "z"}))
	assert.Equal(t, []string{}, removeStrings(nil, []string{"x"}))
}

func TestBuildConsolidationPlan(t *testing.T) {
	survivor := &models.Observation{ID: 1, ImportanceScore: 3, CreatedAtEpoch: 100, Facts: []string{"f1"}, Concepts: []string{"c1"}, FilesRead: []string{"a.go"}}
	d1 := &models.Observation{ID: 2, ImportanceScore: 1, CreatedAtEpoch: 90, Facts: []string{"F1", "f2"}, Concepts: []string{"c2"}, FilesRead: []string{"a.go", "b.go"}, FilesModified: []string{"m.go"}}
	d2 := &models.Observation{ID: 3, ImportanceScore: 1, CreatedAtEpoch: 80, Facts: []string{"f2", "f3"}, Concepts: []string{"c1", "c3"}}
	plan := buildConsolidationPlan("p", []*models.Observation{d1, survivor, d2}, nil)

	assert.Equal(t, int64(1), plan.Survivor.ID)
	require.Len(t, plan.Duplicates, 2)
	assert.Equal(t, []string{"f2", "f3", "Consolidated with #2, #3 (near-duplicate notes)"}, plan.AddedFacts, "only what the survivor lacks, and a trace of the merge")
	assert.Equal(t, []string{"c2", "c3"}, plan.AddedConcepts)
	assert.Equal(t, []string{"b.go", "m.go"}, plan.AddedFiles)
	assert.NotEmpty(t, plan.Token)
	assert.False(t, plan.Applied)

	t.Run("what the survivor carries stays bounded", func(t *testing.T) {
		many := &models.Observation{ID: 9, ImportanceScore: 1, CreatedAtEpoch: 1}
		for i := 0; i < 100; i++ {
			many.Facts = append(many.Facts, fmt.Sprintf("fact %d", i))
		}
		p := buildConsolidationPlan("p", []*models.Observation{survivor, many}, nil)
		assert.LessOrEqual(t, len(survivor.Facts)+len(p.AddedFacts), consolidationMaxFacts)
		assert.Contains(t, p.AddedFacts[len(p.AddedFacts)-1], "Consolidated with #9", "the trace always fits")
	})
}

func TestPlanToken(t *testing.T) {
	a := &models.Observation{ID: 1, Title: sql.NullString{String: "t", Valid: true}, Narrative: sql.NullString{String: "n", Valid: true}}
	b := &models.Observation{ID: 2, Title: sql.NullString{String: "t2", Valid: true}}
	base := planToken(a, []*models.Observation{a, b})
	assert.Equal(t, base, planToken(a, []*models.Observation{b, a}), "the order the notes are given in does not matter")
	assert.NotEqual(t, base, planToken(b, []*models.Observation{a, b}), "another survivor is another plan")
	edited := *a
	edited.Narrative = sql.NullString{String: "changed", Valid: true}
	assert.NotEqual(t, base, planToken(a, []*models.Observation{&edited, b}), "an edited note makes the preview stale")
	assert.NotEqual(t, base, planToken(a, []*models.Observation{a}), "so does a different set")
}

func TestDuplicateGroups(t *testing.T) {
	mk := func(id int64, typ models.ObservationType, title, narrative string) *models.Observation {
		return &models.Observation{ID: id, Type: typ, Title: sql.NullString{String: title, Valid: true}, Narrative: sql.NullString{String: narrative, Valid: true}}
	}
	text := "the harbour crane control board was replaced after the second inspection found a cracked relay"
	notes := []*models.Observation{
		mk(1, models.ObsTypeBugfix, "Crane board replaced", text),
		mk(2, models.ObsTypeBugfix, "Crane board replaced", text+" yesterday"),
		mk(3, models.ObsTypeDiscovery, "Crane board replaced", text), // the same words but another type
		mk(4, models.ObsTypeBugfix, "Tariff table", "an entirely different subject about pelican dockyard tariff rates"),
	}
	got := duplicateGroups(notes, 0.8)
	require.Len(t, got, 1)
	assert.Equal(t, []int64{1, 2}, []int64{got[0][0].ID, got[0][1].ID}, "alike and of the same type; the other type and the other subject are left out")
	assert.Empty(t, duplicateGroups(notes, 1.0+1e-9), "a threshold nothing reaches")
	assert.Empty(t, duplicateGroups(nil, 0.9))
}

// ---- with the real stores ----

func consolidationService(t *testing.T, mutate func(*config.Config)) (*Service, func()) {
	t.Helper()
	svc, cleanup := testService(t)
	cfg := *config.Default()
	cfg.ConsolidationEnabled = true
	cfg.ConsolidationMinSimilarity = 0.8
	cfg.ConsolidationMaxPerRun = 5
	if mutate != nil {
		mutate(&cfg)
	}
	svc.config = &cfg
	svc.relationStore = gorm.NewRelationStore(svc.store)
	return svc, cleanup
}

const craneText = "The harbour crane control board was replaced after the second inspection found a cracked relay in the hoist circuit"

// dupNote stores a note; scope "" means the extractor's automatic scope, a value is a scope chosen on purpose.
func dupNote(t *testing.T, svc *Service, project, title, narrative string, typ models.ObservationType, scope models.ObservationScope, facts, concepts []string) int64 {
	t.Helper()
	id, _, err := svc.observationStore.StoreObservation(context.Background(), "sdk-"+project, project, &models.ParsedObservation{
		Type: typ, Title: title, Narrative: narrative, Facts: facts, Concepts: concepts, Scope: scope}, 1, 1)
	require.NoError(t, err)
	time.Sleep(3 * time.Millisecond)
	return id
}

func TestPlanConsolidation_Validation(t *testing.T) {
	svc, cleanup := consolidationService(t, nil)
	defer cleanup()
	ctx := context.Background()
	a := dupNote(t, svc, "p_aaaaaa", "Crane board", craneText, models.ObsTypeBugfix, "", nil, nil)
	b := dupNote(t, svc, "p_aaaaaa", "Crane board", craneText+" today", models.ObsTypeBugfix, "", nil, nil)
	other := dupNote(t, svc, "q_bbbbbb", "Crane board", craneText, models.ObsTypeBugfix, "", nil, nil)
	archived := dupNote(t, svc, "p_aaaaaa", "Crane board", craneText+" archived", models.ObsTypeBugfix, "", nil, nil)
	require.NoError(t, svc.observationStore.ArchiveObservation(ctx, archived, "x"))
	superseded := dupNote(t, svc, "p_aaaaaa", "Crane board", craneText+" superseded", models.ObsTypeBugfix, "", nil, nil)
	require.NoError(t, svc.observationStore.MarkAsSuperseded(ctx, superseded))
	rollup := dupNote(t, svc, "p_aaaaaa", "Roll-up: crane", craneText+" rollup", models.ObsTypeBugfix, "", nil, []string{gorm.RollupConcept})

	cases := []struct {
		name string
		want string
		ids  []int64
	}{
		{"one note", "at least two", []int64{a}},
		{"the same note twice", "at least two", []int64{a, a}},
		{"too many", "at most", func() []int64 {
			var ids []int64
			for i := int64(1); i <= consolidationMaxIDs+1; i++ {
				ids = append(ids, i)
			}
			return ids
		}()},
		{"a note that does not exist", "does not exist or is archived", []int64{a, 99999}},
		{"an archived note", "does not exist or is archived", []int64{a, archived}},
		{"another project", "different projects", []int64{a, other}},
		{"a superseded note", "is superseded", []int64{a, superseded}},
		{"a roll-up", "is a roll-up", []int64{a, rollup}},
	}
	for _, c := range cases {
		_, err := svc.planConsolidation(ctx, c.ids)
		require.Error(t, err, c.name)
		assert.ErrorIs(t, err, ErrConsolidationInvalid, c.name)
		assert.Contains(t, err.Error(), c.want, c.name)
	}
	plan, err := svc.planConsolidation(ctx, []int64{a, b})
	require.NoError(t, err)
	assert.Equal(t, "p_aaaaaa", plan.Project)
	assert.Len(t, plan.Duplicates, 1)
}

func TestConsolidation_AppliesMergesCopiesRelationsAndUndoes(t *testing.T) {
	svc, cleanup := consolidationService(t, nil)
	defer cleanup()
	ctx := context.Background()
	p := "p_aaaaaa"
	a := dupNote(t, svc, p, "Crane board replaced", craneText, models.ObsTypeBugfix, "", []string{"relay was cracked"}, []string{"hardware"})
	b := dupNote(t, svc, p, "Crane board replaced", craneText+" again", models.ObsTypeBugfix, "", []string{"relay was cracked", "board is model X9"}, []string{"hardware", "cranes"})
	c := dupNote(t, svc, p, "Crane board replaced", craneText+" once more", models.ObsTypeBugfix, "", []string{"fixed by the second shift"}, nil)
	elsewhere := dupNote(t, svc, p, "Tariff table", "an unrelated note about pelican dockyard tariffs", models.ObsTypeDiscovery, "", nil, nil)
	require.NoError(t, svc.observationStore.UpdateObservationFeedback(ctx, a, 0))
	_, err := svc.observationStore.UpdateObservation(ctx, a, &gorm.ObservationUpdate{})
	require.NoError(t, err)

	// b relates to the unrelated note, and to c (inside the group: not copied).
	rel := func(from, to int64) {
		_, err := svc.relationStore.StoreRelation(ctx, models.NewObservationRelation(from, to, models.RelationRelatesTo, 0.7, models.DetectionSourceConceptOverlap, "test"))
		require.NoError(t, err)
	}
	rel(b, elsewhere)
	rel(b, c)

	plan, err := svc.planConsolidation(ctx, []int64{a, b, c})
	require.NoError(t, err)
	assert.Equal(t, 1, plan.RelationsToCopy, "only the relation to a note outside the group")
	survivor := plan.Survivor.ID

	foldID, err := svc.applyConsolidation(ctx, plan)
	require.NoError(t, err)

	live := liveIDs(t, svc, p)
	assert.Contains(t, live, survivor)
	archivedCount := 0
	for _, id := range []int64{a, b, c} {
		if id != survivor {
			assert.NotContains(t, live, id, "a duplicate is archived")
			archivedCount++
		}
	}
	assert.Equal(t, 2, archivedCount)
	assert.Contains(t, live, elsewhere)

	merged, err := svc.observationStore.GetObservationByID(ctx, survivor)
	require.NoError(t, err)
	assert.Subset(t, merged.Facts, []string{"relay was cracked", "board is model X9", "fixed by the second shift"})
	assert.Subset(t, merged.Concepts, []string{"hardware", "cranes"})
	assert.True(t, strings.HasPrefix(merged.Facts[len(merged.Facts)-1], "Consolidated with #"), "the trace is last")

	var reason string
	dup := a
	if survivor == a {
		dup = b
	}
	require.NoError(t, svc.store.DB.Raw("SELECT archived_reason FROM observations WHERE id = ?", dup).Scan(&reason).Error)
	assert.Equal(t, fmt.Sprintf("consolidated into #%d", survivor), reason)

	rels, err := svc.relationStore.GetRelationsByObservationID(ctx, survivor)
	require.NoError(t, err)
	hasElsewhere := false
	for _, r := range rels {
		if r.SourceID == elsewhere || r.TargetID == elsewhere {
			hasElsewhere = true
		}
	}
	assert.True(t, hasElsewhere, "the survivor took over the relation to the unrelated note")

	fold, err := gorm.NewObservationFoldStore(svc.store).Get(ctx, foldID)
	require.NoError(t, err)
	require.NotNil(t, fold)
	assert.Equal(t, gorm.FoldConsolidation, fold.Kind)
	assert.Equal(t, survivor, fold.SurvivorID)

	// Undo: the duplicates are live again, the survivor gives back what it took, the copied relation goes.
	rep, err := svc.restoreFold(ctx, foldID)
	require.NoError(t, err)
	assert.Equal(t, gorm.FoldConsolidation, rep.Kind)
	assert.False(t, rep.SurvivorArchived, "a consolidation's survivor stays")
	assert.Len(t, rep.Restored, 2)
	live = liveIDs(t, svc, p)
	for _, id := range []int64{a, b, c} {
		assert.Contains(t, live, id)
	}
	after, err := svc.observationStore.GetObservationByID(ctx, survivor)
	require.NoError(t, err)
	original := map[int64][]string{a: {"relay was cracked"}, b: {"relay was cracked", "board is model X9"}, c: {"fixed by the second shift"}}
	assert.ElementsMatch(t, original[survivor], after.Facts, "the survivor has its own facts again")
	rels, err = svc.relationStore.GetRelationsByObservationID(ctx, survivor)
	require.NoError(t, err)
	for _, r := range rels {
		if survivor != b {
			assert.False(t, r.SourceID == elsewhere || r.TargetID == elsewhere, "the copied relation is gone")
		}
	}
	_, err = svc.restoreFold(ctx, foldID)
	assert.ErrorIs(t, err, ErrFoldUndone)
}

func TestConsolidationHandler_PreviewConfirmAndStale(t *testing.T) {
	svc, cleanup := consolidationService(t, nil)
	defer cleanup()
	ctx := context.Background()
	p := "p_aaaaaa"
	a := dupNote(t, svc, p, "Crane board replaced", craneText, models.ObsTypeBugfix, "", nil, nil)
	b := dupNote(t, svc, p, "Crane board replaced", craneText+" again", models.ObsTypeBugfix, "", nil, nil)

	rec := doRequest(t, svc, http.MethodPost, "/api/observations/consolidate", map[string]any{"ids": []int64{a, b}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var preview ConsolidationPlan
	decode(t, rec, &preview)
	assert.False(t, preview.Applied)
	assert.NotEmpty(t, preview.Token)
	assert.Len(t, liveIDs(t, svc, p), 2, "a preview changes nothing")

	rec = doRequest(t, svc, http.MethodPost, "/api/observations/consolidate", map[string]any{"ids": []int64{a, b}, "confirm": "wrong"})
	assert.Equal(t, http.StatusConflict, rec.Code, "a token that is not the plan's is refused")

	// A note is edited between the preview and the confirm: the preview is stale.
	newText := "edited after the preview"
	_, err := svc.observationStore.UpdateObservation(ctx, b, &gorm.ObservationUpdate{Narrative: &newText})
	require.NoError(t, err)
	rec = doRequest(t, svc, http.MethodPost, "/api/observations/consolidate", map[string]any{"ids": []int64{a, b}, "confirm": preview.Token})
	assert.Equal(t, http.StatusConflict, rec.Code, "a stale preview is refused")
	assert.Len(t, liveIDs(t, svc, p), 2)

	rec = doRequest(t, svc, http.MethodPost, "/api/observations/consolidate", map[string]any{"ids": []int64{a, b}})
	decode(t, rec, &preview)
	rec = doRequest(t, svc, http.MethodPost, "/api/observations/consolidate", map[string]any{"ids": []int64{a, b}, "confirm": preview.Token})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var done ConsolidationPlan
	decode(t, rec, &done)
	assert.True(t, done.Applied)
	assert.NotZero(t, done.FoldID)
	assert.Len(t, liveIDs(t, svc, p), 1)

	assert.Equal(t, http.StatusUnprocessableEntity, doRequest(t, svc, http.MethodPost, "/api/observations/consolidate", map[string]any{"ids": []int64{a}}).Code)
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPost, "/api/observations/consolidate", nil).Code)
}

func TestRunConsolidationPass(t *testing.T) {
	svc, cleanup := consolidationService(t, nil)
	defer cleanup()
	ctx := context.Background()
	p := "p_aaaaaa"
	// A group of three near-identical automatic notes, and a pair of another type.
	g := []int64{
		dupNote(t, svc, p, "Crane board replaced", craneText, models.ObsTypeBugfix, "", nil, nil),
		dupNote(t, svc, p, "Crane board replaced", craneText+" later", models.ObsTypeBugfix, "", nil, nil),
		dupNote(t, svc, p, "Crane board replaced", craneText+" much later", models.ObsTypeBugfix, "", nil, nil),
	}
	otherType := dupNote(t, svc, p, "Crane board replaced", craneText, models.ObsTypeDiscovery, "", nil, nil)
	// The same words, but protected: a decision, a rated note, a note saved on purpose.
	decision := dupNote(t, svc, p, "Crane board replaced", craneText, models.ObsTypeDecision, "", nil, nil)
	rated := dupNote(t, svc, p, "Crane board replaced", craneText+" rated", models.ObsTypeBugfix, "", nil, nil)
	require.NoError(t, svc.observationStore.UpdateObservationFeedback(ctx, rated, 1))
	saved := dupNote(t, svc, p, "Crane board replaced", craneText+" saved", models.ObsTypeBugfix, models.ScopeProject, nil, nil)

	assert.Equal(t, 1, svc.runConsolidationPass(ctx), "one group: the three automatic notes of one type")
	live := liveIDs(t, svc, p)
	keptOfGroup := 0
	for _, id := range g {
		if _, ok := live[id]; ok {
			keptOfGroup++
		}
	}
	assert.Equal(t, 1, keptOfGroup, "two of the three were folded into the third")
	for _, id := range []int64{otherType, decision, rated, saved} {
		assert.Contains(t, live, id, "another type, and every protected note, is left alone")
	}
	assert.Equal(t, 0, svc.runConsolidationPass(ctx), "a second pass finds nothing")

	t.Run("a threshold nothing reaches changes nothing, and a budget bounds a pass", func(t *testing.T) {
		svc2, cleanup2 := consolidationService(t, func(c *config.Config) { c.ConsolidationMaxPerRun = 1 })
		defer cleanup2()
		for i := 0; i < 3; i++ {
			dupNote(t, svc2, "a_aaaaaa", fmt.Sprintf("Crane %d", i), craneText+fmt.Sprintf(" variant %d", i), models.ObsTypeBugfix, "", nil, nil)
			dupNote(t, svc2, "b_bbbbbb", fmt.Sprintf("Quay %d", i), "the new quay light was installed along the east pier and tested by the harbour master "+fmt.Sprint(i), models.ObsTypeBugfix, "", nil, nil)
		}
		assert.Equal(t, 1, svc2.runConsolidationPass(ctx), "one group in all, though two projects have one")
	})
}
