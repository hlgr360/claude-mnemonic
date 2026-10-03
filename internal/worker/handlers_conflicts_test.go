package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
)

// conflictAPI is a test service with the conflict store the real service has.
func conflictAPI(t *testing.T) (*Service, func()) {
	t.Helper()
	svc, cleanup := testService(t)
	cfg := *config.Default()
	svc.config = &cfg
	if svc.conflictStore == nil {
		svc.conflictStore = gorm.NewConflictStore(svc.store)
	}
	return svc, cleanup
}

type conflictsBody struct {
	Conflicts []struct {
		Older, Newer struct {
			ID           int64 `json:"id"`
			IsSuperseded bool  `json:"is_superseded"`
		}
		Relation             string `json:"relation"`
		Confidence           string `json:"confidence"`
		Proposer             string `json:"proposer"`
		Decision             string `json:"decision"`
		Reason               string `json:"reason"`
		ID                   int64  `json:"id"`
		RestorableUntilEpoch int64  `json:"restorable_until_epoch"`
		Resolved             bool   `json:"resolved"`
	} `json:"conflicts"`
	Total     int `json:"total"`
	OpenCount int `json:"open_count"`
}

func listConflicts(t *testing.T, svc *Service, query string) conflictsBody {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/conflicts"+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var out conflictsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

// proposeByHand creates a conflict through the API and returns its id.
func proposeByHand(t *testing.T, svc *Service, older, newer int64) int64 {
	t.Helper()
	rec := doRequest(t, svc, http.MethodPost, "/api/conflicts", map[string]any{"older_id": older, "newer_id": newer, "reason": "same setting"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var out struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.ID
}

func resolveVia(t *testing.T, svc *Service, id int64, decision string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, svc, http.MethodPost, fmt.Sprintf("/api/conflicts/%d/resolve", id), map[string]string{"decision": decision})
}

func TestConflictsAPI_EmptyList(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()

	got := listConflicts(t, svc, "")
	assert.Empty(t, got.Conflicts)
	assert.Zero(t, got.Total)
	assert.Zero(t, got.OpenCount)

	rec := doRequest(t, svc, http.MethodGet, "/api/conflicts/count", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"open":0}`, rec.Body.String())
}

func TestConflictsAPI_CreateListResolveUndo(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()
	older, newer := twoNotes(t, svc, conflictTestProject)

	// Given in the wrong order, the pair is still put in order by age.
	id := proposeByHand(t, svc, newer, older)

	got := listConflicts(t, svc, "")
	require.Len(t, got.Conflicts, 1)
	c := got.Conflicts[0]
	assert.Equal(t, id, c.ID)
	assert.Equal(t, older, c.Older.ID)
	assert.Equal(t, newer, c.Newer.ID)
	assert.Equal(t, "manual", c.Proposer)
	assert.Equal(t, "same setting", c.Reason)
	assert.False(t, c.Resolved)
	assert.Equal(t, 1, got.OpenCount)

	rec := resolveVia(t, svc, id, gorm.DecisionSupersedeOlder)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resolved conflictsBody
	require.NoError(t, json.Unmarshal([]byte(`{"conflicts":[`+rec.Body.String()+`]}`), &resolved))
	assert.True(t, resolved.Conflicts[0].Resolved)
	assert.Equal(t, gorm.DecisionSupersedeOlder, resolved.Conflicts[0].Decision)
	assert.True(t, resolved.Conflicts[0].Older.IsSuperseded)
	assert.Zero(t, resolved.Conflicts[0].RestorableUntilEpoch, "with the default retention nothing is ever deleted")

	assert.Equal(t, 0, listConflicts(t, svc, "").Total, "open list is empty now")
	done := listConflicts(t, svc, "?status=resolved")
	require.Equal(t, 1, done.Total)
	assert.Equal(t, 0, done.OpenCount)
	assert.Equal(t, 1, listConflicts(t, svc, "?status=all").Total)

	rec = doRequest(t, svc, http.MethodPost, fmt.Sprintf("/api/conflicts/%d/undo", id), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, listConflicts(t, svc, "").OpenCount)
	hidden, err := svc.observationStore.GetObservationByID(context.Background(), older)
	require.NoError(t, err)
	assert.False(t, hidden.IsSuperseded, "undo makes the note visible again")
}

func TestConflictsAPI_ResolveErrors(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()
	older, newer := twoNotes(t, svc, conflictTestProject)
	id := proposeByHand(t, svc, older, newer)

	assert.Equal(t, http.StatusNotFound, resolveVia(t, svc, 9999, gorm.DecisionKeepBoth).Code, "unknown conflict")
	assert.Equal(t, http.StatusBadRequest, resolveVia(t, svc, id, "burn_it").Code, "unknown decision")
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPost, fmt.Sprintf("/api/conflicts/%d/resolve", id), nil).Code, "no body")
	assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPost, "/api/conflicts/abc/resolve", map[string]string{"decision": "keep_both"}).Code, "bad id")

	require.Equal(t, http.StatusOK, resolveVia(t, svc, id, gorm.DecisionKeepBoth).Code)
	assert.Equal(t, http.StatusConflict, resolveVia(t, svc, id, gorm.DecisionSupersedeOlder).Code, "already decided")

	assert.Equal(t, http.StatusNotFound, doRequest(t, svc, http.MethodPost, "/api/conflicts/9999/undo", nil).Code)
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, fmt.Sprintf("/api/conflicts/%d/undo", id), nil).Code)
	assert.Equal(t, http.StatusConflict, doRequest(t, svc, http.MethodPost, fmt.Sprintf("/api/conflicts/%d/undo", id), nil).Code, "not decided any more")
}

func TestConflictsAPI_ResolveWhenAnObservationWasDeleted(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()
	older, newer := twoNotes(t, svc, conflictTestProject)
	id := proposeByHand(t, svc, older, newer)

	require.NoError(t, svc.store.DB.Exec(`DELETE FROM observations WHERE id = ?`, older).Error)
	assert.Equal(t, http.StatusGone, resolveVia(t, svc, id, gorm.DecisionSupersedeNewer).Code)
	assert.Equal(t, 0, listConflicts(t, svc, "").Total, "a conflict whose observation is gone is not listed")
}

func TestConflictsAPI_CreateValidation(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()
	older, newer := twoNotes(t, svc, conflictTestProject)
	other := createTestObservation(t, svc.observationStore, "proj_bbbbbb", "Rates of another project", "Elsewhere", nil)

	post := func(body any) int { return doRequest(t, svc, http.MethodPost, "/api/conflicts", body).Code }
	assert.Equal(t, http.StatusBadRequest, post(nil), "no body")
	assert.Equal(t, http.StatusBadRequest, post(map[string]any{"older_id": older, "newer_id": older}), "the same observation twice")
	assert.Equal(t, http.StatusBadRequest, post(map[string]any{"older_id": 0, "newer_id": newer}), "missing id")
	assert.Equal(t, http.StatusNotFound, post(map[string]any{"older_id": older, "newer_id": 99999}), "unknown observation")
	assert.Equal(t, http.StatusUnprocessableEntity, post(map[string]any{"older_id": older, "newer_id": other}), "different projects")

	id := proposeByHand(t, svc, older, newer)
	rec := doRequest(t, svc, http.MethodPost, "/api/conflicts", map[string]any{"older_id": newer, "newer_id": older})
	assert.Equal(t, http.StatusOK, rec.Code, "asking again, in either order, answers with the existing proposal")
	var again struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &again))
	assert.Equal(t, id, again.ID)
	assert.Equal(t, 1, listConflicts(t, svc, "").Total)
}

func TestConflictsAPI_ProjectFilterStatusAndPaging(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()
	a1, a2 := twoNotes(t, svc, conflictTestProject)
	b1, b2 := twoNotes(t, svc, "proj_bbbbbb")
	proposeByHand(t, svc, a1, a2)
	proposeByHand(t, svc, b1, b2)

	assert.Equal(t, 2, listConflicts(t, svc, "").Total)
	only := listConflicts(t, svc, "?project="+conflictTestProject)
	require.Equal(t, 1, only.Total)
	assert.Equal(t, a1, only.Conflicts[0].Older.ID)
	assert.Equal(t, 1, only.OpenCount, "the open count follows the project filter")
	assert.Len(t, listConflicts(t, svc, "?limit=1").Conflicts, 1, "limit applies")
	paged := listConflicts(t, svc, "?limit=1&offset=1")
	assert.Len(t, paged.Conflicts, 1)
	assert.Equal(t, 2, paged.Total)

	rec := doRequest(t, svc, http.MethodGet, "/api/conflicts?status=bogus", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = doRequest(t, svc, http.MethodGet, "/api/conflicts?project=../etc", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = doRequest(t, svc, http.MethodGet, "/api/conflicts/count?project=proj_bbbbbb", nil)
	assert.JSONEq(t, `{"open":1}`, rec.Body.String())
}

func TestConflictsAPI_ProjectFilterFollowsAliases(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()
	a1, a2 := twoNotes(t, svc, conflictTestProject)
	proposeByHand(t, svc, a1, a2)
	require.NoError(t, gorm.NewProjectAliasStore(svc.store).SetAlias(context.Background(), "proj_cccccc", conflictTestProject, "test"))

	assert.Equal(t, 1, listConflicts(t, svc, "?project=proj_cccccc").Total)
}

func TestConflictsAPI_RestorableUntilShowsWhenRetentionIsOn(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()
	svc.config.SupersededRetentionDays = 3
	older, newer := twoNotes(t, svc, conflictTestProject)
	id := proposeByHand(t, svc, older, newer)

	before := time.Now().UnixMilli()
	require.Equal(t, http.StatusOK, resolveVia(t, svc, id, gorm.DecisionSupersedeOlder).Code)
	c := listConflicts(t, svc, "?status=resolved").Conflicts[0]
	threeDays := int64(3 * 24 * time.Hour / time.Millisecond)
	assert.GreaterOrEqual(t, c.RestorableUntilEpoch, before+threeDays)
	assert.LessOrEqual(t, c.RestorableUntilEpoch, time.Now().UnixMilli()+threeDays)

	// Keeping both hides nothing, so there is nothing to restore.
	older2, newer2 := twoNotes(t, svc, "proj_bbbbbb")
	id2 := proposeByHand(t, svc, older2, newer2)
	require.Equal(t, http.StatusOK, resolveVia(t, svc, id2, gorm.DecisionKeepBoth).Code)
	for _, item := range listConflicts(t, svc, "?status=resolved&project=proj_bbbbbb").Conflicts {
		assert.Zero(t, item.RestorableUntilEpoch)
	}
}

func TestConflictsAPI_DecisionsAreAnnounced(t *testing.T) {
	svc, cleanup := conflictAPI(t)
	defer cleanup()
	stream := httptest.NewRecorder()
	client, err := svc.sseBroadcaster.AddClient(stream)
	require.NoError(t, err)
	defer svc.sseBroadcaster.RemoveClient(client)
	older, newer := twoNotes(t, svc, conflictTestProject)

	id := proposeByHand(t, svc, older, newer)
	require.Equal(t, http.StatusOK, resolveVia(t, svc, id, gorm.DecisionKeepBoth).Code)
	require.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodPost, fmt.Sprintf("/api/conflicts/%d/undo", id), nil).Code)

	var actions []string
	for _, line := range strings.Split(stream.Body.String(), "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var m struct {
			Type   string `json:"type"`
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal([]byte(payload), &m))
		if m.Type == "conflict" {
			actions = append(actions, m.Action)
		}
	}
	assert.Equal(t, []string{"proposed", "resolved", "reopened"}, actions)
}
