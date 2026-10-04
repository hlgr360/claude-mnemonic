#!/usr/bin/env python3
"""Automatic project merge, switched on (it is off by default): only projects that share a git remote and whose old
folder is gone are merged, by themselves, with a backup, an alias that says so, and an announcement. A second live
clone, namesakes with other remotes and a name alone are left for a person."""
import json, os, shutil, subprocess, sys, tempfile, threading, time, urllib.error, urllib.request

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


# The announcement is a server-sent event: listen from the start.
events = []


def listen():
    try:
        with urllib.request.urlopen(f"http://localhost:{PORT}/api/events", timeout=400) as r:
            for line in r:
                if line.startswith(b"data:"):
                    events.append(line[5:].decode().strip())
    except Exception:
        pass


threading.Thread(target=listen, daemon=True).start()

env = dict(os.environ, HOME=f"{E2E}/home", CLAUDE_MNEMONIC_WORKER_PORT=PORT, DO_NOT_TRACK="1")
proc = subprocess.Popen([f"{E2E}/bin/mcp-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(f"{E2E}/mcp-automerge.log", "w"),
                        text=True, env=env, cwd="/")
_id = 0


def must(tool, **a):
    global _id
    _id += 1
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": _id, "method": "tools/call", "params": {"name": tool, "arguments": a}}) + "\n")
    proc.stdin.flush()
    r = json.loads(proc.stdout.readline())["result"]
    if r.get("isError", False):
        raise SystemExit(f"setup step failed: {tool} {a} -> {r['content'][0]['text'][:300]}")
    return r["content"][0]["text"]


proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {"protocolVersion": "2025-11-25", "clientInfo": {"name": "claude-ai"}}}) + "\n")
proc.stdin.flush()
proc.stdout.readline()

base = os.path.realpath(tempfile.mkdtemp(prefix="e2e-automerge-"))


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
        must("remember", path=d, title=t, text=f"{t}: written by the automatic merge suite.")
        time.sleep(0.03)
    return pid


print("== seed: one repository in a live clone and in a clone whose folder will be gone, a second live clone, and decoys")
live = folder("live", "ledger", remote="https://git.example.org/team/ledger.git")
moved = folder("moved", "ledger", remote="https://git.example.org/team/ledger.git")
second = folder("second", "ledger", remote="git@git.example.org:team/ledger.git")
ns1 = folder("n1", "report", remote="https://git.example.org/team/report.git")
ns2 = folder("n2", "report", remote="https://git.example.org/clients/report.git")
bare1 = folder("b1", "notes")
bare2 = folder("b2", "notes")
L = project(live, [f"Ledger live note {i}" for i in range(4)])
M = project(moved, [f"Ledger moved note {i}" for i in range(2)])
S = project(second, [f"Ledger second note {i}" for i in range(2)])
N1 = project(ns1, [f"Report one note {i}" for i in range(3)])
N2 = project(ns2, [f"Report two note {i}" for i in range(2)])
B1 = project(bare1, [f"Notes one note {i}" for i in range(5)])
B2 = project(bare2, ["Notes two note"])

deadline = time.time() + 30
while time.time() < deadline and not call("GET", "/api/projects/duplicates")[1]["suggestions"]:
    time.sleep(0.5)
d = call("GET", "/api/projects/duplicates")[1]
check("automatic merging is reported as on", d["auto_merge"] is True)
check("before anything is gone, nothing is marked for automatic merging", not any(s["auto_mergeable"] for s in d["suggestions"]), [s["auto_mergeable"] for s in d["suggestions"]])

# The old clone's folder and both namesakes' folders go away; the live clone and the second clone stay.
for gone in (moved, ns1, ns2):
    shutil.rmtree(gone)

print("== the worker merges what is certain on its own (its first pass runs 45 s after start, then every minute)")
deadline = time.time() + 240
alias = None
while time.time() < deadline:
    aliases = call("GET", "/api/projects/aliases")[1]
    alias = next((a for a in aliases if a["alias"] == M), None)
    if alias:
        break
    time.sleep(3)
check("the clone whose folder is gone was merged into the live one", alias is not None and alias["canonical"] == L, alias)
check("the alias says it was automatic", alias is not None and alias["source"] == "auto-merge", alias)
check("its notes are the survivor's now", call("GET", f"/api/projects/{L}/stats")[1]["observations"] == 6)
check("the old id still resolves to the survivor", json.loads(must("project_resolve", id=M))["id"] == L)
backups = os.listdir(f"{E2E}/home/.claude-mnemonic/backups") if os.path.isdir(f"{E2E}/home/.claude-mnemonic/backups") else []
check("a backup was taken before the merge", any(f"merge-{M}" in b for b in backups), backups)
time.sleep(1)
announced = [e for e in events if '"auto_merged"' in e]
check("it was announced to the dashboard, with the backup", announced and M in announced[0] and L in announced[0] and "backup" in announced[0], events[-3:])

print("== and nothing else")
time.sleep(70)  # another pass
check("a second live clone of the same repository is left for a person", call("GET", f"/api/projects/{S}/stats")[1]["observations"] == 2)
check("namesakes with different remotes are left alone even though both folders are gone",
      call("GET", f"/api/projects/{N1}/stats")[1]["observations"] == 3 and call("GET", f"/api/projects/{N2}/stats")[1]["observations"] == 2)
check("a name and a small project are only ever suggested", call("GET", f"/api/projects/{B2}/stats")[1]["observations"] == 1 and call("GET", f"/api/projects/{B1}/stats")[1]["observations"] == 5)
aliases = call("GET", "/api/projects/aliases")[1]
check("exactly one alias exists", len(aliases) == 1, aliases)
d = call("GET", "/api/projects/duplicates")[1]
left = {frozenset((s["survivor"]["project"], s["other"]["project"])) for s in d["suggestions"]}
check("the live second clone is still suggested, for a person to decide", frozenset((L, S)) in left, sorted(map(sorted, left)))
check("the worker is healthy", call("GET", "/health")[1].get("ready") is True)

proc.stdin.close()
proc.wait(timeout=10)
print(f"\n{ok} passed, {fail} failed")
sys.exit(1 if fail else 0)
