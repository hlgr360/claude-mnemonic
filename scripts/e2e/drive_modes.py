#!/usr/bin/env python3
"""Claude Code mode is unchanged, and Desktop mode starts a dead worker on first use. Real binaries, isolated HOME."""
import json, os, shutil, subprocess, sys, time, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += cond
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


class Mcp:
    def __init__(self, port, client, *extra):
        env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=str(port), DO_NOT_TRACK="1")
        self.p = subprocess.Popen([f"{E2E}/bin/mcp-server", *extra], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=open(f"{E2E}/mcp2.log", "a"), text=True, env=env, cwd="/")
        self.n = 0
        self.init = self.rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": client}})["result"]

    def rpc(self, method, params=None):
        self.n += 1
        self.p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": self.n, "method": method, **({"params": params} if params else {})}) + "\n")
        self.p.stdin.flush()
        return json.loads(self.p.stdout.readline())

    def tools(self):
        return [t["name"] for t in self.rpc("tools/list")["result"]["tools"]]

    def call(self, name, **a):
        r = self.rpc("tools/call", {"name": name, "arguments": a})["result"]
        return r.get("isError", False), r["content"][0]["text"]

    def close(self):
        self.p.stdin.close()
        self.p.wait(timeout=10)


def health(port):
    try:
        return json.load(urllib.request.urlopen(f"http://localhost:{port}/health", timeout=2)).get("ready")
    except Exception:
        return None


print("== Claude Code mode, real binary, pinned to a project that has data")
projects = json.load(urllib.request.urlopen("http://localhost:37999/api/projects/summary"))
proj = projects[0]["project"]
code = Mcp(37999, "claude-code", "--project", proj)
check("no instructions in code mode", "instructions" not in code.init)
names = code.tools()
check("no desktop tools are listed", not any(n in names for n in ["project_suggest", "project_resolve", "project_list", "context", "remember"]), names)
err, text = code.call("remember", text="x")
check("desktop tools are unknown in code mode", err and "unknown tool" in text, text)
err, text = code.call("search", query="indexer embeds documents", project=proj)
check("search keeps using the project search endpoint", not err and '"project"' in text, text[:200])
code.close()

print("== Desktop mode with --project pinned keeps that project as the default")
pinned = Mcp(37999, "claude-ai", "--project", proj)
err, text = pinned.call("remember", text="Written to the pinned project by default, no project argument.")
check("remember defaults to the pinned project", not err and proj in text, text)
pinned.close()

print("== --mode flag forces the mode regardless of client")
forced = Mcp(37999, "claude-code", "--mode", "desktop")
check("forced desktop lists the project tools", "project_suggest" in forced.tools())
forced.close()

print("== Lazy worker bootstrap: nothing listens on 37998 yet")
os.makedirs(f"{E2E}/home/.claude-mnemonic/bin", exist_ok=True)
shutil.copy(f"{E2E}/bin/worker", f"{E2E}/home/.claude-mnemonic/bin/worker")
check("port 37998 is dead before the first call", health(37998) is None)
boot = Mcp(37998, "claude-ai")
t0 = time.time()
err, text = boot.call("project_list")
took = time.time() - t0
check("the first Desktop call started the worker and succeeded", not err and text.strip().startswith("["), text[:200])
check(f"worker answers on 37998 afterwards (took {took:.1f}s)", health(37998) is True)
t0 = time.time()
err, _ = boot.call("project_list")
check("the next call does not restart anything", not err and time.time() - t0 < 2)
boot.close()

print("== Bootstrap failure is reported clearly")
shutil.rmtree(f"{E2E}/home/.claude-mnemonic/bin")
subprocess.run(["bash", "-c", "kill $(lsof -ti :37998) 2>/dev/null"])
time.sleep(2)
broken = Mcp(37998, "claude-ai")
err, text = broken.call("project_list")
check("a worker that cannot be started is an error result, not a hang", err and "could not be started" in text, text[:300])
broken.close()

print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
