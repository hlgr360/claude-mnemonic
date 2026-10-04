#!/usr/bin/env python3
"""The knowledge graph: the real worker relates notes that read alike (through the real vector index), only within a
project, newer to older, never as 'supersedes', hides notes a person superseded, reports real numbers, and rebuilds."""
import json, os, subprocess, sys, tempfile, time, urllib.error, urllib.parse, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(f"http://localhost:{PORT}{path}", method=method, data=data,
                                 headers={"Content-Type": "application/json"} if data else {})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read().strip()
            return r.status, (json.loads(raw) if raw[:1] in (b"{", b"[") else raw.decode())
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(f"{E2E}/mcp-relations.log", "w"),
                        text=True, env=env, cwd="/")
_id = 0


def rpc(method, params=None):
    global _id
    _id += 1
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": _id, "method": method, **({"params": params} if params else {})}) + "\n")
    proc.stdin.flush()
    return json.loads(proc.stdout.readline())


def must(name, **a):
    r = rpc("tools/call", {"name": name, "arguments": a})["result"]
    if r.get("isError", False):
        raise SystemExit(f"setup step failed: {name} {a} -> {r['content'][0]['text'][:300]}")
    return r["content"][0]["text"]


def remember(path, title, text):
    must("remember", path=path, title=title, text=text)
    time.sleep(0.05)  # newer notes must really be newer


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
base = tempfile.mkdtemp(prefix="e2e-relations-")
dirs = {k: os.path.join(base, k) for k in ("graphp", "otherp")}
ids = {}
for k, d in dirs.items():
    os.makedirs(d)
    ids[k] = json.loads(must("project_resolve", path=d))["id"]
P, O = ids["graphp"], ids["otherp"]

print("== seed: one topic in four notes, another in three, a lone note, and the first topic in another project")
for title, text in [
    ("Shipping rates are cached in Redis", "The shipping rate cache is stored in Redis with a time to live of sixty minutes."),
    ("Shipping rate cache lifetime raised", "The shipping rate cache in Redis now lives for twenty four hours instead of sixty minutes."),
    ("Shipping rate cache is warmed on deploy", "After a deploy the shipping rate cache in Redis is warmed so the first checkout is fast."),
    ("Shipping rate cache keys include the carrier", "Keys of the shipping rate cache in Redis contain the carrier code and the destination country."),
]:
    remember(dirs["graphp"], title, text)
for title, text in [
    ("Parcel labels are printed as PDF", "Parcel labels are rendered as PDF files and sent to the warehouse printer queue."),
    ("Label printer queue retries failed jobs", "The warehouse label printer queue retries failed PDF label jobs three times."),
    ("Label PDF size follows the carrier", "Each carrier needs its own PDF label size, chosen when the parcel label is rendered."),
]:
    remember(dirs["graphp"], title, text)
remember(dirs["graphp"], "The office aquarium needs a new filter", "The clownfish tank in the office needs a replacement water filter this month.")
remember(dirs["otherp"], "Shipping rates are cached in Redis too", "In this other project the shipping rate cache is also stored in Redis for sixty minutes.")


def graph(query=""):
    return call("GET", f"/api/graph{query}")[1]


def note_id(project, word):
    rows = call("GET", f"/api/observations?project={urllib.parse.quote(project)}&limit=100")[1]
    rows = rows["observations"] if isinstance(rows, dict) else rows
    return next(o["id"] for o in rows if word in o["title"])


cluster_a = [note_id(P, w) for w in ("cached in Redis", "lifetime raised", "warmed on deploy", "keys include")]
cluster_b = [note_id(P, w) for w in ("printed as PDF", "retries failed", "PDF size")]
lone = note_id(P, "aquarium")
other = note_id(O, "cached in Redis too")

print("== the builder relates the notes on its own (its first pass runs a minute after the worker starts)")
deadline = time.time() + 240
while time.time() < deadline:
    g = graph(f"?project={urllib.parse.quote(P)}")
    if len({n["id"] for n in g["nodes"]} & set(cluster_a + cluster_b)) == len(cluster_a + cluster_b):
        break
    time.sleep(3)
g = graph(f"?project={urllib.parse.quote(P)}")
node_ids = {n["id"] for n in g["nodes"]}
check("the notes of both topics are in the graph", set(cluster_a + cluster_b) <= node_ids, sorted(node_ids))
check("the lone note about something else has no relation", lone not in node_ids)
check("nothing relates across projects", other not in node_ids and graph(f"?project={urllib.parse.quote(O)}")["edges"] == [])
check("an edge always points from a newer note to an older one", all(e["source"] > e["target"] for e in g["edges"]), g["edges"][:3])
check("edges stay inside the project", all(e["source"] in node_ids and e["target"] in node_ids for e in g["edges"]))
check("the builder never decides that a note supersedes another", all(e["type"] not in ("supersedes", "causes") for e in g["edges"]), {e["type"] for e in g["edges"]})
check("every relation is at least as close as the configured minimum (0.6)", all(e["confidence"] >= 0.6 for e in g["edges"]), [e["confidence"] for e in g["edges"]])
out = {}
for e in g["edges"]:
    out[e["source"]] = out.get(e["source"], 0) + 1
check("a note has at most three relations to older notes", max(out.values()) <= 3, out)
by_a = {e["source"]: e["target"] for e in g["edges"] if e["source"] in cluster_a and e["target"] in cluster_a}
check("the notes about the shipping cache are related to each other", len(by_a) >= 3, by_a)
check("the label notes are related to each other", len([e for e in g["edges"] if e["source"] in cluster_b and e["target"] in cluster_b]) >= 2)
check("each edge has a reason and a type", all(e.get("reason") and e["type"] for e in g["edges"]))

print("== the numbers are real")
st = call("GET", f"/api/graph/stats?project={urllib.parse.quote(P)}")[1]
check("the stats count the graph, not an estimate", st["edgeCount"] == len(g["edges"]) and st["nodeCount"] == len(g["nodes"]), (st["edgeCount"], st["nodeCount"], len(g["edges"]), len(g["nodes"])))
check("the graph is reported as enabled and nothing is left to do", st["enabled"] is True and st["pending"] == 0, (st["enabled"], st["pending"]))
check("the median degree and the maximum are real", st["maxDegree"] >= st["medianDegree"] >= 1, (st["maxDegree"], st["medianDegree"]))
allg = graph()
check("the graph of all projects holds both projects' notes", {cluster_a[0], cluster_b[0]} <= {n["id"] for n in allg["nodes"]})

print("== filters and limits")
strong = graph(f"?project={urllib.parse.quote(P)}&min_confidence=0.99")
check("a confidence floor narrows it", len(strong["edges"]) < len(g["edges"]))
check("only a kind that exists is returned for that kind", all(e["type"] == "relates_to" for e in graph(f"?project={urllib.parse.quote(P)}&types=relates_to")["edges"]))
small = graph(f"?project={urllib.parse.quote(P)}&max_nodes=3")
check("a node limit keeps the best connected and says so", small["truncated"] is True and len(small["nodes"]) == 3 and all(e["source"] in {n["id"] for n in small["nodes"]} for e in small["edges"]))
check("bad parameters are refused", call("GET", "/api/graph?types=bogus")[0] == 400 and call("GET", "/api/graph?min_confidence=7")[0] == 400)

print("== a note a person superseded leaves the graph, and comes back on undo")
target = next(e["target"] for e in g["edges"] if e["target"] in cluster_a)
newer_of = next(e["source"] for e in g["edges"] if e["target"] == target)
code, c = call("POST", "/api/conflicts", {"older_id": target, "newer_id": newer_of, "reason": "e2e"})
check("a proposal can be made for the pair", code in (200, 201), (code, c))
cid = c["id"]
call("POST", f"/api/conflicts/{cid}/resolve", {"decision": "supersede_older"})
hidden = graph(f"?project={urllib.parse.quote(P)}")
check("the superseded note is not in the graph and neither are its relations",
      target not in {n["id"] for n in hidden["nodes"]} and all(target not in (e["source"], e["target"]) for e in hidden["edges"]))
call("POST", f"/api/conflicts/{cid}/undo")
back = graph(f"?project={urllib.parse.quote(P)}")
check("after undo the graph is as it was", len(back["edges"]) == len(g["edges"]) and target in {n["id"] for n in back["nodes"]}, (len(back["edges"]), len(g["edges"])))

print("== rebuilding starts over and arrives at the same graph")
before = len(allg["edges"])
code, r = call("POST", "/api/relations/rebuild")
check("the rebuild reports what it deleted", code == 200 and r["deleted"] >= before, (code, r, before))
check("right after a reset the graph is empty or building", graph()["total_edges"] <= before)
deadline = time.time() + 180
while time.time() < deadline and len(graph()["edges"]) < before:
    time.sleep(3)
again = graph()
check("the builder recreates the same relations", len(again["edges"]) == before, (len(again["edges"]), before))
check("and nothing is left waiting", call("GET", "/api/graph/stats")[1]["pending"] == 0)
check("the worker is healthy", call("GET", "/health")[1].get("ready") is True)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
