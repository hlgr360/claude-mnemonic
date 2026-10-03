package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

func TestBriefDue(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{ProjectBriefMinNewObs: 10, ProjectBriefMaxAgeDays: 7}
	day := func(n int) int64 { return now.Add(-time.Duration(n) * 24 * time.Hour).UnixMilli() }

	cases := []struct {
		cfgOverride  func(*config.Config)
		name         string
		generated    int64
		newObs, live int
		hasBrief     bool
		want         bool
	}{
		{nil, "no brief and too few observations", 0, 9, 9, false, false},
		{nil, "no brief and enough observations", 0, 10, 10, false, true},
		{nil, "a brief, nothing new", day(1), 0, 40, true, false},
		{nil, "a brief, a few new, recent", day(1), 3, 43, true, false},
		{nil, "a brief and enough new ones", day(1), 10, 50, true, true},
		{nil, "an old brief and one new observation", day(8), 1, 41, true, true},
		{nil, "an old brief and nothing new", day(30), 0, 40, true, false},
		{nil, "exactly at the age limit", day(7), 1, 41, true, true},
		{nil, "just under the age limit", day(6), 1, 41, true, false},
		{func(c *config.Config) { c.ProjectBriefMaxAgeDays = 0 }, "age rule switched off", day(90), 1, 41, true, false},
		{func(c *config.Config) { c.ProjectBriefMinNewObs = 0 }, "a threshold of zero means the default of ten", 0, 9, 9, false, false},
		{func(c *config.Config) { c.ProjectBriefMinNewObs = 3 }, "a lower threshold", 0, 3, 3, false, true},
	}
	for _, c := range cases {
		cc := *cfg
		if c.cfgOverride != nil {
			c.cfgOverride(&cc)
		}
		assert.Equal(t, c.want, briefDue(&cc, now, c.hasBrief, c.generated, c.newObs, c.live), c.name)
	}
}

// briefService is a test service with its own config and a writer that needs no model.
func briefService(t *testing.T, mutate func(*config.Config)) (*Service, *fakeBriefWriter, func()) {
	t.Helper()
	svc, cleanup := testService(t)
	cfg := *config.Default()
	cfg.ProjectBriefEnabled = true
	cfg.ProjectBriefMinNewObs = 3
	cfg.ProjectBriefMaxPerRun = 5
	if mutate != nil {
		mutate(&cfg)
	}
	svc.config = &cfg
	w := &fakeBriefWriter{}
	svc.briefWriter = w.write
	return svc, w, cleanup
}

type fakeBriefWriter struct {
	err   error
	block chan struct{}
	calls []sdk.BriefInput
	mu    sync.Mutex
}

func (f *fakeBriefWriter) write(_ context.Context, in sdk.BriefInput) (*sdk.BriefResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	if f.err != nil {
		return nil, f.err
	}
	return &sdk.BriefResult{Text: "As of today.\n\n## What this is\nA brief for " + in.Name, Source: "fake source"}, nil
}

func (f *fakeBriefWriter) projects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Name)
	}
	return out
}

var observationSeq atomic.Int64

// addObs stores n observations with titles and texts that are all different, so none is taken for a duplicate.
func addObs(t *testing.T, svc *Service, project string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		seq := observationSeq.Add(1)
		_, _, err := svc.observationStore.StoreObservation(context.Background(), "sdk-"+project, project,
			&models.ParsedObservation{
				Type: models.ObsTypeDiscovery, Title: fmt.Sprintf("note %d of %s", seq, project),
				Narrative: fmt.Sprintf("narrative number %d about something different in %s", seq, project),
			}, 1, 1)
		require.NoError(t, err)
		time.Sleep(3 * time.Millisecond)
	}
}

func TestRunBriefPass_WritesBriefsOnlyForProjectsThatNeedOneAndOnlyOnce(t *testing.T) {
	svc, w, cleanup := briefService(t, nil)
	defer cleanup()
	ctx := context.Background()
	addObs(t, svc, "big_aaaaaa", 5)
	addObs(t, svc, "small_bbbbbb", 2)

	assert.Equal(t, 1, svc.runBriefPass(ctx), "only the project with enough observations")
	assert.Equal(t, []string{"big"}, w.projects())
	b, err := svc.summaryStore.GetBrief(ctx, "big_aaaaaa")
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Contains(t, b.Text, "A brief for big")
	assert.Equal(t, "fake source", b.Source)
	none, _ := svc.summaryStore.GetBrief(ctx, "small_bbbbbb")
	assert.Nil(t, none)

	assert.Equal(t, 0, svc.runBriefPass(ctx), "nothing new, nothing written")
	assert.Len(t, w.calls, 1)

	// the writer was given what it needs
	in := w.calls[0]
	assert.Equal(t, "big", in.Name)
	assert.Equal(t, 5, in.Total)
	assert.Len(t, in.Observations, 5)
}

func TestRunBriefPass_RewritesInPlaceAfterEnoughNewObservationsOrAnOldBriefWithSomethingNew(t *testing.T) {
	svc, _, cleanup := briefService(t, nil)
	defer cleanup()
	ctx := context.Background()
	addObs(t, svc, "big_aaaaaa", 4)
	require.Equal(t, 1, svc.runBriefPass(ctx))
	first, _ := svc.summaryStore.GetBrief(ctx, "big_aaaaaa")

	addObs(t, svc, "big_aaaaaa", 2)
	assert.Equal(t, 0, svc.runBriefPass(ctx), "two new ones are not enough and the brief is fresh")

	addObs(t, svc, "big_aaaaaa", 1)
	assert.Equal(t, 1, svc.runBriefPass(ctx), "three new ones are")
	second, _ := svc.summaryStore.GetBrief(ctx, "big_aaaaaa")
	assert.Equal(t, first.ID, second.ID, "replaced in place, one brief per project")
	assert.Greater(t, second.GeneratedEpoch, first.GeneratedEpoch)

	// an old brief is refreshed as soon as anything is new
	// make it all 8 to 10 days old: the observations first, then the brief written after them
	require.NoError(t, svc.store.DB.Exec(`UPDATE observations SET created_at_epoch = ? WHERE project = ?`,
		time.Now().Add(-10*24*time.Hour).UnixMilli(), "big_aaaaaa").Error)
	require.NoError(t, svc.store.DB.Exec(`UPDATE session_summaries SET created_at_epoch = ? WHERE id = ?`,
		time.Now().Add(-8*24*time.Hour).UnixMilli(), second.ID).Error)
	assert.Equal(t, 0, svc.runBriefPass(ctx), "old, but nothing is new")
	addObs(t, svc, "big_aaaaaa", 1)
	assert.Equal(t, 1, svc.runBriefPass(ctx), "old and something is new")
}

func TestRunBriefPass_RespectsTheCapAndWritesTheBusiestFirst(t *testing.T) {
	svc, w, cleanup := briefService(t, func(c *config.Config) { c.ProjectBriefMaxPerRun = 2 })
	defer cleanup()
	addObs(t, svc, "few_cccccc", 3)
	addObs(t, svc, "most_aaaaaa", 9)
	addObs(t, svc, "some_bbbbbb", 5)

	assert.Equal(t, 2, svc.runBriefPass(context.Background()))
	assert.Equal(t, []string{"most", "some"}, w.projects(), "the projects with the most new observations first")
	assert.Equal(t, 1, svc.runBriefPass(context.Background()), "the next pass takes the one that was left")
	assert.Equal(t, []string{"most", "some", "few"}, w.projects())
}

func TestRunBriefPass_AFailureIsSkippedAndTheOthersStillGetTheirBrief(t *testing.T) {
	svc, w, cleanup := briefService(t, nil)
	defer cleanup()
	addObs(t, svc, "aaa_aaaaaa", 4)
	addObs(t, svc, "bbb_bbbbbb", 4)
	w.err = errors.New("model down")

	assert.Equal(t, 0, svc.runBriefPass(context.Background()))
	assert.Len(t, w.calls, 2, "both were tried")
	none, _ := svc.summaryStore.GetBrief(context.Background(), "aaa_aaaaaa")
	assert.Nil(t, none, "nothing half-written")

	w.err = nil
	assert.Equal(t, 2, svc.runBriefPass(context.Background()), "the next pass tries again")
}

func TestRunBriefPass_AliasesAreNotProjectsOfTheirOwn(t *testing.T) {
	svc, w, cleanup := briefService(t, nil)
	defer cleanup()
	addObs(t, svc, "main_aaaaaa", 4)
	addObs(t, svc, "frag_bbbbbb", 4)
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases", setAliasRequest{Alias: "frag_bbbbbb", Canonical: "main_aaaaaa"}).Code)

	assert.Equal(t, 1, svc.runBriefPass(context.Background()))
	assert.Equal(t, []string{"main"}, w.projects())
}

func TestRunBriefPass_NeverRunsAProjectWithoutObservations(t *testing.T) {
	svc, w, cleanup := briefService(t, func(c *config.Config) { c.ProjectBriefMinNewObs = 1 })
	defer cleanup()
	seedProjects(t, svc, "empty_aaaaaa")
	_, _, err := svc.summaryStore.StoreSummary(context.Background(), "sdk-s", "summ_bbbbbb", &models.ParsedSummary{Request: "only a summary"}, 1, 0)
	require.NoError(t, err)

	assert.Equal(t, 0, svc.runBriefPass(context.Background()))
	assert.Empty(t, w.calls)
}

func TestBriefLoop_WritesBriefsOnItsOwnWhenSwitchedOn(t *testing.T) {
	old := briefFirstPassDelay
	briefFirstPassDelay = 40 * time.Millisecond
	defer func() { briefFirstPassDelay = old }()

	svc, _, cleanup := briefService(t, func(c *config.Config) { c.ProjectBriefIntervalMinutes = 1 })
	defer cleanup()
	addObs(t, svc, "auto_aaaaaa", 4)

	svc.wg.Add(1)
	go svc.briefLoop()
	require.Eventually(t, func() bool {
		b, _ := svc.summaryStore.GetBrief(context.Background(), "auto_aaaaaa")
		return b != nil
	}, 3*time.Second, 20*time.Millisecond, "the first pass wrote the brief")
	svc.cancel()
	svc.wg.Wait()
}

func getBrief(t *testing.T, svc *Service, project string) (int, BriefResponse) {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/projects/"+project+"/brief", nil)
	var b BriefResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b))
	}
	return rec.Code, b
}

func TestBriefEndpoints_PostWritesAndGetReadsIncludingThroughAnAlias(t *testing.T) {
	svc, w, cleanup := briefService(t, func(c *config.Config) { c.ProjectBriefEnabled = false })
	defer cleanup()
	addObs(t, svc, "main_aaaaaa", 2) // below the automatic threshold: asking is the user's own decision
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/aliases", setAliasRequest{Alias: "frag_bbbbbb", Canonical: "main_aaaaaa"}).Code)

	code, _ := getBrief(t, svc, "main_aaaaaa")
	assert.Equal(t, http.StatusNotFound, code, "no brief yet")

	rec := doRequest(t, svc, http.MethodPost, "/api/projects/frag_bbbbbb/brief", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, []string{"main"}, w.projects(), "written for the canonical project")

	code, b := getBrief(t, svc, "frag_bbbbbb")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "main_aaaaaa", b.Project)
	assert.Contains(t, b.Text, "A brief for main")
	assert.Equal(t, "fake source", b.Source)
	assert.Equal(t, time.UnixMilli(b.AsOfEpoch).UTC().Format("2006-01-02"), b.AsOf)
	assert.Greater(t, b.AsOfEpoch, int64(0))
}

func TestBriefEndpoints_Refusals(t *testing.T) {
	svc, w, cleanup := briefService(t, nil)
	defer cleanup()
	seedProjects(t, svc, "empty_aaaaaa")

	rec := doRequest(t, svc, http.MethodPost, "/api/projects/invented_zzzzzz/brief", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "unknown project")

	w.err = sdk.ErrNothingToBrief
	rec = doRequest(t, svc, http.MethodPost, "/api/projects/empty_aaaaaa/brief", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a project with nothing to summarise")

	w.err = errors.New("model down")
	addObs(t, svc, "empty_aaaaaa", 1)
	rec = doRequest(t, svc, http.MethodPost, "/api/projects/empty_aaaaaa/brief", nil)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "model down")

	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodGet, "/api/projects/..%2Fetc/brief", nil).Code)

	svc.briefWriter = nil // and no processor in the test service
	rec = doRequest(t, svc, http.MethodPost, "/api/projects/empty_aaaaaa/brief", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "nothing can write a brief")
}

func TestBriefEndpoints_OnlyOneBriefPerProjectAtATime(t *testing.T) {
	svc, w, cleanup := briefService(t, nil)
	defer cleanup()
	addObs(t, svc, "main_aaaaaa", 4)
	w.block = make(chan struct{})

	first := make(chan int, 1)
	go func() { first <- doRequest(t, svc, http.MethodPost, "/api/projects/main_aaaaaa/brief", nil).Code }()
	require.Eventually(t, func() bool { return len(w.projects()) == 1 }, 3*time.Second, 10*time.Millisecond)

	rec := doRequest(t, svc, http.MethodPost, "/api/projects/main_aaaaaa/brief", nil)
	assert.Equal(t, http.StatusConflict, rec.Code, "the second request while one is being written")
	assert.Equal(t, 0, svc.runBriefPass(context.Background()), "and the automatic pass leaves that project alone too")

	close(w.block)
	assert.Equal(t, http.StatusOK, <-first)
	assert.Len(t, w.projects(), 1)
}

func TestCatchUp_CarriesTheBriefWhenThereIsOne(t *testing.T) {
	svc, _, cleanup := briefService(t, nil)
	defer cleanup()
	addObs(t, svc, "main_aaaaaa", 4)

	_, digest := catchUp(t, svc, "/api/projects/main_aaaaaa/catch-up")
	assert.Nil(t, digest.Brief, "no brief yet")

	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, "/api/projects/main_aaaaaa/brief", nil).Code)
	_, digest = catchUp(t, svc, "/api/projects/main_aaaaaa/catch-up")
	require.NotNil(t, digest.Brief)
	assert.Contains(t, digest.Brief.Text, "A brief for main")
	assert.Equal(t, "fake source", digest.Brief.Source)
	assert.NotEmpty(t, digest.Brief.AsOf)
}
