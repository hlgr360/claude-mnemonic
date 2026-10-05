#!/usr/bin/env python3
"""plugin/lib/statusline.sh sets up Claude Code's status line, which a plugin cannot do itself (Claude Code applies only
"agent" and "subagentStatusLine" from a plugin's settings). It must set it when none is set, never replace someone else's
without being told, keep every other setting, back up before replacing, remove only its own, and never touch a file it cannot
read as JSON. Everything runs against a throwaway HOME and CLAUDE_CONFIG_DIR."""
import json
import os
import shutil
import subprocess
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "plugin", "lib", "statusline.sh")


@unittest.skipUnless(shutil.which("sh") and shutil.which("python3"), "needs sh and python3")
class StatuslineSetup(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.home = os.path.join(self.tmp.name, "home")
        self.config = os.path.join(self.home, ".claude")
        self.settings = os.path.join(self.config, "settings.json")
        self.binary = os.path.join(self.home, ".claude-mnemonic", "bin", "hooks", "statusline")
        os.makedirs(os.path.dirname(self.binary))

    def install_binary(self):
        with open(self.binary, "w") as f:
            f.write("#!/bin/sh\necho hi\n")
        os.chmod(self.binary, 0o755)

    def write(self, data):
        os.makedirs(self.config, exist_ok=True)
        with open(self.settings, "w", encoding="utf-8") as f:
            f.write(data if isinstance(data, str) else json.dumps(data))

    def read(self):
        with open(self.settings, encoding="utf-8") as f:
            return json.load(f)

    def run_script(self, *args):
        env = {"HOME": self.home, "PATH": os.environ["PATH"], "CLAUDE_CONFIG_DIR": self.config}
        return subprocess.run(["sh", SCRIPT, *args], env=env, capture_output=True, text=True, timeout=30)

    def test_status_says_none_ours_or_other(self):
        self.assertEqual(self.run_script("status").stdout.strip(), "NONE", "no file at all")
        self.write({"theme": "dark"})
        self.assertEqual(self.run_script("status").stdout.strip(), "NONE")
        self.write({"statusLine": {"type": "command", "command": "/x/.claude-mnemonic/bin/hooks/statusline"}})
        self.assertEqual(self.run_script("status").stdout.strip(), "OURS")
        self.write({"statusLine": {"type": "command", "command": "~/bin/my-line.sh"}})
        self.assertEqual(self.run_script("status").stdout.strip(), "OTHER ~/bin/my-line.sh")

    def test_enable_creates_the_file_and_sets_the_installed_binary(self):
        self.install_binary()
        out = self.run_script("enable")
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertEqual(out.stdout.strip(), "ENABLED " + self.binary)
        self.assertEqual(self.read(), {"statusLine": {"type": "command", "command": self.binary, "padding": 0}})

    def test_enable_keeps_every_other_setting(self):
        self.install_binary()
        self.write({"theme": "dark", "env": {"A": "1"}, "enabledPlugins": {"x@y": True}, "hooks": {"Stop": []}})
        self.assertEqual(self.run_script("enable").returncode, 0)
        data = self.read()
        self.assertEqual({k: data[k] for k in ("theme", "env", "enabledPlugins", "hooks")},
                         {"theme": "dark", "env": {"A": "1"}, "enabledPlugins": {"x@y": True}, "hooks": {"Stop": []}})
        self.assertEqual(data["statusLine"]["command"], self.binary)

    def test_someone_elses_status_line_is_left_alone_unless_replacement_is_asked_for(self):
        self.install_binary()
        original = {"theme": "dark", "statusLine": {"type": "command", "command": "~/bin/my-line.sh"}}
        self.write(original)
        out = self.run_script("enable")
        self.assertEqual(out.returncode, 3)
        self.assertIn("OTHER ~/bin/my-line.sh", out.stdout)
        self.assertIn("not changed", out.stderr)
        self.assertEqual(self.read(), original, "nothing was written")
        self.assertFalse(os.path.exists(self.settings + ".mnemonic-backup"))

    def test_replace_backs_up_the_old_file_first(self):
        self.install_binary()
        original = {"theme": "dark", "statusLine": {"type": "command", "command": "~/bin/my-line.sh"}}
        self.write(original)
        out = self.run_script("enable", "--replace")
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertEqual(self.read()["statusLine"]["command"], self.binary)
        with open(self.settings + ".mnemonic-backup", encoding="utf-8") as f:
            self.assertEqual(json.load(f), original, "the previous settings are kept")

    def test_enable_when_it_is_already_ours_is_a_quiet_update(self):
        self.install_binary()
        self.write({"statusLine": {"type": "command", "command": "/old/path/.claude-mnemonic/bin/hooks/statusline", "padding": 2}})
        self.assertEqual(self.run_script("enable").returncode, 0)
        self.assertEqual(self.read()["statusLine"]["command"], self.binary)
        self.assertFalse(os.path.exists(self.settings + ".mnemonic-backup"), "replacing our own needs no backup")

    def test_it_refuses_to_enable_a_status_line_that_is_not_installed_yet(self):
        out = self.run_script("enable")  # no binary
        self.assertEqual(out.returncode, 4)
        self.assertIn("start a Claude Code session", out.stderr)
        self.assertFalse(os.path.exists(self.settings), "no settings written for a binary that is not there")

    def test_disable_removes_only_its_own(self):
        self.write({"theme": "dark", "statusLine": {"type": "command", "command": "/x/.claude-mnemonic/bin/hooks/statusline"}})
        self.assertEqual(self.run_script("disable").stdout.strip(), "DISABLED")
        self.assertEqual(self.read(), {"theme": "dark"})
        other = {"statusLine": {"type": "command", "command": "~/bin/my-line.sh"}}
        self.write(other)
        out = self.run_script("disable")
        self.assertEqual(out.returncode, 3)
        self.assertEqual(self.read(), other, "someone else's status line stays")
        self.write({"theme": "dark"})
        self.assertEqual(self.run_script("disable").stdout.strip(), "NONE")

    def test_a_settings_file_that_is_not_json_is_never_touched(self):
        self.install_binary()
        self.write("{ not json")
        for args in (("status",), ("enable",), ("enable", "--replace"), ("disable",)):
            with self.subTest(args=args):
                out = self.run_script(*args)
                self.assertNotEqual(out.returncode, 0)
                self.assertIn("not valid JSON", out.stderr)
        with open(self.settings, encoding="utf-8") as f:
            self.assertEqual(f.read(), "{ not json")

    def test_a_plugin_install_alone_has_no_status_line_which_is_why_this_exists(self):
        # The reference says only "agent" and "subagentStatusLine" take effect from a plugin's settings, so the manifest
        # must not claim to set a statusLine.
        with open(os.path.join(ROOT, "plugin", ".claude-plugin", "plugin.json.tpl"), encoding="utf-8") as f:
            manifest = f.read()
        self.assertNotIn("statusLine", manifest)


if __name__ == "__main__":
    unittest.main()
