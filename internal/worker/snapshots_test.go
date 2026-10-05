package worker

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotDue(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	assert.True(t, snapshotDue(time.Time{}, now, day), "none yet")
	assert.True(t, snapshotDue(now.Add(-25*time.Hour), now, day), "older than the interval")
	assert.True(t, snapshotDue(now.Add(-day), now, day), "exactly the interval")
	assert.False(t, snapshotDue(now.Add(-2*time.Hour), now, day), "a recent one")
	assert.False(t, snapshotDue(time.Time{}, now, 0), "an interval of zero is never")
	assert.False(t, snapshotDue(now.Add(-100*day), now, -time.Hour), "nor a negative one")
}

func snapshotFiles(t *testing.T, svc *Service) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(svc.store.DefaultSnapshotDir(), "snapshot-*.db"))
	require.NoError(t, err)
	return m
}

func TestRunSnapshotPass_TakesTheRegularSnapshotOnceAndNotAgainWhileItIsCurrent(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	cfg := *svc.config
	cfg.SnapshotIntervalHours, cfg.SnapshotsDailyKeep = 24, 2
	svc.config = &cfg
	ctx := context.Background()

	path, err := svc.runSnapshotPass(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, path)
	assert.True(t, strings.HasSuffix(path, "-daily.db"), path)
	assert.Len(t, snapshotFiles(t, svc), 1)

	again, err := svc.runSnapshotPass(ctx)
	require.NoError(t, err)
	assert.Empty(t, again, "the newest is current, so no second one")
	assert.Len(t, snapshotFiles(t, svc), 1)

	// A day on, it is due again; the keep setting prunes the oldest regular snapshot.
	old := time.Now().Add(-25 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	next, err := svc.runSnapshotPass(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, next)
	assert.Len(t, snapshotFiles(t, svc), 2)
}

func TestRunSnapshotPass_IsOffWithAnIntervalOfZero(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	cfg := *svc.config
	cfg.SnapshotIntervalHours = 0
	svc.config = &cfg

	path, err := svc.runSnapshotPass(context.Background())
	require.NoError(t, err)
	assert.Empty(t, path)
	assert.Empty(t, snapshotFiles(t, svc))
}

func TestSnapshotBeforeCleanup_SkipsWhenAnySnapshotIsUnderAnHourOld(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	ctx := context.Background()

	svc.snapshotBeforeCleanup(ctx, "cap")
	require.Len(t, snapshotFiles(t, svc), 1, "none yet, so one is taken")
	assert.Contains(t, snapshotFiles(t, svc)[0], "-before-cap.db")

	svc.snapshotBeforeCleanup(ctx, "cap")
	assert.Len(t, snapshotFiles(t, svc), 1, "a burst of archived notes writes one copy, not one each")

	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(snapshotFiles(t, svc)[0], old, old))
	svc.snapshotBeforeCleanup(ctx, "cap")
	assert.Len(t, snapshotFiles(t, svc), 2, "an hour later, another")
}

func TestStats_SaysHowManySnapshotsThereAreAndHowOldTheNewestIs(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()

	rec := doRequest(t, svc, http.MethodGet, "/api/stats", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"snapshots":{"count":0}`)

	svc.snapshotBeforeCleanup(context.Background(), "cap")
	rec = doRequest(t, svc, http.MethodGet, "/api/stats", nil)
	assert.Contains(t, rec.Body.String(), `"snapshots":{"count":1,"newest_age_seconds":`)
}
