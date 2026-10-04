package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// fakeNeighbours returns, for every observation, the older notes of its project (closest = most recent) at a
// fixed similarity, and records the limits it was asked for.
type fakeNeighbours struct {
	svc         *Service
	err         map[int64]error
	limits      []int
	sim         float64
	mu          sync.Mutex
	unavailable bool
}

func (f *fakeNeighbours) find(ctx context.Context, obs *models.Observation, minSim float64, limit, _ int) ([]similarNote, bool, error) {
	f.mu.Lock()
	f.limits = append(f.limits, limit)
	f.mu.Unlock()
	if f.unavailable {
		return nil, false, nil
	}
	if err := f.err[obs.ID]; err != nil {
		return nil, true, err
	}
	var rows []gorm.Observation
	if err := f.svc.store.DB.WithContext(ctx).Where("project = ? AND created_at_epoch < ? AND COALESCE(is_superseded,0) = 0", obs.Project, obs.CreatedAtEpoch).
		Order("created_at_epoch DESC").Find(&rows).Error; err != nil {
		return nil, true, err
	}
	var out []similarNote
	for _, r := range rows {
		if f.sim >= minSim {
			o, err := f.svc.observationStore.GetObservationByID(ctx, r.ID)
			if err != nil {
				return nil, true, err
			}
			out = append(out, similarNote{obs: o, sim: f.sim})
		}
		if len(out) == limit {
			break
		}
	}
	return out, true, nil
}

func graphService(t *testing.T, mutate func(*config.Config)) (*Service, *fakeNeighbours, func()) {
	t.Helper()
	svc, cleanup := testService(t)
	cfg := *config.Default()
	if mutate != nil {
		mutate(&cfg)
	}
	svc.config = &cfg
	if svc.relationStore == nil {
		svc.relationStore = gorm.NewRelationStore(svc.store)
	}
	f := &fakeNeighbours{svc: svc, sim: 0.7, err: map[int64]error{}}
	svc.relationNeighbours = f.find
	return svc, f, cleanup
}

// addNotes stores n observations in a project, each a little newer than the one before, with distinct text.
func addNotes(t *testing.T, svc *Service, project string, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := 0; i < n; i++ {
		ids = append(ids, createTestObservation(t, svc.observationStore, project,
			"Relation note "+project+" "+string(rune('A'+i)), "Distinct narrative "+project+" "+string(rune('a'+i))+" xyz", nil))
		time.Sleep(3 * time.Millisecond)
	}
	return ids
}

func relationsOf(t *testing.T, svc *Service) []*models.ObservationRelation {
	t.Helper()
	var out []*models.ObservationRelation
	for _, typ := range models.AllRelationTypes {
		rs, err := svc.relationStore.GetRelationsByType(context.Background(), typ, 1000)
		require.NoError(t, err)
		out = append(out, rs...)
	}
	return out
}

func TestRelationFor_TypesAreLimitedAndSupersedesIsNeverDecidedByARule(t *testing.T) {
	obs := func(id int64, typ models.ObservationType, modified ...string) *models.Observation {
		return &models.Observation{ID: id, Type: typ, FilesModified: modified}
	}
	// Same type and a shared modified file: the file rule says "supersedes". That is a person's decision.
	r := relationFor(obs(2, models.ObsTypeDiscovery, "a.go"), obs(1, models.ObsTypeDiscovery, "a.go"), 0.71)
	assert.Equal(t, models.RelationRelatesTo, r.RelationType)
	assert.Equal(t, models.DetectionSourceEmbeddingSimilarity, r.DetectionSource)
	assert.Equal(t, int64(2), r.SourceID, "from the newer note")
	assert.Equal(t, int64(1), r.TargetID, "to the older one")
	assert.InDelta(t, 0.71, r.Confidence, 0.0001, "the confidence is how close the notes are")
	assert.Contains(t, r.Reason, "0.71")

	// A bugfix after a feature that touched the same file: the rules may say "fixes".
	r = relationFor(obs(2, models.ObsTypeBugfix, "a.go"), obs(1, models.ObsTypeFeature, "a.go"), 0.66)
	assert.Equal(t, models.RelationFixes, r.RelationType)
	assert.Equal(t, models.DetectionSourceFileOverlap, r.DetectionSource)
	assert.InDelta(t, 0.66, r.Confidence, 0.0001, "still the similarity, not the rule's own score")

	// Type progression without shared files.
	r = relationFor(obs(2, models.ObsTypeFeature), obs(1, models.ObsTypeDecision), 0.62)
	assert.Equal(t, models.RelationDependsOn, r.RelationType)
	assert.Equal(t, models.DetectionSourceTypeProgression, r.DetectionSource)

	// Nothing typed applies.
	r = relationFor(obs(2, models.ObsTypeDiscovery), obs(1, models.ObsTypeChange), 0.6)
	assert.Equal(t, models.RelationRelatesTo, r.RelationType)

	for _, typ := range []models.RelationType{models.RelationFixes, models.RelationDependsOn, models.RelationEvolvesFrom} {
		assert.True(t, typedRelation(typ), string(typ))
	}
	for _, typ := range []models.RelationType{models.RelationSupersedes, models.RelationCauses, models.RelationRelatesTo} {
		assert.False(t, typedRelation(typ), string(typ))
	}
}

func TestRunRelationPass_RelatesEachNoteToItsClosestOlderOnesAndRemembersWhatItDid(t *testing.T) {
	svc, f, cleanup := graphService(t, func(c *config.Config) { c.GraphRelationsMaxPerObs = 2 })
	defer cleanup()
	ids := addNotes(t, svc, "proj_aaaaaa", 4)

	looked, more := svc.runRelationPass(context.Background())
	assert.Equal(t, 4, looked)
	assert.False(t, more)
	rels := relationsOf(t, svc)
	// note 0: none; note 1: 1; note 2: 2; note 3: 2 (the limit)
	assert.Len(t, rels, 5)
	for _, r := range rels {
		assert.Greater(t, r.SourceID, r.TargetID, "always from the newer note to an older one")
	}
	assert.Contains(t, f.limits, 2, "the configured maximum per note is what is asked for")

	pending, err := svc.relationStore.CountUncheckedForRelations(context.Background())
	require.NoError(t, err)
	assert.Zero(t, pending)
	looked, _ = svc.runRelationPass(context.Background())
	assert.Zero(t, looked, "a second pass has nothing new")
	assert.Len(t, relationsOf(t, svc), 5, "and creates nothing twice")

	// a new note is picked up on its own
	addNotes(t, svc, "proj_aaaaaa", 1)
	looked, _ = svc.runRelationPass(context.Background())
	assert.Equal(t, 1, looked)
	assert.Len(t, relationsOf(t, svc), 7)
	_ = ids
}

func TestRunRelationPass_NeverCreatesSupersedes(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	addNotes(t, svc, "proj_aaaaaa", 3)
	svc.runRelationPass(context.Background())
	for _, r := range relationsOf(t, svc) {
		assert.NotEqual(t, models.RelationSupersedes, r.RelationType)
		assert.NotEqual(t, models.RelationCauses, r.RelationType)
	}
}

func TestRunRelationPass_AFailedNoteIsRetriedAndNoVectorSearchMeansNothingIsMarked(t *testing.T) {
	svc, f, cleanup := graphService(t, nil)
	defer cleanup()
	ids := addNotes(t, svc, "proj_aaaaaa", 3)

	f.err[ids[2]] = errors.New("vector search failed")
	looked, _ := svc.runRelationPass(context.Background())
	assert.Equal(t, 2, looked, "the others are done, the failed one is not marked")
	pending, _ := svc.relationStore.CountUncheckedForRelations(context.Background())
	assert.EqualValues(t, 1, pending)

	delete(f.err, ids[2])
	looked, _ = svc.runRelationPass(context.Background())
	assert.Equal(t, 1, looked, "retried on the next pass")

	more := addNotes(t, svc, "proj_aaaaaa", 1)
	f.unavailable = true
	looked, _ = svc.runRelationPass(context.Background())
	assert.Zero(t, looked)
	pending, _ = svc.relationStore.CountUncheckedForRelations(context.Background())
	assert.EqualValues(t, 1, pending, "without vector search nothing is marked, so it is tried again later")
	_ = more
}

func TestRunRelationPass_RespectsTheSettingsAndTheBatch(t *testing.T) {
	svc, _, cleanup := graphService(t, func(c *config.Config) { c.GraphEnabled = false })
	defer cleanup()
	addNotes(t, svc, "proj_aaaaaa", 2)
	looked, _ := svc.runRelationPass(context.Background())
	assert.Zero(t, looked, "switched off")
	assert.Empty(t, relationsOf(t, svc))

	svc.config.GraphEnabled = true
	svc.relationRunning.Store(true)
	looked, _ = svc.runRelationPass(context.Background())
	assert.Zero(t, looked, "one pass at a time")
	svc.relationRunning.Store(false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	looked, _ = svc.runRelationPass(ctx)
	assert.Zero(t, looked, "stops when cancelled")
}

func TestRunRelationPass_ABigArchiveIsWorkedThroughInBatches(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	// Inserted directly: the store merges notes that read alike, and these only need to exist.
	for i := 0; i < relationBatch+5; i++ {
		require.NoError(t, svc.store.DB.Exec(
			`INSERT INTO observations (sdk_session_id, project, type, title, created_at, created_at_epoch, scope) VALUES (?, ?, 'discovery', ?, '2026-01-01T00:00:00Z', ?, 'project')`,
			"s-bulk", "proj_aaaaaa", fmt.Sprintf("Bulk note %d", i), int64(1000+i)).Error)
	}
	looked, more := svc.runRelationPass(context.Background())
	assert.Equal(t, relationBatch, looked)
	assert.True(t, more, "more are waiting, so the loop comes back soon")
	looked, more = svc.runRelationPass(context.Background())
	assert.Equal(t, 5, looked)
	assert.False(t, more)
}

func TestRunRelationPass_AnnouncesNewRelations(t *testing.T) {
	svc, _, cleanup := graphService(t, nil)
	defer cleanup()
	addNotes(t, svc, "proj_aaaaaa", 2)
	// no listener needed: broadcasting with none connected must be harmless
	assert.NotPanics(t, func() { svc.runRelationPass(context.Background()) })
}
