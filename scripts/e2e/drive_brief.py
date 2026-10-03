#!/usr/bin/env python3
"""Project briefs: written automatically and on request by the real worker (with a fake claude), stored once per
project, cleaned, dated, and shown first to Desktop through the real MCP server."""
import datetime, json, os, re, subprocess, sys, tempfile, time, urllib.error, urllib.parse, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
PROMPTS = os.path.join(E2E, "fake-claude-prompts.log")
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


def call(method, path):
    req = urllib.request.Request(f"http://localhost:{PORT}{path}", method=method)
    try:
        with urllib.request.urlopen(req, timeout=180) as r:
            raw = r.read().strip()
            return r.status, (json.loads(raw) if raw[:1] in (b"{", b"[") else raw.decode())
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(f"{E2E}/mcp-brief.log", "w"),
                        text=True, env=env, cwd="/")
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
    err, text = tool(name, **a)
    if err:
        raise SystemExit(f"setup step failed: {name} {a} -> {text[:300]}")
    return text


rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
today = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d")
base = tempfile.mkdtemp(prefix="e2e-brief-")

# Seeded straight away, because the first automatic pass runs 30 s after the worker started.
print("== seed: three projects, one with a checkpoint note that has private text")
dirs = {k: os.path.join(base, k) for k in ("shipper", "tiny", "noted")}
ids, titles = {}, {}
for k, d in dirs.items():
    os.makedirs(d)
    ids[k] = json.loads(must("project_resolve", path=d))["id"]
for i in range(4):
    titles[("shipper", i)] = f"Shipper note {i}"
    must("remember", path=dirs["shipper"], title=titles[("shipper", i)], text=f"The shipper project learned something durable, number {i}.")
for i in range(2):
    must("remember", path=dirs["tiny"], title=f"Tiny note {i}", text=f"A small project with few notes, number {i}.")
for i in range(4):
    must("remember", path=dirs["noted"], title=f"Noted note {i}", text=f"A project with a checkpoint, number {i}.")
must("checkpoint", project=ids["shipper"], thread="Release work", goal="ship the <private>secret roadmap</private> release",
     progress="tests written", decisions="names, not ids", next_steps="write the docs")


def get(pid):
    return call("GET", f"/api/projects/{urllib.parse.quote(pid)}/brief")


def prompts():
    try:
        return [c for c in open(PROMPTS, encoding="utf-8").read().split("=====END=====\n") if "PROJECT BRIEF REQUEST" in c]
    except OSError:
        return []


print("== the automatic pass writes the briefs that are due (and only those)")
deadline = time.time() + 120
while time.time() < deadline and not (get(ids["shipper"])[0] == 200 and get(ids["noted"])[0] == 200):
    time.sleep(1)
code, brief = get(ids["shipper"])
check("a project with enough observations got a brief on its own", code == 200, (code, brief))
check("so did the other one", get(ids["noted"])[0] == 200)
check("a project below the threshold did not", get(ids["tiny"])[0] == 404)
if code != 200:
    print(f"\n{ok} passed, {fail} failed")
    sys.exit(1)

print("== what was stored")
text = brief["text"]
check("it is dated and says what it was written from", text.startswith(f"As of {today}, written from 4 of 4 observations and 1 checkpoint note. It can lag behind recent work."), text[:200])
check("the structured answer says the same", brief["as_of"] == today and brief["source"] == "4 of 4 observations and 1 checkpoint note", brief)
check("the model's sections are kept", "## What this is" in text and "## Current state\nIt works." in text)
cited = re.findall(r"\[#(\d+)\]", text)
check("a citation of a real observation stays", len(cited) == 1 and cited[0] != "999999", cited)
check("a citation of an observation that was not in the request is removed", "999999" not in text)
check("an email address is removed", "me@example.com" not in text and "[email removed]" in text)
check("the model's own 'open items' are dropped", "invented open item" not in text and "## Open items" not in text)
check("the open threads come from the checkpoint note", "## Open threads (from your checkpoint notes)\n- Release work (updated" in text and "write the docs" in text, text[-260:])

print("== what the model was asked")
sent = [p for p in prompts() if "PROJECT: shipper" in p]
check("the model was asked once for that project", len(sent) == 1, len(sent))
if sent:
    p = sent[0]
    check("with the brief system prompt, not the extraction one", "You maintain a short project brief" in p and "memory extraction agent" not in p)
    check("with the date, the observations and their ids", f"AS OF: {today}" in p and "Shipper note 0" in p and "Shipper note 3" in p and re.search(r"\[#\d+\] \(discovery, ", p))
    check("with the checkpoint note as context, but not its next steps", "Release work" in p and "ship the" in p and "write the docs" not in p)
    check("and without the private text", "secret roadmap" not in p)

print("== it is stored once per project, as a summary the dashboard shows")
rows = call("GET", f"/api/summaries?project={urllib.parse.quote(ids['shipper'])}&limit=50")[1]
briefs = [r for r in rows if (r.get("request") or "") == "Project brief"]
check("one brief row, next to the checkpoint note", len(briefs) == 1 and any((r.get("request") or "") == "Release work" for r in rows), [r.get("request") for r in rows])

print("== Desktop receives it first")
err, text = tool("catch_up", project=ids["shipper"])
check("catch_up starts with the brief", not err and text.startswith("Project brief for "), text[:160])
check("followed by the thread notes", text.index("Thread: Release work") > text.index("## What this is"))
err, text = tool("context", project=ids["shipper"])
check("context starts with the brief, then the raw observations", not err and text.startswith("Project brief (it can lag behind recent work):") and "Saved observations (raw):" in text, text[:160])
err, text = tool("context", project=ids["tiny"])
check("a project without a brief gets context exactly as before", not err and "Project brief" not in text)
err, text = tool("catch_up", project=ids["tiny"])
check("and catch_up without it", not err and "Project brief" not in text)

print("== on request, whether or not the threshold is reached")
code, body = call("POST", f"/api/projects/{urllib.parse.quote(ids['tiny'])}/brief")
check("a small project can be given a brief by hand", code == 200 and "As of " in body["text"] and body["source"] == "2 of 2 observations", (code, body))
err, text = tool("catch_up", project=ids["tiny"])
check("and then Desktop gets it", not err and text.startswith("Project brief for "))
before = len(prompts())
code, again = call("POST", f"/api/projects/{urllib.parse.quote(ids['shipper'])}/brief")
check("asking again rewrites it", code == 200 and len(prompts()) == before + 1, (code, len(prompts()), before))
rows = call("GET", f"/api/summaries?project={urllib.parse.quote(ids['shipper'])}&limit=50")[1]
check("in place: still one brief row", len([r for r in rows if (r.get("request") or "") == "Project brief"]) == 1)
check("a project that does not exist is refused", call("POST", "/api/projects/invented_zzzzzz/brief")[0] == 422)
check("so is a project reference that is not one", call("GET", "/api/projects/..%2Fetc/brief")[0] == 400)

print("== nothing more is written when nothing is new")
n = len(prompts())
time.sleep(70)  # more than the pass interval of one minute
check("a later automatic pass leaves the briefs alone", len(prompts()) == n, (n, len(prompts())))

print("== the status shows the task")
st = call("GET", "/api/llm/status")[1]
check("the brief task runs on the Claude CLI", st["backends"].get("brief") == "claude", st["backends"])
check("the worker is healthy", call("GET", "/health")[1].get("ready") is True)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
