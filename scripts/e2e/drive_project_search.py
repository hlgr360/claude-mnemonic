#!/usr/bin/env python3
"""A search scoped to one project finds that project's notes, the same ones an all-projects search finds.

Real MCP server, real worker, real embeddings. Two projects hold a few dozen notes each; one note of the first is about a
distinctive subject. The search tool with a project must return it first (and more than that one note), as it does without."""
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
                        stderr=open(f"{E2E}/mcp-projsearch.log", "w"), text=True, env=env, cwd="/")
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
    with urllib.request.urlopen(f"http://localhost:{PORT}{path}", timeout=30) as r:
        return json.loads(r.read())


def wait_until(cond, seconds=120):
    end = time.time() + seconds
    while time.time() < end:
        if cond():
            return True
        time.sleep(2)
    return cond()


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
base = tempfile.mkdtemp(prefix="e2e-projsearch-")
ledger, other = os.path.join(base, "ledger"), os.path.join(base, "harbor")
os.makedirs(ledger), os.makedirs(other)
ledger_id = json.loads(tool("project_resolve", path=ledger)[1])["id"]
other_id = json.loads(tool("project_resolve", path=other)[1])["id"]

# Warm the embedding model first: the very first note of a fresh worker can be stored before the model is ready.
warm = os.path.join(base, "warmup")
os.makedirs(warm)
tool("remember", path=warm, title="Warm up", text="A throwaway note that loads the embedding model before the real notes are stored.", type="discovery")
wait_until(lambda: api("/api/stats").get("vectorCount", 0) > 0, 90)

words = ("amber basalt cobalt dune ember fjord garnet harbor indigo jasper kelp lagoon marble nectar onyx prairie quartz reef sable tundra "
         "umber velvet willow xenon yarrow zephyr").split()
N = 105  # a busy project: many notes that read alike
M = 20   # the other project


def store(folder, i, title, body):
    for _ in range(4):
        err, text = tool("remember", path=folder, title=title, text=body, type="discovery")
        if not err:
            return
        time.sleep(1.5)
    raise SystemExit(f"setup step failed: remember: {text[:200]}")


print(f"== one project with {N} notes that read alike, another with {M}")
for i in range(N):
    a, b, c = words[i % 26], words[(i * 7 + 3) % 26], words[(i * 11 + 5) % 26]
    if i == 0:
        store(ledger, i, "Zebra ledger reconciliation runs quarterly",
              "The finance team does the zebra ledger reconciliation every quarter, and the zebra ledger is signed off by two people.")
    else:
        store(ledger, i, f"{a.title()} {b} {c} survey {i}", f"Note {i}: the {a} {b} {c} register was checked on day {i} with checksum {i * 7919}.")
    if i < M:
        store(other, i, f"Dock {a} {b} berth {i}", f"Berth {i}: the {a} {b} crane at the harbour was inspected on day {i}.")
    if i == 5:
        store(other, i, "Quokka feeding rota for the harbour office", "The office keeps a rota for feeding the quokka, and the quokka rota is posted on the fridge.")

last = [-1, 0]


def settled():
    n = api("/api/stats").get("vectorCount", 0)
    last[1] = last[1] + 1 if n == last[0] else 0
    last[0] = n
    return n >= N + M + 1 and last[1] >= 2


wait_until(settled)


def found(q, project=None):
    args = {"query": q}
    if project:
        args["project"] = project
    err, text = tool("search", **args)
    if err:
        return []
    return [o["title"] for o in json.loads(text)["observations"]]


zebra = "Zebra ledger reconciliation runs quarterly"
quokka = "Quokka feeding rota for the harbour office"
print("== search without a project")
check("the zebra note is first", found("zebra ledger reconciliation")[:1] == [zebra], found("zebra ledger reconciliation"))
print("== search with the project")
for q in ("zebra ledger reconciliation", "zebra"):
    r = found(q, ledger_id)
    check(f"'{q}' finds the zebra note first", r[:1] == [zebra], r)
    check(f"'{q}' is not cut to a single unrelated note", len(r) != 1 or r[0] == zebra, r)
r = found("quokka feeding rota", other_id)
check("the other project's own note is found in that project", r[:1] == [quokka], r)
r = found("quokka feeding rota", ledger_id)
check("another project's note is not found in this project", quokka not in r, r)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
