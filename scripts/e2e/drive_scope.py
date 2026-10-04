#!/usr/bin/env python3
"""Scope: the rule decides a new note's scope (global only for a general lesson that changed none of the project's
files), a re-scope previews and applies it safely (token, backup, notes chosen by hand are never touched), and a scope
set through the API is pinned."""
import json, os, sqlite3, subprocess, sys, tempfile, urllib.error, urllib.parse, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
DB = f"{E2E}/home/.claude-mnemonic/claude-mnemonic.db"
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
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(f"{E2E}/mcp-scope.log", "w"),
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


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
base = tempfile.mkdtemp(prefix="e2e-scope-")
d = os.path.join(base, "scoped")
os.makedirs(d)
P = json.loads(must("project_resolve", path=d))["id"]
q = urllib.parse.quote(P)

print("== the rule decides the scope of notes that did not say")
notes = [
    ("A general lesson about retries", "Retry with backoff and a cap, never in a tight loop.", ["best-practice"], []),
    ("A general lesson that touched a file", "The retry helper was changed to cap the delay.", ["best-practice"], ["retry.go"]),
    ("An architecture note", "The worker keeps one queue per project.", ["architecture", "testing", "workflow"], []),
    ("A named anti-pattern", "Sharing one global mutex for every request serialises the server.", ["anti-pattern", "tooling"], []),
    ("A plain note", "The staging database is rebuilt every night at two.", [], []),
]
code, body = call("POST", "/api/observations/bulk-import", {"project": P, "observations": [
    {"type": "discovery", "title": t, "narrative": n, "concepts": c, "files_modified": f} for t, n, c, f in notes]})
check("the notes were imported", code == 200 and body.get("imported") == 5, (code, body))


def observations():
    rows = call("GET", f"/api/observations?project={q}&limit=100")[1]
    rows = rows["observations"] if isinstance(rows, dict) else rows
    return {o["title"]: o for o in rows}


obs = observations()
check("a general lesson that changed no file is global", obs[notes[0][0]]["scope"] == "global", obs[notes[0][0]]["scope"])
check("a named anti-pattern is global", obs[notes[3][0]]["scope"] == "global")
check("a general lesson that changed a project file stays in the project", obs[notes[1][0]]["scope"] == "project")
check("the tags most notes carry (architecture, testing, workflow) do not make a note global", obs[notes[2][0]]["scope"] == "project")
check("a note without tags is a project note", obs[notes[4][0]]["scope"] == "project")

print("== a note saved on purpose keeps the scope it was saved with")
code, saved = call("POST", "/api/observations/remember", {"project": P, "title": "Saved on purpose as global", "text": "Always run the linter before a release.", "scope": "global"})
check("remember with an explicit scope", code == 200 and not saved.get("duplicate"), (code, saved))

print("== an archive written by the old rule: almost everything global")
ids = {t: obs[t]["id"] for t, *_ in notes}
db = sqlite3.connect(DB, timeout=30)
for t in (notes[1][0], notes[2][0], notes[4][0]):  # the old rule made these global, and recorded it as its own decision
    db.execute("UPDATE observations SET scope = 'global', scope_source = 'auto' WHERE id = ?", (ids[t],))
db.commit()
db.close()

print("== the preview shows what would change, and changes nothing")
code, pre = call("GET", "/api/scope/preview")
check("it reports three notes to project and none to global", code == 200 and pre["to_project"] == 3 and pre["to_global"] == 0, pre)
check("the note saved on purpose is kept by hand, not planned", pre["protected"] >= 1 and all(c["title"] != "Saved on purpose as global" for c in pre["sample"]), pre["protected"])
check("the sample names the notes with their old and new scope", {c["title"] for c in pre["sample"] if c["project"] == P} == {notes[1][0], notes[2][0], notes[4][0]} and all((c["from"], c["to"]) == ("global", "project") for c in pre["sample"] if c["project"] == P))
check("it gives a token and says it is a dry run", pre["dry_run"] is True and pre["confirm"])
check("nothing changed yet", observations()[notes[2][0]]["scope"] == "global")

print("== applying needs the token of a preview")
check("without a token it is refused", call("POST", "/api/scope/apply", {})[0] == 400)
check("with a wrong token it is refused", call("POST", "/api/scope/apply", {"confirm": "0000000000000000"})[0] == 409)

print("== applying takes a backup first and changes only what the preview said")
code, done = call("POST", "/api/scope/apply", {"confirm": pre["confirm"]})
check("three notes changed", code == 200 and done["changed"] == 3, (code, done))
check("a backup of the database exists", bool(done.get("backup")) and os.path.exists(done["backup"]), done.get("backup"))
obs = observations()
check("the notes the old rule over-globalised are project notes now", all(obs[t]["scope"] == "project" for t in (notes[1][0], notes[2][0], notes[4][0])))
check("the real lessons stay global", obs[notes[0][0]]["scope"] == "global" and obs[notes[3][0]]["scope"] == "global")
check("the note saved on purpose is untouched", obs["Saved on purpose as global"]["scope"] == "global")
code, again = call("GET", "/api/scope/preview")
check("a second preview has nothing to do", again["to_project"] + again["to_global"] == 0, again)
check("applying that does nothing and takes no backup", call("POST", "/api/scope/apply", {"confirm": again["confirm"]})[1].get("backup") in (None, ""))

print("== a scope chosen through the API is pinned")
check("editing a scope is accepted", call("PUT", f"/api/observations/{ids[notes[2][0]]}", {"scope": "global"})[0] == 200)
db = sqlite3.connect(DB, timeout=30)
db.execute("UPDATE observations SET scope = 'global', scope_source = 'auto' WHERE id = ?", (ids[notes[4][0]],))
db.commit()
db.close()
code, pre = call("GET", "/api/scope/preview")
changed_titles = {c["title"] for c in pre["sample"] if c["project"] == P}
check("a re-scope would change the note the old rule globalised but not the one I set by hand", changed_titles == {notes[4][0]}, changed_titles)
code, edited = call("PUT", f"/api/observations/{ids[notes[4][0]]}", {"title": notes[4][0] + " (edited)"})
check("a title edit is accepted", code == 200, (code, edited))
code, pre = call("GET", "/api/scope/preview")
sampled = {c["title"] for c in pre["sample"] if c["project"] == P}
check("an edit that is not about the scope does not pin it", sampled == {notes[4][0] + " (edited)"}, sampled)

print("== the worker is still healthy")
check("the worker is healthy", call("GET", "/health")[1].get("ready") is True)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
