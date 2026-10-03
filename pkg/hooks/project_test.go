package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyProjectID is the pre-worktree-aware formula. Plain checkouts must keep
// producing exactly this value so existing projects are not orphaned.
func legacyProjectID(absPath string) string {
	hash := sha256.Sum256([]byte(absPath))
	return fmt.Sprintf("%s_%s", filepath.Base(absPath), hex.EncodeToString(hash[:3]))
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	require.NoError(t, os.MkdirAll(p, 0o755))
	return p
}

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// fakeWorktree builds <base>/main (a repository) and <base>/wt (a linked
// worktree of it) the way git lays them out, without needing git installed.
// gitdirRef is the value written after "gitdir:" in the worktree's .git file.
func fakeWorktree(t *testing.T, base string, relativeGitdir bool) (mainRoot, wtRoot string) {
	t.Helper()
	mainRoot = mkdir(t, base, "main")
	wtRoot = mkdir(t, base, "wt")
	adminDir := mkdir(t, mainRoot, ".git", "worktrees", "wt")
	write(t, filepath.Join(adminDir, "commondir"), "../..\n")

	ref := adminDir
	if relativeGitdir {
		rel, err := filepath.Rel(wtRoot, adminDir)
		require.NoError(t, err)
		ref = rel
	}
	write(t, filepath.Join(wtRoot, ".git"), "gitdir: "+ref+"\n")
	return mainRoot, wtRoot
}

func TestCanonicalProjectPath_PlainCheckoutUnchanged(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	mkdir(t, root, ".git")
	sub := mkdir(t, root, "pkg", "deep")

	assert.Equal(t, root, CanonicalProjectPath(root))
	assert.Equal(t, sub, CanonicalProjectPath(sub), "subdirectories keep their own path, as before")
}

func TestCanonicalProjectPath_NonGitDirUnchanged(t *testing.T) {
	dir := mkdir(t, t.TempDir(), "plain")
	assert.Equal(t, dir, CanonicalProjectPath(dir))
}

func TestCanonicalProjectPath_LinkedWorktreeMapsToMain(t *testing.T) {
	mainRoot, wtRoot := fakeWorktree(t, t.TempDir(), false)

	assert.Equal(t, mainRoot, CanonicalProjectPath(wtRoot))
}

func TestCanonicalProjectPath_SubdirOfWorktreeMapsToSameSubdirOfMain(t *testing.T) {
	mainRoot, wtRoot := fakeWorktree(t, t.TempDir(), false)
	sub := mkdir(t, wtRoot, "internal", "mcp")

	assert.Equal(t, filepath.Join(mainRoot, "internal", "mcp"), CanonicalProjectPath(sub))
}

func TestCanonicalProjectPath_RelativeGitdirReference(t *testing.T) {
	mainRoot, wtRoot := fakeWorktree(t, t.TempDir(), true)

	assert.Equal(t, mainRoot, CanonicalProjectPath(wtRoot))
}

func TestCanonicalProjectPath_BareRepositoryIsItsOwnRoot(t *testing.T) {
	base := t.TempDir()
	bare := mkdir(t, base, "repo.git")
	wtRoot := mkdir(t, base, "wt")
	adminDir := mkdir(t, bare, "worktrees", "wt")
	write(t, filepath.Join(adminDir, "commondir"), "../..\n")
	write(t, filepath.Join(wtRoot, ".git"), "gitdir: "+adminDir+"\n")

	assert.Equal(t, bare, CanonicalProjectPath(wtRoot))
}

func TestCanonicalProjectPath_SubmoduleUnchanged(t *testing.T) {
	base := t.TempDir()
	sm := mkdir(t, base, "super", "vendor", "lib")
	modDir := mkdir(t, base, "super", ".git", "modules", "lib") // no commondir file
	write(t, filepath.Join(sm, ".git"), "gitdir: "+modDir+"\n")

	assert.Equal(t, sm, CanonicalProjectPath(sm))
}

func TestCanonicalProjectPath_MalformedGitFileUnchanged(t *testing.T) {
	cases := map[string]string{
		"no prefix":           "not a gitdir line\n",
		"empty":               "",
		"missing admin dir":   "gitdir: /does/not/exist\n",
		"gitdir without path": "gitdir:\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := mkdir(t, t.TempDir(), "wt")
			write(t, filepath.Join(dir, ".git"), content)
			assert.Equal(t, dir, CanonicalProjectPath(dir))
		})
	}
}

func TestCanonicalProjectPath_EmptyCommondirUnchanged(t *testing.T) {
	base := t.TempDir()
	wt := mkdir(t, base, "wt")
	admin := mkdir(t, base, "admin")
	write(t, filepath.Join(admin, "commondir"), "  \n")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")

	assert.Equal(t, wt, CanonicalProjectPath(wt))
}

func TestProjectIDWithName_PlainCheckoutKeepsLegacyID(t *testing.T) {
	root := mkdir(t, t.TempDir(), "repo")
	mkdir(t, root, ".git")
	sub := mkdir(t, root, "cmd")

	assert.Equal(t, legacyProjectID(root), ProjectIDWithName(root))
	assert.Equal(t, legacyProjectID(sub), ProjectIDWithName(sub))
}

func TestProjectIDWithName_NonGitDirKeepsLegacyID(t *testing.T) {
	dir := mkdir(t, t.TempDir(), "notes")
	assert.Equal(t, legacyProjectID(dir), ProjectIDWithName(dir))
}

func TestProjectIDWithName_WorktreeSharesProjectWithMain(t *testing.T) {
	mainRoot, wtRoot := fakeWorktree(t, t.TempDir(), false)

	id := ProjectIDWithName(wtRoot)
	assert.Equal(t, ProjectIDWithName(mainRoot), id)
	assert.Equal(t, legacyProjectID(mainRoot), id, "worktree resolves to the main checkout's existing ID")
	assert.NotEqual(t, legacyProjectID(wtRoot), id)
}

// TestProjectIDWithName_RealGitWorktree checks the file-format assumptions
// against what git itself writes.
func TestProjectIDWithName_RealGitWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	mainRoot := mkdir(t, base, "repo")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git(mainRoot, "init", "-q")
	write(t, filepath.Join(mainRoot, "a.txt"), "x")
	git(mainRoot, "add", ".")
	git(mainRoot, "commit", "-q", "-m", "init")
	wtRoot := filepath.Join(base, "wt")
	git(mainRoot, "worktree", "add", "-q", "-b", "side", wtRoot)

	// git records real (symlink-resolved) paths, e.g. /private/var on macOS.
	realMain, err := filepath.EvalSymlinks(mainRoot)
	require.NoError(t, err)

	assert.Equal(t, realMain, CanonicalProjectPath(wtRoot))
	sub := mkdir(t, wtRoot, "x", "y")
	assert.Equal(t, filepath.Join(realMain, "x", "y"), CanonicalProjectPath(sub))
}
