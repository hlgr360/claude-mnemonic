#!/usr/bin/env python3
"""Prune / merge projects through the real MCP server and real worker, with real embeddings and a real search path."""
import glob, json, os, re, sqlite3, subprocess, sys, tempfile, time, urllib.request

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
                        stderr=open(f"{E2E}/mcp3.log", "w"), text=True, env=env, cwd="/")
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


def must(name, **a):
    """A setup step: stop at once, naming the step and the worker's answer, instead of failing a check later."""
    err, text = tool(name, **a)
    if err:
        raise SystemExit(f"setup step failed: {name} {a} -> {text[:300]}")
    return text


def api(path, method="GET", body=None):
    req = urllib.request.Request(f"http://localhost:{PORT}{path}", method=method,
                                 data=json.dumps(body).encode() if body is not None else None, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=15) as r:
        raw = r.read()
        return json.loads(raw) if raw else None


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
check("project_manage is listed in desktop mode", "project_manage" in [t["name"] for t in rpc("tools/list")["result"]["tools"]])

base = tempfile.mkdtemp(prefix="e2e-admin-")
dirs = {n: os.path.join(base, n) for n in ("main_proj", "fragment", "doomed")}
for d in dirs.values():
    os.makedirs(d)
ids = {}
notes = {
    "main_proj": ("Retry policy", "The payments service retries failed webhook deliveries with exponential backoff up to five times."),
    "fragment": ("Webhook signing", "Webhook payloads are signed with HMAC SHA256 and the secret rotates every ninety days."),
    "doomed": ("Aquarium plan", "The office aquarium needs a new filter and weekly water tests for the clownfish."),
}
print("== seed three projects through remember(path)")
for name, d in dirs.items():
    ids[name] = json.loads(must("project_resolve", path=d))["id"]
    err, text = tool("remember", path=d, title=notes[name][0], text=notes[name][1], type="discovery")
    check(f"{name} seeded", not err and ids[name] in text, text)
time.sleep(5)  # async vector sync


def search(q):
    err, text = tool("search", query=q)
    return [(o["project"], o["title"]) for o in json.loads(text)["observations"]] if not err else []


check("fragment's note is found under the fragment id before the merge", (ids["fragment"], "Webhook signing") in search("how are webhook payloads signed"), search("how are webhook payloads signed"))
check("doomed's note is searchable before the delete", any(t == "Aquarium plan" for _, t in search("clownfish aquarium filter")))

print("== merge: preview, wrong token, confirm")
err, text = tool("project_manage", action="merge", project=ids["fragment"], into=ids["main_proj"])
token = re.search(r"confirm: (\w+)", text).group(1) if "confirm:" in text else ""
check("merge preview asks for the user's approval and nothing moved", not err and "PREVIEW" in text and token, text[:300])
check("preview changed nothing", json.loads(tool("project_manage", action="stats", project=ids["fragment"])[1])["observations"] == 1)
err, text = tool("project_manage", action="merge", project=ids["fragment"], into=ids["main_proj"], confirm="not-the-token")
check("a wrong token is refused", err and "409" in text, text)
err, text = tool("project_manage", action="merge", project=ids["fragment"], into=ids["main_proj"], confirm=token)
check("confirmed merge reports a backup", not err and "Merged" in text and "backup" in text, text)

backups = sorted(glob.glob(f"{E2E}/home/.claude-mnemonic/backups/snapshot-*merge*.db"))
check("a snapshot file exists", len(backups) == 1, backups)
if backups:
    con = sqlite3.connect(f"file:{backups[0]}?mode=ro", uri=True)
    n = con.execute("select count(*) from observations where project = ?", (ids["fragment"],)).fetchone()[0]
    check("the snapshot still has the fragment's observation (restorable)", n == 1, n)
    con.close()

hits = search("how are webhook payloads signed")
check("the merged note is now found under the surviving project", (ids["main_proj"], "Webhook signing") in hits, hits)
check("and no longer under the fragment id (vector rows re-pointed with real embeddings)", not any(p == ids["fragment"] for p, _ in hits), hits)
res = json.loads(tool("project_resolve", id=ids["fragment"])[1])
check("the fragment id now resolves to the survivor", res["id"] == ids["main_proj"] and res["match"] == "alias", res)
stats = json.loads(tool("project_manage", action="stats", project=ids["main_proj"])[1])
check("survivor holds both notes and lists the alias", stats["observations"] == 2 and ids["fragment"] in stats["aliases"], stats)
rows = {r["project"]: r for r in json.loads(tool("project_list")[1])}
check("the emptied fragment is gone from project_list and the survivor lists it as an alias", ids["fragment"] not in rows and rows[ids["main_proj"]].get("aliases") == [ids["fragment"]], rows)
err, text = tool("remember", project=ids["fragment"], text="Written through the old fragment id after the merge.")
check("writing through the old id lands in the survivor", not err and ids["main_proj"] in text, text)

print("== delete: preview, stale token, confirm")
err, text = tool("project_manage", action="delete", project=ids["doomed"])
dtoken = re.search(r"confirm: (\w+)", text).group(1)
check("delete preview shows what would go and changed nothing", "PREVIEW" in text and "1 observations" in text, text[:300])
must("remember", project=ids["doomed"], title="Added after preview", text="A second aquarium note that arrived after the preview was shown.")
err, text = tool("project_manage", action="delete", project=ids["doomed"], confirm=dtoken)
check("a token from before the data changed is refused", err and "409" in text, text)
time.sleep(4)  # the new note's vectors are synced in the background and are part of the preview's counts
err, text = tool("project_manage", action="delete", project=ids["doomed"])
dtoken = re.search(r"confirm: (\w+)", text).group(1)
err, text = tool("project_manage", action="delete", project=ids["doomed"], confirm=dtoken)
check("confirmed delete succeeds with a backup", not err and "Deleted" in text and "backup" in text, text)
time.sleep(1)
check("the deleted project's vectors are gone from real search", not any("Aquarium" in t or "aquarium" in t.lower() for _, t in search("clownfish aquarium filter water test")), search("clownfish aquarium filter water test"))
err, text = tool("project_manage", action="stats", project=ids["doomed"])
check("stats now says not found", err and "404" in text, text)
check("the other projects are intact", json.loads(tool("project_manage", action="stats", project=ids["main_proj"])[1])["observations"] >= 2)

print("== refusals")
err, text = tool("project_manage", action="delete", project=ids["fragment"])
check("deleting an alias is refused and points at the right move", err and "alias" in text, text)
err, text = tool("project_manage", action="merge", project=ids["main_proj"], into="typo_000000")
check("merging into a project that does not exist is refused", err and "422" in text, text)

print("== integrity: the real database is consistent after all that")
snap = glob.glob(f"{E2E}/home/.claude-mnemonic/backups/*.db")
check("two snapshots were taken (merge, delete)", len(snap) == 2, snap)
con = sqlite3.connect(f"file:{E2E}/home/.claude-mnemonic/claude-mnemonic.db?mode=ro", uri=True)
left = con.execute("select (select count(*) from observations where project=?) + (select count(*) from sdk_sessions where project=?) + (select count(*) from session_summaries where project=?)", (ids["doomed"],)*3).fetchone()[0]
check("nothing remains for the deleted project in observations, sessions or summaries", left == 0, left)
# (the vectors table needs the vec0 extension, which plain sqlite3 lacks: vectors are verified through real search above and by direct SQL in the Go tests)
check("integrity_check ok", con.execute("pragma integrity_check").fetchone()[0] == "ok")
con.close()

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
