#!/usr/bin/env python3
"""Prompt retention: a setting deletes the prompts older than N days, in milliseconds, after a snapshot; with none set nothing goes.

Real worker. Prompts are written straight into the database with millisecond timestamps (as the worker writes them), one 90 days
old, one 29 days old and one fresh, then maintenance is run through the real tool. Earlier versions compared those milliseconds
with a cutoff in seconds, so nothing was ever deleted."""
import datetime, glob, json, os, sqlite3, subprocess, sys, time, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
DB = f"{E2E}/home/.claude-mnemonic/claude-mnemonic.db"
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                        stderr=open(f"{E2E}/mcp-retention.log", "w"), text=True, env=env, cwd="/")
_id = 0


def rpc(method, params=None):
    global _id
    _id += 1
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": _id, "method": method, **({"params": params} if params else {})}) + "\n")
    proc.stdin.flush()
    return json.loads(proc.stdout.readline())


def tool(name, **a):
    r = rpc("tools/call", {"name": name, "arguments": a})["result"]
    return r.get("isError", False), r["content"][0]["text"]


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-code"}})

print("== seed three prompts with millisecond timestamps")
now = datetime.datetime.now(datetime.timezone.utc)
con = sqlite3.connect(DB, timeout=30)
for sid, days in (("ninety-days", 90), ("twenty-nine-days", 29), ("fresh", 0)):
    t = now - datetime.timedelta(days=days)
    con.execute("INSERT INTO user_prompts (claude_session_id, prompt_text, prompt_number, created_at, created_at_epoch) VALUES (?, ?, 1, ?, ?)",
                (sid, f"a prompt from {days} days ago", t.isoformat(), int(t.timestamp() * 1000)))
con.commit()


def prompts():
    c = sqlite3.connect(DB, timeout=30)
    try:
        return {r[0] for r in c.execute("SELECT claude_session_id FROM user_prompts")}
    finally:
        c.close()


check("all three are there", prompts() == {"ninety-days", "twenty-nine-days", "fresh"}, prompts())
check("no snapshot yet", glob.glob(f"{E2E}/home/.claude-mnemonic/backups/*before-prompt-retention*") == [])

print("== maintenance with PROMPT_RETENTION_DAYS=30")
err, text = tool("memory_admin", action="run_maintenance")
check("maintenance ran", not err, text[:200])
deadline = time.time() + 30
while time.time() < deadline and "ninety-days" in prompts():
    time.sleep(1)
check("the prompt from 90 days ago is gone", "ninety-days" not in prompts(), prompts())
check("the one from 29 days ago and the fresh one stay", {"twenty-nine-days", "fresh"} <= prompts(), prompts())
check("a snapshot was taken before the deletion", len(glob.glob(f"{E2E}/home/.claude-mnemonic/backups/*before-prompt-retention*")) == 1,
      glob.glob(f"{E2E}/home/.claude-mnemonic/backups/*"))

print("== a second run finds nothing more to delete")
err, text = tool("memory_admin", action="run_maintenance")
time.sleep(2)
check("the same two prompts remain", prompts() == {"twenty-nine-days", "fresh"}, prompts())
check("and no second snapshot is taken", len(glob.glob(f"{E2E}/home/.claude-mnemonic/backups/*before-prompt-retention*")) == 1)
con.close()

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
