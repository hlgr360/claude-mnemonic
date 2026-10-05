#!/usr/bin/env python3
"""scripts/build-mcpb.sh builds the Claude Desktop extension (.mcpb): a manifest Desktop accepts, the thin wrapper and the
downloader, a version Desktop can compare, and the same bytes every time. The wrapper really starts the installed MCP
server from the unpacked extension. Run: python3 -m unittest scripts/test_desktop_extension.py -v

The real `mcpb validate` needs npx and the network: set RUN_MCPB_VALIDATE=1 to run it here (the release workflow always does).
"""
import hashlib
import json
import os
import shutil
import subprocess
import tempfile
import unittest
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
BUILD = os.path.join(HERE, "build-mcpb.sh")


def build(dist, version, **env):
    e = dict(os.environ, DIST=dist, SKIP_VALIDATE="1", **env)
    return subprocess.run(["bash", BUILD, version], cwd=ROOT, env=e, capture_output=True, text=True, timeout=120)


def read(path):
    with open(path, "rb") as f:
        return f.read()


class BuildMcpb(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dist = os.path.join(self.tmp.name, "dist")

    def built(self, version="v0.21.95.3", **env):
        out = build(self.dist, version, **env)
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        path = os.path.join(self.dist, f"claude-mnemonic-desktop_{version.lstrip('v')}.mcpb")
        self.assertTrue(os.path.isfile(path), path)
        return path

    def manifest(self, path):
        with zipfile.ZipFile(path) as z:
            return json.loads(z.read("manifest.json"))

    def test_the_file_holds_the_manifest_the_wrapper_and_the_downloader_and_nothing_else(self):
        with zipfile.ZipFile(self.built()) as z:
            self.assertEqual(sorted(z.namelist()), ["LICENSE", "manifest.json", "server/.claude-plugin/plugin.json",
                                                     "server/lib/ensure-binaries.sh", "server/mcp-server"])

    def test_the_manifest_is_what_desktop_needs_to_start_the_server_without_an_executable_bit(self):
        m = self.manifest(self.built())
        self.assertEqual(m["manifest_version"], "0.3")
        self.assertEqual(m["name"], "claude-mnemonic")
        self.assertEqual(m["server"]["type"], "binary")
        self.assertEqual(m["server"]["entry_point"], "server/mcp-server")
        self.assertEqual(m["server"]["mcp_config"]["command"], "/bin/sh", "the script is run by sh, so it needs no executable bit after unpacking")
        self.assertEqual(m["server"]["mcp_config"]["args"], ["${__dirname}/server/mcp-server"])
        self.assertEqual(m["compatibility"]["platforms"], ["darwin"], "the downloader has a build for macOS on Apple silicon (and Linux, which Desktop is not)")
        for field in ("description", "author", "version"):
            self.assertTrue(m[field])

    def test_a_fork_number_becomes_a_semver_prerelease_that_increases(self):
        self.assertEqual(self.manifest(self.built("v0.21.95.3"))["version"], "0.21.95-fork.3")

        def key(v):
            core, _, pre = v.partition("-fork.")
            return tuple(int(x) for x in core.split(".")) + (int(pre),)

        versions = [self.manifest(self.built(v))["version"] for v in ("0.21.95.2", "0.21.95.10", "0.21.96.1")]
        self.assertEqual(versions, ["0.21.95-fork.2", "0.21.95-fork.10", "0.21.96-fork.1"])
        self.assertEqual(sorted(versions, key=key), versions, "each release is higher than the one before, two-digit fork numbers included")

    def test_a_plain_upstream_version_stays_as_it_is(self):
        self.assertEqual(self.manifest(self.built("0.21.95"))["version"], "0.21.95")

    def test_a_version_that_is_not_a_release_is_refused(self):
        for bad in ("0.21.95a", "0.21", "v1.2.3.4.5", "latest"):
            with self.subTest(version=bad):
                out = build(self.dist, bad)
                self.assertNotEqual(out.returncode, 0)
                self.assertIn("is not a release version", out.stderr)

    def test_the_downloader_gets_the_plain_release_version_to_fetch(self):
        path = self.built("v0.21.95.3")
        with zipfile.ZipFile(path) as z:
            self.assertEqual(json.loads(z.read("server/.claude-plugin/plugin.json"))["version"], "0.21.95.3")

    def test_the_same_inputs_give_the_same_bytes(self):
        first = hashlib.sha256(read(self.built())).hexdigest()
        second = hashlib.sha256(read(self.built())).hexdigest()
        self.assertEqual(first, second)

    def test_a_fork_of_the_fork_downloads_from_and_links_to_its_own_releases(self):
        path = self.built(MNEMONIC_REPO="someone/their-fork")
        with zipfile.ZipFile(path) as z:
            manifest = z.read("manifest.json").decode()
            script = z.read("server/lib/ensure-binaries.sh").decode()
        self.assertIn("github.com/someone/their-fork", manifest)
        self.assertNotIn("hlgr360/claude-mnemonic", manifest)
        self.assertIn('DEFAULT_REPO="someone/their-fork"', script)

    @unittest.skipUnless(os.environ.get("RUN_MCPB_VALIDATE") == "1" and shutil.which("npx"), "set RUN_MCPB_VALIDATE=1 (needs npx and the network)")
    def test_the_real_validator_accepts_the_manifest(self):
        e = dict(os.environ, DIST=self.dist)
        e.pop("SKIP_VALIDATE", None)
        out = subprocess.run(["bash", BUILD, "v0.21.95.3"], cwd=ROOT, env=e, capture_output=True, text=True, timeout=300)
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertIn("passes", out.stdout)


@unittest.skipUnless(shutil.which("sh"), "needs sh")
class Wrapper(unittest.TestCase):
    """The unpacked extension starts the installed MCP server, the way Desktop runs it (/bin/sh server/mcp-server)."""

    def test_it_runs_the_installed_server_with_its_arguments_and_needs_no_download(self):
        with tempfile.TemporaryDirectory() as tmp:
            dist = os.path.join(tmp, "dist")
            self.assertEqual(build(dist, "v0.21.95.3").returncode, 0)
            root = os.path.join(tmp, "unpacked")
            with zipfile.ZipFile(os.path.join(dist, "claude-mnemonic-desktop_0.21.95.3.mcpb")) as z:
                z.extractall(root)
            home = os.path.join(tmp, "home")
            os.makedirs(os.path.join(home, ".claude-mnemonic", "bin"))
            fake = os.path.join(home, ".claude-mnemonic", "bin", "mcp-server")
            with open(fake, "w") as f:
                f.write('#!/bin/sh\necho "fake server args: $*"\n')
            os.chmod(fake, 0o755)
            # sh server/mcp-server, as Desktop runs it; the shim curl proves nothing is fetched.
            shim = os.path.join(tmp, "bin")
            os.makedirs(shim)
            with open(os.path.join(shim, "curl"), "w") as f:
                f.write("#!/bin/sh\necho curl was called >&2\nexit 1\n")
            os.chmod(os.path.join(shim, "curl"), 0o755)
            env = {"HOME": home, "PATH": shim + os.pathsep + os.environ["PATH"]}
            out = subprocess.run(["/bin/sh", os.path.join(root, "server", "mcp-server"), "--mode", "desktop"], env=env, capture_output=True, text=True, timeout=30)
            self.assertEqual(out.returncode, 0, out.stderr)
            self.assertEqual(out.stdout.strip(), "fake server args: --mode desktop")
            self.assertNotIn("curl was called", out.stderr)


if __name__ == "__main__":
    unittest.main()
