#!/usr/bin/env python3
"""scripts/release-version.sh reads the version a fork release must carry from the upstream tags merged into HEAD.

Run: python3 -m unittest scripts/test_release_version.py -v
Each test builds a throwaway repository with made-up history and tags; this repository is not touched.
"""
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "release-version.sh")
GIT = ["git", "-c", "user.name=Test", "-c", "user.email=test@example.org", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"]


@unittest.skipUnless(shutil.which("bash") and shutil.which("git"), "needs bash and git")
class ReleaseVersion(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = self.tmp.name
        self.git("init", "-q", "-b", "main")

    def git(self, *args):
        subprocess.run(GIT + list(args), cwd=self.repo, check=True, capture_output=True)

    def commit(self, message):
        self.git("commit", "-q", "--allow-empty", "-m", message)

    def run_script(self, *args):
        return subprocess.run(["bash", SCRIPT, *args], cwd=self.repo, capture_output=True, text=True)

    def test_prints_the_highest_merged_release_without_the_v(self):
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.commit("fork work")
        out = self.run_script()
        self.assertEqual((out.returncode, out.stdout.strip()), (0, "0.21.95"), out.stderr)
        self.assertEqual(self.run_script("--tag").stdout.strip(), "v0.21.95")

    def test_versions_sort_numerically_not_as_text(self):
        for tag in ("v0.9.0", "v0.10.0", "v0.2.0"):
            self.commit(tag)
            self.git("tag", tag)
        self.assertEqual(self.run_script().stdout.strip(), "0.10.0")

    def test_a_release_that_is_not_merged_is_not_counted(self):
        self.commit("base")
        self.git("tag", "v1.0.0")
        self.git("checkout", "-q", "-b", "upstream-only")
        self.commit("later upstream release")
        self.git("tag", "v1.1.0")
        self.git("checkout", "-q", "main")
        self.commit("fork work")
        self.assertEqual(self.run_script().stdout.strip(), "1.0.0")

    def test_suffixed_tags_are_ignored(self):
        self.commit("one")
        self.git("tag", "v1.0.0")
        self.commit("two")
        self.git("tag", "v1.1.0-rc1")
        self.git("tag", "v1.1.0-fork.1")
        self.assertEqual(self.run_script().stdout.strip(), "1.0.0")

    def test_fails_with_a_hint_when_no_release_tag_is_merged(self):
        self.commit("one")
        self.git("tag", "not-a-version")
        out = self.run_script()
        self.assertEqual(out.returncode, 1)
        self.assertIn("git fetch upstream --tags", out.stderr)
        self.assertEqual(out.stdout, "")


if __name__ == "__main__":
    unittest.main()
