package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const duplicatesJSON = `{"suggestions": [
  {"survivor": {"project": "shop_aaaaaa", "label": "shop", "observations": 40},
   "other": {"project": "webshop_bbbbbb", "label": "webshop", "observations": 6},
   "strength": "strong", "reasons": [{"code": "same_remote", "text": "both were cloned from git.example.org/team/shop"}, {"code": "path_gone", "text": "the folder of webshop_bbbbbb no longer exists"}],
   "auto_mergeable": true},
  {"survivor": {"project": "app_cccccc", "label": "app", "observations": 9},
   "other": {"project": "app_dddddd", "label": "app", "observations": 2},
   "strength": "weak", "reasons": [{"code": "few_notes", "text": "app_dddddd holds only 2 notes"}]}
], "dismissed": [], "auto_merge": false}`

func TestProjectManage_DuplicatesListsThePairsWithReasonsAndHowToMergeThem(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){"GET /api/projects/duplicates": jsonReply(duplicatesJSON)})
	s := desktopServer(t, fw)

	out, err := call(s, "project_manage", map[string]any{"action": "duplicates"})
	require.NoError(t, err)
	assert.Contains(t, out, "2 pair(s) of projects are probably the same project, strongest evidence first")
	assert.Contains(t, out, "1. [strong] shop (shop_aaaaaa, 40 notes) and webshop (webshop_bbbbbb, 6 notes): both were cloned from git.example.org/team/shop; the folder of webshop_bbbbbb no longer exists. Merging would move webshop_bbbbbb into shop_aaaaaa.")
	assert.Contains(t, out, "2. [weak]")
	assert.Contains(t, out, "only if the user agrees", "a merge is never done on the model's own say-so")
	assert.Contains(t, out, "project_manage dismiss")
}

func TestProjectManage_NoDuplicatesSaysWhatCountsAsEvidence(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){"GET /api/projects/duplicates": jsonReply(`{"suggestions": []}`)})
	out, err := call(desktopServer(t, fw), "project_manage", map[string]any{"action": "duplicates"})
	require.NoError(t, err)
	assert.Contains(t, out, "No projects look like duplicates")
	assert.Contains(t, out, "same git remote")
}

func TestProjectManage_DismissSendsTheResolvedPair(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/summary":             jsonReply(`[{"project":"shop_aaaaaa","display_name":"shop","label":"shop (clients)"},{"project":"shop_bbbbbb","display_name":"shop","label":"shop (team)"}]`),
		"POST /api/projects/duplicates/dismiss": jsonReply(`{"dismissed": true}`),
	})
	s := desktopServer(t, fw)

	out, err := call(s, "project_manage", map[string]any{"action": "dismiss", "project": "shop_aaaaaa", "into": "shop_bbbbbb"})
	require.NoError(t, err)
	assert.Contains(t, out, "not the same project")
	req := fw.requests("/api/projects/duplicates/dismiss")
	require.Len(t, req, 1)
	assert.Equal(t, map[string]any{"a": "shop_aaaaaa", "b": "shop_bbbbbb"}, req[0].body)

	_, err = call(s, "project_manage", map[string]any{"action": "dismiss", "project": "shop_aaaaaa"})
	require.Error(t, err, "both projects are needed")
	_, err = call(s, "project_manage", map[string]any{"action": "dismiss", "project": "shop", "into": "shop_bbbbbb"})
	require.Error(t, err, "a name two projects share is not a project: the user is asked which")
}

func TestUnresolvedNamesakesThatAreProbablyOneSaySo(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/resolve": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("name") == "shop" {
				jsonReply(`{"match":"ambiguous","ambiguous":true,"candidates":["shop_aaaaaa","shop_bbbbbb","shop_cccccc"],"candidate_details":[
					{"project":"shop_aaaaaa","label":"shop (a)","detail":"40 notes","probably_same_as":["shop_bbbbbb"]},
					{"project":"shop_bbbbbb","label":"shop (b)","detail":"6 notes","probably_same_as":["shop_aaaaaa"]},
					{"project":"shop_cccccc","label":"shop (c)","detail":"9 notes"}]}`)(w, r)
				return
			}
			jsonReply(`{"match":"none"}`)(w, r)
		},
	})
	_, err := call(desktopServer(t, fw), "context", map[string]any{"project": "shop"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(a) shop_aaaaaa: 40 notes [probably the same project as shop_bbbbbb]")
	assert.Contains(t, err.Error(), "(b) shop_bbbbbb: 6 notes [probably the same project as shop_aaaaaa]")
	assert.Contains(t, err.Error(), "(c) shop_cccccc: 9 notes.", "a real namesake carries no such note")
	assert.NotContains(t, err.Error(), "(c) shop_cccccc: 9 notes [")
}

func TestDesktopDescriptionsAndInstructionsTellTheModelWhatToDoWithProbableDuplicates(t *testing.T) {
	byName := map[string]string{}
	for _, tool := range desktopTools() {
		byName[tool.Name] = tool.Description
	}
	assert.Contains(t, byName["project_suggest"], "probably_same_as")
	assert.Contains(t, byName["project_resolve"], "probably_same_as")
	assert.Contains(t, desktopInstructions, "probably the same project")
	assert.Contains(t, byName["project_manage"], "duplicates")
}
