#!/usr/bin/env python3
"""Seed projects for the browser test through the real MCP server; print their ids as JSON."""
import json, os, sqlite3, subprocess, tempfile, time
E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
p = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, env=env, cwd="/")
n = 0
def rpc(m, params=None):
    global n; n += 1
    p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": n, "method": m, **({"params": params} if params else {})}) + "\n"); p.stdin.flush()
    return json.loads(p.stdout.readline())
def tool(name, **a):
    r = rpc("tools/call", {"name": name, "arguments": a})["result"]
    if r.get("isError"): raise SystemExit("seed step failed: %s %s -> %s" % (name, a, r["content"][0]["text"]))
    return r["content"][0]["text"]
rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})
base = tempfile.mkdtemp(prefix="ui-e2e-"); ids = {}
for name, (title, text) in {"main_proj": ("Retry policy", "Webhook deliveries retry with exponential backoff."),
                            "fragment": ("Signing", "Payloads are signed with HMAC SHA256."),
                            "doomed": ("Aquarium", "The office aquarium needs a filter."),
                            "spare": ("Spare note", "Another project used for the merge flow.")}.items():
    d = os.path.join(base, name); os.makedirs(d)
    ids[name] = json.loads(tool("project_resolve", path=d))["id"]
    tool("remember", path=d, title=title, text=text)
tool("project_manage", action="alias", alias="old-fragment_abcdef", project=ids["main_proj"])

# Two projects that the old dropdown could not show: one with only an observation (its session row is gone, as after
# a cleanup) and one with only session summaries. The dropdown and the manager must both list them.
d = os.path.join(base, "obs_only"); os.makedirs(d)
ids["obs_only"] = json.loads(tool("project_resolve", path=d))["id"]
tool("remember", path=d, title="Observation without a session", text="This project survives only through its observation.")
ids["summ_only"] = "summ-only_a1b2c3"
db = sqlite3.connect(f"{E2E}/home/.claude-mnemonic/claude-mnemonic.db", timeout=30)
db.execute("DELETE FROM sdk_sessions WHERE project = ?", (ids["obs_only"],))
now = int(time.time() * 1000)
for i, title in enumerate(("First summary", "Second summary")):
    db.execute("INSERT INTO session_summaries (created_at, sdk_session_id, project, request, created_at_epoch) VALUES (?, ?, ?, ?, ?)",
               (time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), f"sdk-summ-only-{i}", ids["summ_only"], title, now + i))
db.commit(); db.close()
print(json.dumps(ids))
p.stdin.close(); p.wait(timeout=10)
