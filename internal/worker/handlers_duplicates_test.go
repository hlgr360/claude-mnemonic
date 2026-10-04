package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
)

const shopRemote = "git.example.org/team/shop"

// addProject gives a project n notes with distinct titles.
func addProject(t *testing.T, svc *Service, project string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		createTestObservation(t, svc.observationStore, project, project+" note "+string(rune('a'+i)), "narrative "+project+" "+string(rune('a'+i)), nil)
		time.Sleep(2 * time.Millisecond)
	}
}

func identify(t *testing.T, svc *Service, project, remote, root string) {
	t.Helper()
	require.NoError(t, svc.identityStore.RecordIdentity(context.Background(), project, remote, root))
}

type duplicatesBody struct {
	Suggestions []struct {
		Survivor struct {
			Project      string `json:"project"`
			Label        string `json:"label"`
			DisplayName  string `json:"display_name"`
			Observations int64  `json:"observations"`
		} `json:"survivor"`
		Other struct {
			Project string `json:"project"`
		} `json:"other"`
		Strength string `json:"strength"`
		Reasons  []struct {
			Code string `json:"code"`
			Text string `json:"text"`
		} `json:"reasons"`
		AutoMergeable bool `json:"auto_mergeable"`
	} `json:"suggestions"`
	Dismissed []struct {
		A struct {
			Project string `json:"project"`
		} `json:"a"`
		B struct {
			Project string `json:"project"`
		} `json:"b"`
	} `json:"dismissed"`
	AutoMerge bool `json:"auto_merge"`
}

func getDuplicates(t *testing.T, svc *Service) duplicatesBody {
	t.Helper()
	rec := doRequest(t, svc, http.MethodGet, "/api/projects/duplicates", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var out duplicatesBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestDuplicatesAPI_SuggestsTheSameRemoteAndLeavesNamesakesWithOtherRemotesAlone(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	addProject(t, svc, "shop_aaaaaa", 4)
	addProject(t, svc, "shop_bbbbbb", 2)
	addProject(t, svc, "app_cccccc", 3)
	addProject(t, svc, "app_dddddd", 3)
	identify(t, svc, "shop_aaaaaa", shopRemote, "/work/shop")
	identify(t, svc, "shop_bbbbbb", shopRemote, "/old/shop")
	identify(t, svc, "app_cccccc", "git.example.org/team/app", "/work/app")
	identify(t, svc, "app_dddddd", "git.example.org/clients/app", "/clients/app")

	got := getDuplicates(t, svc)
	require.Len(t, got.Suggestions, 1, "the two apps are namesakes: different remotes")
	s := got.Suggestions[0]
	assert.Equal(t, "shop_aaaaaa", s.Survivor.Project, "the project with more notes survives")
	assert.Equal(t, "shop_bbbbbb", s.Other.Project)
	assert.EqualValues(t, 4, s.Survivor.Observations)
	assert.Equal(t, "shop", s.Survivor.DisplayName)
	assert.NotEmpty(t, s.Survivor.Label)
	assert.Equal(t, "strong", s.Strength)
	require.NotEmpty(t, s.Reasons)
	assert.Equal(t, "same_remote", s.Reasons[0].Code)
	assert.Contains(t, s.Reasons[0].Text, shopRemote)
	assert.False(t, got.AutoMerge, "automatic merging is off by default")
	assert.Empty(t, got.Dismissed)
}

func TestDuplicatesAPI_NoSuggestionsOnAnArchiveWithoutNamesakes(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	addProject(t, svc, "alpha_aaaaaa", 3)
	addProject(t, svc, "beta_bbbbbb", 3)
	got := getDuplicates(t, svc)
	assert.NotNil(t, got.Suggestions)
	assert.Empty(t, got.Suggestions)
	assert.Equal(t, http.StatusOK, doRequest(t, svc, http.MethodGet, "/api/projects/duplicates", nil).Code)
}

func TestDuplicatesAPI_ANameAloneIsNotSuggested(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	addProject(t, svc, "app_aaaaaa", 5)
	addProject(t, svc, "app_bbbbbb", 5)
	assert.Empty(t, getDuplicates(t, svc).Suggestions)
}

func TestDuplicatesAPI_DismissIsPerPairRememberedAndCanBeRestored(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	for _, p := range []string{"shop_aaaaaa", "shop_bbbbbb", "shop_cccccc"} {
		addProject(t, svc, p, 3)
		identify(t, svc, p, shopRemote, "/work/"+p)
	}
	assert.Len(t, getDuplicates(t, svc).Suggestions, 3)

	rec := doRequest(t, svc, http.MethodPost, "/api/projects/duplicates/dismiss", map[string]string{"a": "shop_cccccc", "b": "shop_aaaaaa"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := getDuplicates(t, svc)
	assert.Len(t, got.Suggestions, 2, "only that pair is gone")
	for _, s := range got.Suggestions {
		assert.False(t, (s.Survivor.Project == "shop_aaaaaa" && s.Other.Project == "shop_cccccc") || (s.Survivor.Project == "shop_cccccc" && s.Other.Project == "shop_aaaaaa"))
	}
	require.Len(t, got.Dismissed, 1)
	assert.ElementsMatch(t, []string{"shop_aaaaaa", "shop_cccccc"}, []string{got.Dismissed[0].A.Project, got.Dismissed[0].B.Project})

	// remembered across a new look (a restart would read the same table)
	assert.Len(t, getDuplicates(t, svc).Suggestions, 2)

	rec = doRequest(t, svc, http.MethodPost, "/api/projects/duplicates/restore", map[string]string{"a": "shop_aaaaaa", "b": "shop_cccccc"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, getDuplicates(t, svc).Suggestions, 3)
	assert.Empty(t, getDuplicates(t, svc).Dismissed)
}

func TestDuplicatesAPI_RefusesBadPairs(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	for _, tc := range []map[string]string{
		{"a": "shop_aaaaaa", "b": "shop_aaaaaa"}, {"a": "", "b": "shop_aaaaaa"}, {"a": "../etc", "b": "shop_aaaaaa"}, {"a": "shop_aaaaaa"},
	} {
		assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPost, "/api/projects/duplicates/dismiss", tc).Code, tc)
		assert.Equal(t, http.StatusBadRequest, doRequest(t, svc, http.MethodPost, "/api/projects/duplicates/restore", tc).Code, tc)
	}
}

func TestDuplicatesAPI_TheExistingSafeMergeDoesTheMergeAndTheSuggestionGoesAway(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	addProject(t, svc, "shop_aaaaaa", 4)
	addProject(t, svc, "shop_bbbbbb", 2)
	identify(t, svc, "shop_aaaaaa", shopRemote, "/work/shop")
	identify(t, svc, "shop_bbbbbb", shopRemote, "/old/shop")
	s := getDuplicates(t, svc).Suggestions[0]

	url := "/api/projects/" + s.Other.Project + "/merge"
	preview := decodeAction(t, doRequest(t, svc, http.MethodPost, url, mergeBody(s.Survivor.Project, "")))
	require.True(t, preview.DryRun)
	done := decodeAction(t, doRequest(t, svc, http.MethodPost, url, mergeBody(s.Survivor.Project, preview.Confirm)))
	require.NotEmpty(t, done.Backup)

	assert.Empty(t, getDuplicates(t, svc).Suggestions)
	ids, _ := svc.identityStore.Identities(context.Background())
	assert.Len(t, ids["shop_aaaaaa"], 2, "the survivor now stands for both clones")
	assert.Empty(t, ids["shop_bbbbbb"])
}

func TestResolveProject_NamesakesThatAreProbablyOneSaySo(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	addProject(t, svc, "shop_aaaaaa", 4)
	addProject(t, svc, "shop_bbbbbb", 2)
	addProject(t, svc, "shop_cccccc", 3)
	identify(t, svc, "shop_aaaaaa", shopRemote, "/work/shop")
	identify(t, svc, "shop_bbbbbb", shopRemote, "/old/shop")
	identify(t, svc, "shop_cccccc", "git.example.org/clients/shop", "/clients/shop")

	rec := doRequest(t, svc, http.MethodGet, "/api/projects/resolve?name=shop", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Candidates []struct {
			Project        string   `json:"project"`
			ProbablySameAs []string `json:"probably_same_as"`
		} `json:"candidate_details"`
		Ambiguous bool `json:"ambiguous"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.True(t, out.Ambiguous)
	same := map[string][]string{}
	for _, c := range out.Candidates {
		same[c.Project] = c.ProbablySameAs
	}
	assert.Equal(t, []string{"shop_bbbbbb"}, same["shop_aaaaaa"])
	assert.Equal(t, []string{"shop_aaaaaa"}, same["shop_bbbbbb"])
	assert.Empty(t, same["shop_cccccc"], "a different remote: a real namesake, nothing is said")
}

// --- identity capture -------------------------------------------------------------------------------------------

func gitRepo(t *testing.T, name, remote string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return dir
}

func waitForIdentities(t *testing.T, svc *Service, project string, want int) []gorm.ProjectIdentity {
	t.Helper()
	var rows []gorm.ProjectIdentity
	require.Eventually(t, func() bool {
		all, err := svc.identityStore.Identities(context.Background())
		require.NoError(t, err)
		rows = all[project]
		return len(rows) == want
	}, 5*time.Second, 25*time.Millisecond)
	return rows
}

func TestNoteProjectPath_RecordsTheRemoteWithoutCredentialsFromEveryPlaceAPathArrives(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	dir := gitRepo(t, "shop", "https://someone:s3cret-token@git.example.org/team/shop.git")

	// resolving a path (Desktop's project_resolve, context, remember)
	rec := doRequest(t, svc, http.MethodGet, "/api/projects/resolve?path="+dir, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.NotEmpty(t, res.ID)
	rows := waitForIdentities(t, svc, res.ID, 1)
	assert.Equal(t, shopRemote, rows[0].Remote)
	assert.Equal(t, dir, rows[0].RootPath)
	assert.NotContains(t, rows[0].Remote, "s3cret")

	// the session context of Claude Code (the hook sends the folder)
	other := gitRepo(t, "shop", "git@git.example.org:team/shop.git")
	rec = doRequest(t, svc, http.MethodGet, "/api/context/inject?project=shop_eeeeee&cwd="+other, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows = waitForIdentities(t, svc, "shop_eeeeee", 1)
	assert.Equal(t, shopRemote, rows[0].Remote, "an ssh remote and an https remote of one repository are one identity")
}

func TestNoteProjectPath_IgnoresWhatIsNotAFolderOfARepositoryAndDoesNotAskTwice(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	plain := t.TempDir()
	for _, p := range []string{"", "/", "relative/path", "file:///does/not/exist", plain} {
		svc.noteProjectPath("quiet_aaaaaa", p)
	}
	svc.noteProjectPath("bad name;", gitRepo(t, "x", "https://git.example.org/a/b"))
	time.Sleep(300 * time.Millisecond)
	all, _ := svc.identityStore.Identities(context.Background())
	assert.Empty(t, all, "no repository, no identity")

	dir := gitRepo(t, "shop", "https://git.example.org/team/shop")
	svc.noteProjectPath("shop_ffffff", dir)
	waitForIdentities(t, svc, "shop_ffffff", 1)
	_, remembered := svc.identitySeen.Load("shop_ffffff\x00" + dir)
	assert.True(t, remembered, "the pair is not looked at again within the hour")
}

// --- automatic merge ---------------------------------------------------------------------------------------------

func autoMergeService(t *testing.T, on bool) (*Service, func()) {
	t.Helper()
	svc, cleanup := testService(t)
	cfg := *svc.config
	cfg.ProjectAutoMergeEnabled = on
	svc.config = &cfg
	return svc, cleanup
}

func TestAutoMerge_IsOffByDefault(t *testing.T) {
	svc, cleanup := testService(t)
	defer cleanup()
	assert.False(t, svc.config.ProjectAutoMergeEnabled)
	addProject(t, svc, "shop_aaaaaa", 4)
	addProject(t, svc, "shop_bbbbbb", 2)
	identify(t, svc, "shop_aaaaaa", shopRemote, t.TempDir())
	identify(t, svc, "shop_bbbbbb", shopRemote, filepath.Join(t.TempDir(), "gone"))
	assert.Zero(t, svc.runAutoMergePass(context.Background()))
	assert.EqualValues(t, 2, countRows(t, svc, `SELECT COUNT(DISTINCT project) FROM observations`))
}

func TestAutoMerge_MergesOnlyTheSameRemoteWhoseOldFolderIsGoneAndLeavesAnAliasAndABackup(t *testing.T) {
	svc, cleanup := autoMergeService(t, true)
	defer cleanup()
	live := t.TempDir()
	gone := filepath.Join(t.TempDir(), "moved-away")

	addProject(t, svc, "shop_aaaaaa", 4) // the survivor
	addProject(t, svc, "shop_bbbbbb", 2) // same remote, its folder is gone: merged
	addProject(t, svc, "shop_cccccc", 2) // same remote, but its folder is still there (a second live clone): not merged
	addProject(t, svc, "app_dddddd", 5)  // namesakes with different remotes: not merged
	addProject(t, svc, "app_eeeeee", 2)
	addProject(t, svc, "tool_ffffff", 5) // same name, no remote, only a few notes on one side: suggested, never merged
	addProject(t, svc, "tool_gggggg", 1)
	identify(t, svc, "shop_aaaaaa", shopRemote, live)
	identify(t, svc, "shop_bbbbbb", shopRemote, gone)
	identify(t, svc, "shop_cccccc", shopRemote, t.TempDir())
	identify(t, svc, "app_dddddd", "git.example.org/team/app", filepath.Join(t.TempDir(), "gone"))
	identify(t, svc, "app_eeeeee", "git.example.org/clients/app", filepath.Join(t.TempDir(), "gone"))

	events := svc.sseBroadcaster
	_ = events
	assert.Equal(t, 1, svc.runAutoMergePass(context.Background()))

	assert.Zero(t, countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = 'shop_bbbbbb'`))
	assert.EqualValues(t, 6, countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = 'shop_aaaaaa'`))
	assert.EqualValues(t, 2, countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = 'shop_cccccc'`), "a live second clone is left for a person")
	assert.EqualValues(t, 5, countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = 'app_dddddd'`))
	assert.EqualValues(t, 2, countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = 'app_eeeeee'`))
	assert.EqualValues(t, 1, countRows(t, svc, `SELECT COUNT(*) FROM observations WHERE project = 'tool_gggggg'`))

	aliases, err := gorm.NewProjectAliasStore(svc.store).ListAliases(context.Background())
	require.NoError(t, err)
	require.Len(t, aliases, 1)
	assert.Equal(t, "shop_bbbbbb", aliases[0].Alias)
	assert.Equal(t, "shop_aaaaaa", aliases[0].Canonical)
	assert.Equal(t, "auto-merge", aliases[0].Source, "an automatic merge can be told from a manual one")
	entries, err := os.ReadDir(svc.store.DefaultSnapshotDir())
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "a backup was taken first")

	assert.Zero(t, svc.runAutoMergePass(context.Background()), "nothing certain is left; the rest is only suggested")
	assert.NotEmpty(t, getDuplicates(t, svc).Suggestions)
}

func TestAutoMerge_DoesNotMergeADismissedPair(t *testing.T) {
	svc, cleanup := autoMergeService(t, true)
	defer cleanup()
	addProject(t, svc, "shop_aaaaaa", 4)
	addProject(t, svc, "shop_bbbbbb", 2)
	identify(t, svc, "shop_aaaaaa", shopRemote, t.TempDir())
	identify(t, svc, "shop_bbbbbb", shopRemote, filepath.Join(t.TempDir(), "gone"))
	require.NoError(t, svc.identityStore.Dismiss(context.Background(), "shop_aaaaaa", "shop_bbbbbb"))
	assert.Zero(t, svc.runAutoMergePass(context.Background()))
	assert.EqualValues(t, 2, countRows(t, svc, `SELECT COUNT(DISTINCT project) FROM observations`))
}
