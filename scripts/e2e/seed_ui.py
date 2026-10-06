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

# A project with more notes than the dashboard's first page (50), so the real totals differ from the page size.
d = os.path.join(base, "bulk"); os.makedirs(d)
ids["bulk"] = json.loads(tool("project_resolve", path=d))["id"]
topics = ["harbour", "orchard", "glacier", "lantern", "compass", "meadow", "saffron", "quartz", "tundra", "velvet", "walnut", "zephyr", "ember"]
for i in range(52):
    tool("remember", path=d, title=f"Bulk note {i:02d} about the {topics[i % len(topics)]} {topics[(i * 5 + 3) % len(topics)]}",
         text=f"Entry {i}: the {topics[i % len(topics)]} notes mention the {topics[(i * 7 + 1) % len(topics)]} in passing, number {i * 31}.")

# A project whose notes the old rule had made global: two of its three notes are global and decided by the rule
# (scope_source auto, set below), the third was saved on purpose (explicit), for the scope dialog.
d = os.path.join(base, "scoped"); os.makedirs(d)
ids["scoped"] = json.loads(tool("project_resolve", path=d))["id"]
legacy_titles = ("Legacy global note about queues", "Legacy global note about releases")
for title, text in ((legacy_titles[0], "The scoped project keeps one queue per tenant."),
                    (legacy_titles[1], "Releases of the scoped project are cut on Thursdays."),
                    ("Note saved on purpose", "The scoped project's owner prefers short commit messages.")):
    tool("remember", path=d, title=title, text=text)

# Two clones of one repository under different folder names (the second was renamed), for the "Possible duplicates"
# section of the project manager: the worker learns the remote when the folder's path reaches it.
def clone(parent, name, remote):
    d = os.path.join(base, parent, name); os.makedirs(d)
    subprocess.run(["git", "-C", d, "init", "-q"], check=True)
    subprocess.run(["git", "-C", d, "remote", "add", "origin", remote], check=True)
    return d
d = clone("clones-a", "inventory", "https://someone:token-1234@git.example.org/team/inventory.git")
ids["dup_main"] = json.loads(tool("project_resolve", path=d))["id"]
for i in range(3):
    tool("remember", path=d, title=f"Inventory count rule {i}", text=f"The inventory counts stock in lots, rule {i}.")
d = clone("clones-b", "stockroom", "git@git.example.org:team/inventory.git")
ids["dup_other"] = json.loads(tool("project_resolve", path=d))["id"]
tool("remember", path=d, title="Stockroom shelf labels", text="The stockroom prints shelf labels on Fridays.")

# A project with three notes about one setting and two proposals between them, for the conflict review panel.
d = os.path.join(base, "reviewed"); os.makedirs(d)
ids["reviewed"] = json.loads(tool("project_resolve", path=d))["id"]
for title, text in (("Cache lifetime is one hour", "The rate cache lives for 60 minutes."),
                    ("Cache lifetime is one day", "The rate cache lives for 24 hours."),
                    ("Cache lifetime is a week", "The rate cache lives for 7 days.")):
    tool("remember", path=d, title=title, text=text)
    time.sleep(0.05)
import urllib.request
def post(path, body):
    req = urllib.request.Request(f"http://localhost:{PORT}{path}", method="POST", data=json.dumps(body).encode(), headers={"Content-Type": "application/json"})
    return json.loads(urllib.request.urlopen(req, timeout=30).read())
rows = json.loads(urllib.request.urlopen(f"http://localhost:{PORT}/api/observations?project={ids['reviewed']}&limit=20", timeout=30).read())
rows = rows["observations"] if isinstance(rows, dict) else rows
by = {o["title"]: o["id"] for o in rows}
post("/api/conflicts", {"older_id": by["Cache lifetime is one hour"], "newer_id": by["Cache lifetime is one day"], "reason": "The lifetime changed from an hour to a day."})
post("/api/conflicts", {"older_id": by["Cache lifetime is one day"], "newer_id": by["Cache lifetime is a week"], "reason": "The lifetime changed again."})

# Two projects that the old dropdown could not show: one with only an observation (its session row is gone, as after
# a cleanup) and one with only session summaries. The dropdown and the manager must both list them.
d = os.path.join(base, "obs_only"); os.makedirs(d)
ids["obs_only"] = json.loads(tool("project_resolve", path=d))["id"]
tool("remember", path=d, title="Observation without a session", text="This project survives only through its observation.")

# A note saved last, with the default importance, in a project of its own. The dashboard's timeline must show it although
# the 52 older notes of the bulk project (given a higher score below) fill the first 50 of the importance-ordered list:
# a note that has just been saved starts at importance 1, behind every note that has earned more.
d = os.path.join(base, "fresh"); os.makedirs(d)
ids["fresh"] = json.loads(tool("project_resolve", path=d))["id"]
time.sleep(0.05)
tool("remember", path=d, title="Freshly saved note about the pantry", text="Saved last, with the default importance score.")

# A project for the Roll-ups tab: nine old notes of one month (an automatic scope, so a roll-up may condense them), three
# near-identical recent notes (for the duplicates view) and a note a person put away.
d = os.path.join(base, "tidy"); os.makedirs(d)
ids["tidy"] = json.loads(tool("project_resolve", path=d))["id"]
berths = "amber basalt cobalt dune ember fjord garnet harbor indigo".split()
for i, w in enumerate(berths):
    tool("remember", path=d, title=f"Berth {w} inspection {i}", text=f"The {w} berth {i} fender was inspected and the {berths[(i * 4 + 1) % 9]} chain replaced, ticket {i * 313}.")
crane = "The harbour crane control board was replaced after the second inspection found a cracked relay in the hoist circuit"
for i, tail in enumerate((" today", " again", " once more")):
    tool("remember", path=d, title="Crane board replaced", text=crane + tail)
tool("remember", path=d, title="Note put away by a person", text="A note about the old ferry timetable that nobody needs any more.")
import urllib.request as _u
_rows = json.loads(_u.urlopen(f"http://localhost:{PORT}/api/observations?project={ids['tidy']}&limit=50", timeout=30).read())["observations"]
_tidy = {}
for o in _rows:
    _tidy.setdefault(o["title"], []).append(o["id"])
ids["summ_only"] = "summ-only_a1b2c3"
db = sqlite3.connect(f"{E2E}/home/.claude-mnemonic/claude-mnemonic.db", timeout=30)
db.execute("DELETE FROM sdk_sessions WHERE project = ?", (ids["obs_only"],))
now = int(time.time() * 1000)
for i, title in enumerate(("First summary", "Second summary")):
    db.execute("INSERT INTO session_summaries (created_at, sdk_session_id, project, request, created_at_epoch) VALUES (?, ?, ?, ?, ?)",
               (time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), f"sdk-summ-only-{i}", ids["summ_only"], title, now + i))
for t in legacy_titles:
    db.execute("UPDATE observations SET scope = 'global', scope_source = 'auto' WHERE project = ? AND title = ?", (ids["scoped"], t))
import datetime
_m = datetime.datetime.now(datetime.timezone.utc)
_y, _mo = divmod(_m.year * 12 + _m.month - 1 - 4, 12)
_base = datetime.datetime(_y, _mo + 1, 10, 12, 0, tzinfo=datetime.timezone.utc)
for k, w in enumerate(berths):
    t = _base + datetime.timedelta(minutes=k)
    db.execute("UPDATE observations SET scope_source = 'auto', created_at_epoch = ?, created_at = ? WHERE id = ?",
               (int(t.timestamp() * 1000), t.isoformat(), _tidy[f"Berth {w} inspection {k}"][0]))
for oid in _tidy["Crane board replaced"]:
    db.execute("UPDATE observations SET scope_source = 'auto' WHERE id = ?", (oid,))
# The bulk notes have earned a higher importance than a note that has just been saved (which starts at 1).
db.execute("UPDATE observations SET importance_score = 1.5 WHERE project = ?", (ids["bulk"],))
db.commit(); db.close()
post("/api/observations/archive", {"ids": _tidy["Note put away by a person"], "reason": "put away in the e2e"})
print(json.dumps(ids))
p.stdin.close(); p.wait(timeout=10)
