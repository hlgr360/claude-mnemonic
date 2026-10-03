#!/usr/bin/env python3
"""The PreCompact hook: the real hook binary and worker, with a fake `claude` that records the summary prompt."""
import json, os, subprocess, sys, time, urllib.parse, urllib.request

E2E = os.environ.get("E2E_DIR") or os.path.dirname(os.path.abspath(__file__))  # work dir holding bin/ and home/
PORT = os.environ.get("E2E_PORT", "37999")
PROMPTS = os.path.join(E2E, "fake-claude-prompts.log")
PROJECT = "compactproj_abc123"
ok = fail = 0


def check(name, cond, detail=""):
    global ok, fail
    ok += bool(cond)
    fail += (not cond)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + ("" if cond else f"  {detail}"))


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(f"http://localhost:{PORT}{path}", data=data, method=method, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=15) as r:
        raw = r.read()
        return json.loads(raw) if raw.strip().startswith(b"{") or raw.strip().startswith(b"[") else raw.decode()


def summaries():
    return call("GET", f"/api/summaries?project={urllib.parse.quote(PROJECT)}&limit=20")


def prompts_seen():
    try:
        return open(PROMPTS, encoding="utf-8").read().count("=====END=====")
    except OSError:
        return 0


def run_hook(session_id, transcript, trigger="auto"):
    env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
    payload = {"session_id": session_id, "transcript_path": transcript, "cwd": "/tmp/" + PROJECT, "hook_event_name": "PreCompact",
               "trigger": trigger, "custom_instructions": None}
    p = subprocess.run([f"{E2E}/bin/pre-compact"], input=json.dumps(payload), capture_output=True, text=True, env=env, timeout=60)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


def turn(kind, content):
    return json.dumps({"type": kind, "message": {"role": kind, "content": content}})


transcript = os.path.join(E2E, "compact-session.jsonl")
with open(transcript, "w", encoding="utf-8") as f:
    f.write("\n".join([
        turn("user", "Let's make the plugin usable from Claude Desktop chat as well as Claude Code."),
        turn("assistant", [{"type": "text", "text": "I edited internal/mcp/desktop.go and implemented project_suggest. We decided to address projects by name instead of id, because ids are cumbersome."},
                           {"type": "tool_use", "name": "Edit", "input": {"file_path": "desktop.go"}}]),
        turn("user", [{"type": "tool_result", "content": "TOOL-RESULT-NOISE that must not be in the excerpt"}]),
        turn("user", "Keep <private>the staging password hunter2</private> out of it. Next we need checkpoints so a compacted chat can recover."),
        turn("assistant", "Agreed. I updated handlers_threads.go and added the checkpoint endpoint; open question: how long may a note be?"),
    ]) + "\n")

print("== a session exists, then Claude Code is about to compact it")
init = call("POST", "/api/sessions/init", {"claudeSessionId": "e2e-compact-1", "project": PROJECT, "prompt": "Make the plugin usable from Desktop"})
check("the session was created", isinstance(init, dict) and init.get("sessionDbId"), init)
before = len(summaries())
code, out, err = run_hook("e2e-compact-1", transcript)
check("the hook exits 0 and lets the compaction continue", code == 0 and '"continue":true' in out.replace(" ", ""), (code, out, err))

print("== the worker summarised the conversation, not just the last reply")
deadline = time.time() + 30
while time.time() < deadline and len(summaries()) <= before:
    time.sleep(0.5)
rows = summaries()
check("a summary was stored under the session's project", len(rows) == before + 1, [r.get("request") for r in rows])
check("it is the one the summariser returned", any("Compaction test summary" in (r.get("request") or "") for r in rows), [r.get("request") for r in rows])
prompt = open(PROMPTS, encoding="utf-8").read() if os.path.exists(PROMPTS) else ""
check("the summariser was told the conversation is about to be compacted", "about to be compacted" in prompt, prompt[:300])
check("it saw the earlier turns (the decision and its reason)", "address projects by name instead of id" in prompt and "ids are cumbersome" in prompt)
check("it saw the latest turn (the open question)", "how long may a note be" in prompt)
check("turns are labelled and in order", prompt.index("User: Let's make the plugin") < prompt.index("User: Keep") if "User: Keep" in prompt else False)
check("tool results are not part of it", "TOOL-RESULT-NOISE" not in prompt)
check("private text never reached the summariser", "hunter2" not in prompt and "staging password" not in prompt)

print("== the hook never gets in the way")
seen = prompts_seen()
code, out, _ = run_hook("e2e-compact-1", os.path.join(E2E, "no-such-transcript.jsonl"))
check("a missing transcript is fine and summarises nothing", code == 0 and prompts_seen() == seen, (code, out))
code, out, _ = run_hook("never-seen-session", transcript, trigger="manual")
check("an unknown session is fine and summarises nothing", code == 0 and prompts_seen() == seen, (code, out))
empty = os.path.join(E2E, "empty.jsonl")
open(empty, "w").close()
code, out, _ = run_hook("e2e-compact-1", empty)
check("an empty transcript is fine and summarises nothing", code == 0 and prompts_seen() == seen, (code, out))
time.sleep(1)
check("the worker is still healthy", call("GET", "/health").get("ready") is True)

print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
