#!/usr/bin/env python3
"""Project names instead of ids, and safe handling of two projects with the same name. Real binaries."""
import json, os, subprocess, sys, tempfile, time

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
                        stderr=open(f"{E2E}/mcp4.log", "w"), text=True, env=env, cwd="/")
_id = 0


def rpc(method, params=None):
    global _id
    _id += 1
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": _id, "method": method, **({"params": params} if params else {})}) + "\n")
    proc.stdin.flush()
    return json.loads(proc.stdout.readline())


def tool(tool_name, **a):
    r = rpc("tools/call", {"name": tool_name, "arguments": a})["result"]
    return r.get("isError", False), r["content"][0]["text"]


def must(tool_name, **a):
    """A setup step: stop at once, naming the step and the worker's answer, instead of failing a check later."""
    err, text = tool(tool_name, **a)
    if err:
        raise SystemExit(f"setup step failed: {tool_name} {a} -> {text[:300]}")
    return text


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})

base = tempfile.mkdtemp(prefix="e2e-names-")
dirs = {"app_a": os.path.join(base, "work", "app"), "app_b": os.path.join(base, "work", "clients", "app"), "solo": os.path.join(base, "other", "solo")}
for d in dirs.values():
    os.makedirs(d)
notes = {"app_a": ("Release checklist", "The release checklist requires a signed tag and a changelog entry."),
         "app_b": ("Customer onboarding", "Onboarding a customer needs an API key and a welcome email.")}
ids = {k: json.loads(must("project_resolve", path=d))["id"] for k, d in dirs.items()}

print("== two folders both called app, one called solo")
check("the two app folders get different ids", ids["app_a"] != ids["app_b"] and ids["app_a"].startswith("app_") and ids["app_b"].startswith("app_"), ids)
for k, (title, text) in notes.items():
    must("remember", path=dirs[k], title=title, text=text)
    must("remember", path=dirs[k], title=title + " (2)", text=text + " Second note.")
must("remember", path=dirs["solo"], title="Solo note", text="A note in the only project with this name.")
time.sleep(4)

print("== project_list shows names, and tells namesakes apart")
rows = {r["project"]: r for r in json.loads(tool("project_list")[1])}
check("a unique name is both label and use", rows[ids["solo"]]["label"] == "solo" and rows[ids["solo"]]["use"] == "solo", rows[ids["solo"]])
for k in ("app_a", "app_b"):
    r = rows[ids[k]]
    check(f"namesake {k}: use is the id, label says what it holds", r["use"] == ids[k] and r["label"].startswith("app (2 observations") and "last used" in r["label"], r)
check("the two labels differ", rows[ids["app_a"]]["label"] != rows[ids["app_b"]]["label"])
check("sample titles are included", rows[ids["app_a"]].get("sample_titles") and rows[ids["app_b"]].get("sample_titles"))

print("== project_suggest carries label and use")
err, text = tool("project_suggest", opening_text="how do we onboard a customer")
body = json.loads(text.split("\n\nNext:")[0])
check("suggestions have label and use", all("label" in s and "use" in s for s in body["suggestions"]), body)
check("the right namesake ranks first and is addressed by id", body["suggestions"][0]["project"] == ids["app_b"] and body["suggestions"][0]["use"] == ids["app_b"], body["suggestions"][:2])
check("the hint tells the model to use labels", "label" in text.split("\n\nNext:")[1])

print("== a unique name works everywhere")
res = json.loads(tool("project_resolve", name="solo")[1])
check("resolve by name", res["id"] == ids["solo"] and res["match"] == "name", res)
err, text = tool("context", project="solo")
check("context by name", not err and "Solo note" in text, text[:200])
err, text = tool("remember", project="solo", text="Saved through the plain name.")
check("remember by name lands in the right project", not err and ids["solo"] in text, text)
err, text = tool("project_manage", action="stats", project="SOLO")
check("project_manage stats by name, case-insensitively", not err and json.loads(text)["project"] == ids["solo"], text)
err, text = tool("project_manage", action="delete", project="solo")
check("delete by name is still only a preview", not err and "PREVIEW" in text and ids["solo"] in text, text[:250])
check("and nothing was deleted", json.loads(tool("project_manage", action="stats", project=ids["solo"])[1])["observations"] >= 2)

print("== a shared name is never guessed")
res = json.loads(tool("project_resolve", name="app")[1])
check("resolve reports it as ambiguous with details", res["match"] == "none" and res["ambiguous"] and len(res["candidate_details"]) == 2 and not res.get("id"), res)
check("the details say how to tell them apart", all("observations" in c["detail"] and "last used" in c["detail"] for c in res["candidate_details"]), res["candidate_details"])
before = {k: json.loads(tool("project_manage", action="stats", project=ids[k])[1])["observations"] for k in ("app_a", "app_b")}
for label, call in {"remember": lambda: tool("remember", project="app", text="Should go nowhere."),
                    "context": lambda: tool("context", project="app"),
                    "project_manage stats": lambda: tool("project_manage", action="stats", project="app"),
                    "project_manage delete": lambda: tool("project_manage", action="delete", project="app"),
                    "project_manage merge": lambda: tool("project_manage", action="merge", project=ids["solo"], into="app")}.items():
    err, text = call()
    check(f"{label} refuses and asks which one", err and '2 projects are called "app"' in text and ids["app_a"] in text and ids["app_b"] in text and "Ask the user" in text, text[:300])
after = {k: json.loads(tool("project_manage", action="stats", project=ids[k])[1])["observations"] for k in ("app_a", "app_b")}
check("nothing was written or changed in either namesake", before == after, (before, after))

print("== a namesake is still reachable by id")
err, text = tool("remember", project=ids["app_a"], text="Saved by id, to the first app.")
check("remember by id works", not err and ids["app_a"] in text, text)
err, text = tool("context", project=ids["app_b"])
check("context by id works", not err and "onboard" in text.lower(), text[:200])

print("== aliases are not namesakes")
must("project_manage", action="alias", alias="app_fffffe", project=ids["app_a"])
rows = {r["project"]: r for r in json.loads(tool("project_list")[1])}
check("an alias of an app does not change the labels", rows[ids["app_a"]]["use"] == ids["app_a"] and rows[ids["app_b"]]["use"] == ids["app_b"])
err, text = tool("project_manage", action="delete", project="app_fffffe")
check("an alias is still refused for destructive actions", err and "alias" in text, text[:200])

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
