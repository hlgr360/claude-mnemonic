package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRemote_AllFormsOfOneRepositoryCompareEqual(t *testing.T) {
	same := []string{
		"git@bitbucket.org:Team/Shop.git",
		"ssh://git@bitbucket.org/team/shop.git",
		"ssh://git@bitbucket.org:22/team/shop",
		"https://bitbucket.org/team/shop.git",
		"https://bitbucket.org/team/shop/",
		"https://someone:s3cret-token@Bitbucket.org/team/shop.git",
		"git://bitbucket.org/team/shop.git",
		"  https://bitbucket.org/Team/SHOP  ",
	}
	for _, raw := range same {
		assert.Equal(t, "bitbucket.org/team/shop", NormalizeRemote(raw), raw)
	}
}

func TestNormalizeRemote_NeverKeepsCredentials(t *testing.T) {
	for _, raw := range []string{
		"https://user:hunter2@git.example.org/team/shop.git",
		"https://oauth2:ghp_abcdef@git.example.org/team/shop.git",
		"ssh://deploy:pw@git.example.org:2222/team/shop.git",
	} {
		out := NormalizeRemote(raw)
		assert.Equal(t, "git.example.org/team/shop", out, raw)
		assert.NotContains(t, out, "hunter2")
		assert.NotContains(t, out, "ghp_")
		assert.NotContains(t, out, "pw")
		assert.NotContains(t, out, "@")
	}
}

func TestNormalizeRemote_HostsThatKeepCaseKeepTheCaseOfThePath(t *testing.T) {
	assert.Equal(t, "git.example.org/Team/Shop", NormalizeRemote("https://Git.Example.org/Team/Shop.git"), "only the host is lower-cased on an unknown host")
	assert.NotEqual(t, NormalizeRemote("https://git.example.org/team/shop"), NormalizeRemote("https://git.example.org/Team/Shop"))
}

func TestNormalizeRemote_AzureDevOpsSshAndHttpsAreOneRepository(t *testing.T) {
	want := "dev.azure.com/org/project/repo"
	assert.Equal(t, want, NormalizeRemote("ssh://ssh.dev.azure.com/v3/org/project/repo"))
	assert.Equal(t, want, NormalizeRemote("org@vs-ssh.visualstudio.com:v3/org/project/repo"))
	assert.Equal(t, want, NormalizeRemote("https://org@dev.azure.com/org/project/_git/repo"))
	assert.Equal(t, want, NormalizeRemote("https://dev.azure.com/Org/Project/_git/Repo"))
}

func TestNormalizeRemote_FoldersAndNonsense(t *testing.T) {
	assert.Equal(t, "srv/git/shop", NormalizeRemote("/srv/git/shop.git"))
	assert.Equal(t, "srv/git/shop", NormalizeRemote("file:///srv/git/shop.git"))
	for _, raw := range []string{"", "   ", "not a url", "https://", "git@host", "://x"} {
		assert.Equal(t, "", NormalizeRemote(raw), raw)
	}
}

func TestPickRemote_PrefersOriginThenTheFirstByName(t *testing.T) {
	assert.Equal(t, "git.example.org/a/shop", PickRemote(map[string]string{
		"upstream": "https://git.example.org/z/shop", "origin": "https://git.example.org/a/shop.git"}))
	assert.Equal(t, "git.example.org/m/shop", PickRemote(map[string]string{
		"zeta": "https://git.example.org/z/shop", "backup": "https://git.example.org/m/shop"}), "no origin: the first name in order")
	assert.Equal(t, "git.example.org/m/shop", PickRemote(map[string]string{
		"origin": "garbage", "backup": "https://git.example.org/m/shop"}), "an origin that cannot be read does not hide the others")
	assert.Equal(t, "", PickRemote(nil))
}

func TestParseRemoteConfig(t *testing.T) {
	out := "remote.origin.url git@bitbucket.org:team/shop.git\nremote.my.fork.url https://bitbucket.org/me/shop\nremote.origin.fetch +refs/heads/*:refs/remotes/origin/*\n\nbogus line\n"
	assert.Equal(t, map[string]string{"origin": "git@bitbucket.org:team/shop.git", "my.fork": "https://bitbucket.org/me/shop"}, ParseRemoteConfig(out))
	assert.Empty(t, ParseRemoteConfig(""))
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestGitIdentity_ReadsTheRemoteAndTheRootOfARealCheckout(t *testing.T) {
	requireGit(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	repo := filepath.Join(root, "shop")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "src", "deep"), 0o755))
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "remote", "add", "origin", "https://user:token@git.example.org/team/shop.git")
	gitIn(t, repo, "remote", "add", "backup", "https://git.example.org/other/shop.git")

	id := GitIdentity(context.Background(), filepath.Join(repo, "src", "deep"))
	assert.Equal(t, repo, id.Root, "the root is the top of the checkout, from any folder inside it")
	assert.Equal(t, "git.example.org/team/shop", id.Remote, "origin, normalised, without the credentials")
}

func TestGitIdentity_NoRemoteNoRepositoryNoFolder(t *testing.T) {
	requireGit(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	local := filepath.Join(root, "local")
	require.NoError(t, os.MkdirAll(local, 0o755))
	gitIn(t, local, "init", "-q")
	assert.Equal(t, Identity{Root: local}, GitIdentity(context.Background(), local), "a checkout without a remote still has a root")

	plain := filepath.Join(root, "plain")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	assert.Equal(t, Identity{}, GitIdentity(context.Background(), plain))
	assert.Equal(t, Identity{}, GitIdentity(context.Background(), filepath.Join(root, "missing")))
}
