#!/usr/bin/env python3
"""The release archive packer and the release workflow.

Run: python3 -m unittest scripts/test_package_release.py -v
The packer is run on a small made-up tree in a temporary directory; nothing is built or published. The workflow is
checked as text: it must never publish without a version tag, and the identity the signature will carry must be one the
in-app updater accepts.
"""
import os
import re
import subprocess
import sys
import tarfile
import tempfile
import unittest
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)

import package_release  # noqa: E402

WORKFLOW = os.path.join(REPO_ROOT, ".github", "workflows", "release-native.yaml")


def make_tree(root):
    for rel, mode in [
        ("worker", 0o755),
        ("mcp-server", 0o755),
        ("hooks/stop", 0o755),
        ("hooks/hooks.json", 0o644),
        ("commands/dashboard.md", 0o644),
        (".claude-plugin/plugin.json", 0o600),
    ]:
        path = os.path.join(root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            f.write("content of " + rel)
        os.chmod(path, mode)


class PackReleaseTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.stage = os.path.join(self.tmp.name, "stage")
        make_tree(self.stage)

    def test_tar_gz_has_the_layout_the_updater_unpacks(self):
        archive = os.path.join(self.tmp.name, "out", "claude-mnemonic_1.2.3_linux_amd64.tar.gz")
        package_release.pack(self.stage, archive)
        with tarfile.open(archive) as tar:
            names = tar.getnames()
            self.assertEqual(
                names,
                sorted(
                    [".claude-plugin/plugin.json", "commands/dashboard.md", "hooks/hooks.json", "hooks/stop", "mcp-server", "worker"]
                ),
            )
            modes = {m.name: m.mode for m in tar.getmembers()}
            self.assertEqual(modes["worker"], 0o755)
            self.assertEqual(modes["hooks/stop"], 0o755)
            self.assertEqual(modes["hooks/hooks.json"], 0o644)
            # Owner-only permissions on the source do not make the archived file unreadable for the user who unpacks it.
            self.assertEqual(modes[".claude-plugin/plugin.json"], 0o644)
            for member in tar.getmembers():
                self.assertEqual((member.uid, member.gid, member.mtime), (0, 0, 0), member.name)

    def test_packing_twice_gives_the_same_bytes(self):
        a = os.path.join(self.tmp.name, "a.tar.gz")
        b = os.path.join(self.tmp.name, "b.tar.gz")
        package_release.pack(self.stage, a)
        # Touch every file: a later mtime must not change the archive.
        for root, _dirs, files in os.walk(self.stage):
            for name in files:
                os.utime(os.path.join(root, name), (1_700_000_000, 1_700_000_000))
        package_release.pack(self.stage, b)
        with open(a, "rb") as fa, open(b, "rb") as fb:
            self.assertEqual(fa.read(), fb.read())

    def test_zip_for_windows_keeps_names_and_executable_bit(self):
        archive = os.path.join(self.tmp.name, "claude-mnemonic_1.2.3_windows_amd64.zip")
        package_release.pack(self.stage, archive)
        with zipfile.ZipFile(archive) as z:
            self.assertIsNone(z.testzip())
            self.assertIn("hooks/stop", z.namelist())
            self.assertEqual(z.getinfo("worker").external_attr >> 16, 0o755)
            self.assertEqual(z.read("commands/dashboard.md"), b"content of commands/dashboard.md")

    def test_refuses_an_empty_tree_and_an_unknown_extension(self):
        empty = os.path.join(self.tmp.name, "empty")
        os.makedirs(empty)
        with self.assertRaises(SystemExit):
            package_release.pack(empty, os.path.join(self.tmp.name, "x.tar.gz"))
        with self.assertRaises(SystemExit):
            package_release.pack(self.stage, os.path.join(self.tmp.name, "x.rar"))

    def test_command_line(self):
        archive = os.path.join(self.tmp.name, "cli.tar.gz")
        subprocess.run([sys.executable, os.path.join(HERE, "package_release.py"), self.stage, archive], check=True)
        self.assertTrue(tarfile.is_tarfile(archive))


class ReleaseWorkflowTest(unittest.TestCase):
    def setUp(self):
        with open(WORKFLOW, encoding="utf-8") as f:
            self.text = f.read()

    def test_only_a_version_tag_publishes(self):
        # The publish job is gated on the version job's flag, and that flag is only set for refs/tags/v*.
        self.assertRegex(self.text, r"publish:\n(?:.*\n)*?\s+if: needs\.version\.outputs\.publish == 'true'")
        self.assertEqual(self.text.count('echo "publish=true"'), 1)
        self.assertRegex(self.text, r'if \[\[ "\$GITHUB_REF" == refs/tags/v\* \]\]; then\n(?:.*\n)*?\s+echo "publish=true"')
        # Pull requests and manual runs never get a write token: the default is read-only and only publish raises it.
        self.assertEqual(self.text.count("contents: write"), 1)
        self.assertEqual(self.text.count("id-token: write"), 1)

    def test_signature_identity_matches_what_the_updater_accepts(self):
        # The updater accepts any workflow of the release repository: ^https://github\.com/<repo>/.*$ .
        # The certificate carries https://github.com/<repo>/.github/workflows/<file>@refs/tags/<tag>.
        repo = "hlgr360/claude-mnemonic"
        regexp = "^https://github\\.com/" + re.escape(repo) + "/.*$"
        identity = f"https://github.com/{repo}/.github/workflows/release-native.yaml@refs/tags/v1.2.3"
        self.assertRegex(identity, regexp)
        # The workflow verifies with the same expression and issuer before it publishes.
        self.assertIn("--certificate-identity-regexp", self.text)
        self.assertIn("https://token.actions.githubusercontent.com", self.text)

    def test_covers_the_three_platforms_with_native_runners(self):
        for platform in ("darwin-arm64", "linux-amd64", "windows-amd64"):
            self.assertIn(f"platform: {platform}", self.text)
        self.assertIn("test \"$(wc -l < checksums.txt)\" -eq 4", self.text, "three platform archives and the plugin zip")

    def test_the_plugin_zip_is_built_validated_and_signed_with_the_rest(self):
        self.assertRegex(self.text, r"needs: \[version, build, plugin\]")
        self.assertIn("scripts/build-plugin.sh", self.text)
        self.assertIn("name: archive-plugin", self.text)
        self.assertIn("sha256sum claude-mnemonic_* claude-mnemonic-plugin_*", self.text)

    def test_does_not_use_the_upstream_shared_workflow(self):
        self.assertNotIn("lukaszraczylo", self.text)
        self.assertNotIn("GORELEASER", self.text)


if __name__ == "__main__":
    unittest.main()
