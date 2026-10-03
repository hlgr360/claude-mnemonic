package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/vector/sqlitevec"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const conflictTestProject = "proj_aaaaaa"

// fakeProposer answers every question with the same verdict for the first older note, and remembers what it was asked.
type fakeProposer struct {
	err      error
	relation string
	calls    []int64
	mu       sync.Mutex
}

func (f *fakeProposer) propose(_ context.Context, newer *models.Observation, olders []*models.Observation) ([]sdk.ConflictVerdict, error) {
	f.mu.Lock()
	f.calls = append(f.calls, newer.ID)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return []sdk.ConflictVerdict{{OlderID: olders[0].ID, Relation: f.relation, Confidence: "high", Reason: "The newer note changes it."}}, nil
}

func (f *fakeProposer) asked() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.calls...)
}

// conflictService is a test service whose neighbour search finds every observation of the asked-for project
// at the given similarity.
func conflictService(t *testing.T, similarity float64, mutate func(*config.Config)) (*Service, *fakeProposer, func()) {
	t.Helper()
	svc, cleanup := testService(t)
	cfg := *config.Default()
	cfg.ConflictProposalsEnabled = true
	if mutate != nil {
		mutate(&cfg)
	}
	svc.config = &cfg
	if svc.conflictStore == nil {
		svc.conflictStore = gorm.NewConflictStore(svc.store)
	}
	p := &fakeProposer{relation: sdk.RelationSupersedes}
	svc.conflictProposer = p.propose
	svc.vectorQueryFn = func(ctx context.Context, _ string, _ int, where map[string]interface{}) ([]sqlitevec.QueryResult, error) {
		var rows []gorm.Observation
		require.NoError(t, svc.store.DB.WithContext(ctx).Where("project = ?", where["project"]).Find(&rows).Error)
		var out []sqlitevec.QueryResult
		for _, r := range rows {
			out = append(out, sqlitevec.QueryResult{
				Similarity: similarity,
				Metadata: map[string]interface{}{
					"doc_type": "observation", "sqlite_id": float64(r.ID), "project": r.Project, "title": r.Title.String,
				},
			})
		}
		return out, nil
	}
	return svc, p, cleanup
}

// twoNotes stores an older and a newer observation in the project, in that order.
func twoNotes(t *testing.T, svc *Service, project string) (older, newer int64) {
	t.Helper()
	older = createTestObservation(t, svc.observationStore, project, "Rates are cached for an hour "+project, "The rate cache lives for 60 minutes", []string{"cache"})
	time.Sleep(3 * time.Millisecond)
	newer = createTestObservation(t, svc.observationStore, project, "Rates are cached for a day "+project, "The rate cache now lives for 24 hours", []string{"cache"})
	return older, newer
}

func checked(t *testing.T, svc *Service, id int64) bool {
	t.Helper()
	var n int64
	require.NoError(t, svc.store.DB.Raw(`SELECT COUNT(*) FROM conflict_checks WHERE observation_id = ?`, id).Scan(&n).Error)
	return n == 1
}

func TestConflictTypeFor(t *testing.T) {
	for relation, want := range map[string]models.ConflictType{
		sdk.RelationSupersedes:  models.ConflictSuperseded,
		sdk.RelationDuplicate:   models.ConflictSuperseded,
		sdk.RelationContradicts: models.ConflictContradicts,
	} {
		got, ok := conflictTypeFor(relation)
		assert.True(t, ok, relation)
		assert.Equal(t, want, got, relation)
	}
	for _, relation := range []string{sdk.RelationRelated, sdk.RelationUnrelated, "", "nonsense"} {
		_, ok := conflictTypeFor(relation)
		assert.False(t, ok, relation)
	}
}

func TestConflictQueryIsClipped(t *testing.T) {
	o := &models.Observation{}
	o.Title.String = "Title"
	o.Narrative.String = string(make([]rune, 5000))
	assert.LessOrEqual(t, len([]rune(conflictQuery(o))), conflictQueryChars)
}

func TestRunConflictPass_ProposesAndRemembersWhatItChecked(t *testing.T) {
	svc, p, cleanup := conflictService(t, 0.9, nil)
	defer cleanup()
	older, newer := twoNotes(t, svc, conflictTestProject)

	assert.Equal(t, 1, svc.runConflictPass(context.Background()))

	records, total, err := svc.conflictStore.ListConflicts(context.Background(), gorm.ConflictFilter{Project: conflictTestProject, Status: gorm.ConflictStatusOpen})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	c := records[0].Conflict
	assert.Equal(t, newer, c.NewerObsID)
	assert.Equal(t, older, c.OlderObsID)
	assert.Equal(t, models.ConflictSuperseded, c.ConflictType)
	assert.Equal(t, sdk.RelationSupersedes, c.Relation)
	assert.Equal(t, "high", c.Confidence)
	assert.Equal(t, conflictProposerLLM, c.Proposer)
	assert.NotEmpty(t, c.Reason)
	assert.Equal(t, []int64{newer}, p.asked(), "the older note has no older neighbours, so the model is only asked about the newer one")

	// Nothing is hidden by a proposal.
	live, err := svc.observationStore.GetObservationByID(context.Background(), older)
	require.NoError(t, err)
	assert.False(t, live.IsSuperseded)

	assert.True(t, checked(t, svc, newer))
	assert.True(t, checked(t, svc, older))

	assert.Equal(t, 0, svc.runConflictPass(context.Background()), "a second pass has nothing new to look at")
	assert.Len(t, p.asked(), 1, "and does not ask the model again")
}

func TestRunConflictPass_AFailedCheckIsRetried(t *testing.T) {
	svc, p, cleanup := conflictService(t, 0.9, nil)
	defer cleanup()
	_, newer := twoNotes(t, svc, conflictTestProject)

	p.err = errors.New("model unavailable")
	assert.Equal(t, 0, svc.runConflictPass(context.Background()))
	assert.False(t, checked(t, svc, newer), "a failed question is not remembered as asked")

	p.mu.Lock()
	p.err = nil
	p.mu.Unlock()
	assert.Equal(t, 1, svc.runConflictPass(context.Background()))
	assert.True(t, checked(t, svc, newer))
}

func TestRunConflictPass_OnlyProposableVerdictsAreKept(t *testing.T) {
	for _, relation := range []string{sdk.RelationRelated, sdk.RelationUnrelated} {
		svc, p, cleanup := conflictService(t, 0.9, nil)
		p.relation = relation
		_, newer := twoNotes(t, svc, conflictTestProject)

		assert.Equal(t, 0, svc.runConflictPass(context.Background()), relation)
		n, err := svc.conflictStore.CountOpen(context.Background(), "")
		require.NoError(t, err)
		assert.Zero(t, n, relation)
		assert.True(t, checked(t, svc, newer), "the answer was no conflict, and that is remembered")
		cleanup()
	}
}

func TestRunConflictPass_DuplicateAndContradictionKeepTheirRelation(t *testing.T) {
	for relation, kind := range map[string]models.ConflictType{
		sdk.RelationDuplicate: models.ConflictSuperseded, sdk.RelationContradicts: models.ConflictContradicts,
	} {
		svc, p, cleanup := conflictService(t, 0.9, nil)
		p.relation = relation
		twoNotes(t, svc, conflictTestProject)

		require.Equal(t, 1, svc.runConflictPass(context.Background()), relation)
		records, _, err := svc.conflictStore.ListConflicts(context.Background(), gorm.ConflictFilter{Status: gorm.ConflictStatusOpen})
		require.NoError(t, err)
		require.Len(t, records, 1)
		assert.Equal(t, kind, records[0].Conflict.ConflictType, relation)
		assert.Equal(t, relation, records[0].Conflict.Relation, relation)
		cleanup()
	}
}

func TestRunConflictPass_BelowTheSimilarityThresholdNothingIsAsked(t *testing.T) {
	svc, p, cleanup := conflictService(t, 0.3, nil)
	defer cleanup()
	_, newer := twoNotes(t, svc, conflictTestProject)

	assert.Equal(t, 0, svc.runConflictPass(context.Background()))
	assert.Empty(t, p.asked())
	assert.True(t, checked(t, svc, newer), "no close neighbour is an answer too")
}

func TestRunConflictPass_NeighboursAreOlderLiveAndInTheSameProject(t *testing.T) {
	svc, p, cleanup := conflictService(t, 0.9, nil)
	defer cleanup()
	older, newer := twoNotes(t, svc, conflictTestProject)
	hidden := createTestObservation(t, svc.observationStore, conflictTestProject, "Rates were cached for ten minutes", "A third, long superseded note", nil)
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET is_superseded = 1, created_at_epoch = created_at_epoch - 100000 WHERE id = ?`, hidden).Error)
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET created_at_epoch = created_at_epoch - 50000 WHERE id = ?`, older).Error)

	var seen []int64
	svc.conflictProposer = func(ctx context.Context, n *models.Observation, olders []*models.Observation) ([]sdk.ConflictVerdict, error) {
		if n.ID == newer {
			for _, o := range olders {
				seen = append(seen, o.ID)
			}
		}
		return p.propose(ctx, n, olders)
	}
	svc.runConflictPass(context.Background())
	assert.Equal(t, []int64{older}, seen, "the superseded note and the newer note itself are not neighbours")
}

func TestRunConflictPass_StaysWithinOneProject(t *testing.T) {
	svc, p, cleanup := conflictService(t, 0.9, nil)
	defer cleanup()
	twoNotes(t, svc, conflictTestProject)
	twoNotes(t, svc, "proj_bbbbbb")

	assert.Equal(t, 2, svc.runConflictPass(context.Background()))
	records, _, err := svc.conflictStore.ListConflicts(context.Background(), gorm.ConflictFilter{Status: gorm.ConflictStatusOpen})
	require.NoError(t, err)
	for _, r := range records {
		assert.Equal(t, r.Older.Project, r.Newer.Project)
	}
	assert.Len(t, p.asked(), 2)
}

func TestRunConflictPass_RespectsTheBudget(t *testing.T) {
	svc, _, cleanup := conflictService(t, 0.9, func(c *config.Config) { c.ConflictProposalsMaxPerRun = 2 })
	defer cleanup()
	for i := 0; i < 3; i++ {
		twoNotes(t, svc, "proj_"+string(rune('a'+i))+"aaaaa")
	}
	checkedCount := func() int64 {
		var n int64
		require.NoError(t, svc.store.DB.Raw(`SELECT COUNT(*) FROM conflict_checks`).Scan(&n).Error)
		return n
	}

	svc.runConflictPass(context.Background())
	assert.EqualValues(t, 2, checkedCount(), "two observations were looked at, in all projects together")
	for i := 0; i < 3; i++ {
		svc.runConflictPass(context.Background())
	}
	assert.EqualValues(t, 6, checkedCount(), "the rest follow in later passes")
}

func TestRunConflictPass_ADecidedPairIsNotProposedAgain(t *testing.T) {
	svc, _, cleanup := conflictService(t, 0.9, nil)
	defer cleanup()
	older, newer := twoNotes(t, svc, conflictTestProject)
	require.Equal(t, 1, svc.runConflictPass(context.Background()))
	records, _, err := svc.conflictStore.ListConflicts(context.Background(), gorm.ConflictFilter{Status: gorm.ConflictStatusOpen})
	require.NoError(t, err)
	_, err = svc.conflictStore.ResolveProposal(context.Background(), records[0].Conflict.ID, gorm.DecisionKeepBoth)
	require.NoError(t, err)

	require.NoError(t, svc.store.DB.Exec(`DELETE FROM conflict_checks`).Error)
	assert.Equal(t, 0, svc.runConflictPass(context.Background()), "the person said keep both, so the pair stays quiet")
	n, err := svc.conflictStore.CountOpen(context.Background(), "")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.True(t, checked(t, svc, newer))
	assert.True(t, checked(t, svc, older))
}

func TestRunConflictPass_NothingHappensWithoutVectorSearchOrABackend(t *testing.T) {
	svc, p, cleanup := conflictService(t, 0.9, nil)
	defer cleanup()
	_, newer := twoNotes(t, svc, conflictTestProject)

	svc.vectorQueryFn = nil
	assert.Equal(t, 0, svc.runConflictPass(context.Background()))
	assert.Empty(t, p.asked())
	assert.False(t, checked(t, svc, newer), "without vector search nothing was checked, so it is tried again later")

	svc.conflictProposer = nil
	svc.processor = nil
	assert.Equal(t, 0, svc.runConflictPass(context.Background()), "no proposer and no processor is not an error")
}

func TestRunConflictPass_OnePassAtATime(t *testing.T) {
	svc, p, cleanup := conflictService(t, 0.9, nil)
	defer cleanup()
	twoNotes(t, svc, conflictTestProject)

	svc.conflictRunning.Store(true)
	assert.Equal(t, 0, svc.runConflictPass(context.Background()))
	assert.Empty(t, p.asked())
	svc.conflictRunning.Store(false)
	assert.Equal(t, 1, svc.runConflictPass(context.Background()))
	assert.False(t, svc.conflictRunning.Load(), "the guard is released afterwards")
}

func TestRunConflictPass_StopsWhenCancelled(t *testing.T) {
	svc, p, cleanup := conflictService(t, 0.9, nil)
	defer cleanup()
	twoNotes(t, svc, conflictTestProject)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Equal(t, 0, svc.runConflictPass(ctx))
	assert.Empty(t, p.asked())
}
