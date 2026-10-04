#!/usr/bin/env python3
"""The unregister and uninstall scripts keep the data directory (~/.claude-mnemonic: the database, settings and
embeddings) unless --purge is given. They used to delete it: unregister-plugin.sh always did, and the uninstallers did
unless --keep-data was passed, so switching from one install route to another could cost all of a person's memory.

Run: python3 -m unittest scripts/test_data_safety.py -v
Every script runs against a temporary HOME that holds a fake database. The commands that stop processes (pkill, lsof,
kill, killall, sleep) are replaced by no-op shims first on PATH, and the test checks that they are the ones that resolve
before it runs anything: the real ~/.claude-mnemonic and any running worker are never touched.
"""
import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SHIMMED = ("pkill", "lsof", "kill", "killall", "ss", "fuser", "sleep")


def script(name):
    return os.path.join(HERE, name)


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


@unittest.skipUnless(shutil.which("bash") and shutil.which("jq"), "needs bash and jq")
class DataSurvives(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.home = os.path.join(self.tmp.name, "home")
        self.shims = os.path.join(self.tmp.name, "shims")
        self.calls = os.path.join(self.tmp.name, "shim.calls")
        os.makedirs(self.shims)
        for name in SHIMMED:
            path = os.path.join(self.shims, name)
            with open(path, "w", encoding="utf-8") as f:
                f.write(f'#!/bin/sh\necho "{name} $*" >> "{self.calls}"\nexit 0\n')
            os.chmod(path, 0o755)
        self.data = os.path.join(self.home, ".claude-mnemonic")
        os.makedirs(os.path.join(self.data, "bin"))
        for rel, text in (("claude-mnemonic.db", "the whole memory"), ("settings.json", "{}"), ("bin/worker", "binary")):
            with open(os.path.join(self.data, rel), "w", encoding="utf-8") as f:
                f.write(text)
        plugins = os.path.join(self.home, ".claude", "plugins")
        for d in ("marketplaces/claude-mnemonic", "cache/claude-mnemonic"):
            os.makedirs(os.path.join(plugins, d))
        self.write_json(os.path.join(plugins, "installed_plugins.json"), {"plugins": {"claude-mnemonic@claude-mnemonic": [{}]}})
        self.write_json(os.path.join(plugins, "known_marketplaces.json"), {"claude-mnemonic": {}})
        self.write_json(os.path.join(self.home, ".claude", "settings.json"), {"enabledPlugins": {"claude-mnemonic@claude-mnemonic": True}})
        self.env = dict(os.environ, HOME=self.home, PATH=self.shims + os.pathsep + os.environ["PATH"])
        self.env.pop("MNEMONIC_MARKETPLACE", None)
        self.assert_shims_resolve_first()

    @staticmethod
    def write_json(path, value):
        with open(path, "w", encoding="utf-8") as f:
            json.dump(value, f)

    def assert_shims_resolve_first(self):
        """Refuse to run a script that would kill real processes if a shim were not the command that gets called."""
        for name in SHIMMED:
            # type -P looks the name up on PATH only: `kill` is also a shell builtin, which a PATH shim cannot replace.
            found = subprocess.run(["bash", "-c", f"type -P {name}"], env=self.env, capture_output=True, text=True).stdout.strip()
            self.assertEqual(found, os.path.join(self.shims, name), f"{name} must resolve to the no-op shim")

    def run_script(self, name, *args):
        return subprocess.run(["bash", script(name), *args], env=self.env, capture_output=True, text=True, timeout=60, stdin=subprocess.DEVNULL)

    def shim_calls(self):
        return read(self.calls) if os.path.exists(self.calls) else ""

    def database_survives(self):
        path = os.path.join(self.data, "claude-mnemonic.db")
        return os.path.isfile(path) and read(path) == "the whole memory"

    # ---- unregister-plugin.sh

    def test_unregister_keeps_the_data_by_default_and_still_unregisters(self):
        out = self.run_script("unregister-plugin.sh")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertTrue(self.database_survives(), "the database must survive an unregister")
        self.assertTrue(os.path.isfile(os.path.join(self.data, "settings.json")))
        self.assertIn("kept", out.stdout)
        self.assertIn("--purge", out.stdout, "it says how to delete the data too")
        self.assertIn("pkill", self.shim_calls(), "the worker stop went through the shim")
        with open(os.path.join(self.home, ".claude", "plugins", "installed_plugins.json"), encoding="utf-8") as f:
            self.assertNotIn("claude-mnemonic@claude-mnemonic", json.load(f)["plugins"], "the registration is still removed")
        self.assertFalse(os.path.exists(os.path.join(self.home, ".claude", "plugins", "cache", "claude-mnemonic")))

    def test_unregister_deletes_the_data_only_with_purge(self):
        out = self.run_script("unregister-plugin.sh", "--purge")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertFalse(os.path.exists(self.data))
        self.assertIn("removed", out.stdout)

    def test_unregister_refuses_an_unknown_option_before_it_does_anything(self):
        out = self.run_script("unregister-plugin.sh", "--wipe")
        self.assertEqual(out.returncode, 2)
        self.assertIn("Usage", out.stderr)
        self.assertEqual(self.shim_calls(), "", "no process was stopped")
        self.assertTrue(self.database_survives())

    # ---- uninstall.sh

    def test_uninstall_keeps_the_data_by_default(self):
        out = self.run_script("uninstall.sh")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertTrue(self.database_survives())
        self.assertIn("--purge", out.stdout)

    def test_uninstall_deletes_the_data_only_with_purge(self):
        self.assertEqual(self.run_script("uninstall.sh", "--purge").returncode, 0)
        self.assertFalse(os.path.exists(self.data))

    def test_uninstall_keep_data_still_keeps_and_wins_over_purge_in_either_order(self):
        for args in (("--keep-data",), ("--purge", "--keep-data"), ("--keep-data", "--purge")):
            with self.subTest(args=args):
                self.setUp()
                self.assertEqual(self.run_script("uninstall.sh", *args).returncode, 0)
                self.assertTrue(self.database_survives())

    def test_uninstall_a_misspelt_option_keeps_the_data(self):
        self.assertEqual(self.run_script("uninstall.sh", "--purg").returncode, 0)
        self.assertTrue(self.database_survives(), "when in doubt the data stays")

    # ---- install.sh --uninstall

    def test_installer_uninstall_keeps_the_data_by_default(self):
        out = self.run_script("install.sh", "--uninstall")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertTrue(self.database_survives())
        self.assertIn("--purge", out.stdout + out.stderr)

    def test_installer_uninstall_deletes_the_data_only_with_purge(self):
        self.assertEqual(self.run_script("install.sh", "--uninstall", "--purge").returncode, 0)
        self.assertFalse(os.path.exists(self.data))

    def test_installer_uninstall_keep_data_still_keeps(self):
        self.assertEqual(self.run_script("install.sh", "--uninstall", "--keep-data").returncode, 0)
        self.assertTrue(self.database_survives())


class Guards(unittest.TestCase):
    """Checks on the text of the scripts, for the ones that cannot be run here (PowerShell) and as a net for new ones."""

    def test_every_bash_script_that_deletes_the_data_directory_does_so_only_behind_a_purge_decision(self):
        for name in sorted(os.listdir(HERE)):
            if not name.endswith(".sh"):
                continue
            lines = read(script(name)).split("\n")
            for i, line in enumerate(lines):
                if re.search(r'rm -rf "\$DATA_DIR"', line):
                    context = "\n".join(lines[max(0, i - 12): i])
                    self.assertRegex(context, r"PURGE|KEEP_DATA", f"{name}:{i + 1} deletes the data directory without a purge decision")

    def test_the_scripts_stop_processes_only_through_commands_the_tests_can_shim(self):
        # `kill` as a shell builtin would bypass the PATH shim. These scripts use it only as `xargs kill`, which runs the
        # program from PATH; the data-safety tests rely on that to never signal a real process.
        for name in ("unregister-plugin.sh", "uninstall.sh"):
            for n, line in enumerate(read(script(name)).split("\n"), 1):
                code = line.split("#", 1)[0]
                for match in re.finditer(r"(?<![\w-])kill\b", code):
                    before = code[: match.start()]
                    self.assertRegex(before, r"xargs( -r)? $", f"{name}:{n} calls kill directly: {line.strip()}")

    def test_the_powershell_uninstallers_keep_the_data_unless_purge_is_given(self):
        uninstall = read(script("uninstall.ps1"))
        self.assertIn("$RemoveData = $Purge -and -not $KeepData", uninstall)
        self.assertNotRegex(uninstall, r"if \(\$KeepData\) \{\s*Write-Warn \"Keeping", "the old default (purge unless -KeepData) is gone")
        install = read(script("install.ps1"))
        self.assertIn("[switch]$Purge", install)
        self.assertIn("Uninstall-ClaudeMnemonic -Purge:$Purge", install, "the switch reaches the function (it used to be unreachable)")
        self.assertRegex(install, r"if \(-not \$Purge\) \{\s*Write-Warn \"Keeping data directory")

    def test_the_headers_say_that_the_data_is_kept_by_default(self):
        for name in ("uninstall.sh", "uninstall.ps1", "unregister-plugin.sh"):
            head = "\n".join(read(script(name)).split("\n")[:12])
            self.assertRegex(head, r"(?i)kept|keep", name)
            self.assertNotRegex(head, r"Remove everything including data \(default\)", name)


if __name__ == "__main__":
    unittest.main()
