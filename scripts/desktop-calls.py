#!/usr/bin/env python3
"""Measure whether Claude Desktop chat actually uses claude-mnemonic.

Desktop logs a line for every tool call a chat makes to the claude-mnemonic server (without the tool's
name), and the worker logs which endpoint each call hit. This script lines those up with marks you leave
as you go, so a set of prompts can be compared before and after a change to the tool descriptions.

    desktop-calls.py mark "P1 search my memory" --expect call    # right before sending a prompt in a NEW chat
    desktop-calls.py mark "N1 explain git worktrees" --expect none
    desktop-calls.py report                                      # after the last prompt
    desktop-calls.py clear                                       # start a new run

The verdict for each prompt rests on Desktop's own count of tools/call lines in its window (from your
mark to the next mark). The tool names are inferred from the worker's requests and are best effort:
Claude Code's hooks use some of the same endpoints. Standard library only.
"""
import argparse
import json
import os
import re
import sys
from datetime import datetime, timedelta, timezone

DEFAULT_MARKS = os.path.join(os.path.expanduser("~"), ".claude-mnemonic", "desktop-measure.jsonl")
DEFAULT_WORKER_LOG = "/tmp/claude-mnemonic-worker.log"
LAST_WINDOW = timedelta(minutes=10)  # the final prompt has no next mark to end its window

# worker endpoint -> the MCP tool that makes that request
ENDPOINTS = [
    ("GET", r"^/api/projects/suggest$", "project_suggest"),
    ("GET", r"^/api/projects/resolve$", "project_resolve"),
    ("GET", r"^/api/projects/summary$", "project_list"),
    ("GET", r"^/api/projects/[^/]+/stats$", "project_manage"),
    ("DELETE", r"^/api/projects/[^/]+$", "project_manage"),
    ("POST", r"^/api/projects/[^/]+/merge$", "project_manage"),
    ("GET", r"^/api/context/inject$", "context"),
    ("POST", r"^/api/observations/remember$", "remember"),
    ("POST", r"^/api/threads/checkpoint$", "checkpoint"),
    ("GET", r"^/api/projects/[^/]+/catch-up$", "catch_up"),
    ("GET", r"^/api/search/cross-project$", "search"),
    ("GET", r"^/api/context/search$", "search"),
]

DESKTOP_LINE = re.compile(r'^(\d{4}-\d\d-\d\dT[\d:.]+Z) \[[^\]]*\] \[info\] Message from client: method="tools/call"')
WORKER_LINE = re.compile(r'^(\d{4}/\d\d/\d\d \d\d:\d\d:\d\d) "([A-Z]+) https?://[^/ ]+(/[^ ?"]*)')


def default_desktop_log():
    home = os.path.expanduser("~")
    if sys.platform == "darwin":
        return os.path.join(home, "Library", "Logs", "Claude", "mcp-server-claude-mnemonic.log")
    if sys.platform.startswith("win"):
        return os.path.join(os.environ.get("APPDATA", home), "Claude", "logs", "mcp-server-claude-mnemonic.log")
    return os.path.join(home, ".config", "Claude", "logs", "mcp-server-claude-mnemonic.log")


def read_lines(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read().splitlines()
    except OSError:
        return []


def desktop_calls(lines):
    """Timestamps (aware) of every tools/call the Desktop client sent to the server."""
    out = []
    for line in lines:
        m = DESKTOP_LINE.match(line)
        if m:
            out.append(datetime.strptime(m.group(1), "%Y-%m-%dT%H:%M:%S.%fZ").replace(tzinfo=timezone.utc))
    return out


def worker_requests(lines):
    """(aware local time, method, path) of each request line in the worker log."""
    out = []
    for line in lines:
        m = WORKER_LINE.match(line)
        if m:
            when = datetime.strptime(m.group(1), "%Y/%m/%d %H:%M:%S").astimezone()  # naive -> system local time
            out.append((when, m.group(2), m.group(3)))
    return out


def tool_for(method, path):
    for m, pattern, tool in ENDPOINTS:
        if m == method and re.match(pattern, path):
            return tool
    return None


def read_marks(path):
    marks = []
    for line in read_lines(path):
        try:
            d = json.loads(line)
            marks.append({"at": datetime.fromisoformat(d["at"]), "label": d["label"], "expect": d.get("expect")})
        except (ValueError, KeyError):
            continue
    return sorted(marks, key=lambda m: m["at"])


def windows(marks, now):
    out = []
    for i, m in enumerate(marks):
        end = marks[i + 1]["at"] if i + 1 < len(marks) else min(now, m["at"] + LAST_WINDOW)
        out.append((m, m["at"], end))
    return out


def verdict(expect, calls):
    if expect == "call":
        return "OK" if calls > 0 else "MISS"
    if expect == "none":
        return "OK" if calls == 0 else "UNEXPECTED"
    return "-"


def build_report(marks, calls, requests, now):
    rows = []
    for m, start, end in windows(marks, now):
        n = sum(1 for t in calls if start <= t < end)
        tools = sorted({tool_for(meth, p) for when, meth, p in requests if start <= when < end} - {None})
        rows.append({"label": m["label"], "expect": m["expect"], "calls": n, "tools": tools, "verdict": verdict(m["expect"], n)})
    return rows


def render(rows):
    lines = [f"{'prompt':<44} {'expect':<7} {'calls':>5}  {'verdict':<10} tools (inferred)"]
    for r in rows:
        lines.append(f"{r['label'][:44]:<44} {r['expect'] or '-':<7} {r['calls']:>5}  {r['verdict']:<10} {', '.join(r['tools']) or '-'}")
    should, shouldnt = [r for r in rows if r["expect"] == "call"], [r for r in rows if r["expect"] == "none"]
    lines.append("")
    if should:
        lines.append(f"used claude-mnemonic when it should: {sum(r['verdict'] == 'OK' for r in should)}/{len(should)}")
    if shouldnt:
        lines.append(f"stayed out when it should: {sum(r['verdict'] == 'OK' for r in shouldnt)}/{len(shouldnt)}")
    lines.append("Tool names come from the worker's requests; Claude Code's hooks use some of the same endpoints.")
    return "\n".join(lines)


def cmd_mark(args):
    os.makedirs(os.path.dirname(args.marks), exist_ok=True)
    entry = {"at": datetime.now().astimezone().isoformat(), "label": args.label, "expect": args.expect}
    with open(args.marks, "a", encoding="utf-8") as f:
        f.write(json.dumps(entry) + "\n")
    print(f"marked: {args.label}" + (f" (expect {args.expect})" if args.expect else ""))
    return 0


def cmd_report(args):
    marks = read_marks(args.marks)
    if not marks:
        print(f"No marks in {args.marks}. Run: desktop-calls.py mark \"P1 ...\" --expect call  before each prompt.", file=sys.stderr)
        return 1
    desktop_path = args.desktop_log or default_desktop_log()
    if not os.path.exists(desktop_path):
        print(f"warning: Desktop log not found at {desktop_path} (use --desktop-log)", file=sys.stderr)
    rows = build_report(marks, desktop_calls(read_lines(desktop_path)), worker_requests(read_lines(args.worker_log)), datetime.now().astimezone())
    print(render(rows))
    return 0


def cmd_clear(args):
    if os.path.exists(args.marks):
        os.remove(args.marks)
    print("cleared marks")
    return 0


def parse(argv):
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--marks", default=DEFAULT_MARKS, help=f"marks file (default {DEFAULT_MARKS})")
    sub = p.add_subparsers(dest="command", required=True)
    m = sub.add_parser("mark", help="note that a prompt is about to be sent")
    m.add_argument("label")
    m.add_argument("--expect", choices=["call", "none"], help="whether claude-mnemonic should be used for this prompt")
    m.set_defaults(fn=cmd_mark)
    r = sub.add_parser("report", help="show, per prompt, whether claude-mnemonic was called")
    r.add_argument("--desktop-log", help="Claude Desktop's log for the claude-mnemonic server (default: the standard location)")
    r.add_argument("--worker-log", default=DEFAULT_WORKER_LOG, help=f"worker log (default {DEFAULT_WORKER_LOG})")
    r.set_defaults(fn=cmd_report)
    c = sub.add_parser("clear", help="forget all marks")
    c.set_defaults(fn=cmd_clear)
    return p.parse_args(argv)


def main(argv=None):
    args = parse(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
