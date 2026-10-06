#!/usr/bin/env python3
"""Notes are never deleted for being many, and an archived note is kept but out of search until it is restored.

Real MCP server, real worker, real embeddings. By default there is no cap: a project that used to be cut to its newest 100
notes holds all of them. Archiving a note (what the optional cap and the later roll-ups do) removes it from search, full-text
and semantic, and unarchiving brings it back."""
import json, os, subprocess, sys, tempfile, time, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                        stderr=open(f"{E2E}/mcp-cap.log", "w"), text=True, env=env, cwd="/")
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


def api(path, method="GET", body=None):
    req = urllib.request.Request(f"http://localhost:{PORT}{path}", method=method,
                                 data=json.dumps(body).encode() if body is not None else None, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        raw = r.read()
        return json.loads(raw) if raw else None


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
folder = os.path.join(tempfile.mkdtemp(prefix="e2e-cap-"), "ledger")
os.makedirs(folder)
project = json.loads(tool("project_resolve", path=folder)[1])["id"]

# Warm the embedding model first: the very first note of a fresh worker can be stored before the model is ready.
warm = os.path.join(os.path.dirname(folder), "warmup")
os.makedirs(warm)
tool("remember", path=warm, title="Warm up", text="A throwaway note that loads the embedding model before the real notes are stored.", type="discovery")
end = time.time() + 90
while time.time() < end and api("/api/stats").get("vectorCount", 0) == 0:
    time.sleep(2)

print("== a project holds more than 100 notes (the old cap deleted the oldest)")
words = ("amber basalt cobalt dune ember fjord garnet harbor indigo jasper kelp lagoon marble nectar onyx prairie quartz reef sable tundra "
         "umber velvet willow xenon yarrow zephyr").split()
retries = 0
for i in range(105):
    a, b, c = words[i % 26], words[(i * 7 + 3) % 26], words[(i * 11 + 5) % 26]
    title = "Zebra ledger reconciliation runs quarterly" if i == 0 else f"{a.title()} {b} {c} survey {i}"
    body = (f"Note {i}: the {a} {b} {c} register was checked on day {i} with checksum {i * 7919}."
            if i else "The finance team does the zebra ledger reconciliation every quarter, and the zebra ledger is signed off by two people.")
    # A store can fail now and then while the vector sync of the notes before it holds the database (seen as a 500 "failed to
    # store observation"); retry a few times and count them, the notes are what is under test here.
    for attempt in range(4):
        err, text = tool("remember", path=folder, title=title, text=body, type="discovery")
        if not err:
            break
        retries += 1
        time.sleep(1.5)
    if err:
        raise SystemExit(f"setup step failed: remember #{i}: {text[:200]}")


def wait_until(cond, seconds=90):
    """Poll: the vector sync is asynchronous, so a note is searchable a little after it is stored."""
    end = time.time() + seconds
    while time.time() < end:
        if cond():
            return True
        time.sleep(2)
    return cond()


last = [-1, 0]


def vectors_settled():
    n = api("/api/stats").get("vectorCount", 0)
    last[1] = last[1] + 1 if n == last[0] else 0
    last[0] = n
    return n >= 105 and last[1] >= 2  # unchanged for two polls


wait_until(vectors_settled)
print(f"  (remember was retried {retries} time(s))")
summary = {p["project"]: p for p in api("/api/projects/summary")}
check("all 105 notes are there", summary[project]["observations"] == 105, summary.get(project))
listed = api(f"/api/observations?project={project}&limit=200")["observations"]
zebra_id = next((o["id"] for o in listed if o["title"] == "Zebra ledger reconciliation runs quarterly"), None)
check("the oldest note is still there (the cap used to delete it first)", zebra_id is not None and zebra_id == min(o["id"] for o in listed), zebra_id)


def search_full(q):
    err, text = tool("search", query=q, project=project)
    if err:
        return []
    return [(o["id"], o["title"][:40]) for o in json.loads(text)["observations"]]


def search(q):
    return [i for i, _ in search_full(q)]


print("== archive one note: kept, but out of search")
check("the note is found before", wait_until(lambda: zebra_id in search("zebra ledger reconciliation"), 40), search_full("zebra ledger reconciliation"))
done = api("/api/observations/archive", "POST", {"ids": [zebra_id], "reason": "e2e"})
check("the worker archived it", done["archived_count"] == 1, done)
time.sleep(6)  # its vectors are dropped
check("it is no longer found by search", zebra_id not in search("zebra ledger reconciliation"), search("zebra ledger reconciliation"))
check("nor in the project's list", zebra_id not in [o["id"] for o in api(f"/api/observations?project={project}&limit=200")["observations"]])
check("but the list can include archived notes", zebra_id in [o["id"] for o in api(f"/api/observations?project={project}&limit=200&include_archived=true")["observations"]])
summary = {p["project"]: p for p in api("/api/projects/summary")}
check("the project counts 104 live notes", summary[project]["observations"] == 104, summary.get(project))

print("== restore it: back in search")
api(f"/api/observations/{zebra_id}/unarchive", "POST", {})
check("found again by search (its vectors are put back)", wait_until(lambda: zebra_id in search("zebra ledger reconciliation"), 40), search("zebra ledger reconciliation"))
summary = {p["project"]: p for p in api("/api/projects/summary")}
check("the project counts 105 again", summary[project]["observations"] == 105, summary.get(project))

print("== archive through bulk-status: its vectors leave the index too")
vectors = lambda: api("/api/stats").get("vectorCount", 0)
vectors_before = vectors()
done = api("/api/observations/bulk-status", "POST", {"action": "archive", "ids": [zebra_id], "reason": "e2e bulk"})
check("the worker archived it", done["updated"] == 1, done)
check("its vectors are dropped from the index", wait_until(lambda: vectors() < vectors_before, 40), (vectors_before, vectors()))
check("it is no longer found by search", zebra_id not in search("zebra ledger reconciliation"), search("zebra ledger reconciliation"))
api(f"/api/observations/{zebra_id}/unarchive", "POST", {})
check("restoring it puts its vectors back", wait_until(lambda: vectors() >= vectors_before, 40), (vectors_before, vectors()))
check("and it is found again", wait_until(lambda: zebra_id in search("zebra ledger reconciliation"), 40), search("zebra ledger reconciliation"))

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
