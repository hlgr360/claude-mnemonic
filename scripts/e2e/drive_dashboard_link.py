#!/usr/bin/env python3
"""Where the dashboard is: the real session-start hook tells the user once per version (as a message for the user, never in
the model's context), the real MCP server gives Desktop the link, and the link is the live worker's address. Claude Code's
tool list is unchanged."""
import json, os, shutil, subprocess, sys, tempfile, time, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
URL = f"http://localhost:{PORT}"
MARKER = os.path.join(E2E, "home", ".claude-mnemonic", ".dashboard-announced")
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")


def mcp(*extra):
    return subprocess.Popen([f"{E2E}/bin/mcp-server", *extra], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=open(f"{E2E}/mcp-dashboard.log", "a"), text=True, env=env, cwd="/")


class Client:
    def __init__(self, client_name, *extra):
        self.p = mcp(*extra)
        self.n = 0
        self.rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": client_name}})

    def rpc(self, method, params=None):
        self.n += 1
        self.p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": self.n, "method": method, **({"params": params} if params else {})}) + "\n")
        self.p.stdin.flush()
        return json.loads(self.p.stdout.readline())

    def tool(self, name, **a):
        r = self.rpc("tools/call", {"name": name, "arguments": a})["result"]
        return r.get("isError", False), r["content"][0]["text"]

    def names(self):
        return [t["name"] for t in self.rpc("tools/list")["result"]["tools"]]

    def close(self):
        self.p.stdin.close()
        self.p.wait(timeout=10)


def run_hook(cwd):
    payload = {"session_id": "dash-" + str(time.time()), "cwd": cwd, "hook_event_name": "SessionStart", "source": "startup"}
    p = subprocess.run([f"{E2E}/bin/session-start"], input=json.dumps(payload), capture_output=True, text=True, env=env, timeout=60)
    out = json.loads(p.stdout.strip().splitlines()[-1]) if p.stdout.strip() else {}
    return p.returncode, out, p.stderr


base = os.path.realpath(tempfile.mkdtemp(prefix="e2e-dashlink-"))
proj = os.path.join(base, "memoryproj")
os.makedirs(proj)
desktop = Client("claude-ai")
pid = json.loads(desktop.tool("project_resolve", path=proj)[1])["id"]

print("== the session-start hook says where the dashboard is, once")
check("there is no marker before the first session", not os.path.exists(MARKER))
code, out, _ = run_hook(proj)
check("the first session succeeds", code == 0 and out.get("continue") is True, (code, out))
msg = out.get("systemMessage", "")
check("it shows the user a message with the address of this worker", URL in msg and "/memory-dashboard" in msg, out)
check("the message is one line", "\n" not in msg)
check("a marker remembers which version said it", os.path.exists(MARKER) and open(MARKER).read().strip() != "", MARKER)
code, out, _ = run_hook(proj)
check("the next session says nothing", code == 0 and "systemMessage" not in out, out)

print("== after an update it is said again, and never reaches the model")
desktop.tool("remember", path=proj, title="Dashboard link note", text="The dashboard link e2e suite stores one note so the session context is not empty.")
time.sleep(1)
with open(MARKER, "w") as f:
    f.write("an-older-version\n")
code, out, _ = run_hook(proj)
check("a new version says it again", URL in out.get("systemMessage", ""), out)
ctx = out.get("hookSpecificOutput", {}).get("additionalContext", "")
check("the session context is still injected (the note is in it)", "Dashboard link note" in ctx, ctx[:200])
check("and the address is not in the model's context", URL not in ctx and "localhost" not in ctx and "/memory-dashboard" not in ctx and "memory dashboard" not in ctx, ctx[:300])
code, out, _ = run_hook(proj)
check("and then it is quiet again", "systemMessage" not in out, out)

print("== Claude Desktop gets the link from a tool, and it is the live worker's address")
is_err, text = desktop.tool("dashboard")
check("the dashboard tool answers with this worker's address", not is_err and f"{URL} ." in text and "Give the user this link" in text, text)
# The e2e builds the worker without the embedded UI (make build copies ui/dist into it), so the page itself is covered by
# the dashboard suite; here the address must be the live worker's.
health = json.load(urllib.request.urlopen(URL + "/health", timeout=15))
check("that address is the live worker the tool talked to", health.get("ready") is True and health.get("version"), health)
check("the tool is in Desktop's tool list", "dashboard" in desktop.names())

print("== Claude Desktop restarts the worker through the server, outside its sandbox")
check("the restart tool is in Desktop's tool list", "restart" in desktop.names())
# A worker restarts itself from ~/.claude-mnemonic/bin (where an installation puts it), which this isolated home does not have.
os.makedirs(f"{E2E}/home/.claude-mnemonic/bin", exist_ok=True)
shutil.copy(f"{E2E}/bin/worker", f"{E2E}/home/.claude-mnemonic/bin/worker")
before = json.load(urllib.request.urlopen(URL + "/health", timeout=15))
t0 = time.time()
is_err, text = desktop.tool("restart")
check("the restart tool says the worker is back", not is_err and "ready again" in text, text)
after = json.load(urllib.request.urlopen(URL + "/health", timeout=15))
# The same process would have an uptime of before + everything that passed; a new one has been up for less than the wait.
check("it is a new worker process: the uptime started over", after.get("ready") is True and after["uptime_seconds"] < before["uptime_seconds"] + (time.time() - t0) - 1, (before, after, time.time() - t0))
is_err, text = desktop.tool("project_resolve", path=proj)
check("the notes are still there after the restart", not is_err and json.loads(text)["id"] == pid, text)
desktop.close()

print("== Claude Code's tool list is unchanged")
code_mode = Client("claude-code", "--mode", "code")
check("Code mode has no dashboard or restart tool (it has the slash commands)", "dashboard" not in code_mode.names() and "restart" not in code_mode.names())
is_err, text = code_mode.tool("dashboard")
check("and calling it is an unknown tool", is_err and "unknown tool" in text, text)
code_mode.close()
check("the worker is healthy", json.load(urllib.request.urlopen(URL + "/health", timeout=10)).get("ready") is True)

print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
