#!/usr/bin/env python3
"""Tests for register-plugin.sh's cleanup of old plugin cache versions (issue #33).

Run: python3 -m unittest scripts/test_register_plugin.py -v
The script is run against a temporary HOME; the real ~/.claude is never touched.
"""
import json
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "register-plugin.sh")
REPO = os.path.dirname(HERE)


@unittest.skipUnless(shutil.which("bash") and (shutil.which("jq") or shutil.which("python3")), "needs bash and jq or python3")
class KeepOldVersions(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp()
        self.addCleanup(lambda: shutil.rmtree(self.home, ignore_errors=True))
        os.makedirs(os.path.join(self.home, ".claude", "plugins"))
        self.base = os.path.join(self.home, ".claude", "plugins", "cache", "claude-mnemonic", "claude-mnemonic")
        os.makedirs(self.base)

    def make_versions(self, *names):
        """Create version directories, the first the oldest, with distinct modification times."""
        for i, name in enumerate(names):
            path = os.path.join(self.base, name)
            os.makedirs(os.path.join(path, "hooks"), exist_ok=True)
            stamp = 1_700_000_000 + i * 1000
            os.utime(path, (stamp, stamp))

    def versions(self):
        return sorted(n for n in os.listdir(self.base) if os.path.isdir(os.path.join(self.base, n)))

    def install(self, version, keep=None):
        env = dict(os.environ, HOME=self.home)
        env.pop("CLAUDE_MNEMONIC_KEEP_VERSIONS", None)
        if keep is not None:
            env["CLAUDE_MNEMONIC_KEEP_VERSIONS"] = keep
        p = subprocess.run(["bash", SCRIPT, version], capture_output=True, text=True, env=env, cwd=REPO, timeout=60)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        return p.stdout

    def test_the_newest_two_previous_versions_are_kept_by_default(self):
        self.make_versions("v1", "v2", "v3", "v4", "v5")
        out = self.install("v6")
        self.assertEqual(self.versions(), ["v4", "v5", "v6"])
        for gone in ("v1", "v2", "v3"):
            self.assertIn(f"Removed old plugin version {gone}", out)
        self.assertIn("Keeping previous plugin version v5", out)
        self.assertIn("Keeping previous plugin version v4", out)

    def test_zero_removes_every_other_version_like_before(self):
        self.make_versions("v1", "v2", "v3")
        self.install("v4", keep="0")
        self.assertEqual(self.versions(), ["v4"])

    def test_a_larger_number_keeps_more(self):
        self.make_versions("v1", "v2", "v3", "v4")
        self.install("v5", keep="10")
        self.assertEqual(self.versions(), ["v1", "v2", "v3", "v4", "v5"])

    def test_nonsense_falls_back_to_the_default(self):
        self.make_versions("v1", "v2", "v3", "v4", "v5")
        self.install("v6", keep="lots")
        self.assertEqual(self.versions(), ["v4", "v5", "v6"])

    def test_reinstalling_a_version_that_exists_does_not_count_it_as_a_kept_one(self):
        self.make_versions("v1", "v2", "v3", "v4", "v5")
        self.install("v5")
        self.assertEqual(self.versions(), ["v3", "v4", "v5"], "v5 stays, the two newest others (v4, v3) are kept, v1 and v2 go")

    def test_files_in_the_cache_directory_are_left_alone(self):
        self.make_versions("v1", "v2", "v3", "v4", "v5")
        stray = os.path.join(self.base, "notes.txt")
        with open(stray, "w") as f:
            f.write("keep me")
        self.install("v6", keep="0")
        self.assertTrue(os.path.exists(stray))

    def test_the_new_version_is_registered_as_before(self):
        self.make_versions("v1")
        self.install("v2")
        with open(os.path.join(self.home, ".claude", "plugins", "installed_plugins.json")) as f:
            entry = json.load(f)["plugins"]["claude-mnemonic@claude-mnemonic"][0]
        self.assertEqual(entry["version"], "v2")
        self.assertEqual(entry["installPath"], os.path.join(self.base, "v2"))
        self.assertTrue(os.path.isdir(os.path.join(self.base, "v2", "hooks")))

    def test_a_kept_version_still_runs_its_hook_with_the_current_binaries(self):
        """The reason keeping old versions is harmless: their wrappers call the stable binaries."""
        self.make_versions("v1")
        stable = os.path.join(self.home, ".claude-mnemonic", "bin", "hooks")
        os.makedirs(stable)
        binary = os.path.join(stable, "stop")
        with open(binary, "w") as f:
            f.write('#!/bin/sh\necho "ran the current binary"\n')
        os.chmod(binary, 0o755)
        wrapper = os.path.join(self.base, "v1", "hooks", "stop")
        shutil.copy(os.path.join(REPO, "hooks", "stop"), wrapper)
        os.chmod(wrapper, 0o755)

        self.install("v2")
        self.install("v3")
        self.assertIn("v1", self.versions(), "still there after two later installs")
        out = subprocess.run([wrapper], capture_output=True, text=True, env=dict(os.environ, HOME=self.home), timeout=30).stdout
        self.assertIn("ran the current binary", out)

    def test_the_first_install_works_without_a_cache_directory(self):
        shutil.rmtree(os.path.join(self.home, ".claude", "plugins", "cache"))
        self.install("v1")
        self.assertEqual(self.versions(), ["v1"])


if __name__ == "__main__":
    unittest.main()
