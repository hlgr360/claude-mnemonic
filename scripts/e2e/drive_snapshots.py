#!/usr/bin/env python3
"""The worker takes a regular snapshot of the database (the interval is on for this suite only): a copy appears in backups/
half a minute after start, it holds the notes stored before it, /api/stats says so, and it is kept apart from the snapshots
taken before a destructive action."""
import glob, json, os, sqlite3, subprocess, sys, tempfile, time, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
BACKUPS = f"{E2E}/home/.claude-mnemonic/backups"
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                        stderr=open(f"{E2E}/mcp-snap.log", "w"), text=True, env=env, cwd="/")
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


def api(path):
    with urllib.request.urlopen(f"http://localhost:{PORT}{path}", timeout=20) as r:
        return json.loads(r.read())


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
folder = os.path.join(tempfile.mkdtemp(prefix="e2e-snap-"), "ledger")
os.makedirs(folder)
err, text = tool("remember", path=folder, title="Snapshot canary note", text="A note stored before the regular snapshot, which the snapshot must contain.", type="discovery")
check("a note is stored first", not err, text)

print("== a regular snapshot appears without anyone asking for it")
check("there is no snapshot yet", glob.glob(f"{BACKUPS}/*.db") == [], glob.glob(f"{BACKUPS}/*.db"))
end = time.time() + 120
while time.time() < end and not glob.glob(f"{BACKUPS}/snapshot-*-daily.db"):
    time.sleep(3)
daily = glob.glob(f"{BACKUPS}/snapshot-*-daily.db")
check("a daily snapshot was written", len(daily) == 1, daily)
if daily:
    con = sqlite3.connect(f"file:{daily[0]}?mode=ro&immutable=1", uri=True)
    titles = [r[0] for r in con.execute("select title from observations")]
    con.close()
    check("it is a copy of the database with the note in it", "Snapshot canary note" in titles, titles[:5])
stats = api("/api/stats")
check("the stats say how many snapshots there are and how old the newest is",
      stats.get("snapshots", {}).get("count") == 1 and 0 <= stats["snapshots"].get("newest_age_seconds", -1) < 300, stats.get("snapshots"))
time.sleep(5)
check("no second snapshot follows (one a day)", len(glob.glob(f"{BACKUPS}/snapshot-*-daily.db")) == 1)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
