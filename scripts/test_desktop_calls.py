#!/usr/bin/env python3
"""Tests for desktop-calls.py. Run: python3 -m unittest scripts/test_desktop_calls.py -v"""
import contextlib
import importlib.util
import io
import json
import os
import tempfile
import unittest
from datetime import datetime, timedelta, timezone

_spec = importlib.util.spec_from_file_location("desktop_calls", os.path.join(os.path.dirname(os.path.abspath(__file__)), "desktop-calls.py"))
dc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(dc)

# real lines, as Claude Desktop and the worker write them
DESKTOP_CALL = '2026-10-03T14:10:38.880Z [claude-mnemonic] [info] Message from client: method="tools/call" id=3 params { metadata: undefined }'
DESKTOP_OTHER = '2026-10-03T14:06:51.997Z [claude-mnemonic] [info] Message from client: method="tools/list" id=1 params { metadata: undefined }'
WORKER_SUGGEST = '2026/10/03 16:09:11 "GET http://localhost:37777/api/projects/suggest?query=Claude+Desktop HTTP/1.1" from 127.0.0.1:62448 - 200 351B in 26.225959ms'

UTC = timezone.utc


def utc_line(dt):
    return f'{dt.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3]}Z [claude-mnemonic] [info] Message from client: method="tools/call" id=9 params {{ metadata: undefined }}'


def worker_line(dt, method, path):
    return f'{dt.astimezone().strftime("%Y/%m/%d %H:%M:%S")} "{method} http://localhost:37777{path} HTTP/1.1" from 127.0.0.1:1 - 200 5B in 1ms'


T0 = datetime(2026, 10, 3, 16, 0, 0).astimezone()


def at(minutes, seconds=0):
    return T0 + timedelta(minutes=minutes, seconds=seconds)


class Parsing(unittest.TestCase):
    def test_desktop_calls_counts_only_tool_calls_and_reads_utc(self):
        got = dc.desktop_calls([DESKTOP_OTHER, DESKTOP_CALL, "garbage"])
        self.assertEqual(got, [datetime(2026, 10, 3, 14, 10, 38, 880000, tzinfo=UTC)])

    def test_worker_requests_parse_method_path_and_local_time(self):
        got = dc.worker_requests([WORKER_SUGGEST, "not a request line"])
        self.assertEqual(len(got), 1)
        when, method, path = got[0]
        self.assertEqual((method, path), ("GET", "/api/projects/suggest"), "the query string is dropped")
        self.assertEqual(when, datetime(2026, 10, 3, 16, 9, 11).astimezone(), "naive worker time is local time")
        # the same moment as Desktop's UTC stamp for a 14:09:11Z call when the machine is at UTC+2
        if datetime(2026, 10, 3, 16, 9, 11).astimezone().utcoffset() == timedelta(hours=2):
            self.assertEqual(when.astimezone(UTC), datetime(2026, 10, 3, 14, 9, 11, tzinfo=UTC))

    def test_tool_for_maps_every_tool_endpoint(self):
        cases = {
            ("GET", "/api/projects/suggest"): "project_suggest", ("GET", "/api/projects/resolve"): "project_resolve",
            ("GET", "/api/projects/summary"): "project_list", ("GET", "/api/context/inject"): "context",
            ("POST", "/api/observations/remember"): "remember", ("GET", "/api/search/cross-project"): "search",
            ("GET", "/api/context/search"): "search", ("GET", "/api/projects/x_111111/stats"): "project_manage",
            ("POST", "/api/threads/checkpoint"): "checkpoint", ("GET", "/api/projects/x_111111/catch-up"): "catch_up",
            ("DELETE", "/api/projects/x_111111"): "project_manage", ("POST", "/api/projects/x_111111/merge"): "project_manage",
        }
        for (method, path), tool in cases.items():
            self.assertEqual(dc.tool_for(method, path), tool, path)
        self.assertIsNone(dc.tool_for("GET", "/api/health"))
        self.assertIsNone(dc.tool_for("POST", "/api/sessions/init"), "hook traffic is not attributed to a tool")


class Windows(unittest.TestCase):
    def marks(self):
        return [{"at": at(0), "label": "P1", "expect": "call"}, {"at": at(2), "label": "N1", "expect": "none"}, {"at": at(4), "label": "P2", "expect": "call"}]

    def test_each_window_runs_from_its_mark_to_the_next(self):
        calls = [at(0, 30), at(0, 50), at(3), at(4, 20)]
        rows = dc.build_report(self.marks(), calls, [], at(30))
        self.assertEqual([r["calls"] for r in rows], [2, 1, 1])

    def test_a_call_exactly_at_the_next_mark_belongs_to_the_next_prompt(self):
        rows = dc.build_report(self.marks(), [at(2)], [], at(30))
        self.assertEqual([r["calls"] for r in rows], [0, 1, 0])

    def test_calls_before_the_first_mark_are_ignored(self):
        rows = dc.build_report(self.marks(), [at(-5)], [], at(30))
        self.assertEqual(sum(r["calls"] for r in rows), 0)

    def test_the_last_window_is_capped_so_later_activity_is_not_counted(self):
        rows = dc.build_report(self.marks(), [at(4, 30), at(30)], [], at(60))
        self.assertEqual(rows[-1]["calls"], 1, "a call 26 minutes after the last mark is not that prompt's")

    def test_the_last_window_ends_now_if_that_is_sooner(self):
        w = dc.windows(self.marks(), at(5))
        self.assertEqual(w[-1][2], at(5))

    def test_tools_are_inferred_from_the_worker_requests_in_the_window(self):
        requests = [(at(0, 10), "GET", "/api/projects/suggest"), (at(0, 20), "GET", "/api/context/inject"),
                    (at(0, 21), "GET", "/api/health"), (at(3), "GET", "/api/projects/summary")]
        rows = dc.build_report(self.marks(), [at(0, 5)], requests, at(30))
        self.assertEqual(rows[0]["tools"], ["context", "project_suggest"])
        self.assertEqual(rows[1]["tools"], ["project_list"])
        self.assertEqual(rows[2]["tools"], [])


class Verdicts(unittest.TestCase):
    def test_verdict(self):
        self.assertEqual(dc.verdict("call", 2), "OK")
        self.assertEqual(dc.verdict("call", 0), "MISS")
        self.assertEqual(dc.verdict("none", 0), "OK")
        self.assertEqual(dc.verdict("none", 1), "UNEXPECTED")
        self.assertEqual(dc.verdict(None, 5), "-")

    def test_render_summarises_both_kinds_of_prompt(self):
        rows = [{"label": "P1", "expect": "call", "calls": 1, "tools": ["project_suggest"], "verdict": "OK"},
                {"label": "P2", "expect": "call", "calls": 0, "tools": [], "verdict": "MISS"},
                {"label": "N1", "expect": "none", "calls": 0, "tools": [], "verdict": "OK"}]
        text = dc.render(rows)
        self.assertIn("used claude-mnemonic when it should: 1/2", text)
        self.assertIn("stayed out when it should: 1/1", text)
        self.assertIn("MISS", text)
        self.assertIn("project_suggest", text)


class CommandLine(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(self.dir, ignore_errors=True))
        self.marks = os.path.join(self.dir, "marks.jsonl")

    def run_cli(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = dc.main(["--marks", self.marks, *argv])
        return code, out.getvalue(), err.getvalue()

    def test_mark_appends_and_clear_removes(self):
        self.assertEqual(self.run_cli("mark", "P1 memory", "--expect", "call")[0], 0)
        self.assertEqual(self.run_cli("mark", "N1 plain", "--expect", "none")[0], 0)
        marks = dc.read_marks(self.marks)
        self.assertEqual([m["label"] for m in marks], ["P1 memory", "N1 plain"])
        self.assertEqual([m["expect"] for m in marks], ["call", "none"])
        self.run_cli("clear")
        self.assertFalse(os.path.exists(self.marks))
        self.assertEqual(self.run_cli("clear")[0], 0, "clearing nothing is fine")

    def test_report_without_marks_explains_what_to_do(self):
        code, _, err = self.run_cli("report", "--desktop-log", os.path.join(self.dir, "none.log"))
        self.assertEqual(code, 1)
        self.assertIn("mark", err)

    def test_report_end_to_end_with_real_shaped_logs(self):
        start = datetime.now().astimezone() - timedelta(minutes=5)
        for label, expect, offset in (("P1 memory", "call", 0), ("N1 plain", "none", 60)):
            entry = {"at": (start + timedelta(seconds=offset)).isoformat(), "label": label, "expect": expect}
            with open(self.marks, "a") as f:
                f.write(json.dumps(entry) + "\n")
        desktop_log = os.path.join(self.dir, "desktop.log")
        worker_log = os.path.join(self.dir, "worker.log")
        with open(desktop_log, "w") as f:
            f.write(utc_line(start + timedelta(seconds=20)) + "\n")  # a call during P1, none during N1
        with open(worker_log, "w") as f:
            f.write(worker_line(start + timedelta(seconds=20), "GET", "/api/projects/suggest") + "\n")

        code, out, _ = self.run_cli("report", "--desktop-log", desktop_log, "--worker-log", worker_log)
        self.assertEqual(code, 0)
        p1 = next(l for l in out.splitlines() if l.startswith("P1 memory"))
        n1 = next(l for l in out.splitlines() if l.startswith("N1 plain"))
        self.assertIn("OK", p1)
        self.assertIn("project_suggest", p1)
        self.assertIn("OK", n1)
        self.assertIn("used claude-mnemonic when it should: 1/1", out)
        self.assertIn("stayed out when it should: 1/1", out)

    def test_report_survives_missing_logs(self):
        self.run_cli("mark", "P1", "--expect", "call")
        code, out, err = self.run_cli("report", "--desktop-log", os.path.join(self.dir, "missing.log"), "--worker-log", os.path.join(self.dir, "missing2.log"))
        self.assertEqual(code, 0)
        self.assertIn("MISS", out, "no log means no calls were seen")
        self.assertIn("warning", err)

    def test_corrupt_marks_are_skipped(self):
        with open(self.marks, "w") as f:
            f.write("not json\n" + json.dumps({"at": at(0).isoformat(), "label": "good", "expect": "call"}) + "\n" + json.dumps({"nope": 1}) + "\n")
        self.assertEqual([m["label"] for m in dc.read_marks(self.marks)], ["good"])


if __name__ == "__main__":
    unittest.main()
