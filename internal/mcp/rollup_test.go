package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rollupWorker(t *testing.T) *fakeWorker {
	return newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/projects/shop_aaaaaa/rollup": jsonReply(`{"project":"shop_aaaaaa","dry_run":true,"groups":[]}`),
		"GET /api/folds":                        jsonReply(`{"folds":[]}`),
		"POST /api/folds/7/restore":             jsonReply(`{"kind":"rollup","restored":[1,2]}`),
		"POST /api/observations/consolidate":    jsonReply(`{"project":"shop_aaaaaa","token":"abc123","applied":false}`),
	})
}

func TestMemoryAdmin_RollupPreviewsUnlessToldOtherwise(t *testing.T) {
	fw := rollupWorker(t)
	s := desktopServer(t, fw)

	out, err := call(s, "memory_admin", map[string]any{"action": "rollup", "project": "shop_aaaaaa"})
	require.NoError(t, err)
	assert.Contains(t, out, `"dry_run":true`)
	reqs := fw.requests("/api/projects/shop_aaaaaa/rollup")
	require.Len(t, reqs, 1)
	assert.Equal(t, true, reqs[0].body["dry_run"], "no dry_run argument means a preview: a model cannot archive notes by accident")

	_, err = call(s, "memory_admin", map[string]any{"action": "rollup", "project": "shop_aaaaaa", "dry_run": false, "max_groups": 2})
	require.NoError(t, err)
	reqs = fw.requests("/api/projects/shop_aaaaaa/rollup")
	require.Len(t, reqs, 2)
	assert.Equal(t, false, reqs[1].body["dry_run"])
	assert.EqualValues(t, 2, reqs[1].body["max_groups"])
}

func TestMemoryAdmin_RollupNeedsAProject(t *testing.T) {
	fw := rollupWorker(t)
	s := desktopServer(t, fw)
	s.project = ""
	_, err := call(s, "memory_admin", map[string]any{"action": "rollup"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project is required")
	assert.Zero(t, fw.total())
}

func TestMemoryAdmin_FoldsAndRestore(t *testing.T) {
	fw := rollupWorker(t)
	s := desktopServer(t, fw)

	_, err := call(s, "memory_admin", map[string]any{"action": "folds", "project": "shop_aaaaaa", "kind": "rollup", "include_undone": true, "limit": 5})
	require.NoError(t, err)
	q := fw.requests("/api/folds")[0].query
	assert.Equal(t, "shop_aaaaaa", q["project"])
	assert.Equal(t, "rollup", q["kind"])
	assert.Equal(t, "true", q["include_undone"])
	assert.Equal(t, "5", q["limit"])

	out, err := call(s, "memory_admin", map[string]any{"action": "restore_fold", "id": 7})
	require.NoError(t, err)
	assert.Contains(t, out, `"restored":[1,2]`)
	assert.Equal(t, http.MethodPost, fw.requests("/api/folds/7/restore")[0].method)

	_, err = call(s, "memory_admin", map[string]any{"action": "restore_fold"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")
}

func TestMemoryAdmin_ConsolidatePreviewsFirstThenAppliesWithTheToken(t *testing.T) {
	fw := rollupWorker(t)
	s := desktopServer(t, fw)

	out, err := call(s, "memory_admin", map[string]any{"action": "consolidate", "ids": []int64{4, 9, 12}})
	require.NoError(t, err)
	assert.Contains(t, out, `"token":"abc123"`)
	reqs := fw.requests("/api/observations/consolidate")
	require.Len(t, reqs, 1)
	assert.Equal(t, []any{float64(4), float64(9), float64(12)}, reqs[0].body["ids"])
	assert.Equal(t, "", reqs[0].body["confirm"], "no token means a preview")

	_, err = call(s, "memory_admin", map[string]any{"action": "consolidate", "ids": []int64{4, 9, 12}, "confirm": "abc123"})
	require.NoError(t, err)
	reqs = fw.requests("/api/observations/consolidate")
	require.Len(t, reqs, 2)
	assert.Equal(t, "abc123", reqs[1].body["confirm"])

	_, err = call(s, "memory_admin", map[string]any{"action": "consolidate", "ids": []int64{4}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least two")
	assert.Len(t, fw.requests("/api/observations/consolidate"), 2, "a single note never reaches the worker")
}

func TestMemoryAdmin_ToolDescriptionListsTheNewActionsInBothModes(t *testing.T) {
	code := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, code, "claude-code")
	assert.Contains(t, toolDescriptions(t, code)["memory_admin"], "rollup, folds, restore_fold, consolidate")
}
