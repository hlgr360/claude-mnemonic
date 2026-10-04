#!/usr/bin/env python3
"""The install, registration and release scripts take the release repository and the marketplace name from one place
(MNEMONIC_REPO, MNEMONIC_MARKETPLACE) instead of hard-coding upstream's, and default to this fork's repository and to
the marketplace name they always used.

Run: python3 -m unittest scripts/test_release_repo.py -v
Everything runs against temporary directories; the real ~/.claude is never touched. unregister-plugin.sh is not run
here: it kills whatever listens on the worker port, which would be a real worker on a developer machine.
"""
import glob
import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(HERE)
FORK = "hlgr360/claude-mnemonic"
UPSTREAM = "lukaszraczylo"


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def script(name):
    return os.path.join(HERE, name)


@unittest.skipUnless(shutil.which("bash") and shutil.which("jq") and shutil.which("python3"), "needs bash, jq and python3")
class RegisterPlugin(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp()
        self.addCleanup(lambda: shutil.rmtree(self.home, ignore_errors=True))
        os.makedirs(os.path.join(self.home, ".claude", "plugins"))

    def register(self, **env):
        e = dict(os.environ, HOME=self.home)
        e.pop("MNEMONIC_REPO", None)
        e.pop("MNEMONIC_MARKETPLACE", None)
        e.update(env)
        p = subprocess.run(["bash", script("register-plugin.sh"), "v9.9.9"], capture_output=True, text=True, env=e, cwd=REPO_ROOT, timeout=60)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def written(self):
        claude = os.path.join(self.home, ".claude")
        known = json.loads(read(os.path.join(claude, "plugins", "known_marketplaces.json")))
        settings = json.loads(read(os.path.join(claude, "settings.json")))
        installed = json.loads(read(os.path.join(claude, "plugins", "installed_plugins.json")))
        return known, settings, installed

    def test_defaults_are_this_forks_repository_and_the_marketplace_name_it_always_used(self):
        self.register()
        known, settings, installed = self.written()
        self.assertEqual(list(known), ["claude-mnemonic"])
        self.assertEqual(known["claude-mnemonic"]["source"], {"source": "github", "repo": FORK})
        self.assertTrue(known["claude-mnemonic"]["installLocation"].endswith("/marketplaces/claude-mnemonic"))
        self.assertEqual(settings["extraKnownMarketplaces"]["claude-mnemonic"]["source"], {"repo": FORK, "source": "github"})
        self.assertTrue(settings["enabledPlugins"]["claude-mnemonic@claude-mnemonic"])
        self.assertIn("claude-mnemonic@claude-mnemonic", installed["plugins"])

    def test_the_repository_and_the_marketplace_can_be_set(self):
        self.register(MNEMONIC_REPO="someone/their-fork", MNEMONIC_MARKETPLACE="custom")
        known, settings, installed = self.written()
        self.assertEqual(list(known), ["custom"])
        self.assertEqual(known["custom"]["source"], {"source": "github", "repo": "someone/their-fork"})
        self.assertTrue(known["custom"]["installLocation"].endswith("/marketplaces/custom"))
        self.assertEqual(settings["extraKnownMarketplaces"]["custom"]["source"], {"repo": "someone/their-fork", "source": "github"})
        self.assertTrue(settings["enabledPlugins"]["claude-mnemonic@custom"])
        entry = installed["plugins"]["claude-mnemonic@custom"][0]
        self.assertIn("/cache/custom/claude-mnemonic/v9.9.9", entry["installPath"])
        self.assertNotIn("claude-mnemonic@claude-mnemonic", installed["plugins"])

    def test_the_python_fallback_writes_the_same_thing(self):
        """Systems without jq run the embedded Python block; run it directly with the arguments the script gives it."""
        text = read(script("register-plugin.sh"))
        self.assertIn('"$STATUSLINE_CMD" "$MARKETPLACE_NAME" "$MARKETPLACE_PATH" "$REPO" <<', text, "the call passes the arguments in the order used below")
        block = re.search(r"<<'PYEOF'\n(.*?)\nPYEOF\n", text, re.S).group(1)
        claude = os.path.join(self.home, ".claude")
        args = [
            os.path.join(claude, "plugins", "installed_plugins.json"), os.path.join(claude, "settings.json"),
            os.path.join(claude, "plugins", "known_marketplaces.json"), "claude-mnemonic@custom",
            os.path.join(claude, "plugins", "cache", "custom", "claude-mnemonic", "v9.9.9"), "v9.9.9", "2026-10-04T00:00:00.000Z",
            "${CLAUDE_PLUGIN_ROOT}/hooks/statusline", "custom", os.path.join(claude, "plugins", "marketplaces", "custom"), "someone/their-fork",
        ]
        p = subprocess.run(["python3", "-", *args], input=block, capture_output=True, text=True, timeout=60)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        known, settings, _ = self.written()
        self.assertEqual(known["custom"]["source"], {"source": "github", "repo": "someone/their-fork"})
        self.assertEqual(settings["extraKnownMarketplaces"]["custom"]["source"], {"repo": "someone/their-fork", "source": "github"})


@unittest.skipUnless(shutil.which("bash") and shutil.which("jq"), "needs bash and jq")
class UpdateMarketplace(unittest.TestCase):
    def run_it(self, **env):
        work = tempfile.mkdtemp()
        self.addCleanup(lambda: shutil.rmtree(work, ignore_errors=True))
        with open(os.path.join(work, "marketplace.json"), "w") as f:
            json.dump({"plugins": [{"name": "claude-mnemonic", "version": "0", "releases": {"latest": "0", "versions": {}}}]}, f)
        with open(os.path.join(work, "checksums.txt"), "w") as f:
            f.write("\n".join(f"{c * 64}  claude-mnemonic_1.2.3_{t}.tar.gz" for c, t in zip("abcd", ("darwin_amd64", "darwin_arm64", "linux_amd64", "windows_amd64"))) + "\n")
        e = dict(os.environ)
        e.pop("MNEMONIC_REPO", None)
        e.update(env)
        p = subprocess.run(["bash", script("update-marketplace.sh"), "1.2.3", "checksums.txt"], capture_output=True, text=True, env=e, cwd=work, timeout=60)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        return json.loads(read(os.path.join(work, "marketplace.json")))["plugins"][0]["releases"]["versions"]["1.2.3"]["downloads"]

    def test_download_urls_point_at_this_forks_releases_by_default(self):
        downloads = self.run_it()
        self.assertEqual(downloads["linux-amd64"]["url"], f"https://github.com/{FORK}/releases/download/v1.2.3/claude-mnemonic_1.2.3_linux_amd64.tar.gz")
        self.assertEqual(downloads["windows-amd64"]["url"], f"https://github.com/{FORK}/releases/download/v1.2.3/claude-mnemonic_1.2.3_windows_amd64.zip")
        self.assertEqual(downloads["darwin-arm64"]["sha256"], "b" * 64)

    def test_download_urls_follow_the_configured_repository(self):
        downloads = self.run_it(MNEMONIC_REPO="someone/their-fork")
        for entry in downloads.values():
            self.assertTrue(entry["url"].startswith("https://github.com/someone/their-fork/releases/download/v1.2.3/"), entry["url"])


@unittest.skipUnless(shutil.which("bash"), "needs bash")
class ConfigurationBlocks(unittest.TestCase):
    """The variables at the top of install.sh and uninstall.sh, evaluated in a throwaway HOME."""

    def evaluate(self, name, start, end, names, **env):
        lines = read(script(name)).splitlines()
        i = next(n for n, l in enumerate(lines) if start in l)
        j = next(n for n, l in enumerate(lines) if n > i and end in l)
        block = "\n".join(lines[i:j])
        home = tempfile.mkdtemp()
        self.addCleanup(lambda: shutil.rmtree(home, ignore_errors=True))
        e = {"HOME": home, "PATH": os.environ["PATH"]}
        e.update(env)
        code = "set -e\n" + block + "\n" + "\n".join(f'echo "{n}=${n}"' for n in names)
        p = subprocess.run(["bash", "-c", code], capture_output=True, text=True, env=e, timeout=30)
        self.assertEqual(p.returncode, 0, p.stderr)
        values = dict(line.split("=", 1) for line in p.stdout.splitlines())
        return {k: v.replace(home, "~") for k, v in values.items()}

    def test_install_sh_defaults_and_overrides(self):
        names = ["GITHUB_REPO", "MARKETPLACE_NAME", "INSTALL_DIR", "CACHE_DIR", "PLUGIN_KEY"]
        d = self.evaluate("install.sh", "# Configuration", "# Colors for output", names)
        self.assertEqual(d, {"GITHUB_REPO": FORK, "MARKETPLACE_NAME": "claude-mnemonic", "INSTALL_DIR": "~/.claude/plugins/marketplaces/claude-mnemonic",
                             "CACHE_DIR": "~/.claude/plugins/cache/claude-mnemonic/claude-mnemonic", "PLUGIN_KEY": "claude-mnemonic@claude-mnemonic"})
        o = self.evaluate("install.sh", "# Configuration", "# Colors for output", names, MNEMONIC_REPO="someone/their-fork", MNEMONIC_MARKETPLACE="custom")
        self.assertEqual(o, {"GITHUB_REPO": "someone/their-fork", "MARKETPLACE_NAME": "custom", "INSTALL_DIR": "~/.claude/plugins/marketplaces/custom",
                             "CACHE_DIR": "~/.claude/plugins/cache/custom/claude-mnemonic", "PLUGIN_KEY": "claude-mnemonic@custom"})

    def test_uninstall_sh_follows_the_marketplace_name(self):
        text = read(script("uninstall.sh"))
        start = next(l for l in text.splitlines() if l.startswith("MARKETPLACE_NAME="))
        names = ["MARKETPLACE_NAME", "INSTALL_DIR", "CACHE_DIR", "PLUGIN_KEY"]
        d = self.evaluate("uninstall.sh", start, "DATA_DIR=", names, MNEMONIC_MARKETPLACE="custom")
        self.assertEqual(d["INSTALL_DIR"], "~/.claude/plugins/marketplaces/custom")
        self.assertEqual(d["CACHE_DIR"], "~/.claude/plugins/cache/custom")


class NoHardCodedUpstream(unittest.TestCase):
    def test_no_script_names_upstream_any_more(self):
        offenders = [os.path.basename(p) for p in sorted(glob.glob(os.path.join(HERE, "*.sh")) + glob.glob(os.path.join(HERE, "*.ps1")))
                     if UPSTREAM in read(p)]
        self.assertEqual(offenders, [], "the repository comes from one variable per script (MNEMONIC_REPO), defaulting to this fork")

    def test_every_script_that_names_a_repository_defaults_to_this_fork(self):
        for name, pattern in (("install.sh", r'GITHUB_REPO="\$\{MNEMONIC_REPO:-hlgr360/claude-mnemonic\}"'),
                              ("register-plugin.sh", r'REPO="\$\{MNEMONIC_REPO:-hlgr360/claude-mnemonic\}"'),
                              ("update-marketplace.sh", r'REPO="\$\{MNEMONIC_REPO:-hlgr360/claude-mnemonic\}"'),
                              ("install.ps1", r'\$GitHubRepo = if \(\$env:MNEMONIC_REPO\) \{ \$env:MNEMONIC_REPO \} else \{ "hlgr360/claude-mnemonic" \}')):
            self.assertRegex(read(script(name)), pattern, name)

    def test_powershell_scripts_use_the_marketplace_variable_for_the_registry_entry(self):
        """PowerShell is not available to run these here, so the lines that matter are checked textually."""
        install, uninstall = read(script("install.ps1")), read(script("uninstall.ps1"))
        self.assertIn("-NotePropertyName $MarketplaceName", install)
        self.assertIn("Properties.Remove($MarketplaceName)", install)
        self.assertIn('$PluginKey = "claude-mnemonic@$MarketplaceName"', install)
        self.assertIn("Properties.Remove($MarketplaceName)", uninstall)
        self.assertIn('$PluginKey = "claude-mnemonic@$MarketplaceName"', uninstall)
        for text in (install, uninstall):
            self.assertIn('else { "claude-mnemonic" }', text, "the marketplace name defaults to what it always was")


if __name__ == "__main__":
    unittest.main()
