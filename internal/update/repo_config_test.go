package update

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withRepo sets GitHubRepo and the identity override for one test.
func withRepo(t *testing.T, repo, identity string) {
	t.Helper()
	oldRepo, oldIdentity := GitHubRepo, CertificateIdentityRegexp
	GitHubRepo, CertificateIdentityRegexp = repo, identity
	t.Cleanup(func() { GitHubRepo, CertificateIdentityRegexp = oldRepo, oldIdentity })
}

func TestRepoConfig_DefaultsToThisForksRepository(t *testing.T) {
	assert.Equal(t, "hlgr360/claude-mnemonic", GitHubRepo)
	assert.Equal(t, "https://api.github.com/repos/hlgr360/claude-mnemonic/releases/latest", ReleasesAPI())
	assert.Equal(t, "https://raw.githubusercontent.com/hlgr360/claude-mnemonic/main/scripts/install.sh", InstallScriptURL())
}

func TestRepoConfig_FollowsTheConfiguredRepository(t *testing.T) {
	withRepo(t, "someone/their-fork", "")
	assert.Equal(t, "https://api.github.com/repos/someone/their-fork/releases/latest", ReleasesAPI())
	assert.Equal(t, "https://raw.githubusercontent.com/someone/their-fork/main/scripts/install.sh", InstallScriptURL())
	assert.Equal(t, "curl -sSL https://raw.githubusercontent.com/someone/their-fork/main/scripts/install.sh | bash", GetManualUpdateCommand(""))
	assert.Contains(t, GetManualUpdateCommand("v1.2.3"), "their-fork/main/scripts/install.sh | bash -s -- v1.2.3")
}

func TestCertificateIdentity_IsDerivedFromTheRepositoryAndAnchored(t *testing.T) {
	withRepo(t, "owner/my.repo", "")
	re := certificateIdentityRegexp()
	assert.Equal(t, `^https://github\.com/owner/my\.repo/.*$`, re, "dots in the name are escaped and the pattern is anchored")

	matches := func(identity string) bool {
		ok, err := regexp.MatchString(re, identity)
		require.NoError(t, err)
		return ok
	}
	assert.True(t, matches("https://github.com/owner/my.repo/.github/workflows/release.yaml@refs/tags/v1.0.0"), "a workflow of the repository")
	assert.False(t, matches("https://github.com/owner/my-repo/.github/workflows/release.yaml@refs/tags/v1.0.0"), "a dot in the name is not any character")
	assert.False(t, matches("https://github.com/other/my.repo/.github/workflows/release.yaml@refs/tags/v1.0.0"), "another owner")
	assert.False(t, matches("https://github.com/owner/my.repo-evil/.github/workflows/x.yaml@refs/heads/main"), "a repository whose name merely starts the same")
	assert.False(t, matches("https://evil.example.org/?u=https://github.com/owner/my.repo/x"), "the pattern is anchored: it cannot be smuggled into a longer identity")
}

func TestCertificateIdentity_CanBeOverridden(t *testing.T) {
	withRepo(t, "owner/repo", `^https://github\.com/shared/actions/\.github/workflows/release\.yaml@.*$`)
	assert.Equal(t, `^https://github\.com/shared/actions/\.github/workflows/release\.yaml@.*$`, certificateIdentityRegexp(),
		"for releases signed by a reusable workflow in another repository")
}

func TestCosignVerifyArgs(t *testing.T) {
	withRepo(t, "owner/repo", "")
	args := cosignVerifyArgs("/tmp/b.json", "/tmp/checksums.txt")
	assert.Equal(t, []string{
		"verify-blob", "--bundle", "/tmp/b.json",
		"--certificate-identity-regexp", `^https://github\.com/owner/repo/.*$`,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"/tmp/checksums.txt",
	}, args)
}

// fakeCosign puts a cosign on PATH that records its arguments and exits with the given code.
func fakeCosign(t *testing.T, exitCode string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > '" + argsFile + "'\necho \"fake cosign says no\" >&2\nexit " + exitCode + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cosign"), []byte(script), 0o755)) // #nosec G306 -- a test stub that must be executable
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestVerifySigstoreBundle_AsksCosignForTheConfiguredIdentity(t *testing.T) {
	withRepo(t, "owner/repo", "")
	argsFile := fakeCosign(t, "0")
	u := New("1.0.0", t.TempDir())

	require.NoError(t, u.verifySigstoreBundle(context.Background(), "/tmp/checksums.txt", "/tmp/bundle.json"))
	got, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	assert.Equal(t, cosignVerifyArgs("/tmp/bundle.json", "/tmp/checksums.txt"), lines)
	assert.Contains(t, lines, `^https://github\.com/owner/repo/.*$`)
}

func TestVerifySigstoreBundle_RefusesWhenCosignRefuses(t *testing.T) {
	withRepo(t, "owner/repo", "")
	fakeCosign(t, "1")
	u := New("1.0.0", t.TempDir())
	err := u.verifySigstoreBundle(context.Background(), "/tmp/checksums.txt", "/tmp/bundle.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cosign verification failed")
	assert.Contains(t, err.Error(), "fake cosign says no", "cosign's own explanation is kept")
}

func TestVerifySigstoreBundle_RefusesWithoutCosign(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on it
	u := New("1.0.0", t.TempDir())
	err := u.verifySigstoreBundle(context.Background(), "/tmp/checksums.txt", "/tmp/bundle.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cosign not installed")
}
