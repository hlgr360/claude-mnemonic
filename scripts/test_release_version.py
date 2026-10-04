#!/usr/bin/env python3
"""scripts/release-version.sh reads the upstream version merged into HEAD, and --next the fork release that follows it.

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

    def test_fork_release_tags_are_never_the_upstream_base(self):
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.commit("fork release")
        self.git("tag", "v0.21.95.3")
        self.assertEqual(self.run_script().stdout.strip(), "0.21.95")
        self.assertEqual(self.run_script("--tag").stdout.strip(), "v0.21.95")

    def origin(self):
        """A bare repository as origin, so --next can read the tags the fork has already published."""
        bare = os.path.join(self.tmp.name, "origin.git")
        subprocess.run(["git", "init", "-q", "--bare", bare], check=True, capture_output=True)
        self.git("remote", "add", "origin", bare)
        return bare

    def publish(self, *tags):
        for tag in tags:
            self.git("tag", tag)
        self.git("push", "-q", "origin", *tags)

    def test_next_is_the_first_fork_number_when_none_is_published(self):
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.origin()
        out = self.run_script("--next")
        self.assertEqual((out.returncode, out.stdout.strip()), (0, "v0.21.95.1"), out.stderr)

    def test_next_counts_on_from_the_highest_published_number(self):
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.origin()
        self.publish("v0.21.95.1", "v0.21.95.2")
        self.assertEqual(self.run_script("--next").stdout.strip(), "v0.21.95.3")

    def test_next_compares_numbers_not_text(self):
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.origin()
        self.publish("v0.21.95.2", "v0.21.95.9", "v0.21.95.10")
        self.assertEqual(self.run_script("--next").stdout.strip(), "v0.21.95.11")

    def test_next_also_counts_a_tag_that_is_only_in_this_clone(self):
        # Otherwise a tag made locally and not pushed yet would be handed out twice.
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.origin()
        self.publish("v0.21.95.1")
        self.git("tag", "v0.21.95.2")
        self.assertEqual(self.run_script("--next").stdout.strip(), "v0.21.95.3")

    def test_next_ignores_other_upstream_versions_and_pre_releases(self):
        self.commit("one")
        self.git("tag", "v0.21.94")
        self.git("tag", "v0.21.95")
        self.origin()
        self.publish("v0.21.94.7", "v0.21.95.1-rc1", "v0.21.950.4")
        self.assertEqual(self.run_script("--next").stdout.strip(), "v0.21.95.1")

    def test_next_starts_again_at_one_for_a_new_upstream_version(self):
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.origin()
        self.publish("v0.21.95.1", "v0.21.95.2")
        self.commit("merge upstream 0.21.96")
        self.git("tag", "v0.21.96")
        self.assertEqual(self.run_script("--next").stdout.strip(), "v0.21.96.1")

    def test_next_fails_clearly_when_origin_cannot_be_read(self):
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.git("remote", "add", "origin", os.path.join(self.tmp.name, "does-not-exist.git"))
        out = self.run_script("--next")
        self.assertEqual(out.returncode, 1)
        self.assertIn("Cannot read the tags of origin", out.stderr)
        self.assertEqual(out.stdout, "", "no guess is printed")

    def test_an_unknown_option_is_refused(self):
        self.commit("one")
        self.git("tag", "v0.21.95")
        self.assertEqual(self.run_script("--bogus").returncode, 2)

    def test_fails_with_a_hint_when_no_release_tag_is_merged(self):
        self.commit("one")
        self.git("tag", "not-a-version")
        out = self.run_script()
        self.assertEqual(out.returncode, 1)
        self.assertIn("git fetch upstream --tags", out.stderr)
        self.assertEqual(out.stdout, "")


if __name__ == "__main__":
    unittest.main()
