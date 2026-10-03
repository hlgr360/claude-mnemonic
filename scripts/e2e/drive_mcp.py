#!/usr/bin/env python3
"""Drive the real mcp-server over stdio the way Claude Desktop chat does, against a real isolated worker."""
import json, os, subprocess, sys, tempfile, time, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
WORKER = f"http://localhost:{PORT}"
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    if cond:
        ok += 1
        print(f"  PASS  {name}")
    else:
        fail += 1
        print(f"  FAIL  {name}  {detail}")


env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                        stderr=open(f"{E2E}/mcp.log", "w"), text=True, env=env, cwd="/")
_id = 0


def rpc(method, params=None, notify=False):
    global _id
    msg = {"jsonrpc": "2.0", "method": method}
    if params is not None:
        msg["params"] = params
    if not notify:
        _id += 1
        msg["id"] = _id
    proc.stdin.write(json.dumps(msg) + "\n")
    proc.stdin.flush()
    if notify:
        return None
    return json.loads(proc.stdout.readline())


def tool(name, **args):
    r = rpc("tools/call", {"name": name, "arguments": args})["result"]
    return r.get("isError", False), r["content"][0]["text"]


def api(path, method="GET", body=None):
    req = urllib.request.Request(WORKER + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        raw = r.read()
        return json.loads(raw) if raw else None


def git(cwd, *a):
    subprocess.run(["git", *a], cwd=cwd, check=True, capture_output=True,
                   env=dict(os.environ, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@e.x", GIT_COMMITTER_NAME="t",
                            GIT_COMMITTER_EMAIL="t@e.x", GIT_CONFIG_GLOBAL="/dev/null"))


print("== handshake as Claude Desktop chat")
init = rpc("initialize", {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "claude-ai", "version": "0.1.0"}})["result"]
rpc("notifications/initialized", notify=True)
check("instructions are returned in desktop mode", "project_suggest" in init.get("instructions", ""))
names = [t["name"] for t in rpc("tools/list")["result"]["tools"]]
check("project tools are listed", all(n in names for n in ["project_suggest", "project_resolve", "project_list", "context", "remember"]), names)
check("existing tools are still listed", "search" in names and "observation" in names)

print("== nothing exists yet; a write with no project is refused")
err, text = tool("remember", text="a thought from a declined chat")
check("remember without a project is an error", err and "a project is required" in text, text)
check("and nothing was stored", api("/api/projects/summary") == [])

base = tempfile.mkdtemp(prefix="e2e-")
main_repo = os.path.join(base, "knowledge_base")
os.makedirs(main_repo)
git(main_repo, "init", "-q")
open(os.path.join(main_repo, "a.txt"), "w").write("x")
git(main_repo, "add", ".")
git(main_repo, "commit", "-q", "-m", "init")
worktree = os.path.join(base, "wt-random-name")
git(main_repo, "worktree", "add", "-q", "-b", "side", worktree)
other = os.path.join(base, "billing_api")
os.makedirs(other)

print("== Cowork with a folder: the model passes the host path")
err, text = tool("project_resolve", path=main_repo)
res = json.loads(text)
check("path resolves to an id (no history yet)", not err and res["match"] == "path" and res["known"] is False, text)
kb_id = res["id"]
check("id has the plugin's name_hash shape", kb_id.startswith("knowledge_base_") and len(kb_id.split("_")[-1]) == 6, kb_id)

err, text = tool("project_resolve", path=worktree)
wt = json.loads(text)
check("a linked worktree resolves to the MAIN repo's id", wt["id"] == kb_id, f"{wt} vs {kb_id}")

err, text = tool("remember", path=worktree, title="Indexer uses bge-small", type="decision",
                 text="The knowledge base indexer embeds documents with bge-small and stores vectors in sqlite-vec.")
check("remember via a worktree path creates the main repo's project", not err and kb_id in text, text)
err, text = tool("remember", path=main_repo, title="Indexer uses bge-small", type="decision",
                 text="The knowledge base indexer embeds documents with bge-small and stores vectors in sqlite-vec.")
check("repeating the same memory is deduplicated", not err and "Already saved" in text, text)

err, text = tool("remember", path=other, title="AWX rotation", type="discovery",
                 text="AWX credentials are rotated monthly by a pipeline that writes them to Key Vault.")
check("a second folder becomes a second project", not err, text)
awx_id = json.loads(tool("project_resolve", path=other)[1])["id"]

print("== project list and unknown-project refusal")
rows = json.loads(tool("project_list")[1])
check("both projects are listed with counts", {r["project"] for r in rows} == {kb_id, awx_id} and all(r["observations"] == 1 for r in rows), rows)
err, text = tool("remember", project="made-up_zzzzzz", text="x")
check("an invented project is refused with guidance", err and "unknown project" in text, text)

print("== Chat: suggest by content (vector sync is async, so allow it a moment)")
time.sleep(4)
err, text = tool("project_suggest", opening_text="how are documents embedded and stored for the indexer?")
body = json.loads(text.split("\n\nNext:")[0])
check("suggest ranks the matching project first", body["suggestions"] and body["suggestions"][0]["project"] == kb_id, body)
check("vector search was used", body["vector_used"] is True, body)
check("the result repeats the protocol", "ask the user" in text and "do not call remember" in text)

err, text = tool("project_suggest", opening_text="rotate the awx credentials in key vault")
body = json.loads(text.split("\n\nNext:")[0])
check("a different topic ranks the other project first", body["suggestions"][0]["project"] == awx_id, body)

print("== Declined chat: read-only cross-project search")
err, text = tool("search", query="credentials rotated monthly key vault")
res = json.loads(text)
check("search with no project spans projects", not err and any(o["project"] == awx_id for o in res["observations"]), text[:300])
err, text = tool("search", query="embed documents sqlite-vec indexer")
check("and finds the other project too", any(o["project"] == kb_id for o in json.loads(text)["observations"]), text[:300])

print("== Context for a chosen project")
err, text = tool("context", project=kb_id)
check("context loads the project's memory", not err and "bge-small" in text, text[:300])
err, text = tool("context", project="knowledge_base")
check("a unique name works too", not err and "bge-small" in text, text[:200])

print("== Alias: a fragment resolves to its canonical project")
frag = "working-directory-setup-fc06bf_e5a4ab"
api("/api/projects/aliases", "POST", {"alias": frag, "canonical": kb_id, "source": "e2e"})
res = json.loads(tool("project_resolve", id=frag)[1])
check("alias id resolves to the canonical project", res["id"] == kb_id and res["match"] == "alias", res)
err, text = tool("remember", project=frag, text="Written through the alias; must land in the canonical project.")
check("a write through an alias lands in the canonical project", not err and kb_id in text, text)
rows = {r["project"]: r for r in json.loads(tool("project_list")[1])}
check("summary shows the alias relation", rows[kb_id].get("aliases") == [frag] and rows[kb_id]["observations"] == 2, rows[kb_id])

print("== Persistence check straight from the worker API")
obs = api(f"/api/observations?project={kb_id}&limit=20")
obs = obs["observations"] if isinstance(obs, dict) else obs
check("stored observations carry the source client", any("claude-ai" in " ".join(o.get("facts") or []) for o in obs), [o.get("facts") for o in obs])

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
