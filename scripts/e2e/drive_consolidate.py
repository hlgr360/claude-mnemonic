#!/usr/bin/env python3
"""Consolidation: near-duplicate notes are folded into one and the duplicates archived (kept, hidden, restorable).

Real MCP server and worker with real embeddings. By hand, through the tools: suggest, preview, a wrong token is refused, apply,
search finds the survivor and not the duplicates, restore. Automatically: the worker's own pass folds a group of automatic notes
and leaves notes that were saved on purpose, and decisions, alone."""
import glob, json, os, sqlite3, subprocess, sys, tempfile, time, urllib.request

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
                        stderr=open(f"{E2E}/mcp-consolidate.log", "w"), text=True, env=env, cwd="/")
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
        time.sleep(2)
    return cond()


def remember(folder, title, text, typ="discovery", concepts=None):
    for _ in range(4):
        args = dict(path=folder, title=title, text=text, type=typ)
        if concepts:
            args["concepts"] = concepts
        err, out = tool("remember", **args)
        if not err:
            return
        time.sleep(1.5)
    raise SystemExit(f"setup step failed: remember {title}: {out[:200]}")


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
base = tempfile.mkdtemp(prefix="e2e-consolidate-")
folder = os.path.join(base, "irrigation")
os.makedirs(folder)
project = json.loads(tool("project_resolve", path=folder)[1])["id"]

# Warm the embedding model first: the very first note of a fresh worker can be stored before the model is ready.
warm = os.path.join(base, "warmup")
os.makedirs(warm)
remember(warm, "Warm up", "A throwaway note that loads the embedding model before the real notes are stored.")
wait_until(lambda: api("/api/stats").get("vectorCount", 0) > 0, 90)

print("== seed: three duplicates to fold by hand, three that the worker folds, a protected pair and a decision pair")
MANUAL = "The marmot irrigation valve controller was replaced after the night watering schedule failed on the east orchard line"
AUTO = "The pelican tariff ledger export was corrected after the quarterly rate table showed a wrong harbour surcharge on the invoice"
PROT = "The quokka feeding rota board was rebuilt after the volunteer sign up sheet lost the weekend shifts for the west enclosure"
DEC = "We decided the lagoon pump filter is replaced every spring because the autumn leaf fall clogs the intake screens quickly"
for text, title, n, concepts in ((MANUAL, "Marmot valve controller replaced", 3, [["valves"], ["valves", "orchard"], ["schedule"]]),
                                 (AUTO, "Pelican tariff ledger corrected", 3, [None, None, None])):
    for i in range(n):
        remember(folder, title, text + [" today", " again", " once more"][i], concepts=concepts[i])
for i in range(2):
    remember(folder, "Quokka rota board rebuilt", PROT + [" first", " second"][i])
for i in range(2):
    remember(folder, "Lagoon pump filter policy", DEC + [" first", " second"][i], typ="decision")

listed = api(f"/api/observations?project={project}&limit=100")["observations"]
by_title = {}
for o in listed:
    by_title.setdefault(o["title"], []).append(o["id"])
manual_ids, auto_ids = sorted(by_title["Marmot valve controller replaced"]), sorted(by_title["Pelican tariff ledger corrected"])
prot_ids, dec_ids = sorted(by_title["Quokka rota board rebuilt"]), sorted(by_title["Lagoon pump filter policy"])
check("all notes were stored", len(manual_ids) == 3 and len(auto_ids) == 3 and len(prot_ids) == 2 and len(dec_ids) == 2, by_title)

# What the extractor saves has an automatic scope; remember records the client's choice, which protects those notes from the
# worker's own pass. So only the second group is flipped to automatic: the first (and the pairs) can only be touched by hand.
con = sqlite3.connect(DB, timeout=30)
con.execute(f"UPDATE observations SET scope_source = 'auto' WHERE id IN ({','.join('?' * len(auto_ids))})", auto_ids)
con.commit()
con.close()


def live_ids():
    return {o["id"] for o in api(f"/api/observations?project={project}&limit=200")["observations"]}


vectors = lambda: api("/api/stats").get("vectorCount", 0)
wait_until(lambda: vectors() >= 11, 90)


def search_ids(q):
    err, out = tool("search", query=q, project=project)
    return [o["id"] for o in json.loads(out)["observations"]] if not err else []


print("== by hand: a single note is refused, a preview changes nothing, a wrong token is refused")
err, text = tool("memory_admin", action="consolidate", ids=[manual_ids[0]])
check("a single note is refused before it reaches the worker", err and "at least two" in text, text[:200])
err, text = tool("memory_admin", action="consolidate", ids=manual_ids)
check("the preview did not fail", not err, text[:300])
plan = json.loads(text)
check("it is a preview with a token", plan["applied"] is False and plan["token"], plan)
check("it names a survivor and two duplicates", plan["survivor"]["id"] in manual_ids and len(plan["duplicates"]) == 2, plan)
check("it lists what the survivor would take over", plan["added_concepts"] and any("Consolidated with" in f for f in plan["added_facts"]), plan)
check("nothing changed yet", set(manual_ids) <= live_ids())
err, text = tool("memory_admin", action="consolidate", ids=manual_ids, confirm="not-the-token")
check("a token that is not the plan's is refused", err, text[:200])
check("and still nothing changed", set(manual_ids) <= live_ids())

print("== by hand: apply")
err, text = tool("memory_admin", action="consolidate", ids=manual_ids, confirm=plan["token"])
check("the consolidation worked", not err, text[:300])
done = json.loads(text)
survivor = done["survivor"]["id"]
dups = [d["id"] for d in done["duplicates"]]
check("it is applied and recorded", done["applied"] is True and done["fold_id"], done)
live = live_ids()
check("the survivor is live and the duplicates are archived", survivor in live and not (set(dups) & live), sorted(live))
merged = next(o for o in api(f"/api/observations?project={project}&limit=100")["observations"] if o["id"] == survivor)
check("the survivor took over the concepts of the duplicates", {"valves", "orchard", "schedule"} <= set(merged.get("concepts") or []), merged.get("concepts"))
check("a snapshot was taken first", len(glob.glob(f"{E2E}/home/.claude-mnemonic/backups/*before-consolidate*")) >= 1)
found = wait_until(lambda: survivor in search_ids("marmot irrigation valve controller"), 60)
hits = search_ids("marmot irrigation valve controller")
check("a search finds the survivor", found, hits)
check("and none of the archived duplicates", not (set(dups) & set(hits)), hits)

print("== by hand: the list, and a restore")
err, text = tool("memory_admin", action="folds", project=project, kind="consolidation")
folds = json.loads(text)["folds"]
mine = [f for f in folds if sorted(f["sources"]) == sorted(dups)]
check("the consolidation is listed with its two sources", len(mine) == 1 and mine[0]["survivor"] == survivor, folds)
err, text = tool("memory_admin", action="restore_fold", id=mine[0]["id"])
check("the restore worked", not err, text[:300])
rep = json.loads(text)
check("both duplicates are live again and the survivor stays", len(rep["restored"]) == 2 and rep["survivor_archived"] is False, rep)
live = live_ids()
check("all three notes are live", set(manual_ids) <= live)
after = next(o for o in api(f"/api/observations?project={project}&limit=100")["observations"] if o["id"] == survivor)
check("the survivor gave back what it had taken over", set(after.get("concepts") or []) <= set(merged.get("concepts") or []) and
      len(after.get("concepts") or []) < len(merged.get("concepts") or []), (after.get("concepts"), merged.get("concepts")))
err, text = tool("memory_admin", action="restore_fold", id=mine[0]["id"])
check("restoring it twice is refused", err and "already restored" in text, text[:200])

print("== automatic: the worker's own pass folds the automatic group and leaves the rest")
done = wait_until(lambda: any(sorted(f["sources"]) and set(f["sources"]) <= set(auto_ids) for f in api("/api/folds")["folds"]), 240)
check("a consolidation of the automatic notes appeared without anyone asking", done)
live = live_ids()
check("two of the three automatic notes are archived, one stays", len(set(auto_ids) & live) == 1, sorted(live))
check("the notes saved on purpose are untouched", set(prot_ids) <= live)
check("the decisions are untouched", set(dec_ids) <= live)
check("the manual group was left alone by the pass", set(manual_ids) <= live)
check("no consolidation names a protected note",
      not any(set(f["sources"]) & set(prot_ids + dec_ids) or f["survivor"] in prot_ids + dec_ids for f in api("/api/folds?kind=consolidation&include_undone=true")["folds"]))
time.sleep(70)  # a second pass finds nothing left
check("a later pass changes nothing", len([f for f in api("/api/folds?kind=consolidation")["folds"] if set(f["sources"]) <= set(auto_ids)]) == 1)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
