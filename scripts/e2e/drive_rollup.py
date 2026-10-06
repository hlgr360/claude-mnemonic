#!/usr/bin/env python3
"""Roll-ups: old notes are condensed by a model, the originals are archived (kept, hidden, restorable) and linked.

Real MCP server, real worker, real embeddings, a fake claude that answers the roll-up request. Thirty old notes in three
months, a decision, a rated note, a note saved on purpose and three recent ones: three roll-ups, originals archived, the decision/rated/recent notes
untouched, the roll-up found by search and the originals not, a snapshot first, nothing archived when the model fails,
and a restore that brings a month back."""
import datetime, glob, json, os, sqlite3, subprocess, sys, tempfile, time, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
PROMPTS = os.path.join(E2E, "fake-claude-prompts.log")
FAIL = os.path.join(E2E, "fake-claude-rollup-fail")
DB = f"{E2E}/home/.claude-mnemonic/claude-mnemonic.db"
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                        stderr=open(f"{E2E}/mcp-rollup.log", "w"), text=True, env=env, cwd="/")
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
    with urllib.request.urlopen(req, timeout=300) as r:
        raw = r.read()
        return json.loads(raw) if raw else None


def wait_until(cond, seconds=90):
    end = time.time() + seconds
    while time.time() < end:
        if cond():
            return True
        time.sleep(2)
    return cond()


def prompts():
    return open(PROMPTS).read() if os.path.exists(PROMPTS) else ""


def requests_for_rollup():
    return prompts().count("ROLL-UP REQUEST")


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
folder = os.path.join(tempfile.mkdtemp(prefix="e2e-rollup-"), "harbour")
os.makedirs(folder)
project = json.loads(tool("project_resolve", path=folder)[1])["id"]

# Warm the embedding model first: the very first note of a fresh worker can be stored before the model is ready.
warm = os.path.join(os.path.dirname(folder), "warmup")
os.makedirs(warm)
tool("remember", path=warm, title="Warm up", text="A throwaway note that loads the embedding model before the real notes are stored.", type="discovery")
wait_until(lambda: api("/api/stats").get("vectorCount", 0) > 0, 90)

print("== seed: thirty old notes in three months, a decision, a rated note, a hand-scoped note, three recent notes")
months = [
    ("Quokka harbour budget approval", "the quokka harbour budget was approved by the board after the second review"),
    ("Pelican dockyard tariff review", "the pelican dockyard tariff was reviewed and the new rate table was published"),
    ("Marmot orchard irrigation plan", "the marmot orchard irrigation plan moved to night watering to save water"),
]
filler = "amber basalt cobalt dune ember fjord garnet harbor indigo jasper kelp lagoon".split()
batch = []
for m, (head, text) in enumerate(months):
    batch.append(("discovery", head, f"In this period {text}."))
    for i in range(1, 10):
        batch.append(("discovery", f"Routine entry {m}-{i} about {filler[(m * 3 + i) % 12]}",
                      f"Entry {m}-{i}: the {filler[(i * 5 + m) % 12]} register was checked on day {i}, checksum {m * 1000 + i * 7919}."))
batch.append(("decision", "Decision: keep the quokka rota", "We decided to keep the quokka feeding rota as it is, because the volunteers prefer it."))
batch.append(("discovery", "Rated note about the lagoon pump", "The lagoon pump needs its filter changed every spring, which the user rated as useful."))
batch.append(("discovery", "Hand-scoped note about the harbour gate", "The harbour gate code is rotated by the harbour master every quarter, saved on purpose."))
for i in range(3):
    batch.append(("discovery", f"Recent note {i} about the new quay", f"Recent {i}: the new quay crane was commissioned this week, test {i}."))
retries = 0
for typ, title, body in batch:
    # A store can fail now and then while the vector sync of the notes before it holds the database; retry a few times.
    for attempt in range(4):
        err, text = tool("remember", path=folder, title=title, text=body, type=typ)
        if not err:
            break
        retries += 1
        time.sleep(1.5)
    if err:
        raise SystemExit(f"setup step failed: remember {title}: {text[:200]}")
print(f"  (remember was retried {retries} time(s))")
listed = api(f"/api/observations?project={project}&limit=200")["observations"]
by_title = {o["title"]: o["id"] for o in listed}
month_ids = [[by_title[months[m][0]]] + [by_title[f"Routine entry {m}-{i} about {filler[(m * 3 + i) % 12]}"] for i in range(1, 10)] for m in range(3)]
decision_id, rated_id = by_title["Decision: keep the quokka rota"], by_title["Rated note about the lagoon pump"]
scoped_id = by_title["Hand-scoped note about the harbour gate"]
recent_ids = [by_title[f"Recent note {i} about the new quay"] for i in range(3)]
api(f"/api/observations/{rated_id}/feedback", "POST", {"feedback": 1})

# Date the old notes: ten each in a month five, four and three months ago (so all are well past the 60 days), the decision
# and the rated note among them.
now = datetime.datetime.now(datetime.timezone.utc)


def month_day10(offset):
    y, mo = divmod(now.year * 12 + now.month - 1 - offset, 12)
    return datetime.datetime(y, mo + 1, 10, 12, 0, tzinfo=datetime.timezone.utc)


con = sqlite3.connect(DB, timeout=30)
for m, ids in enumerate(month_ids):
    base = month_day10(5 - m)
    extra = [decision_id] if m == 0 else [rated_id] if m == 1 else [scoped_id]
    for k, oid in enumerate(ids + extra):
        t = base + datetime.timedelta(minutes=k)
        # What the extractor saves has an automatic scope; remember (the three protected extras) records the client's choice.
        auto = "scope_source = 'auto'," if oid in ids else ""
        con.execute(f"UPDATE observations SET {auto} created_at_epoch = ?, created_at = ? WHERE id = ?", (int(t.timestamp() * 1000), t.isoformat(), oid))
con.commit()
con.close()
all_old = [i for ids in month_ids for i in ids]


def live_ids():
    return {o["id"] for o in api(f"/api/observations?project={project}&limit=200")["observations"]}


vectors = lambda: api("/api/stats").get("vectorCount", 0)
wait_until(lambda: vectors() >= len(batch) + 1)
check("the project has all notes live", len(live_ids()) == len(batch), len(live_ids()))

print("== a preview shows the groups and changes nothing")
err, text = tool("memory_admin", action="rollup", project=project)
check("the tool did not fail", not err, text[:200])
prev = json.loads(text)
check("it is a preview", prev["dry_run"] is True, prev)
check("three groups, one per month", len(prev["groups"]) == 3 and [len(g["ids"]) for g in prev["groups"]] == [10, 10, 10], [(g["label"], len(g["ids"])) for g in prev["groups"]])
protected = {decision_id, rated_id, scoped_id, *recent_ids}
check("the decision, the rated note, the hand-scoped note and the recent notes are not in any group",
      not (protected & {i for g in prev["groups"] for i in g["ids"]}))
check("the model was not asked", requests_for_rollup() == 0)
check("nothing was archived", len(live_ids()) == len(batch))

print("== a model that fails: every note stays live")
open(FAIL, "w").close()
err, text = tool("memory_admin", action="rollup", project=project, dry_run=False)
os.remove(FAIL)
fail_run = json.loads(text) if not err else {}
check("the groups report an error", not err and fail_run["groups"] and all(g.get("error") for g in fail_run["groups"]), text[:300])
check("nothing was archived and no roll-up was written", len(live_ids()) == len(batch), len(live_ids()))
check("no roll-up is recorded", api("/api/folds?include_undone=true")["folds"] == [])
snaps = glob.glob(f"{E2E}/home/.claude-mnemonic/backups/*before-rollup*")
check("a snapshot was taken before the first archive was attempted", len(snaps) >= 1, snaps)

print("== the roll-ups")
vectors_before = vectors()
err, text = tool("memory_admin", action="rollup", project=project, dry_run=False)
check("the tool did not fail", not err, text[:300])
run = json.loads(text)
check("three roll-ups were written", len([g for g in run["groups"] if not g.get("error")]) == 3, run)
check("thirty originals were archived", sum(g["archived"] for g in run["groups"]) == 30, run)
live = live_ids()
check("none of the originals is live", not (set(all_old) & live))
check("the decision, the rated note, the hand-scoped note and the recent notes are untouched", protected <= live)
rollup_ids = [g["rollup_id"] for g in run["groups"]]
check("the project holds the six protected notes and the three roll-ups", live == {*protected, *rollup_ids}, sorted(live))

rollups = {o["id"]: o for o in api(f"/api/observations?project={project}&limit=200")["observations"] if o["id"] in rollup_ids}
sample = rollups[rollup_ids[0]]
text_of = (sample.get("narrative") or "") + " " + (sample.get("title") or "")
check("a roll-up is marked, titled and scoped to its project", "rollup" in (sample.get("concepts") or []) and sample["title"].startswith("Roll-up:") and sample["scope"] == "project", sample)
check("the model's citation of a note that was not in the request is gone", "999999" not in text_of, text_of[:300])
check("the model's email address is gone", "me@example.com" not in text_of, text_of[:300])
check("it says what it condenses", "Rolled up from 10 notes" in text_of, text_of[:400])
check("the model saw the old notes and never the protected ones",
      "quokka rota" not in prompts() and "lagoon pump" not in prompts() and "harbour gate" not in prompts() and "new quay" not in prompts())

print("== search finds the roll-up, not the originals")
vq = months[0][0].lower()
found = wait_until(lambda: any(o["id"] in rollup_ids for o in json.loads(tool("search", query=vq, project=project)[1])["observations"]), 60)
hits = [o["id"] for o in json.loads(tool("search", query=vq, project=project)[1])["observations"]]
check("a search for a note's subject finds a roll-up", found, hits)
check("and none of the archived originals", not (set(all_old) & set(hits)), hits)
check("the originals' vectors left the index (thirty notes out, three roll-ups in)", wait_until(lambda: vectors() < vectors_before, 60), (vectors_before, vectors()))

print("== the list of roll-ups, and a restore")
err, text = tool("memory_admin", action="folds", project=project)
folds = json.loads(text)["folds"]
check("three roll-ups are listed, each with ten sources", len(folds) == 3 and all(len(f["sources"]) == 10 for f in folds) and all(f["kind"] == "rollup" for f in folds), folds)
target = next(f for f in folds if f["survivor"] == rollup_ids[0])
err, text = tool("memory_admin", action="restore_fold", id=target["id"])
check("the restore worked", not err, text[:300])
rep = json.loads(text)
check("ten notes are live again and the roll-up is archived", len(rep["restored"]) == 10 and rep["survivor_archived"] is True, rep)
live = live_ids()
check("the month's originals are back and its roll-up is out", set(month_ids[0]) <= live and rollup_ids[0] not in live)
check("the other months are still rolled up", not (set(month_ids[1]) & live) and rollup_ids[1] in live)
found = wait_until(lambda: month_ids[0][0] in [o["id"] for o in json.loads(tool("search", query=vq, project=project)[1])["observations"]], 60)
check("a restored note is found by search again (its vectors are put back)", found)
err, text = tool("memory_admin", action="restore_fold", id=target["id"])
check("restoring it twice is refused", err and "already restored" in text, text[:200])
err, text = tool("memory_admin", action="folds", project=project)
check("it is no longer listed", len(json.loads(text)["folds"]) == 2)
err, text = tool("memory_admin", action="folds", project=project, include_undone=True)
check("unless the undone ones are asked for", len(json.loads(text)["folds"]) == 3)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
