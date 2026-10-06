#!/usr/bin/env python3
"""Roll-ups, automatic: with the setting on, the worker's own pass condenses a project's old notes without being asked.

Real MCP server and worker, a fake claude. Ten old notes of one month are seeded; the first pass runs 45 s after the worker
started (then every minute here), so the suite waits for it."""
import datetime, json, os, sqlite3, subprocess, sys, tempfile, time, urllib.request

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
                        stderr=open(f"{E2E}/mcp-rollup-auto.log", "w"), text=True, env=env, cwd="/")
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
    with urllib.request.urlopen(f"http://localhost:{PORT}{path}", timeout=60) as r:
        return json.loads(r.read())


def wait_until(cond, seconds):
    end = time.time() + seconds
    while time.time() < end:
        if cond():
            return True
        time.sleep(3)
    return cond()


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
base = tempfile.mkdtemp(prefix="e2e-rollup-auto-")
folder = os.path.join(base, "dockyard")
os.makedirs(folder)
project = json.loads(tool("project_resolve", path=folder)[1])["id"]

print("== seed ten old notes of one month (automatic scope), and three recent ones")
words = "amber basalt cobalt dune ember fjord garnet harbor indigo jasper".split()
titles = [f"Dockyard entry {i} about {words[i]}" for i in range(10)]
for i, t in enumerate(titles):
    for _ in range(4):
        err, text = tool("remember", path=folder, title=t, text=f"Entry {i}: the {words[(i * 3) % 10]} crane was serviced on day {i}, checksum {i * 7919}.", type="discovery")
        if not err:
            break
        time.sleep(1.5)
    if err:
        raise SystemExit(f"setup step failed: remember: {text[:200]}")
for i in range(3):
    tool("remember", path=folder, title=f"Fresh dockyard note {i}", text=f"Fresh {i}: the dockyard got a new quay light this week, number {i * 31}.", type="discovery")

listed = api(f"/api/observations?project={project}&limit=100")["observations"]
by_title = {o["title"]: o["id"] for o in listed}
old_ids = [by_title[t] for t in titles]
fresh_ids = [by_title[f"Fresh dockyard note {i}"] for i in range(3)]
now = datetime.datetime.now(datetime.timezone.utc)
y, mo = divmod(now.year * 12 + now.month - 1 - 4, 12)
base_day = datetime.datetime(y, mo + 1, 10, 12, 0, tzinfo=datetime.timezone.utc)
con = sqlite3.connect(DB, timeout=30)
for k, oid in enumerate(old_ids):
    t = base_day + datetime.timedelta(minutes=k)
    con.execute("UPDATE observations SET scope_source = 'auto', created_at_epoch = ?, created_at = ? WHERE id = ?", (int(t.timestamp() * 1000), t.isoformat(), oid))
con.commit()
con.close()

print("== the worker's own pass does it")
done = wait_until(lambda: len(api("/api/folds")["folds"]) >= 1, 240)
check("a roll-up appeared without anyone asking", done)
folds = api("/api/folds")["folds"]
check("exactly one roll-up, of the ten old notes", len(folds) == 1 and sorted(folds[0]["sources"]) == sorted(old_ids), folds)
live = {o["id"] for o in api(f"/api/observations?project={project}&limit=100")["observations"]}
check("the old notes are archived and the fresh ones are not", not (set(old_ids) & live) and set(fresh_ids) <= live, sorted(live))
check("the roll-up is live", folds and folds[0]["survivor"] in live)
time.sleep(70)  # a second pass finds nothing left: no second roll-up, and the roll-up is not rolled up again
check("a later pass changes nothing", len(api("/api/folds?include_undone=true")["folds"]) == 1)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
