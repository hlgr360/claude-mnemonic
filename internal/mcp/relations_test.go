package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const connectionsJSON = `{
  "observation": {"id": 12, "title": "Queue retries are capped", "type": "decision", "project": "alpha_aaaaaa"},
  "connections": [
    {"relation": {"relation_type": "fixes", "confidence": 0.9, "reason": "both name the retry cap", "source_id": 12, "target_id": 7},
     "direction": "older", "note": {"id": 7, "title": "Retries loop forever", "type": "bugfix", "project": "alpha_aaaaaa", "created_at_epoch": 1788000000000}},
    {"relation": {"relation_type": "depends_on", "confidence": 0.7, "source_id": 15, "target_id": 12},
     "direction": "newer", "note": {"id": 15, "title": "", "type": "feature", "project": "alpha_aaaaaa"}}
  ],
  "total": 2
}`

func relationsWorker(t *testing.T, extra map[string]func(http.ResponseWriter, *http.Request)) *fakeWorker {
	t.Helper()
	routes := map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/observations/12/connections": jsonReply(connectionsJSON),
		"GET /api/relations/types": jsonReply(`{"total": 4, "types": [
			{"type": "relates_to", "description": "The two notes are about the same thing.", "automatic": true, "count": 3},
			{"type": "fixes", "description": "The source note fixes the problem the target describes.", "automatic": true, "count": 1},
			{"type": "supersedes", "description": "Replaces; decided in the conflict review.", "automatic": false, "count": 0}]}`),
		"GET /api/projects/resolve": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("name") == "alpha" {
				jsonReply(`{"id":"alpha_aaaaaa","match":"name","known":true}`)(w, r)
				return
			}
			jsonReply(`{"match":"none"}`)(w, r)
		},
		"GET /api/context/search": jsonReply(`{"observations": [{"id": 12, "title": "Queue retries are capped"}]}`),
	}
	for k, v := range extra {
		routes[k] = v
	}
	return newFakeWorker(t, routes)
}

func TestRelated_FullModeAnswersWithIdsPhrasesAndDates(t *testing.T) {
	fw := relationsWorker(t, nil)
	s := NewServer(fw.Client(), fw.URL, "alpha_aaaaaa", "test")
	s.SetMode(ModeCode)

	out, err := call(s, "observation", map[string]any{"action": "related", "id": 12})
	require.NoError(t, err)
	assert.Contains(t, out, `Note #12 "Queue retries are capped" (decision, alpha_aaaaaa) has 2 connections:`)
	assert.Contains(t, out, `- #7 "Retries loop forever" (bugfix, alpha_aaaaaa, 2026-08-29, older): this note fixes it [fixes, 0.90] — both name the retry cap`)
	assert.Contains(t, out, `- #15 (untitled) (feature, alpha_aaaaaa, newer): this note is built on by it [depends_on, 0.70]`, "a note without a title is still named by its id")
	assert.Contains(t, out, "Pass any of these ids to related")
	require.Len(t, fw.requests("/api/observations/12/connections"), 1)
	assert.Empty(t, fw.requests("/api/observations/12/connections")[0].query, "no filter asked for, none sent")
}

func TestRelated_PassesTheFiltersOnAndSaysWhenNothingMatches(t *testing.T) {
	fw := relationsWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/observations/13/connections": jsonReply(`{"observation": {"id": 13, "title": "Lonely", "type": "discovery", "project": "alpha_aaaaaa"}, "connections": [], "total": 0}`),
	})
	s := NewServer(fw.Client(), fw.URL, "alpha_aaaaaa", "test")
	s.SetMode(ModeCode)

	out, err := call(s, "observation", map[string]any{"action": "related", "id": 12,
		"types": []string{"fixes", "depends_on"}, "direction": "older", "min_confidence": 0.6, "limit": 5})
	require.NoError(t, err)
	assert.NotEmpty(t, out)
	q := fw.requests("/api/observations/12/connections")[0].query
	assert.Equal(t, map[string]string{"types": "fixes,depends_on", "direction": "older", "min_confidence": "0.6", "limit": "5"}, q)

	out, err = call(s, "observation", map[string]any{"action": "related", "id": 13})
	require.NoError(t, err)
	assert.Contains(t, out, "has no connections yet", "an unfiltered empty answer says the graph is built in the background")
	out, err = call(s, "observation", map[string]any{"action": "related", "id": 13, "types": []string{"fixes"}})
	require.NoError(t, err)
	assert.Contains(t, out, "has no connections that match", "a filtered empty answer points at the filters")
}

func TestRelated_ShowsHowManyThereAreWhenTheListIsCut(t *testing.T) {
	fw := relationsWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/observations/12/connections": jsonReply(`{"observation": {"id": 12, "title": "T", "type": "decision", "project": "p"},
			"connections": [{"relation": {"relation_type": "relates_to", "confidence": 0.8, "source_id": 12}, "direction": "older", "note": {"id": 3, "title": "Three", "type": "discovery", "project": "p"}}], "total": 35}`),
	})
	s := NewServer(fw.Client(), fw.URL, "p", "test")
	s.SetMode(ModeCode)
	out, err := call(s, "observation", map[string]any{"action": "related", "id": 12, "limit": 1})
	require.NoError(t, err)
	assert.Contains(t, out, "has 35 connections; the 1 most certain:")
	assert.Contains(t, out, "Narrow with types, direction or min_confidence")
}

func TestRelated_FindsTheNoteByQueryInTheProject(t *testing.T) {
	fw := relationsWorker(t, nil)
	s := desktopServer(t, fw)

	out, err := call(s, "related", map[string]any{"query": "retry cap", "project": "alpha"})
	require.NoError(t, err)
	assert.Contains(t, out, "Note #12")
	search := fw.requests("/api/context/search")
	require.Len(t, search, 1)
	assert.Equal(t, "alpha_aaaaaa", search[0].query["project"], "the project given by name is resolved to its id")
	assert.Equal(t, "retry cap", search[0].query["query"])
	assert.Equal(t, "1", search[0].query["limit"])

	_, err = call(s, "related", map[string]any{"query": "retry cap"})
	require.Error(t, err, "Desktop has no default project")
	assert.Contains(t, err.Error(), "a project is required")
	_, err = call(s, "related", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass the id of a note")
	_, err = call(s, "related", map[string]any{"id": -3})
	require.Error(t, err)
}

func TestRelated_NoMatchForTheQueryIsSaidPlainly(t *testing.T) {
	fw := relationsWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/context/search": jsonReply(`{"observations": []}`),
	})
	s := desktopServer(t, fw)
	_, err := call(s, "related", map[string]any{"query": "nothing like it", "project": "alpha"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no note in this project matches")
}

func TestRelationTypes_ListsMeaningCountAndWhoCreatesThem(t *testing.T) {
	fw := relationsWorker(t, nil)
	s := desktopServer(t, fw)

	out, err := call(s, "relation_types", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, out, "Relations in all projects: 4 in all.")
	assert.Contains(t, out, "- relates_to (3, created by the graph): The two notes are about the same thing.")
	assert.Contains(t, out, "- supersedes (0, set by a person or a tool, never by the graph)")
	assert.Empty(t, fw.requests("/api/relations/types")[0].query)

	_, err = call(s, "relation_types", map[string]any{"project": "alpha"})
	require.NoError(t, err)
	assert.Equal(t, "alpha_aaaaaa", fw.requests("/api/relations/types")[1].query["project"])
	out, err = call(s, "relation_types", map[string]any{"id": 12})
	require.NoError(t, err)
	assert.Contains(t, out, "around note #12")
	assert.Equal(t, "12", fw.requests("/api/relations/types")[2].query["observation_id"])

	code := NewServer(fw.Client(), fw.URL, "alpha_aaaaaa", "test")
	code.SetMode(ModeCode)
	out, err = call(code, "observation", map[string]any{"action": "relation_types"})
	require.NoError(t, err, "full mode reaches it through the observation tool")
	assert.Contains(t, out, "relates_to")
}

func TestRelationships_SendsTheDepthTheWorkerReadsAndTheFilters(t *testing.T) {
	fw := relationsWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/observations/12/graph": jsonReply(`{"center_id": 12, "relations": []}`),
	})
	s := NewServer(fw.Client(), fw.URL, "alpha_aaaaaa", "test")
	s.SetMode(ModeCode)
	_, err := call(s, "observation", map[string]any{"action": "relationships", "id": 12, "max_depth": 3, "types": []string{"fixes"}, "min_confidence": 0.5})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"depth": "3", "types": "fixes", "min_confidence": "0.5"}, fw.requests("/api/observations/12/graph")[0].query)
}

func TestRelationTools_AreListedInDesktopOnlyAsTools(t *testing.T) {
	fw := relationsWorker(t, nil)
	desktop := desktopServer(t, fw)
	assert.Contains(t, toolNames(t, desktop), "related")
	assert.Contains(t, toolNames(t, desktop), "relation_types")

	code := NewServer(fw.Client(), fw.URL, "alpha_aaaaaa", "test")
	code.SetMode(ModeCode)
	assert.NotContains(t, toolNames(t, code), "related", "Claude Code reaches it through the observation tool")
	_, err := call(code, "related", map[string]any{"id": 12})
	require.Error(t, err)
}
