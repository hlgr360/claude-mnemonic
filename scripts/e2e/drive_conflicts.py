#!/usr/bin/env python3
"""Conflict review: the real worker proposes pairs through a fake claude, a person decides through the API,
decided notes are hidden from sessions and search (and only those), undo restores them, and a "keep both"
is remembered."""
import json, os, re, subprocess, sys, tempfile, time, urllib.error, urllib.parse, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
PROMPTS = os.path.join(E2E, "fake-claude-prompts.log")
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
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(f"{E2E}/mcp-conflicts.log", "w"),
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
    time.sleep(0.05)  # the newer note must really be newer


def prompts():
    try:
        return [c for c in open(PROMPTS, encoding="utf-8").read().split("=====END=====\n") if "CONFLICT CHECK REQUEST" in c]
    except OSError:
        return []


def injected(project):
    return [o["id"] for o in call("GET", f"/api/context/inject?project={urllib.parse.quote(project)}")[1].get("observations", [])]


def found(project, query):
    return [o["id"] for o in call("GET", f"/api/context/search?project={urllib.parse.quote(project)}&query={urllib.parse.quote(query)}")[1].get("observations", [])]


def listing(query="", status="open"):
    return call("GET", f"/api/conflicts?status={status}{query}")[1]


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
base = tempfile.mkdtemp(prefix="e2e-conflicts-")
dirs = {k: os.path.join(base, k) for k in ("rates", "other")}
ids = {}
for k, d in dirs.items():
    os.makedirs(d)
    ids[k] = json.loads(must("project_resolve", path=d))["id"]

# Seeded straight away, because the first automatic pass runs 45 s after the worker started.
print("== seed: two notes about one setting, a note about something else, a similar note in another project")
remember(dirs["rates"], "Shipping rate cache lives for 60 minutes", "Shipping rates are cached in Redis with a time to live of 60 minutes.")
remember(dirs["rates"], "Label printing uses PDF output", "Parcel labels are rendered as PDF files and sent to the warehouse printer queue.")
remember(dirs["rates"], "Shipping rate cache now lives for 24 hours", "Shipping rates are cached in Redis and the time to live is now 24 hours instead of 60 minutes.")
remember(dirs["other"], "Shipping rate cache lives for 30 minutes", "In the other project shipping rates are cached in Redis for 30 minutes.")


def note_id(project, word):
    rows = call("GET", f"/api/observations?project={urllib.parse.quote(project)}&limit=50")[1]
    rows = rows["observations"] if isinstance(rows, dict) else rows
    return next(o["id"] for o in rows if word in o["title"])


older = note_id(ids["rates"], "60 minutes")
newer = note_id(ids["rates"], "24 hours")
labels = note_id(ids["rates"], "PDF")
foreign = note_id(ids["other"], "30 minutes")

print("== the proposer finds the pair on its own")
deadline = time.time() + 150
while time.time() < deadline and listing()["open_count"] == 0:
    time.sleep(2)
got = listing(f"&project={urllib.parse.quote(ids['rates'])}")
check("one proposal is waiting for a decision", got["open_count"] == 1 and got["total"] == 1, got["open_count"])
c = got["conflicts"][0] if got["conflicts"] else {}
check("it is about the two notes, older and newer the right way round", (c.get("older", {}).get("id"), c.get("newer", {}).get("id")) == (older, newer), c)
check("with the model's relation, confidence and reason", (c.get("relation"), c.get("confidence"), c.get("proposer")) == ("supersedes", "high", "llm") and c.get("reason"), c)
check("both notes come with it in full", c.get("older", {}).get("narrative", "").endswith("60 minutes.") and "24 hours" in c.get("newer", {}).get("narrative", ""))
check("the count agrees", call("GET", f"/api/conflicts/count?project={urllib.parse.quote(ids['rates'])}")[1] == {"open": 1})
check("the other project's note is not part of it", foreign not in (c.get("older", {}).get("id"), c.get("newer", {}).get("id")) and listing(f"&project={urllib.parse.quote(ids['other'])}")["total"] == 0)

print("== a proposal hides nothing")
# A session's context groups notes that are alike, so a note can be absent from it without being hidden. What a
# proposal must not do is change it: remember what sessions see now and compare after each decision and undo.
baseline = sorted(injected(ids["rates"]))
check("sessions see the newer note and the unrelated one", newer in baseline and labels in baseline, baseline)
cid = c["id"]

print("== what the model was asked")
sent = [p for p in prompts() if "Shipping rate cache now lives for 24 hours" in p]
check("once about the newer note", len(sent) == 1, len(sent))
if sent:
    p = sent[0]
    check("with the conflict system prompt, not the extraction one", "Use supersedes ONLY if" in p and "memory extraction agent" not in p)
    check("showing the older note of the same project", "Shipping rate cache lives for 60 minutes" in p)
    check("and not the note of the other project", "30 minutes" not in p)
    check("not the unrelated note either", "Label printing" not in p)

print("== deciding: newer replaces older")
code, body = call("POST", f"/api/conflicts/{cid}/resolve", {"decision": "supersede_older"})
check("the decision is accepted and recorded", code == 200 and body["resolved"] and body["decision"] == "supersede_older" and body["superseded_obs_id"] == older, (code, body))
check("no retention is configured, so nothing is promised to be deleted", "restorable_until_epoch" not in body)
check("the older note is no longer injected, and nothing else changed", older not in injected(ids["rates"]) and sorted(injected(ids["rates"])) == baseline, injected(ids["rates"]))
check("it is no longer found by a prompt search", older not in found(ids["rates"], "shipping rate cache time to live"))
feed = call("GET", f"/api/observations?project={urllib.parse.quote(ids['rates'])}&limit=50")[1]
feed = feed["observations"] if isinstance(feed, dict) else feed
hidden = next((o for o in feed if o["id"] == older), None)
check("it is still in the dashboard feed, marked", hidden is not None and hidden.get("is_superseded") is True, hidden)
check("and can be fetched by id", call("GET", f"/api/observations/{older}")[0] == 200)
check("the open list is empty, the decided one has it", listing()["open_count"] == 0 and listing(status="resolved")["total"] == 1)
check("deciding twice is refused", call("POST", f"/api/conflicts/{cid}/resolve", {"decision": "keep_both"})[0] == 409)
check("an unknown decision is refused", call("POST", f"/api/conflicts/{cid}/resolve", {"decision": "burn"})[0] in (400, 409))

print("== undo")
code, body = call("POST", f"/api/conflicts/{cid}/undo")
check("the proposal is open again", code == 200 and body["resolved"] is False and listing()["open_count"] == 1, (code, body))
check("sessions see what they saw before the decision", sorted(injected(ids["rates"])) == baseline, injected(ids["rates"]))
check("undoing an open proposal is refused", call("POST", f"/api/conflicts/{cid}/undo")[0] == 409)

print("== deciding the other way, then keeping both")
code, body = call("POST", f"/api/conflicts/{cid}/resolve", {"decision": "supersede_newer"})
check("older replaces newer hides the newer note, and the older one shows again", code == 200 and body["superseded_obs_id"] == newer and newer not in injected(ids["rates"]) and older in injected(ids["rates"]), (code, body))
call("POST", f"/api/conflicts/{cid}/undo")
code, body = call("POST", f"/api/conflicts/{cid}/resolve", {"decision": "keep_both"})
check("keep both hides nothing", code == 200 and not body.get("superseded_obs_id") and sorted(injected(ids["rates"])) == baseline, (code, injected(ids["rates"])))

print("== a person can propose a pair too")
code, body = call("POST", "/api/conflicts", {"older_id": newer, "newer_id": labels, "reason": "just checking"})
check("given in the wrong order, it is put in order by age, as a manual proposal", code == 201 and body["proposer"] == "manual" and (body["older"]["id"], body["newer"]["id"]) == (labels, newer), (code, body["older"]["id"], body["newer"]["id"]))
check("a pair that is already known answers with the existing proposal", call("POST", "/api/conflicts", {"older_id": newer, "newer_id": older})[0] == 200 and listing(status="all")["total"] == 2)
check("a pair from two projects is refused", call("POST", "/api/conflicts", {"older_id": older, "newer_id": foreign})[0] == 422)
check("so is a note that does not exist", call("POST", "/api/conflicts", {"older_id": older, "newer_id": 99999999})[0] == 404)
manual = body["id"]
check("one proposal is open now (the manual one)", listing()["open_count"] == 1)
call("POST", f"/api/conflicts/{manual}/resolve", {"decision": "keep_both"})

print("== a decided pair is not proposed again")
n = len(prompts())
time.sleep(75)  # more than the pass interval of one minute
check("a later automatic pass asks nothing and proposes nothing", len(prompts()) == n and listing()["open_count"] == 0, (n, len(prompts()), listing()["open_count"]))

print("== the status shows the task")
st = call("GET", "/api/llm/status")[1]
check("the conflict task runs on the Claude CLI", st["backends"].get("conflict") == "claude", st["backends"])
check("the worker is healthy", call("GET", "/health")[1].get("ready") is True)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
