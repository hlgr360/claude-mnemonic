package projects

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/hooks"
)

func TestDisplayName(t *testing.T) {
	tests := map[string]string{
		"claude-mnemonic_41bfcd": "claude-mnemonic",
		"my_repo_ab12cd":         "my_repo",
		"nohash":                 "nohash",
		"short_abc":              "short_abc", // suffix is not a 6-char hash
		"_ab12cd":                "_ab12cd",
		"":                       "",
	}
	for in, want := range tests {
		assert.Equal(t, want, DisplayName(in), in)
	}
}

func TestResolve_ByID(t *testing.T) {
	known := []string{"repo_aaaaaa", "other_bbbbbb"}
	aliases := map[string]string{"frag_cccccc": "repo_aaaaaa", "gone_dddddd": "missing_eeeeee"}

	t.Run("exact", func(t *testing.T) {
		assert.Equal(t, Resolution{ID: "repo_aaaaaa", Match: MatchExact, Known: true},
			Resolve(Ref{ID: "repo_aaaaaa"}, known, aliases))
	})
	t.Run("alias resolves to canonical", func(t *testing.T) {
		assert.Equal(t, Resolution{ID: "repo_aaaaaa", Match: MatchAlias, Known: true},
			Resolve(Ref{ID: "frag_cccccc"}, known, aliases))
	})
	t.Run("alias to a project with no data yet", func(t *testing.T) {
		assert.Equal(t, Resolution{ID: "missing_eeeeee", Match: MatchAlias, Known: false},
			Resolve(Ref{ID: "gone_dddddd"}, known, aliases))
	})
	t.Run("alias wins over a stale known entry", func(t *testing.T) {
		// The fragment still has rows (so it is "known") but is declared an alias.
		withFrag := append([]string{"frag_cccccc"}, known...)
		assert.Equal(t, "repo_aaaaaa", Resolve(Ref{ID: "frag_cccccc"}, withFrag, aliases).ID)
	})
	t.Run("unknown", func(t *testing.T) {
		assert.Equal(t, Resolution{Match: MatchNone}, Resolve(Ref{ID: "nope_ffffff"}, known, aliases))
	})
	t.Run("empty ref", func(t *testing.T) {
		assert.Equal(t, Resolution{Match: MatchNone}, Resolve(Ref{}, known, aliases))
	})
	t.Run("nil aliases", func(t *testing.T) {
		assert.Equal(t, MatchExact, Resolve(Ref{ID: "repo_aaaaaa"}, known, nil).Match)
	})
}

func TestResolve_ByPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myproj")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	id := hooks.ProjectIDWithName(dir)

	t.Run("known project", func(t *testing.T) {
		assert.Equal(t, Resolution{ID: id, Match: MatchPath, Known: true},
			Resolve(Ref{Path: dir}, []string{id}, nil))
	})
	t.Run("new project is valid but unknown", func(t *testing.T) {
		assert.Equal(t, Resolution{ID: id, Match: MatchPath, Known: false},
			Resolve(Ref{Path: dir}, nil, nil))
	})
	t.Run("aliased path resolves to canonical", func(t *testing.T) {
		got := Resolve(Ref{Path: dir}, []string{"canon_111111"}, map[string]string{id: "canon_111111"})
		assert.Equal(t, Resolution{ID: "canon_111111", Match: MatchAlias, Known: true}, got)
	})
	t.Run("file URI as sent in MCP roots", func(t *testing.T) {
		assert.Equal(t, id, Resolve(Ref{Path: "file://" + dir}, nil, nil).ID)
	})
	t.Run("percent-encoded file URI", func(t *testing.T) {
		spaced := filepath.Join(t.TempDir(), "my proj")
		require.NoError(t, os.MkdirAll(spaced, 0o755))
		encoded := "file://" + filepath.ToSlash(filepath.Dir(spaced)) + "/my%20proj"
		assert.Equal(t, hooks.ProjectIDWithName(spaced), Resolve(Ref{Path: encoded}, nil, nil).ID)
	})
	t.Run("relative path is rejected", func(t *testing.T) {
		assert.Equal(t, Resolution{Match: MatchNone}, Resolve(Ref{Path: "some/dir"}, nil, nil))
	})
	t.Run("same path as Code's own computation", func(t *testing.T) {
		// Cowork passes the host path; it must land on the ID the hooks produce.
		assert.Equal(t, hooks.ProjectIDWithName(dir), Resolve(Ref{Path: dir}, nil, nil).ID)
	})
}

func TestResolve_PathInsideLinkedWorktreeMapsToMain(t *testing.T) {
	base := t.TempDir()
	mainRoot := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt")
	admin := filepath.Join(mainRoot, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.MkdirAll(wt, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))

	mainID := hooks.ProjectIDWithName(mainRoot)
	got := Resolve(Ref{Path: wt}, []string{mainID}, nil)
	assert.Equal(t, Resolution{ID: mainID, Match: MatchPath, Known: true}, got)
}

func TestResolve_ByName(t *testing.T) {
	known := []string{"claude-mnemonic_aaaaaa", "oci_awx_bbbbbb", "knowledge_base_cccccc", "base_dddddd"}

	t.Run("unique directory name", func(t *testing.T) {
		assert.Equal(t, Resolution{ID: "claude-mnemonic_aaaaaa", Match: MatchName, Known: true},
			Resolve(Ref{Name: "claude-mnemonic"}, known, nil))
	})
	t.Run("case insensitive and trimmed", func(t *testing.T) {
		assert.Equal(t, "oci_awx_bbbbbb", Resolve(Ref{Name: "  OCI_AWX "}, known, nil).ID)
	})
	t.Run("name containing an underscore keeps its own underscores", func(t *testing.T) {
		assert.Equal(t, "knowledge_base_cccccc", Resolve(Ref{Name: "knowledge_base"}, known, nil).ID)
	})
	t.Run("exact beats substring", func(t *testing.T) {
		// "base" is a substring of knowledge_base but exactly names base_dddddd.
		assert.Equal(t, "base_dddddd", Resolve(Ref{Name: "base"}, known, nil).ID)
	})
	t.Run("full ID typed as a name", func(t *testing.T) {
		assert.Equal(t, Resolution{ID: "oci_awx_bbbbbb", Match: MatchExact, Known: true},
			Resolve(Ref{Name: "oci_awx_bbbbbb"}, known, nil))
	})
	t.Run("ambiguous names return candidates and no ID", func(t *testing.T) {
		two := []string{"repo_aaaaaa", "repo_bbbbbb"}
		assert.Equal(t, Resolution{Match: MatchNone, Candidates: []string{"repo_aaaaaa", "repo_bbbbbb"}, Ambiguous: true},
			Resolve(Ref{Name: "repo"}, two, nil))
	})
	t.Run("no exact match offers substring candidates", func(t *testing.T) {
		assert.Equal(t, Resolution{Match: MatchNone, Candidates: []string{"claude-mnemonic_aaaaaa"}},
			Resolve(Ref{Name: "mnemonic"}, known, nil), "near misses are not flagged as ambiguous")
	})
	t.Run("nothing matches", func(t *testing.T) {
		assert.Equal(t, Resolution{Match: MatchNone}, Resolve(Ref{Name: "zzz"}, known, nil))
	})
	t.Run("alias directory name resolves to its canonical project", func(t *testing.T) {
		aliases := map[string]string{"working-directory-setup-fc06bf_e5a4ab": "claude-mnemonic_aaaaaa"}
		got := Resolve(Ref{Name: "working-directory-setup-fc06bf"}, known, aliases)
		assert.Equal(t, Resolution{ID: "claude-mnemonic_aaaaaa", Match: MatchName, Known: true}, got)
	})
	t.Run("alias and canonical with the same name are not ambiguous", func(t *testing.T) {
		aliases := map[string]string{"claude-mnemonic_ffffff": "claude-mnemonic_aaaaaa"}
		withFrag := append([]string{"claude-mnemonic_ffffff"}, known...)
		assert.Equal(t, "claude-mnemonic_aaaaaa", Resolve(Ref{Name: "claude-mnemonic"}, withFrag, aliases).ID)
	})
}

func TestResolve_Precedence(t *testing.T) {
	known := []string{"a_111111", "b_222222"}
	dir := filepath.Join(t.TempDir(), "p")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	assert.Equal(t, "a_111111", Resolve(Ref{ID: "a_111111", Path: dir, Name: "b"}, known, nil).ID, "ID beats path and name")
	assert.Equal(t, hooks.ProjectIDWithName(dir), Resolve(Ref{Path: dir, Name: "b"}, known, nil).ID, "path beats name")
}
