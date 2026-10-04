#!/usr/bin/env python3
"""Projects that are really one: the worker records where a folder came from (the git remote, without credentials) when
a path reaches it, suggests the pairs that share a remote, leaves namesakes with other remotes and a name alone out,
remembers a dismissal, and merges through the existing safe path. Real git repositories, the real worker and MCP server."""
import json, os, re, sqlite3, subprocess, sys, tempfile, time, urllib.error, urllib.request

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
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(f"{E2E}/mcp-duplicates.log", "w"),
                        text=True, env=env, cwd="/")
_id = 0


def rpc(method, params=None):
    global _id
    _id += 1
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": _id, "method": method, **({"params": params} if params else {})}) + "\n")
    proc.stdin.flush()
    return json.loads(proc.stdout.readline())


def raw(tool, **a):
    r = rpc("tools/call", {"name": tool, "arguments": a})["result"]
    return r.get("isError", False), r["content"][0]["text"]


def must(tool, **a):
    is_err, text = raw(tool, **a)
    if is_err:
        raise SystemExit(f"setup step failed: {tool} {a} -> {text[:300]}")
    return text


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
base = os.path.realpath(tempfile.mkdtemp(prefix="e2e-duplicates-"))


def folder(*parts, remote=None):
    d = os.path.join(base, *parts)
    os.makedirs(d)
    if remote is not None:
        subprocess.run(["git", "-C", d, "init", "-q"], check=True)
        if remote:
            subprocess.run(["git", "-C", d, "remote", "add", "origin", remote], check=True)
    return d


def project(d, titles):
    pid = json.loads(must("project_resolve", path=d))["id"]
    for t in titles:
        must("remember", path=d, title=t, text=f"{t}: written by the duplicates suite for {os.path.basename(d)}.")
        time.sleep(0.03)
    return pid


print("== seed: three clones of one repository (two with the same folder name, one renamed), two real namesakes, two plain folders")
shop_a = folder("a", "shop", remote="https://dev:pw-9999@git.example.org/team/shop.git")
shop_b = folder("b", "shop", remote="git@git.example.org:team/shop.git")
web_c = folder("c", "webshop", remote="ssh://git@git.example.org:22/team/shop")
app_x = folder("x", "app", remote="https://git.example.org/team/app.git")
app_y = folder("y", "app", remote="https://git.example.org/clients/app.git")
tool_p = folder("p", "tool")
tool_q = folder("q", "tool")
A = project(shop_a, [f"Shop alpha note {i}" for i in range(5)])
B = project(shop_b, [f"Shop beta note {i}" for i in range(2)])
C = project(web_c, ["Webshop gamma note"])
X = project(app_x, [f"App x note {i}" for i in range(3)])
Y = project(app_y, [f"App y note {i}" for i in range(3)])
P = project(tool_p, [f"Tool p note {i}" for i in range(3)])
Q = project(tool_q, [f"Tool q note {i}" for i in range(3)])


def duplicates():
    return call("GET", "/api/projects/duplicates")[1]


def pairs(d):
    return {frozenset((s["survivor"]["project"], s["other"]["project"])) for s in d["suggestions"]}


print("== the worker learns where each folder came from (in the background, when the path arrives)")
deadline = time.time() + 30
while time.time() < deadline and len(pairs(duplicates())) < 3:
    time.sleep(0.5)
d = duplicates()
check("the three clones of one repository are suggested as three pairs", pairs(d) == {frozenset((A, B)), frozenset((A, C)), frozenset((B, C))}, sorted(map(sorted, pairs(d))))
check("every one is strong evidence, because the remote is the same", all(s["strength"] == "strong" for s in d["suggestions"]), [s["strength"] for s in d["suggestions"]])
check("an https URL with credentials, an scp-like URL and an ssh URL with a port are one repository", all(
    any(r["code"] == "same_remote" and "git.example.org/team/shop" in r["text"] for r in s["reasons"]) for s in d["suggestions"]), d["suggestions"][:1])
ab = next(s for s in d["suggestions"] if {s["survivor"]["project"], s["other"]["project"]} == {A, B})
check("the project with more notes is the one that survives", ab["survivor"]["project"] == A and ab["survivor"]["observations"] == 5, ab["survivor"])
check("two projects with the same name and different remotes are namesakes, not suggested", frozenset((X, Y)) not in pairs(d))
check("two projects that share only a name are not suggested", frozenset((P, Q)) not in pairs(d))
check("automatic merging is off by default", d["auto_merge"] is False)
check("no credential appears in the answer", "pw-9999" not in json.dumps(d) and "dev:" not in json.dumps(d))
db = sqlite3.connect(f"{E2E}/home/.claude-mnemonic/claude-mnemonic.db", timeout=30)
stored = db.execute("SELECT project, remote, root_path FROM project_identities ORDER BY project").fetchall()
db.close()
check("the stored identity is the normalised remote and the checkout root, never the credentials", any(r[0] == A and r[1] == "git.example.org/team/shop" and r[2] == shop_a for r in stored)
      and all("pw-9999" not in r[1] and "@" not in r[1] for r in stored), stored)
check("a folder that is not a repository records nothing", not any(r[0] in (P, Q) for r in stored), stored)

print("== Claude Desktop sees the same through its tools")
text = must("project_manage", action="duplicates")
check("project_manage duplicates lists the pairs with the evidence and how to merge them", "3 pair(s)" in text and "git.example.org/team/shop" in text and "only if the user agrees" in text, text[:400])
res = json.loads(must("project_resolve", name="shop"))
same = {c["project"]: c.get("probably_same_as", []) for c in res.get("candidate_details", [])}
check("resolving the name the clones share says they are probably one project", res.get("ambiguous") and same.get(A) == [B] and same.get(B) == [A], res)
res = json.loads(must("project_resolve", name="app"))
check("resolving the name of real namesakes says nothing of the kind", res.get("ambiguous") and all(not c.get("probably_same_as") for c in res["candidate_details"]), res)
is_err, msg = raw("context", project="shop")
check("asking for the ambiguous name explains that they are probably the same project", is_err and "probably the same project as" in msg, msg)

print("== a dismissal is remembered per pair")
check("dismissing a pair through the MCP tool is accepted", "not the same project" in must("project_manage", action="dismiss", project=A, into=B))
d = duplicates()
check("that pair is gone and the other two pairs of the group stay", pairs(d) == {frozenset((A, C)), frozenset((B, C))}, sorted(map(sorted, pairs(d))))
check("the dismissed pair is listed so it can be taken back", len(d["dismissed"]) == 1 and {d["dismissed"][0]["a"]["project"], d["dismissed"][0]["b"]["project"]} == {A, B})
db = sqlite3.connect(f"{E2E}/home/.claude-mnemonic/claude-mnemonic.db", timeout=30)
check("it is stored, so it survives a restart", db.execute("SELECT COUNT(*) FROM project_duplicate_dismissals").fetchone()[0] == 1)
db.close()
check("a dismissal can be restored", call("POST", "/api/projects/duplicates/restore", {"a": B, "b": A})[0] == 200 and len(pairs(duplicates())) == 3)
check("bad pairs are refused", call("POST", "/api/projects/duplicates/dismiss", {"a": A, "b": A})[0] == 400 and call("POST", "/api/projects/duplicates/dismiss", {"a": "", "b": B})[0] == 400)

print("== a folder that is gone is more evidence, and nothing merges by itself while automatic merging is off")
import shutil
shutil.rmtree(shop_b)
d = duplicates()
ab = next(s for s in d["suggestions"] if {s["survivor"]["project"], s["other"]["project"]} == {A, B})
check("the pair now also says the folder is gone", any(r["code"] == "path_gone" for r in ab["reasons"]), ab["reasons"])
check("and is the kind that automatic merging would take", ab["auto_mergeable"] is True)
time.sleep(4)
code, stats = call("GET", f"/api/projects/{B}/stats")
check("but with automatic merging off it is still there, untouched", code == 200 and stats["observations"] == 2, (code, stats))

print("== the merge is the existing safe one: preview, backup, alias, and the survivor takes over the identities")
preview = must("project_manage", action="merge", project=C, into=A)
check("the preview moves nothing and asks for approval", "Nothing was changed" in preview or "preview" in preview.lower(), preview[:300])
token = re.search(r"\nconfirm: (\S+)", preview)
check("it carries a confirmation token", token is not None, preview[-300:])
done = must("project_manage", action="merge", project=C, into=A, confirm=token.group(1))
check("the confirmed merge reports its backup", "backup" in done.lower(), done)
check("the merged-away project's notes are the survivor's now", call("GET", f"/api/projects/{A}/stats")[1]["observations"] == 6)
d = duplicates()
check("its pairs are gone and the one left is the one with the missing folder", pairs(d) == {frozenset((A, B))}, sorted(map(sorted, pairs(d))))
db = sqlite3.connect(f"{E2E}/home/.claude-mnemonic/claude-mnemonic.db", timeout=30)
owners = {r[0] for r in db.execute("SELECT project FROM project_identities")}
alias = db.execute("SELECT canonical, source FROM project_aliases WHERE alias = ?", (C,)).fetchone()
db.close()
check("the survivor now stands for the merged project's folder too", C not in owners and A in owners, owners)
check("the old id is an alias of the survivor, left by a manual merge", alias is not None and alias[0] == A and alias[1] != "auto-merge", alias)
check("the old id still resolves to the survivor", json.loads(must("project_resolve", id=C))["id"] == A)
check("the worker is healthy", call("GET", "/health")[1].get("ready") is True)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
