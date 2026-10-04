#!/usr/bin/env python3
"""Tests for install-desktop.py. Run: python3 -m unittest scripts/test_install_desktop.py -v"""
import importlib.util
import io
import json
import os
import stat
import tempfile
import unittest

_spec = importlib.util.spec_from_file_location("install_desktop", os.path.join(os.path.dirname(os.path.abspath(__file__)), "install-desktop.py"))
inst = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(inst)

ENTRY = {"command": "/home/u/.claude-mnemonic/bin/mcp-server", "args": []}
NAME = "claude-mnemonic"

TWO = """{
  "mcpServers": {
    "joplin": {
      "command": "uvx",
      "args": ["joplin-mcp"]
    },
    "azure": {
      "command": "npx",
      "args": ["-y", "@azure/mcp", "--note", "has } and { and \\" quote"],
      "env": {"TOKEN": "s3cr3t"}
    }
  },
  "preferences": {
    "sidebarMode": "chat"
  }
}
"""
FOUR = TWO.replace("  ", "    ")
TABS = TWO.replace("  ", "\t")
CRLF = TWO.replace("\n", "\r\n")
COMPACT = '{"mcpServers":{"joplin":{"command":"uvx","args":["joplin-mcp"]}},"preferences":{"sidebarMode":"chat"}}'

FORMATS = {"two-space": TWO, "four-space": FOUR, "tabs": TABS, "crlf": CRLF, "compact": COMPACT}


def tmpfile(testcase, content=None, name="claude_desktop_config.json"):
    d = tempfile.mkdtemp()
    testcase.addCleanup(lambda: __import__("shutil").rmtree(d, ignore_errors=True))
    path = os.path.join(d, name)
    if content is not None:
        with open(path, "w", encoding="utf-8", newline="") as f:
            f.write(content)
    return path


def read(path):
    with open(path, encoding="utf-8", newline="") as f:
        return f.read()


class SetAndRemove(unittest.TestCase):
    def test_install_adds_exactly_one_entry_and_nothing_else(self):
        for label, text in FORMATS.items():
            with self.subTest(label):
                out = inst.set_server(text, NAME, ENTRY)
                want = json.loads(text)
                want["mcpServers"][NAME] = ENTRY
                self.assertEqual(json.loads(out), want)

    def test_every_other_byte_is_untouched_exact_round_trip(self):
        for label, text in FORMATS.items():
            with self.subTest(label):
                self.assertEqual(inst.remove_server(inst.set_server(text, NAME, ENTRY), NAME), text)

    def test_the_other_servers_text_is_preserved_verbatim(self):
        out = inst.set_server(TWO, NAME, ENTRY)
        # the other servers' blocks, including odd characters and inline arrays, appear unchanged
        self.assertIn('"args": ["-y", "@azure/mcp", "--note", "has } and { and \\" quote"],', out)
        self.assertIn('"env": {"TOKEN": "s3cr3t"}', out)
        self.assertTrue(out.endswith("}\n"))

    def test_new_entry_matches_the_files_indentation_and_line_endings(self):
        self.assertIn('\n    "claude-mnemonic": {\n      "command"', inst.set_server(TWO, NAME, ENTRY))
        self.assertIn('\n        "claude-mnemonic": {\n            "command"', inst.set_server(FOUR, NAME, ENTRY))
        self.assertIn('\n\t\t"claude-mnemonic": {\n\t\t\t"command"', inst.set_server(TABS, NAME, ENTRY))
        crlf = inst.set_server(CRLF, NAME, ENTRY)
        self.assertNotIn("\n\n", crlf.replace("\r\n", ""))
        self.assertEqual(crlf.count("\n"), crlf.count("\r\n"), "no bare LF is introduced into a CRLF file")

    def test_update_in_place_changes_only_our_entry_in_every_position(self):
        new = {"command": "/new/path", "args": ["--project", "p_111111"]}
        for position in ("first", "middle", "last", "only"):
            with self.subTest(position):
                names = {"first": [NAME, "a", "b"], "middle": ["a", NAME, "b"], "last": ["a", "b", NAME], "only": [NAME]}[position]
                servers = {n: ({"command": "old"} if n == NAME else {"command": n, "args": ["x"]}) for n in names}
                text = json.dumps({"mcpServers": servers, "other": 1}, indent=2) + "\n"
                out = inst.set_server(text, NAME, new)
                want = json.loads(text)
                want["mcpServers"][NAME] = new
                self.assertEqual(json.loads(out), want)
                self.assertEqual(list(json.loads(out)["mcpServers"]), names, "order is kept")

    def test_remove_in_every_position_keeps_valid_json_and_the_rest(self):
        for position in ("first", "middle", "last", "only"):
            with self.subTest(position):
                names = {"first": [NAME, "a", "b"], "middle": ["a", NAME, "b"], "last": ["a", "b", NAME], "only": [NAME]}[position]
                servers = {n: {"command": n, "args": ["x"]} for n in names}
                text = json.dumps({"mcpServers": servers, "other": 1}, indent=2) + "\n"
                out = inst.remove_server(text, NAME)
                want = json.loads(text)
                del want["mcpServers"][NAME]
                self.assertEqual(json.loads(out), want)

    def test_missing_mcpservers_key_is_added(self):
        text = '{\n  "preferences": {"a": 1}\n}\n'
        out = inst.set_server(text, NAME, ENTRY)
        self.assertEqual(json.loads(out), {"mcpServers": {NAME: ENTRY}, "preferences": {"a": 1}})
        self.assertIn('"preferences": {"a": 1}', out)

    def test_empty_objects(self):
        for text in ("{}", "{ }", "{\n}\n", '{"mcpServers": {}}', '{\n  "mcpServers": {}\n}\n'):
            with self.subTest(text):
                out = inst.set_server(text, NAME, ENTRY)
                self.assertEqual(json.loads(out), {"mcpServers": {NAME: ENTRY}})
                # Uninstalling leaves an empty mcpServers: it cannot know whether the user had one before.
                self.assertEqual(json.loads(inst.remove_server(out, NAME)), {"mcpServers": {}})

    def test_only_the_top_level_mcpservers_is_edited(self):
        text = '{\n  "nested": {\n    "mcpServers": {"decoy": {}}\n  },\n  "mcpServers": {"a": {}}\n}\n'
        out = json.loads(inst.set_server(text, NAME, ENTRY))
        self.assertEqual(out["nested"], {"mcpServers": {"decoy": {}}})
        self.assertEqual(set(out["mcpServers"]), {"a", NAME})

    def test_a_similarly_named_server_is_never_touched(self):
        text = json.dumps({"mcpServers": {"claude-mnemonic-extra": {"command": "x"}, "my-claude-mnemonic": {"command": "y"}}}, indent=2)
        out = json.loads(inst.remove_server(inst.set_server(text, NAME, ENTRY), NAME))
        self.assertEqual(out, json.loads(text))

    def test_strings_with_braces_quotes_and_unicode_do_not_confuse_the_scanner(self):
        tricky = {"mcpServers": {"x": {"command": "a\"}{\\b", "args": ["é", "日本", "{[", "]}"]}}, "k": "v"}
        text = json.dumps(tricky, indent=2, ensure_ascii=False) + "\n"
        out = inst.set_server(text, NAME, ENTRY)
        self.assertEqual(json.loads(out)["mcpServers"]["x"], tricky["mcpServers"]["x"])
        self.assertIn("日本", out, "non-ASCII text is preserved, not escaped")
        self.assertEqual(inst.remove_server(out, NAME), text)

    def test_not_an_object_is_rejected(self):
        for text in ("[]", '"x"', "42", "null"):
            with self.subTest(text), self.assertRaises(inst.ConfigError):
                inst.set_server(text, NAME, ENTRY)
        with self.assertRaises(inst.ConfigError):
            inst.set_server('{"mcpServers": []}', NAME, ENTRY)

    def test_remove_when_absent_returns_the_text_unchanged(self):
        self.assertEqual(inst.remove_server(TWO, NAME), TWO)
        self.assertEqual(inst.remove_server('{"a": 1}', NAME), '{"a": 1}')


class Verify(unittest.TestCase):
    def test_a_wrong_edit_is_refused(self):
        original = '{"mcpServers": {"a": {"command": "x"}}}'
        with self.assertRaises(inst.ConfigError):
            inst.verify(original, '{"mcpServers": {"a": {"command": "CHANGED"}}}', NAME, ENTRY)
        with self.assertRaises(inst.ConfigError):
            inst.verify(original, '{"mcpServers": {"a"', NAME, ENTRY)
        with self.assertRaises(inst.ConfigError):
            inst.verify(original, '{"mcpServers": {"a": {"command": "x"}}, "extra": 1}', NAME, ENTRY)


class CommandLine(unittest.TestCase):
    def setUp(self):
        self.binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")

    def run_cli(self, *argv, config):
        out, err = io.StringIO(), io.StringIO()
        import contextlib
        # --no-copy: these tests must never overwrite the real clipboard
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = inst.main(["--config", config, "--binary", self.binary, "--no-copy", *argv])
        return code, out.getvalue(), err.getvalue()

    def entry(self, *args):
        return {"command": self.binary, "args": list(args)}

    def test_install_into_an_existing_config_backs_up_and_adds(self):
        cfg = tmpfile(self, TWO)
        code, out, _ = self.run_cli(config=cfg)
        self.assertEqual(code, 0)
        self.assertIn("Added 'claude-mnemonic'", out)
        self.assertIn("Quit Claude Desktop", out)
        want = json.loads(TWO)
        want["mcpServers"][NAME] = self.entry()
        self.assertEqual(json.loads(read(cfg)), want)
        backups = [f for f in os.listdir(os.path.dirname(cfg)) if ".bak-" in f]
        self.assertEqual(len(backups), 1)
        self.assertEqual(read(os.path.join(os.path.dirname(cfg), backups[0])), TWO, "the backup is the original, byte for byte")

    def test_rapid_changes_never_overwrite_the_first_backup(self):
        cfg = tmpfile(self, TWO)
        self.run_cli(config=cfg)                       # backup 1: the original
        self.run_cli("--project", "p_111111", config=cfg)   # backup 2, within the same second
        self.run_cli("uninstall", config=cfg)          # backup 3
        d = os.path.dirname(cfg)
        backups = sorted(f for f in os.listdir(d) if ".bak-" in f)
        self.assertEqual(len(backups), 3, "each change keeps its own backup")
        contents = [read(os.path.join(d, b)) for b in backups]
        self.assertIn(TWO, contents, "the original configuration is still among the backups")

    def test_install_creates_a_missing_config(self):
        cfg = os.path.join(tempfile.mkdtemp(), "Claude", "claude_desktop_config.json")
        self.addCleanup(lambda: __import__("shutil").rmtree(os.path.dirname(os.path.dirname(cfg)), ignore_errors=True))
        code, out, _ = self.run_cli(config=cfg)
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(read(cfg)), {"mcpServers": {NAME: self.entry()}})
        self.assertTrue(read(cfg).endswith("\n"))
        self.assertNotIn("Backup:", out, "there was nothing to back up")

    def test_install_says_where_the_dashboard_is(self):
        import unittest.mock as mock
        cfg = tmpfile(self, TWO)
        with mock.patch.dict(os.environ, {"CLAUDE_MNEMONIC_WORKER_PORT": "4100"}):
            code, out, _ = self.run_cli(config=cfg)
            self.assertEqual(code, 0)
            self.assertIn("The memory dashboard is at http://localhost:4100", out)
            self.assertIn("/memory-dashboard", out)
            again = self.run_cli(config=cfg)[1]
            self.assertIn("Already up to date", again)
            self.assertIn("http://localhost:4100", again, "also when nothing had to change")
            self.assertNotIn("dashboard", self.run_cli("--dry-run", config=cfg)[1], "a dry run says nothing about it")

    def test_uninstall_does_not_mention_the_dashboard(self):
        cfg = tmpfile(self, TWO)
        self.run_cli(config=cfg)
        self.assertNotIn("dashboard", self.run_cli("uninstall", config=cfg)[1])

    def test_worker_port_environment_then_settings_then_default(self):
        home = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(home, ignore_errors=True))
        self.assertEqual(inst.worker_port({}, home), 37777)
        os.makedirs(os.path.join(home, ".claude-mnemonic"))
        settings = os.path.join(home, ".claude-mnemonic", "settings.json")
        with open(settings, "w") as f:
            f.write('{"CLAUDE_MNEMONIC_WORKER_PORT": 4200}')
        self.assertEqual(inst.worker_port({}, home), 4200)
        self.assertEqual(inst.worker_port({"CLAUDE_MNEMONIC_WORKER_PORT": "4300"}, home), 4300, "the environment wins")
        for bad in ("abc", "0", "-5", ""):
            self.assertEqual(inst.worker_port({"CLAUDE_MNEMONIC_WORKER_PORT": bad}, home), 4200, bad)
        with open(settings, "w") as f:
            f.write("not json")
        self.assertEqual(inst.worker_port({}, home), 37777, "an unreadable settings file means the default")
        with open(settings, "w") as f:
            f.write('{"CLAUDE_MNEMONIC_WORKER_PORT": "oops"}')
        self.assertEqual(inst.worker_port({}, home), 37777)

    def test_install_into_an_empty_file(self):
        cfg = tmpfile(self, "   \n")
        self.assertEqual(self.run_cli(config=cfg)[0], 0)
        self.assertEqual(json.loads(read(cfg)), {"mcpServers": {NAME: self.entry()}})

    def test_second_run_changes_nothing_and_makes_no_backup(self):
        cfg = tmpfile(self, TWO)
        self.run_cli(config=cfg)
        after_first = read(cfg)
        code, out, _ = self.run_cli(config=cfg)
        self.assertEqual(code, 0)
        self.assertIn("Already up to date", out)
        self.assertEqual(read(cfg), after_first)
        self.assertEqual(len([f for f in os.listdir(os.path.dirname(cfg)) if ".bak-" in f]), 1)

    def test_a_changed_setting_updates_in_place(self):
        cfg = tmpfile(self, TWO)
        self.run_cli(config=cfg)
        code, out, _ = self.run_cli("--project", "repo_ab12cd", "--mode", "desktop", config=cfg)
        self.assertEqual(code, 0)
        self.assertIn("Updated 'claude-mnemonic'", out)
        self.assertEqual(json.loads(read(cfg))["mcpServers"][NAME], self.entry("--project", "repo_ab12cd", "--mode", "desktop"))

    def test_auto_mode_adds_no_flag(self):
        cfg = tmpfile(self, TWO)
        self.run_cli("--mode", "auto", config=cfg)
        self.assertEqual(json.loads(read(cfg))["mcpServers"][NAME]["args"], [])

    def test_dry_run_shows_a_diff_and_writes_nothing(self):
        cfg = tmpfile(self, TWO)
        code, out, _ = self.run_cli("--dry-run", config=cfg)
        self.assertEqual(code, 0)
        self.assertIn("Dry run: would write", out)
        self.assertIn('+    "claude-mnemonic": {', out)
        self.assertEqual(read(cfg), TWO)
        self.assertEqual(os.listdir(os.path.dirname(cfg)), [os.path.basename(cfg)], "no backup, no temp file")

    def test_uninstall_removes_only_our_entry(self):
        cfg = tmpfile(self, TWO)
        self.run_cli(config=cfg)
        code, out, _ = self.run_cli("uninstall", config=cfg)
        self.assertEqual(code, 0)
        self.assertIn("Removed 'claude-mnemonic'", out)
        self.assertEqual(read(cfg), TWO, "back to the original text exactly")

    def test_uninstall_when_not_configured_is_a_no_op(self):
        cfg = tmpfile(self, TWO)
        code, out, _ = self.run_cli("uninstall", config=cfg)
        self.assertEqual(code, 0)
        self.assertIn("Nothing to do", out)
        self.assertEqual(read(cfg), TWO)

    def test_status(self):
        cfg = tmpfile(self, TWO)
        self.assertIn("not configured", self.run_cli("status", config=cfg)[1])
        self.run_cli(config=cfg)
        out = self.run_cli("status", config=cfg)[1]
        self.assertIn("configured as 'claude-mnemonic'", out)
        self.assertIn(self.binary, out)
        missing = os.path.join(tempfile.mkdtemp(), "none.json")
        self.assertIn("does not exist yet", self.run_cli("status", config=missing)[1])

    def test_status_warns_when_the_configured_binary_is_gone(self):
        cfg = tmpfile(self, json.dumps({"mcpServers": {NAME: {"command": "/no/such/binary"}}}))
        self.assertIn("that binary does not exist", self.run_cli("status", config=cfg)[1])

    def test_invalid_json_is_refused_without_touching_the_file(self):
        cfg = tmpfile(self, '{"mcpServers": {')
        code, _, err = self.run_cli(config=cfg)
        self.assertEqual(code, 1)
        self.assertIn("not valid JSON", err)
        self.assertEqual(read(cfg), '{"mcpServers": {')
        self.assertEqual(os.listdir(os.path.dirname(cfg)), [os.path.basename(cfg)])

    def test_wrong_shapes_are_refused(self):
        for text in ("[]", '{"mcpServers": "x"}'):
            with self.subTest(text):
                cfg = tmpfile(self, text)
                code, _, err = self.run_cli(config=cfg)
                self.assertEqual(code, 1)
                self.assertTrue(err.startswith("error:"))
                self.assertEqual(read(cfg), text)

    def test_a_missing_binary_is_refused_unless_forced(self):
        cfg = tmpfile(self, TWO)
        out, err = io.StringIO(), io.StringIO()
        import contextlib
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = inst.main(["--config", cfg, "--binary", "/no/such/mcp-server"])
        self.assertEqual(code, 1)
        self.assertIn("make install", err.getvalue())
        self.assertEqual(read(cfg), TWO)
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = inst.main(["--config", cfg, "--binary", "/no/such/mcp-server", "--force", "--no-copy"])
        self.assertEqual(code, 0)

    def test_a_custom_name_leaves_the_default_entry_alone(self):
        cfg = tmpfile(self, TWO)
        self.run_cli(config=cfg)
        self.run_cli("--name", "mnemonic-pinned", "--project", "p_111111", config=cfg)
        servers = json.loads(read(cfg))["mcpServers"]
        self.assertEqual(servers[NAME], self.entry())
        self.assertEqual(servers["mnemonic-pinned"], self.entry("--project", "p_111111"))

    @unittest.skipIf(os.name == "nt", "POSIX permissions")
    def test_file_permissions_are_preserved(self):
        cfg = tmpfile(self, TWO)
        os.chmod(cfg, 0o640)
        self.run_cli(config=cfg)
        self.assertEqual(stat.S_IMODE(os.stat(cfg).st_mode), 0o640)

    def test_crlf_and_tab_files_survive_a_full_install_and_uninstall(self):
        for label in ("crlf", "tabs", "four-space", "compact"):
            with self.subTest(label):
                cfg = tmpfile(self, FORMATS[label])
                self.run_cli(config=cfg)
                self.run_cli("uninstall", config=cfg)
                self.assertEqual(read(cfg), FORMATS[label])


class Locations(unittest.TestCase):
    def test_default_paths_are_absolute_and_named_right(self):
        self.assertTrue(os.path.isabs(inst.default_config_path()))
        self.assertTrue(inst.default_config_path().endswith("claude_desktop_config.json"))
        self.assertIn(".claude-mnemonic", inst.default_binary_path())
        self.assertTrue(os.path.basename(inst.default_binary_path()).startswith("mcp-server"))


if __name__ == "__main__":
    unittest.main()


class Instructions(unittest.TestCase):
    def test_default_name_is_used_without_a_parenthesis(self):
        text = inst.render_instructions()
        self.assertIn("in the claude-mnemonic connector.", text)
        self.assertIn("project_suggest tool of the claude-mnemonic connector", text)
        self.assertNotIn("{", text, "every placeholder is filled in")

    def test_a_custom_name_is_used_and_still_says_what_it_is(self):
        text = inst.render_instructions("memory")
        self.assertIn('in the "memory" (claude-mnemonic) connector.', text)
        self.assertIn("project_suggest tool of the memory connector", text)
        self.assertNotIn("{", text)

    def test_the_rules_the_server_also_enforces_are_in_it(self):
        text = inst.render_instructions()
        for want in ("in addition to any built-in memory", "Never pick a project for me", "do not save anything",
                     "If two projects share a name, ask me", "Do not use it for general questions", "Tell me which source",
                     "call catch_up", "checkpoint tool", "do not checkpoint", "compacted or summarised"):
            self.assertIn(want, text)

    def test_the_document_and_the_installer_cannot_drift_apart(self):
        doc = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "DESKTOP.md"), encoding="utf-8").read()
        self.assertIn(inst.render_instructions().rstrip("\n"), doc, "DESKTOP.md must contain exactly what the installer prints")

    def test_clipboard_tool_selection_and_fallback(self):
        import unittest.mock as mock
        with mock.patch.object(inst.shutil, "which", side_effect=lambda c: "/usr/bin/" + c if c == "xclip" else None):
            self.assertEqual(inst.clipboard_command(), ["xclip", "-selection", "clipboard"])
        with mock.patch.object(inst.shutil, "which", return_value=None):
            self.assertIsNone(inst.clipboard_command())
            self.assertIsNone(inst.copy_to_clipboard("x"), "no tool means a clear None, not a crash")
        with mock.patch.object(inst.shutil, "which", return_value="/usr/bin/pbcopy"), \
                mock.patch.object(inst.subprocess, "run", side_effect=OSError("boom")):
            self.assertIsNone(inst.copy_to_clipboard("x"), "a failing tool degrades the same way")

    def test_copy_sends_exactly_the_text_to_the_tool(self):
        import unittest.mock as mock
        with mock.patch.object(inst.shutil, "which", side_effect=lambda c: "/usr/bin/pbcopy" if c == "pbcopy" else None), \
                mock.patch.object(inst.subprocess, "run") as run:
            self.assertEqual(inst.copy_to_clipboard("héllo"), "pbcopy")
            self.assertEqual(run.call_args.args[0], ["pbcopy"])
            self.assertEqual(run.call_args.kwargs["input"], "héllo".encode("utf-8"))

    def run_cli(self, *argv, config):
        import contextlib
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = inst.main(["--config", config, "--no-copy", *argv])
        return code, out.getvalue(), err.getvalue()

    def test_instructions_prints_and_never_touches_the_config(self):
        cfg = tmpfile(self, TWO)
        code, out, _ = self.run_cli("instructions", config=cfg)
        self.assertEqual(code, 0)
        self.assertIn("project_suggest", out)
        self.assertEqual(read(cfg), TWO)
        self.assertEqual(os.listdir(os.path.dirname(cfg)), [os.path.basename(cfg)], "no backup, nothing written")

    def test_instructions_copy_reports_what_happened(self):
        import unittest.mock as mock
        cfg = tmpfile(self, TWO)
        with mock.patch.object(inst, "copy_to_clipboard", return_value="pbcopy"):
            self.assertIn("copied to the clipboard with pbcopy", self.run_cli("instructions", "--copy", config=cfg)[1])
        with mock.patch.object(inst, "copy_to_clipboard", return_value=None):
            self.assertIn("could not copy", self.run_cli("instructions", "--copy", config=cfg)[1])

    def test_install_shows_the_instruction_itself_not_just_a_pointer(self):
        binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")
        cfg = tmpfile(self, TWO)
        out = self.run_cli("--binary", binary, config=cfg)[1]
        self.assertIn(inst.render_instructions().rstrip("\n"), out, "the full text is printed")
        self.assertIn("ignores claude-mnemonic", out)
        self.assertIn("Copy the text below.", out, "with --no-copy it says to copy it by hand")
        self.assertIn("instructions --copy", out, "and how to show it again")

    def test_an_already_configured_run_still_shows_it(self):
        binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")
        cfg = tmpfile(self, TWO)
        self.run_cli("--binary", binary, config=cfg)
        out = self.run_cli("--binary", binary, config=cfg)[1]
        self.assertIn("Already up to date", out)
        self.assertIn(inst.render_instructions().rstrip("\n"), out, "this was the gap: nothing was shown on a re-run")

    def test_an_update_shows_it_with_the_new_connector_name(self):
        binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")
        cfg = tmpfile(self, TWO)
        out = self.run_cli("--binary", binary, "--name", "memory", config=cfg)[1]
        self.assertIn(inst.render_instructions("memory").rstrip("\n"), out)
        self.assertIn('"memory" (claude-mnemonic)', out)

    def test_dry_run_uninstall_and_status_do_not_show_it(self):
        binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")
        cfg = tmpfile(self, TWO)
        marker = "ignores claude-mnemonic"
        self.assertNotIn(marker, self.run_cli("--binary", binary, "--dry-run", config=cfg)[1])
        self.run_cli("--binary", binary, config=cfg)
        self.assertNotIn(marker, self.run_cli("--binary", binary, "--dry-run", config=cfg)[1], "not even when already up to date")
        self.assertNotIn(marker, self.run_cli("status", config=cfg)[1])
        self.assertNotIn(marker, self.run_cli("uninstall", config=cfg)[1])

    def test_install_copies_to_the_clipboard_by_default_and_says_so(self):
        import contextlib
        import unittest.mock as mock
        binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")
        cfg = tmpfile(self, TWO)
        out = io.StringIO()
        with mock.patch.object(inst, "copy_to_clipboard", return_value="pbcopy") as copy, contextlib.redirect_stdout(out):
            inst.main(["--config", cfg, "--binary", binary])
        copy.assert_called_once_with(inst.render_instructions())
        self.assertIn("It is on your clipboard (copied with pbcopy)", out.getvalue())
        self.assertIn(inst.render_instructions().rstrip("\n"), out.getvalue(), "it is printed as well, in case pasting is not possible")

    def test_a_missing_clipboard_tool_degrades_to_a_clear_message(self):
        import contextlib
        import unittest.mock as mock
        binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")
        cfg = tmpfile(self, TWO)
        out = io.StringIO()
        with mock.patch.object(inst, "copy_to_clipboard", return_value=None), contextlib.redirect_stdout(out):
            self.assertEqual(inst.main(["--config", cfg, "--binary", binary]), 0, "the install itself still succeeds")
        self.assertIn("No clipboard tool was found: select and copy the text below.", out.getvalue())

    def test_no_copy_never_calls_the_clipboard(self):
        import contextlib
        import unittest.mock as mock
        binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")
        cfg = tmpfile(self, TWO)
        with mock.patch.object(inst, "copy_to_clipboard") as copy, contextlib.redirect_stdout(io.StringIO()):
            inst.main(["--config", cfg, "--binary", binary, "--no-copy"])
        copy.assert_not_called()

    def test_dry_run_and_uninstall_never_call_the_clipboard(self):
        import contextlib
        import unittest.mock as mock
        binary = tmpfile(self, "#!/bin/sh\n", name="mcp-server")
        cfg = tmpfile(self, TWO)
        with mock.patch.object(inst, "copy_to_clipboard") as copy, contextlib.redirect_stdout(io.StringIO()):
            inst.main(["--config", cfg, "--binary", binary, "--dry-run"])
            inst.main(["--config", cfg, "--binary", binary, "--no-copy"])
            inst.main(["--config", cfg, "uninstall"])
            inst.main(["--config", cfg, "status"])
        copy.assert_not_called()
