#!/usr/bin/env python3
"""Recovering a Desktop chat's thread of work: checkpoint and catch_up over stdio, a "new chat" reading it back."""
import json, os, subprocess, sys, tempfile, time, urllib.parse, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


def api(path):
    with urllib.request.urlopen(f"http://localhost:{PORT}{path}", timeout=15) as r:
        return json.loads(r.read())


class Chat:
    """One MCP server process, like one Claude Desktop chat."""

    def __init__(self, n):
        env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
        self.proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=open(f"{E2E}/mcp-threads{n}.log", "w"), text=True, env=env, cwd="/")
        self._id = 0
        self.rpc("initialize", {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}})

    def rpc(self, method, params=None):
        self._id += 1
        self.proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": self._id, "method": method, **({"params": params} if params else {})}) + "\n")
        self.proc.stdin.flush()
        return json.loads(self.proc.stdout.readline())

    def tool(self, name, **a):
        r = self.rpc("tools/call", {"name": name, "arguments": a})["result"]
        return r.get("isError", False), r["content"][0]["text"]

    def close(self):
        self.proc.stdin.close()
        self.proc.wait(timeout=10)


folder = os.path.join(tempfile.mkdtemp(prefix="e2e-threads-"), "overlay")
os.makedirs(folder)

print("== the tools are there in a chat")
chat = Chat(1)
names = [t["name"] for t in chat.rpc("tools/list")["result"]["tools"]]
check("checkpoint and catch_up are listed", "checkpoint" in names and "catch_up" in names, names)
pid = json.loads(chat.tool("project_resolve", path=folder)[1])["id"]

print("== checkpoint keeps one note per thread")
err, text = chat.tool("checkpoint", path=folder, thread="Overlay design", goal="Make the plugin usable from Desktop",
                      progress="Chat tools done, instruction pasted", next_steps="Measure usage")
check("the first checkpoint creates the note (and the project, from a folder path)", not err and "Saved a new note" in text and pid in text, text)
err, text = chat.tool("checkpoint", project=pid, thread="overlay  DESIGN", goal="Make the plugin usable from Desktop",
                      progress="Measuring skipped; judged by daily use", decisions="Names, not ids, in every tool")
check("the same thread name updates it", not err and "Updated the note" in text, text)
time.sleep(0.05)
chat.tool("checkpoint", project=pid, thread="Compaction recovery", goal="Survive Desktop compaction",
          progress="Designing checkpoint and catch_up", next_steps="Write the tools")
summaries = api(f"/api/summaries?project={urllib.parse.quote(pid)}&limit=20")
check("two notes exist, visible to the dashboard as summaries", len(summaries) == 2, [s.get("request") for s in summaries])

print("== a new chat catches up")
chat.close()
chat = Chat(2)
err, text = chat.tool("catch_up", project=pid)
check("catch_up works in a fresh chat", not err and f"project {pid}" in text, text)
check("the thread worked on last comes first", text.index("Thread: Compaction recovery") < text.index("Thread: overlay  DESIGN"), text)
check("an updated note shows its current state, not the old one", "Measuring skipped" in text and "Chat tools done" not in text, text)
check("resolved open items are gone", "Measure usage" not in text, text)
check("decisions of the thread are shown", "Names, not ids" in text)

print("== project decisions are part of the digest")
chat.tool("remember", project=pid, type="decision", title="Use thread notes", text="Chat recovery rests on one living note per thread.")
err, text = chat.tool("catch_up", project=pid)
check("a saved decision shows up", not err and "Use thread notes" in text, text)
chat.tool("remember", project=pid, type="discovery", title="Just a finding", text="Not a decision.")
err, text = chat.tool("catch_up", project=pid)
check("other observation types do not", "Just a finding" not in text, text)

print("== private text never reaches the note")
chat.tool("checkpoint", project=pid, thread="Keys", goal="Rotate <private>sk-live-123456</private> the credentials", progress="Started")
err, text = chat.tool("catch_up", project=pid)
check("it was stripped before storing", not err and "sk-live-123456" not in text and "Rotate" in text, text)
check("and is not in the database either", "sk-live-123456" not in json.dumps(api(f"/api/summaries?project={urllib.parse.quote(pid)}&limit=20")))
err, text = chat.tool("checkpoint", project=pid, thread="Secret only", goal="<private>nothing public</private>")
check("a note that is entirely private is refused", err and "nothing to store" in text, text)

print("== a chat without a project writes nothing")
before = len(api(f"/api/summaries?project={urllib.parse.quote(pid)}&limit=50"))
err, text = chat.tool("checkpoint", thread="Orphan", goal="g")
check("checkpoint without a project is refused", err and "a project is required" in text, text)
err, text = chat.tool("checkpoint", project="invented-project", thread="Orphan", goal="g")
check("a guessed project is refused", err and "unknown project" in text, text)
err, text = chat.tool("catch_up")
check("catch_up without a project says to choose one", err and "a project is required" in text, text)
check("nothing was stored", len(api(f"/api/summaries?project={urllib.parse.quote(pid)}&limit=50")) == before)

print("== a project name works like an id")
err, text = chat.tool("catch_up", project="overlay")
check("catch_up by name", not err and f"project {pid}" in text, text)

print("== thread notes are searchable like summaries")
time.sleep(4)  # asynchronous vector sync
found = api(f"/api/summaries?project={urllib.parse.quote(pid)}&query={urllib.parse.quote('survive Desktop compaction')}&limit=5")
check("a semantic query finds the thread note", any("Compaction recovery" in (s.get("request") or "") for s in found), [s.get("request") for s in found])

chat.close()
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
