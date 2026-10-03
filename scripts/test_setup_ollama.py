#!/usr/bin/env python3
"""Tests for setup-ollama.py. Run: python3 -m unittest scripts/test_setup_ollama.py -v"""
import importlib.util
import io
import json
import os
import tempfile
import threading
import unittest
import unittest.mock as mock
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

_spec = importlib.util.spec_from_file_location("setup_ollama", os.path.join(os.path.dirname(os.path.abspath(__file__)), "setup-ollama.py"))
so = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(so)

P = so.PREFIX


class FakeOllama:
    """An Ollama that can be told what to do. Every request is recorded."""

    def __init__(self):
        self.installed = [{"name": "llama3.2:latest"}, {"name": "qllama/bge-small-en-v1.5:latest"}]
        self.loaded = []
        self.pull_lines = None  # None: succeed normally
        self.pull_status = 200
        self.chat_reply = {"message": {"role": "assistant", "content": "ready"}, "done": True}
        self.requests = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def _send(self, obj, status=200):
                raw = json.dumps(obj).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                outer.requests.append(("GET", self.path, None))
                if self.path == "/api/version":
                    self._send({"version": "0.0.1-fake"})
                elif self.path == "/api/tags":
                    self._send({"models": outer.installed})
                elif self.path == "/api/ps":
                    self._send({"models": outer.loaded})
                else:
                    self._send({"error": "not found"}, 404)

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
                outer.requests.append(("POST", self.path, body))
                if self.path == "/api/pull":
                    if outer.pull_status != 200:
                        self._send({"error": "pull refused"}, outer.pull_status)
                        return
                    lines = outer.pull_lines if outer.pull_lines is not None else [
                        {"status": "pulling manifest"},
                        {"status": "pulling abc123", "total": 1000000000, "completed": 100000000},
                        {"status": "pulling abc123", "total": 1000000000, "completed": 600000000},
                        {"status": "pulling abc123", "total": 1000000000, "completed": 1000000000},
                        {"status": "success"},
                    ]
                    if outer.pull_lines is None:
                        outer.installed.append({"name": body["name"] if ":" in body["name"] else body["name"] + ":latest"})
                    raw = ("\n".join(json.dumps(l) for l in lines) + "\n").encode()
                    self.send_response(200)
                    self.send_header("Content-Length", str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)
                elif self.path == "/api/chat":
                    self._send(outer.chat_reply)
                else:
                    self._send({"error": "not found"}, 404)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()

    def paths(self, method=None):
        return [p for m, p, _ in self.requests if method in (None, m)]


class Base(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(self.dir, ignore_errors=True))
        self.settings = os.path.join(self.dir, ".claude-mnemonic", "settings.json")
        self.fake = FakeOllama()
        self.addCleanup(self.fake.close)
        self.lines = []

    def run_cli(self, *argv, stdin_tty=False, answers=()):
        answers = list(answers)
        with mock.patch.object(so.sys.stdin, "isatty", return_value=stdin_tty), \
                mock.patch.object(so, "ask", side_effect=lambda prompt: answers.pop(0)):
            code = so.main(["--settings", self.settings, "--url", self.fake.url, *argv], out=self.lines.append)
        return code

    def text(self):
        return "\n".join(self.lines)

    def saved(self):
        with open(self.settings, encoding="utf-8") as f:
            return json.load(f)

    def write(self, obj):
        os.makedirs(os.path.dirname(self.settings), exist_ok=True)
        with open(self.settings, "w", encoding="utf-8") as f:
            json.dump(obj, f)

    def backups(self):
        d = os.path.dirname(self.settings)
        return sorted(f for f in os.listdir(d) if ".bak-" in f) if os.path.isdir(d) else []


class Helpers(Base):
    def test_normalize_url_and_precedence(self):
        self.assertEqual(so.normalize_url(""), so.DEFAULT_URL)
        self.assertEqual(so.normalize_url("box:11434/"), "http://box:11434")
        self.assertEqual(so.resolve_url("a:1", {P + "OLLAMA_URL": "b:2"}, {"OLLAMA_HOST": "c:3"}), "http://a:1", "--url first")
        self.assertEqual(so.resolve_url(None, {P + "OLLAMA_URL": "b:2"}, {"OLLAMA_HOST": "c:3"}), "http://b:2", "then the setting, like the worker")
        self.assertEqual(so.resolve_url(None, {}, {"OLLAMA_HOST": "c:3"}), "http://c:3")
        self.assertEqual(so.resolve_url(None, {}, {}), so.DEFAULT_URL)

    def test_has_model_treats_no_tag_as_latest(self):
        inst = [{"name": "gemma3:12b"}, {"name": "llama3.2:latest"}]
        self.assertTrue(so.has_model(inst, "gemma3:12b"))
        self.assertTrue(so.has_model(inst, "llama3.2"))
        self.assertFalse(so.has_model(inst, "gemma3"))
        self.assertFalse(so.has_model(inst, ""))

    def test_parse_tasks(self):
        self.assertEqual(so.parse_tasks(""), [])
        self.assertEqual(so.parse_tasks("verify, summary,verify"), ["verify", "summary"])
        with self.assertRaises(so.SetupError):
            so.parse_tasks("summary,magic")

    def test_models_file_is_valid_and_recommends_nothing_yet(self):
        models = so.load_models()
        self.assertTrue(models)
        names = [m["name"] for m in models]
        self.assertEqual(len(names), len(set(names)))
        for m in models:
            self.assertTrue({"name", "size_gb", "license", "status", "notes"} <= set(m), m)
        self.assertNotIn("recommended", {m["status"] for m in models}, "nothing is recommended until it passes the comparison (#26)")
        self.assertNotIn("qwen", " ".join(names), "Qwen models are deliberately not offered")

    def test_aliases(self):
        models = so.load_models()
        m = so.known(models, "llama3.2:latest")
        self.assertEqual(m["name"], "llama3.2:3b")
        self.assertEqual(so.installed_as(m, [{"name": "llama3.2:latest"}]), "llama3.2:latest")
        self.assertIsNone(so.installed_as(m, [{"name": "gemma3:12b"}]))

    def test_plan_changes_only_lists_what_differs(self):
        cur = {P + "OLLAMA_MODEL": "gemma3:12b", "OTHER": 1}
        self.assertEqual(so.plan_changes(cur, "gemma3:12b", []), {})
        ch = so.plan_changes(cur, "granite3.3:8b", ["verify"], "http://x:1")
        self.assertEqual(ch, {P + "OLLAMA_MODEL": ("gemma3:12b", "granite3.3:8b"), P + "OLLAMA_URL": (None, "http://x:1"),
                              P + "LLM_BACKEND_VERIFY": (None, "ollama")})

    def test_plan_disable_only_touches_backend_keys(self):
        cur = {P + "LLM_BACKEND_SUMMARY": "ollama", P + "OLLAMA_MODEL": "m", P + "LLM_BACKEND_VERIFY": "claude"}
        self.assertEqual(set(so.plan_disable(cur)), {P + "LLM_BACKEND_SUMMARY", P + "LLM_BACKEND_VERIFY"})
        out = so.apply_changes(cur, so.plan_disable(cur))
        self.assertEqual(out, {P + "OLLAMA_MODEL": "m"})


class Contract(unittest.TestCase):
    def test_every_key_the_script_writes_is_one_the_worker_reads(self):
        config_go = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "internal", "config", "config.go"), encoding="utf-8").read()
        written = [P + "OLLAMA_MODEL", P + "OLLAMA_URL"] + [P + "LLM_BACKEND_" + t.upper() for t in so.TASKS]
        for key in written:
            self.assertIn(f'"{key}"', config_go, f"{key} is written by setup-ollama.py but not read by the worker")

    def test_the_tasks_match_the_workers(self):
        sdk = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "internal", "worker", "sdk", "backends.go"), encoding="utf-8").read()
        for task in so.TASKS:
            self.assertIn(f'Task = "{task}"', sdk, f"the worker has no {task!r} task")


class OllamaCalls(Base):
    def test_state_lists_installed_and_loaded(self):
        self.fake.loaded = [{"name": "llama3.2:latest"}]
        st = so.get_state(self.fake.url)
        self.assertEqual(st["version"], "0.0.1-fake")
        self.assertEqual([m["name"] for m in st["installed"]], ["llama3.2:latest", "qllama/bge-small-en-v1.5:latest"])
        self.assertEqual([m["name"] for m in st["loaded"]], ["llama3.2:latest"])

    def test_unreachable_is_a_setup_error_with_the_address(self):
        url = self.fake.url
        self.fake.close()
        with self.assertRaises(so.SetupError) as cm:
            so.get_state(url)
        self.assertIn("not reachable", str(cm.exception))
        self.assertIn(url, str(cm.exception))

    def test_pull_reports_progress_and_succeeds(self):
        seen = []
        so.pull(self.fake.url, "gemma3:12b", lambda s, c, t: seen.append((s, c, t)))
        self.assertIn(("pulling abc123", 600000000, 1000000000), seen)
        self.assertEqual(seen[-1][0], "success")
        self.assertEqual(self.fake.requests[-1][2], {"name": "gemma3:12b", "stream": True})

    def test_pull_failures(self):
        self.fake.pull_lines = [{"status": "pulling manifest"}, {"error": "model not found"}]
        with self.assertRaises(so.SetupError) as cm:
            so.pull(self.fake.url, "nope:1b")
        self.assertIn("model not found", str(cm.exception))

        self.fake.pull_lines = [{"status": "pulling manifest"}]
        with self.assertRaises(so.SetupError) as cm:
            so.pull(self.fake.url, "x:1b")
        self.assertIn("without success", str(cm.exception))

        self.fake.pull_lines, self.fake.pull_status = None, 500
        with self.assertRaises(so.SetupError):
            so.pull(self.fake.url, "x:1b")

    def test_smoke_test(self):
        seconds, answer = so.smoke_test(self.fake.url, "llama3.2:latest")
        self.assertEqual(answer, "ready")
        self.assertGreaterEqual(seconds, 0)
        body = self.fake.requests[-1][2]
        self.assertEqual(body["model"], "llama3.2:latest")
        self.assertFalse(body["stream"])

    def test_smoke_test_empty_answer_and_error(self):
        self.fake.chat_reply = {"message": {"content": "  "}}
        with self.assertRaises(so.SetupError):
            so.smoke_test(self.fake.url, "m")
        self.fake.chat_reply = {"error": "model requires more memory"}
        with self.assertRaises(so.SetupError):
            so.smoke_test(self.fake.url, "m")  # no message and no content: an empty answer

    def test_progress_printer_without_a_terminal_prints_per_ten_percent(self):
        out = io.StringIO()
        pr = so.ProgressPrinter(out, tty=False)
        for done in range(0, 101, 1):
            pr("pulling abc", done * 10_000_000, 1_000_000_000)
        lines = [l for l in out.getvalue().splitlines() if l.strip()]
        self.assertLessEqual(len(lines), 12, lines)
        self.assertGreaterEqual(len(lines), 9)
        self.assertIn("100%", lines[-1])


class Settings(Base):
    def test_missing_empty_and_unreadable_files(self):
        self.assertEqual(so.read_settings(self.settings), {})
        self.write({})
        open(self.settings, "w").close()
        self.assertEqual(so.read_settings(self.settings), {})
        with open(self.settings, "w") as f:
            f.write("{not json")
        with self.assertRaises(so.SetupError):
            so.read_settings(self.settings)
        with open(self.settings, "w") as f:
            f.write("[1, 2]")
        with self.assertRaises(so.SetupError):
            so.read_settings(self.settings)

    def test_write_backs_up_keeps_other_keys_and_is_private(self):
        self.write({"CLAUDE_MNEMONIC_WORKER_PORT": 38000, "SOMETHING_ELSE": {"x": 1}})
        backup = so.write_settings(self.settings, {"CLAUDE_MNEMONIC_WORKER_PORT": 38000, "SOMETHING_ELSE": {"x": 1}, P + "OLLAMA_MODEL": "m"})
        self.assertTrue(os.path.exists(backup))
        with open(backup) as f:
            self.assertNotIn("OLLAMA_MODEL", f.read())
        self.assertEqual(self.saved()["SOMETHING_ELSE"], {"x": 1})
        self.assertEqual(oct(os.stat(self.settings).st_mode & 0o777), "0o600")
        self.assertFalse(os.path.exists(self.settings + ".tmp"))

    def test_write_creates_the_directory_and_needs_no_backup_for_a_new_file(self):
        self.assertIsNone(so.write_settings(self.settings, {"a": 1}))
        self.assertEqual(self.saved(), {"a": 1})

    def test_two_writes_in_one_second_do_not_overwrite_a_backup(self):
        self.write({"a": 1})
        first = so.write_settings(self.settings, {"a": 2})
        second = so.write_settings(self.settings, {"a": 3})
        self.assertNotEqual(first, second)
        self.assertEqual(len(self.backups()), 2)


class Setup(Base):
    def test_installed_model_non_interactive_saves_and_switches_only_the_named_tasks(self):
        self.write({"CLAUDE_MNEMONIC_WORKER_PORT": 38000})
        code = self.run_cli("--model", "llama3.2:latest", "--task", "verify")
        self.assertEqual(code, 0, self.text())
        s = self.saved()
        self.assertEqual(s[P + "OLLAMA_MODEL"], "llama3.2:latest")
        self.assertEqual(s[P + "LLM_BACKEND_VERIFY"], "ollama")
        self.assertNotIn(P + "LLM_BACKEND_SUMMARY", s, "an unnamed task stays on the Claude CLI")
        self.assertEqual(s["CLAUDE_MNEMONIC_WORKER_PORT"], 38000, "other settings are kept")
        self.assertEqual(s[P + "OLLAMA_URL"], self.fake.url, "a URL given with --url is saved")
        self.assertEqual(len(self.backups()), 1)
        self.assertNotIn("/api/pull", self.fake.paths("POST"), "an installed model is not downloaded")
        self.assertIn("/api/chat", self.fake.paths("POST"), "it was tested")

    def test_the_warning_does_not_call_any_task_safe_and_names_the_deleting_one(self):
        self.assertEqual(self.run_cli("--model", "llama3.2:latest", "--task", "verify", "--skip-test"), 0)
        text = self.text()
        self.assertIn("permanently deletes", text)
        self.assertNotIn("safest", text)

    def test_no_task_switches_nothing_and_says_so(self):
        self.assertEqual(self.run_cli("--model", "llama3.2:latest"), 0)
        s = self.saved()
        self.assertEqual(s[P + "OLLAMA_MODEL"], "llama3.2:latest")
        self.assertFalse([k for k in s if "LLM_BACKEND" in k])
        self.assertIn("No task was switched over", self.text())

    def test_it_is_idempotent(self):
        self.assertEqual(self.run_cli("--model", "llama3.2:latest", "--task", "verify"), 0)
        before = open(self.settings).read()
        n = len(self.backups())
        self.lines.clear()
        self.assertEqual(self.run_cli("--model", "llama3.2:latest", "--task", "verify"), 0)
        self.assertIn("nothing to change", self.text())
        self.assertEqual(open(self.settings).read(), before)
        self.assertEqual(len(self.backups()), n, "no new backup when nothing changes")

    def test_missing_model_needs_yes_when_not_interactive(self):
        code = self.run_cli("--model", "gemma3:12b")
        self.assertEqual(code, 2)
        self.assertIn("--yes", self.text())
        self.assertIn("ollama pull gemma3:12b", self.text())
        self.assertNotIn("/api/pull", self.fake.paths("POST"))
        self.assertFalse(os.path.exists(self.settings), "nothing was written")

    def test_yes_downloads_tests_and_saves(self):
        code = self.run_cli("--model", "gemma3:12b", "--task", "summary", "--yes")
        self.assertEqual(code, 0, self.text())
        self.assertIn("/api/pull", self.fake.paths("POST"))
        self.assertLess(self.fake.paths("POST").index("/api/pull"), self.fake.paths("POST").index("/api/chat"), "download first, then the test")
        self.assertEqual(self.saved()[P + "LLM_BACKEND_SUMMARY"], "ollama")
        self.assertIn("clearly worse than Haiku", self.text(), "moving a task comes with the quality warning")

    def test_no_pull_never_downloads(self):
        self.assertEqual(self.run_cli("--model", "gemma3:12b", "--no-pull", "--yes"), 2)
        self.assertNotIn("/api/pull", self.fake.paths("POST"))
        self.assertFalse(os.path.exists(self.settings))

    def test_failed_pull_changes_nothing(self):
        self.fake.pull_lines = [{"error": "disk full"}]
        self.write({"a": 1})
        before = open(self.settings).read()
        self.assertEqual(self.run_cli("--model", "gemma3:12b", "--yes"), 1)
        self.assertIn("disk full", self.text())
        self.assertIn("Nothing was changed", self.text())
        self.assertEqual(open(self.settings).read(), before)
        self.assertEqual(self.backups(), [])

    def test_a_model_that_cannot_answer_changes_nothing(self):
        self.fake.chat_reply = {"message": {"content": ""}}
        self.assertEqual(self.run_cli("--model", "llama3.2:latest", "--task", "verify"), 1)
        self.assertFalse(os.path.exists(self.settings))

    def test_skip_test(self):
        self.assertEqual(self.run_cli("--model", "llama3.2:latest", "--skip-test"), 0)
        self.assertNotIn("/api/chat", self.fake.paths("POST"))

    def test_dry_run_changes_and_downloads_nothing(self):
        code = self.run_cli("--model", "gemma3:12b", "--task", "verify", "--dry-run")
        self.assertEqual(code, 0)
        self.assertIn("would download gemma3:12b", self.text())
        self.assertIn("Dry run: nothing was written", self.text())
        self.assertEqual(self.fake.paths("POST"), [])
        self.assertFalse(os.path.exists(self.settings))

    def test_unknown_task_is_refused_before_anything_happens(self):
        self.assertEqual(self.run_cli("--model", "llama3.2:latest", "--task", "magic"), 1)
        self.assertFalse(os.path.exists(self.settings))

    def test_a_model_we_have_not_tried_is_noted_and_a_poor_one_is_warned_about(self):
        self.fake.installed.append({"name": "mystery:7b"})
        self.assertEqual(self.run_cli("--model", "mystery:7b", "--skip-test"), 0)
        self.assertIn("not one of the models we have tried", self.text())
        self.lines.clear()
        self.assertEqual(self.run_cli("--model", "llama3.2:latest", "--skip-test"), 0)
        self.assertIn("Warning", self.text(), "llama3.2 is known to be poor at summaries")
        self.lines.clear()
        self.fake.installed.append({"name": "llama3.1:8b"})
        self.assertEqual(self.run_cli("--model", "llama3.1:8b", "--skip-test"), 0)
        self.assertIn("Warning", self.text(), "a weak model is warned about too")
        self.lines.clear()
        self.fake.installed.append({"name": "gemma3:12b"})
        self.assertEqual(self.run_cli("--model", "gemma3:12b", "--skip-test"), 0)
        self.assertNotIn("Warning", self.text(), "a model that is merely not good enough yet gets the task warning, not this one")

    def test_corrupt_settings_are_never_overwritten(self):
        os.makedirs(os.path.dirname(self.settings))
        with open(self.settings, "w") as f:
            f.write("{oops")
        self.assertEqual(self.run_cli("--model", "llama3.2:latest"), 1)
        self.assertEqual(open(self.settings).read(), "{oops")

    def test_ollama_down_changes_nothing_and_says_how_to_fix_it(self):
        self.fake.close()
        self.assertEqual(self.run_cli("--model", "llama3.2:latest"), 1)
        self.assertIn("ollama.com", self.text())
        self.assertFalse(os.path.exists(self.settings))

    def test_no_model_and_no_terminal_lists_the_choices_and_asks_for_a_decision(self):
        self.assertEqual(self.run_cli(), 2)
        self.assertIn("--model", self.text())
        self.assertIn("gemma3:12b", self.text())
        self.assertFalse(os.path.exists(self.settings))


class Interactive(Base):
    def test_choose_by_number_download_after_confirming_and_pick_tasks(self):
        # models: 1 gemma3:12b, 2 granite3.3:8b ...
        code = self.run_cli(stdin_tty=True, answers=["1", "y", "verify"])
        self.assertEqual(code, 0, self.text())
        s = self.saved()
        self.assertEqual(s[P + "OLLAMA_MODEL"], "gemma3:12b")
        self.assertEqual(s[P + "LLM_BACKEND_VERIFY"], "ollama")
        self.assertIn("/api/pull", self.fake.paths("POST"))

    def test_declining_the_download_changes_nothing(self):
        code = self.run_cli(stdin_tty=True, answers=["1", "n", ""])
        self.assertEqual(code, 0)
        self.assertNotIn("/api/pull", self.fake.paths("POST"))
        self.assertFalse(os.path.exists(self.settings))
        self.assertIn("ollama pull gemma3:12b", self.text())

    def test_a_mistyped_task_is_asked_again(self):
        code = self.run_cli(stdin_tty=True, answers=["4", "magic", "verify"])
        self.assertEqual(code, 0, self.text())
        self.assertIn("unknown task 'magic'", self.text())
        self.assertEqual(self.saved()[P + "LLM_BACKEND_VERIFY"], "ollama")

    def test_enter_cancels(self):
        self.assertEqual(self.run_cli(stdin_tty=True, answers=[""]), 0)
        self.assertIn("Cancelled", self.text())
        self.assertFalse(os.path.exists(self.settings))

    def test_another_model_by_name_and_enter_for_no_tasks(self):
        self.fake.installed.append({"name": "mystery:7b"})
        code = self.run_cli(stdin_tty=True, answers=["o", "mystery:7b", ""])
        self.assertEqual(code, 0, self.text())
        self.assertEqual(self.saved()[P + "OLLAMA_MODEL"], "mystery:7b")
        self.assertFalse([k for k in self.saved() if "LLM_BACKEND" in k])

    def test_choosing_a_model_installed_under_an_alias_uses_that_name_and_does_not_download(self):
        code = self.run_cli(stdin_tty=True, answers=["4", ""])  # 4 is llama3.2:3b, installed as llama3.2:latest
        self.assertEqual(code, 0, self.text())
        self.assertEqual(self.saved()[P + "OLLAMA_MODEL"], "llama3.2:latest")
        self.assertNotIn("/api/pull", self.fake.paths("POST"))

    def test_the_menu_marks_installed_models(self):
        self.run_cli(stdin_tty=True, answers=[""])
        self.assertRegex(self.text(), r"llama3\.2:3b\s+installed")
        self.assertRegex(self.text(), r"gemma3:12b\s+about 8\.1 GB")


class StatusAndDisable(Base):
    def test_status(self):
        self.write({P + "OLLAMA_MODEL": "gemma3:12b", P + "LLM_BACKEND_SUMMARY": "ollama"})
        self.assertEqual(self.run_cli("status"), 0)
        t = self.text()
        self.assertIn("Ollama 0.0.1-fake", t)
        self.assertIn("Configured model: gemma3:12b  (NOT installed)", t)
        self.assertIn("Tasks on Ollama: summary", t)
        self.assertEqual(self.fake.paths("POST"), [], "status changes and tests nothing")

    def test_status_when_ollama_is_down(self):
        self.fake.close()
        self.assertEqual(self.run_cli("status"), 1)
        self.assertIn("ollama.com", self.text())

    def test_disable_puts_tasks_back_and_keeps_the_model(self):
        self.write({P + "OLLAMA_MODEL": "m", P + "LLM_BACKEND_SUMMARY": "ollama", P + "LLM_BACKEND_VERIFY": "ollama", "OTHER": 1})
        self.assertEqual(self.run_cli("disable"), 0)
        self.assertEqual(self.saved(), {P + "OLLAMA_MODEL": "m", "OTHER": 1})
        self.assertEqual(len(self.backups()), 1)

    def test_disable_with_nothing_to_do(self):
        self.write({"a": 1})
        self.assertEqual(self.run_cli("disable"), 0)
        self.assertIn("nothing to change", self.text())
        self.assertEqual(self.backups(), [])

    def test_disable_dry_run(self):
        self.write({P + "LLM_BACKEND_SUMMARY": "ollama"})
        self.assertEqual(self.run_cli("disable", "--dry-run"), 0)
        self.assertEqual(self.saved(), {P + "LLM_BACKEND_SUMMARY": "ollama"})


if __name__ == "__main__":
    unittest.main()
