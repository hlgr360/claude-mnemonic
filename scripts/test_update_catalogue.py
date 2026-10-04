#!/usr/bin/env python3
"""scripts/update-catalogue.sh points the plugin catalogue at a release and opens the pull request, but only when every
check passes: a wrong sha256 in the catalogue breaks the install for everyone who adds it.

Run: python3 -m unittest scripts/test_update_catalogue.py -v
Nothing real is touched: gh, cosign and claude are replaced by shims on PATH, the catalogue's origin is a local bare
repository, and the script runs from a throwaway checkout that has its own git identity (the global one is hidden).
"""
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import package_release  # noqa: E402

TAG = "v0.21.95.3"
VERSION = "0.21.95.3"
OLD = "0.21.95.2"
ZIP = f"claude-mnemonic-plugin_{VERSION}.zip"
IDENTITY = ("Test Maker", "maker@example.org")

GH_SHIM = '''#!/usr/bin/env python3
import json, os, shutil, subprocess, sys
a = sys.argv[1:]
F = os.environ["FIXTURE"]
if a[:2] == ["release", "view"]:
    if "--jq" in a:
        print(open(F + "/latest.txt").read().strip()); sys.exit(0)
    if a[2] != json.load(open(F + "/release.json"))["tagName"]:
        sys.exit(1)
    print(open(F + "/release.json").read()); sys.exit(0)
if a[:2] == ["release", "download"]:
    d = a[a.index("--dir") + 1]
    for i, x in enumerate(a):
        if x == "--pattern":
            shutil.copy(F + "/assets/" + a[i + 1], d)
    sys.exit(0)
if a[:2] == ["repo", "clone"]:
    subprocess.run(["git", "clone", "-q", F + "/origin.git", a[3]], check=True); sys.exit(0)
if a[:2] == ["pr", "create"]:
    open(F + "/pr.log", "a").write(json.dumps(a) + "\\n"); print("https://example.org/pr/1"); sys.exit(0)
sys.exit("unexpected gh call: " + " ".join(a))
'''

CLAUDE_SHIM = '''#!/bin/sh
echo "$*" >> "$FIXTURE/claude.calls"
case "$1 $2" in
  "plugin install") [ "$FAKE_CLAUDE_FAIL" = install ] && exit 1; exit 0 ;;
  "plugin list") echo "  > claude-mnemonic@hlgr360"; echo "    Version: ${FAKE_CLAUDE_VERSION:-0.21.95.3}"; exit 0 ;;
esac
exit 0
'''


def write(path, text, mode=0o755):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    os.chmod(path, mode)


def sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def catalogue(version):
    return {
        "name": "hlgr360",
        "description": "Plugins for coding agents.",
        "owner": {"name": "hlgr360"},
        "plugins": [
            {
                "name": "claude-mnemonic",
                "category": "productivity",
                "source": {
                    "source": "archive",
                    "url": f"https://github.com/hlgr360/claude-mnemonic/releases/download/v{version}/claude-mnemonic-plugin_{version}.zip",
                    "sha256": "0" * 64,
                },
            }
        ],
    }


@unittest.skipUnless(shutil.which("bash") and shutil.which("git") and shutil.which("unzip"), "needs bash, git and unzip")
class UpdateCatalogue(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = self.tmp.name
        self.fixture = os.path.join(root, "fixture")
        self.tools = os.path.join(root, "tools")
        self.repo = os.path.join(root, "checkout")
        os.makedirs(os.path.join(self.fixture, "assets"))
        # The throwaway checkout the script runs from, with its own identity.
        os.makedirs(os.path.join(self.repo, "scripts"))
        for name in ("update-catalogue.sh", "check_plugin_manifest.py"):
            shutil.copy(os.path.join(HERE, name), os.path.join(self.repo, "scripts", name))
        self.git(self.repo, "init", "-q", "-b", "main")
        self.git(self.repo, "config", "user.name", IDENTITY[0])
        self.git(self.repo, "config", "user.email", IDENTITY[1])
        # The catalogue's origin: a bare repository with the entry at the old version.
        self.origin = os.path.join(self.fixture, "origin.git")
        seed = os.path.join(root, "seed")
        os.makedirs(os.path.join(seed, ".claude-plugin"))
        write(os.path.join(seed, ".claude-plugin", "marketplace.json"), json.dumps(catalogue(OLD), indent=2) + "\n", 0o644)
        self.git(seed, "init", "-q", "-b", "main")
        self.git(seed, "-c", "user.name=Seed", "-c", "user.email=seed@example.org", "add", "-A")
        self.git(seed, "-c", "user.name=Seed", "-c", "user.email=seed@example.org", "commit", "-q", "-m", "seed")
        subprocess.run(["git", "clone", "-q", "--bare", seed, self.origin], check=True, capture_output=True)
        write(os.path.join(self.tools, "gh"), GH_SHIM)
        write(os.path.join(self.tools, "claude"), CLAUDE_SHIM)
        write(os.path.join(self.tools, "cosign"), '#!/bin/sh\nexit "${FAKE_COSIGN:-0}"\n')
        self.make_release()

    def git(self, cwd, *args):
        os.makedirs(cwd, exist_ok=True)
        return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True, env=self.git_env()).stdout

    def git_env(self):
        # Hide the machine's global and system git configuration so only the checkout's own identity can be used.
        return dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)

    def make_release(self, version=VERSION, plugin_version=None, description="A short description.", draft=False, prerelease=False,
                     checksum=None, drop=()):
        stage = os.path.join(self.tmp.name, "stage")
        shutil.rmtree(stage, ignore_errors=True)
        write(os.path.join(stage, ".claude-plugin", "plugin.json"), json.dumps({"name": "claude-mnemonic", "version": plugin_version or version, "description": description}), 0o644)
        write(os.path.join(stage, "hooks", "stop"), "#!/bin/sh\n")
        zip_name = f"claude-mnemonic-plugin_{version}.zip"
        zip_path = os.path.join(self.fixture, "assets", zip_name)
        package_release.pack(stage, zip_path)
        self.zip_sha = sha256(zip_path)
        write(os.path.join(self.fixture, "assets", "checksums.txt"), f"{checksum or self.zip_sha}  {zip_name}\n", 0o644)
        write(os.path.join(self.fixture, "assets", "checksums.txt.sigstore.json"), "{}", 0o644)
        names = [n for n in (zip_name, "checksums.txt", "checksums.txt.sigstore.json") if n not in drop]
        release = {"tagName": f"v{version}", "isDraft": draft, "isPrerelease": prerelease, "assets": [{"name": n} for n in names]}
        write(os.path.join(self.fixture, "release.json"), json.dumps(release), 0o644)
        write(os.path.join(self.fixture, "latest.txt"), f"v{version}\n", 0o644)

    def run_script(self, *args, **env):
        e = self.git_env()
        e.update(PATH=self.tools + os.pathsep + os.environ["PATH"], FIXTURE=self.fixture, **env)
        return subprocess.run(["bash", os.path.join(self.repo, "scripts", "update-catalogue.sh"), *args], env=e, capture_output=True, text=True, timeout=120, stdin=subprocess.DEVNULL)

    def origin_branches(self):
        return sorted(self.git(self.origin, "for-each-ref", "--format=%(refname:short)", "refs/heads").split())

    def origin_file(self, branch):
        return json.loads(self.git(self.origin, "show", f"{branch}:.claude-plugin/marketplace.json"))

    # ---- the happy path

    def test_it_updates_the_entry_commits_with_this_checkouts_identity_and_opens_the_pr(self):
        out = self.run_script(TAG, "--trailer", "Co-Authored-By: Someone <someone@example.org>", "--footer", "Footer line")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        branch = f"chore/claude-mnemonic-{VERSION}"
        self.assertIn(branch, self.origin_branches())
        entry = self.origin_file(branch)["plugins"][0]
        self.assertEqual(entry["source"]["url"], f"https://github.com/hlgr360/claude-mnemonic/releases/download/{TAG}/{ZIP}")
        self.assertEqual(entry["source"]["sha256"], self.zip_sha, "the sha256 is computed from the downloaded zip")
        self.assertEqual(entry["category"], "productivity", "the rest of the entry is untouched")
        self.assertEqual(self.origin_file(branch)["owner"], {"name": "hlgr360"})
        log = self.git(self.origin, "log", "-1", "--format=%an <%ae>|%s|%b", branch)
        self.assertTrue(log.startswith(f"{IDENTITY[0]} <{IDENTITY[1]}>|Point claude-mnemonic at {TAG}|"), log)
        self.assertIn("Co-Authored-By: Someone <someone@example.org>", log)
        pr = json.loads(open(os.path.join(self.fixture, "pr.log"), encoding="utf-8").read().strip())
        body = pr[pr.index("--body") + 1]
        self.assertIn(self.zip_sha, body)
        self.assertIn("Footer line", body)
        self.assertIn("installed as claude-mnemonic@hlgr360 0.21.95.3", body)
        self.assertEqual(pr[pr.index("--repo") + 1], "hlgr360/agent-plugins")
        self.assertNotIn("merge", " ".join(pr).lower().replace("merged by", ""), "the script never merges")

    def test_without_a_tag_it_uses_the_latest_release(self):
        out = self.run_script()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertIn(f"chore/claude-mnemonic-{VERSION}", self.origin_branches())

    def test_dry_run_checks_everything_and_pushes_and_opens_nothing(self):
        out = self.run_script(TAG, "--dry-run")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertIn("Dry run", out.stdout)
        self.assertIn(self.zip_sha, out.stdout, "the diff is shown")
        self.assertEqual(self.origin_branches(), ["main"])
        self.assertFalse(os.path.exists(os.path.join(self.fixture, "pr.log")))

    def test_the_install_test_ran_in_an_isolated_config(self):
        self.run_script(TAG)
        calls = open(os.path.join(self.fixture, "claude.calls"), encoding="utf-8").read()
        self.assertIn("plugin marketplace add", calls)
        self.assertIn("plugin install claude-mnemonic@hlgr360", calls)

    # ---- the refusals

    def refuses(self, *args, message, **env):
        out = self.run_script(*args, **env)
        self.assertNotEqual(out.returncode, 0, out.stdout)
        self.assertIn(message, out.stderr)
        self.assertEqual(self.origin_branches(), ["main"], "nothing was pushed")
        self.assertFalse(os.path.exists(os.path.join(self.fixture, "pr.log")), "no pull request was opened")

    def test_a_checksum_that_is_not_the_files_is_refused(self):
        self.make_release(checksum="f" * 64)
        self.refuses(TAG, message="is not the one in checksums.txt")

    def test_a_zip_whose_manifest_says_another_version_is_refused(self):
        self.make_release(plugin_version="0.21.95.9")
        self.refuses(TAG, message="plugin.json says version 0.21.95.9")

    def test_a_description_over_the_upload_limit_is_refused(self):
        self.make_release(description="x" * 501)
        self.refuses(TAG, message="rejected by the upload form")

    def test_a_draft_and_a_prerelease_are_refused(self):
        self.make_release(draft=True)
        self.refuses(TAG, message="draft or a pre-release")
        self.make_release(prerelease=True)
        self.refuses(TAG, message="draft or a pre-release")

    def test_a_release_without_the_signature_bundle_is_refused(self):
        self.make_release(drop=("checksums.txt.sigstore.json",))
        self.refuses(TAG, message="lacks checksums.txt.sigstore.json")

    def test_a_signature_that_does_not_verify_is_refused(self):
        self.refuses(TAG, message="does not verify", FAKE_COSIGN="1")

    def test_an_edited_catalogue_that_does_not_install_is_refused(self):
        self.refuses(TAG, message="does not install", FAKE_CLAUDE_FAIL="install")
        self.refuses(TAG, message="does not install", FAKE_CLAUDE_VERSION="0.21.95.2")

    def test_skip_install_test_skips_only_the_install_test(self):
        out = self.run_script(TAG, "--skip-install-test", FAKE_CLAUDE_FAIL="install")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.fixture, "claude.calls")))

    def test_a_lower_version_is_refused_and_the_same_version_is_a_no_op(self):
        self.make_release(version="0.21.95.1")
        self.refuses("v0.21.95.1", message="no downgrades")
        # The catalogue already at the release: nothing to do.
        self.make_release(version=OLD)
        out = self.run_script(f"v{OLD}")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertIn("nothing to do", out.stdout)
        self.assertEqual(self.origin_branches(), ["main"])

    def test_two_digit_fork_numbers_compare_as_numbers(self):
        self.make_release(version="0.21.95.10")
        out = self.run_script("v0.21.95.10", "--dry-run", FAKE_CLAUDE_VERSION="0.21.95.10")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    def test_a_tag_that_is_not_a_fork_release_is_refused(self):
        for bad in ("v0.21.95", "0.21.95a", "latest"):
            with self.subTest(tag=bad):
                self.refuses(bad, message="not a fork release tag")

    def test_no_git_identity_in_the_checkout_means_no_commit(self):
        self.git(self.repo, "config", "--unset", "user.name")
        self.refuses(TAG, message="no git identity is configured in this checkout")

    def test_an_unknown_option_is_refused(self):
        out = self.run_script("--bogus")
        self.assertEqual(out.returncode, 2)
        self.assertIn("Unknown option", out.stderr)


if __name__ == "__main__":
    unittest.main()
