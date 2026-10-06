#!/usr/bin/env python3
"""Local LLM backend: summaries served by a fake Ollama, then the worker falling back to the CLI when it goes away."""
import json, os, subprocess, sys, threading, time, urllib.parse, urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
OLLAMA_PORT = int(os.environ.get("E2E_OLLAMA_PORT", "37996"))
PROMPTS = os.path.join(E2E, "fake-claude-prompts.log")
MODEL = "gemma3:12b"
ok = fail = 0
chat_requests = []

SUMMARY = ("<summary><request>Local summary from the fake Ollama</request><investigated>How a compacted conversation is kept.</investigated>"
           "<learned>Summaries can come from a local model.</learned><completed>Served by the local backend.</completed>"
           "<next_steps>Compare it with Haiku.</next_steps><notes>Canned by the end-to-end suite.</notes></summary>")


class FakeOllama(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, obj):
        raw = json.dumps(obj).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path == "/api/version":
            self.send({"version": "0.0.0-fake"})
        elif self.path == "/api/tags":
            self.send({"models": [{"name": MODEL, "size": 8100000000, "details": {"parameter_size": "12.2B", "quantization_level": "Q4_K_M", "family": "gemma3"}}]})
        elif self.path == "/api/ps":
            self.send({"models": []})
        else:
            self.send_error(404)

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        if self.path == "/api/chat":
            chat_requests.append(body)
            self.send({"model": body.get("model"), "message": {"role": "assistant", "content": SUMMARY}, "done": True, "done_reason": "stop"})
        else:
            self.send_error(404)


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(f"http://localhost:{PORT}{path}", data=data, method=method, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=15) as r:
        raw = r.read().strip()
        return json.loads(raw) if raw[:1] in (b"{", b"[") else raw.decode()


def summaries(project):
    return call("GET", f"/api/summaries?project={urllib.parse.quote(project)}&limit=20")


def claude_calls():
    try:
        return open(PROMPTS, encoding="utf-8").read().count("=====END=====")
    except OSError:
        return 0


def wait_for_summary(project, title, timeout=40):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if any(title in (r.get("request") or "") for r in summaries(project)):
            return True
        time.sleep(0.5)
    return False


def compact(session_id, project, transcript):
    call("POST", "/api/sessions/init", {"claudeSessionId": session_id, "project": project, "prompt": "Make the plugin usable from Desktop"})
    env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
    payload = {"session_id": session_id, "transcript_path": transcript, "cwd": "/tmp/" + project, "hook_event_name": "PreCompact", "trigger": "auto"}
    p = subprocess.run([f"{E2E}/bin/pre-compact"], input=json.dumps(payload), capture_output=True, text=True, env=env, timeout=60)
    return p.returncode


def turn(kind, content):
    return json.dumps({"type": kind, "message": {"role": kind, "content": content}})


transcript = os.path.join(E2E, "llm-session.jsonl")
with open(transcript, "w", encoding="utf-8") as f:
    f.write("\n".join([
        turn("user", "Let's add a local model option so the summaries can stay on this machine."),
        turn("assistant", "I edited internal/llm/ollama.go and implemented the chat client, then updated the processor to route each task to its own backend."),
        turn("user", "Good. Next, make sure a model that is down falls back to the CLI and add the tests."),
        turn("assistant", "Done: I added a Fallback type in llm.go and wrote tests for it in llm_test.go; the status endpoint reports what is installed."),
    ]) + "\n")

server = HTTPServer(("127.0.0.1", OLLAMA_PORT), FakeOllama)
threading.Thread(target=server.serve_forever, daemon=True).start()

print("== the worker sees the local backend")
st = call("GET", "/api/llm/status")
check("the summary task is on Ollama, the others on Claude", st["backends"] == {"summary": "ollama", "observation": "claude", "verify": "claude", "brief": "claude", "conflict": "claude", "rollup": "claude"}, st["backends"])
check("Ollama is reachable and the configured model is installed", st["ollama"]["reachable"] and st["ollama"]["model_installed"] and st["ollama"]["configured_model"] == MODEL, st["ollama"])
check("the installed models are listed", [m["name"] for m in st["ollama"]["installed"]] == [MODEL], st["ollama"]["installed"])

print("== a summary is produced by the local model")
before = claude_calls()
check("the hook lets Claude Code continue", compact("e2e-llm-1", "llmproj_aaa111", transcript) == 0)
check("the summary from the local model is stored", wait_for_summary("llmproj_aaa111", "Local summary from the fake Ollama"), [r.get("request") for r in summaries("llmproj_aaa111")])
check("Ollama was asked once, for the configured model", len(chat_requests) == 1 and chat_requests[0]["model"] == MODEL, [r.get("model") for r in chat_requests])
if chat_requests:
    msgs = chat_requests[0]["messages"]
    check("with a system and a user message", [m["role"] for m in msgs] == ["system", "user"], [m["role"] for m in msgs])
    check("carrying the conversation that is about to be compacted", "about to be compacted" in msgs[1]["content"] and "Fallback type in llm.go" in msgs[1]["content"])
    check("with room for the excerpt (num_ctx) and a low temperature", chat_requests[0]["options"].get("num_ctx", 0) >= 8192 and chat_requests[0]["options"].get("temperature", 1) <= 0.3, chat_requests[0]["options"])
check("the Claude CLI was not used", claude_calls() == before, (before, claude_calls()))

print("== Ollama goes away: the worker falls back to the CLI")
server.shutdown()
server.server_close()
check("a second compaction still gets a summary", compact("e2e-llm-2", "llmproj_bbb222", transcript) == 0 and wait_for_summary("llmproj_bbb222", "Compaction test summary"), [r.get("request") for r in summaries("llmproj_bbb222")])
check("it came from the CLI", claude_calls() == before + 1, (before, claude_calls()))
st = call("GET", "/api/llm/status")
check("the status says Ollama is unreachable, without failing", st["ollama"]["reachable"] is False and "unreachable" in st["ollama"]["error"], st["ollama"])
check("the backends are still reported", st["backends"]["summary"] == "ollama")
check("the worker is healthy", call("GET", "/health").get("ready") is True)

print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
