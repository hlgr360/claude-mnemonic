#!/usr/bin/env python3
"""Put back notes that exist in a snapshot but not in the live database (lost to the old 100-notes-per-project cap).

Dry run by default: it only prints counts (never note text). With --apply it
  1. refuses unless the running worker is ready and knows the archive endpoints (a worker without the cap fix would delete them again),
  2. takes a safety snapshot of the live database (VACUUM INTO, reading it read-only),
  3. in ONE transaction copies each lost note back with its own id, dates, scores and scope, but HIDDEN (is_archived = 1, reason
     "restored from snapshot ..."), plus any session rows it needs,
  4. unarchives each one through the worker's API, so the worker puts it back in the search index (vectors) itself.
Run it again after an interruption: step 3 skips notes that are already there and step 4 finishes the ones still hidden.

Reads snapshots read-only and immutable; never changes a snapshot. Only projects whose id starts with --project-prefix, and that
still exist in the live database, are considered.

Usage:
  python3 scripts/restore_lost_notes.py --project-prefix shop_            # dry run: counts only
  python3 scripts/restore_lost_notes.py --project-prefix shop_ --apply    # restore (the worker must be running, v0.21.105.2 or later)"""
import argparse, datetime, glob, json, os, re, sqlite3, sys, time, urllib.error, urllib.request

REASON = "restored from snapshot "


def ro(path, immutable=False):
    return sqlite3.connect(f"file:{path}?mode=ro{'&immutable=1' if immutable else ''}", uri=True, timeout=60)


def columns(con, table):
    return [r[1] for r in con.execute(f"PRAGMA table_info({table})")]


def snapshot_key(path):
    m = re.search(r"snapshot-(\d{8})-(\d{6})\.(\d+)", os.path.basename(path))
    return (m.group(1), m.group(2), m.group(3)) if m else ("", "", "")


def find_lost(live, snaps, prefix):
    """Notes in a snapshot, not in the live database, of a project that still exists. The newest snapshot's copy wins."""
    live_ids = {r[0] for r in live.execute("SELECT id FROM observations")}
    live_projects = {r[0] for r in live.execute("SELECT project FROM observations UNION SELECT project FROM sdk_sessions")}
    lost, by_snapshot = {}, {}
    for path in sorted(snaps, key=snapshot_key, reverse=True):
        con = ro(path, immutable=True)
        try:
            cols = columns(con, "observations")
            con.row_factory = sqlite3.Row
            for row in con.execute("SELECT * FROM observations WHERE project LIKE ? || '%'", (prefix,)):
                if row["id"] in live_ids or row["id"] in lost or row["project"] not in live_projects:
                    continue
                lost[row["id"]] = {"row": dict(row), "cols": cols, "snapshot": path}
                by_snapshot[os.path.basename(path)] = by_snapshot.get(os.path.basename(path), 0) + 1
        finally:
            con.close()
    return lost, by_snapshot


def day(epoch_ms):
    return datetime.datetime.fromtimestamp(epoch_ms / 1000, datetime.timezone.utc).strftime("%Y-%m-%d")


def report(lost, by_snapshot, live):
    print(f"lost notes found: {len(lost)}")
    if not lost:
        return
    for name, n in sorted(by_snapshot.items()):
        print(f"  from {name[:60]}: {n}")
    projects, types, days = {}, {}, {}
    for v in lost.values():
        r = v["row"]
        projects[r["project"]] = projects.get(r["project"], 0) + 1
        types[r["type"]] = types.get(r["type"], 0) + 1
        days[day(r["created_at_epoch"])] = days.get(day(r["created_at_epoch"]), 0) + 1
    for p, n in sorted(projects.items()):
        print(f"  project {p}: {n} notes, now {live.execute('SELECT COUNT(*) FROM observations WHERE project=? AND COALESCE(is_archived,0)=0', (p,)).fetchone()[0]} live")
    print("  by type:", dict(sorted(types.items())))
    print("  by day (UTC):", dict(sorted(days.items())))
    print(f"  ids {min(lost)}..{max(lost)}")
    sessions = {v["row"]["sdk_session_id"] for v in lost.values()}
    have = {r[0] for r in live.execute("SELECT sdk_session_id FROM sdk_sessions")}
    print(f"  sessions: {len(sessions)} referenced, {len(sessions - have)} missing from the live database")


def api(base, path, method="GET"):
    req = urllib.request.Request(base + path, method=method, data=b"{}" if method == "POST" else None, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.status, r.read()


def worker_ok(base):
    try:
        status, body = api(base, "/health")
        health = json.loads(body)
        s2, _ = api(base, "/api/observations?archived_only=true&limit=1")
        return health.get("ready") is True and s2 == 200, health.get("version", "?")
    except Exception as e:  # noqa: BLE001
        return False, f"unreachable: {e}"


def safety_snapshot(live_path, home):
    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S.%f")[:-3]
    dest = os.path.join(home, "backups", f"snapshot-{stamp}-before-restore-lost-notes.db")
    con = ro(live_path)
    try:
        con.execute(f"VACUUM INTO '{dest}'")
    finally:
        con.close()
    return dest


def scope_source(facts):
    """facts is JSON text, but some rows hold it as bytes (a BLOB): read both."""
    if isinstance(facts, (bytes, bytearray)):
        facts = bytes(facts).decode("utf-8", "replace")
    return "explicit" if "Saved explicitly via" in (facts or "") else "auto"


def insert_hidden(live_path, live_cols, sess_cols, lost):
    now = int(time.time() * 1000)
    con = sqlite3.connect(live_path, timeout=60)
    con.execute("PRAGMA busy_timeout = 60000")
    added = added_sessions = 0
    try:
        con.execute("BEGIN IMMEDIATE")
        have_ids = {r[0] for r in con.execute("SELECT id FROM observations")}
        have_sessions = {r[0] for r in con.execute("SELECT sdk_session_id FROM sdk_sessions")}
        for oid in sorted(lost):
            if oid in have_ids:
                continue
            v = lost[oid]
            row = {c: v["row"][c] for c in v["cols"] if c in live_cols}
            sid = row["sdk_session_id"]
            if sid not in have_sessions:
                # the session row comes from the snapshot the note came from, or a minimal one is made
                src = ro(v["snapshot"], immutable=True)
                src.row_factory = sqlite3.Row
                try:
                    srow = src.execute("SELECT * FROM sdk_sessions WHERE sdk_session_id = ?", (sid,)).fetchone()
                finally:
                    src.close()
                if srow:
                    scols = [c for c in srow.keys() if c in sess_cols and c != "id"]
                    con.execute(f"INSERT OR IGNORE INTO sdk_sessions ({','.join(scols)}) VALUES ({','.join('?' * len(scols))})", [srow[c] for c in scols])
                else:
                    con.execute("INSERT OR IGNORE INTO sdk_sessions (claude_session_id, project, started_at, started_at_epoch, sdk_session_id) VALUES (?,?,?,?,?)",
                                (sid, row["project"], row["created_at"], row["created_at_epoch"], sid))
                have_sessions.add(sid)
                added_sessions += 1
            if not row.get("scope_source"):
                row["scope_source"] = scope_source(row.get("facts"))
            row["is_archived"] = 1
            row["archived_at_epoch"] = now
            row["archived_reason"] = REASON + os.path.basename(v["snapshot"])
            names = list(row)
            con.execute(f"INSERT INTO observations ({','.join(names)}) VALUES ({','.join('?' * len(names))})", [row[n] for n in names])
            added += 1
        con.execute("COMMIT")
    except Exception:
        con.execute("ROLLBACK")
        raise
    finally:
        con.close()
    return added, added_sessions


def unarchive_all(live_path, base, pause):
    live = ro(live_path)
    try:
        ids = [r[0] for r in live.execute("SELECT id FROM observations WHERE is_archived = 1 AND archived_reason LIKE ? ORDER BY id", (REASON + "%",))]
    finally:
        live.close()
    done = failed = 0
    for oid in ids:
        for attempt in range(8):
            try:
                api(base, f"/api/observations/{oid}/unarchive", "POST")
                done += 1
                break
            except urllib.error.HTTPError as e:
                if e.code == 429 and attempt < 7:  # the worker's rate limiter: wait and ask again
                    time.sleep(1 + attempt)
                    continue
                failed += 1
                print(f"  note {oid}: HTTP {e.code}")
                break
            except (urllib.error.URLError, ConnectionError, TimeoutError) as e:  # a restart or a blip: wait and ask again
                if attempt < 7:
                    time.sleep(1 + attempt)
                    continue
                failed += 1
                print(f"  note {oid}: {e}")
                break
        time.sleep(pause)
    return done, failed, len(ids)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--home", default=os.path.expanduser("~/.claude-mnemonic"))
    ap.add_argument("--project-prefix", required=True, help="only projects whose id starts with this, for example shop_ (a project id is <folder name>_<6 hex>)")
    ap.add_argument("--api", default="http://localhost:37777")
    ap.add_argument("--pause", type=float, default=0.15, help="seconds between unarchive calls")
    ap.add_argument("--apply", action="store_true", help="write (default: dry run)")
    ap.add_argument("--no-api", action="store_true", help="testing only: insert hidden and stop")
    a = ap.parse_args()
    live_path = os.path.join(a.home, "claude-mnemonic.db")
    snaps = glob.glob(os.path.join(a.home, "backups", "snapshot-*.db"))
    live = ro(live_path)
    try:
        lost, by_snapshot = find_lost(live, snaps, a.project_prefix)
        pending = live.execute("SELECT COUNT(*) FROM observations WHERE is_archived = 1 AND archived_reason LIKE ?", (REASON + "%",)).fetchone()[0]
        live_cols, sess_cols = set(columns(live, "observations")), set(columns(live, "sdk_sessions"))
        print(f"{len(snaps)} snapshots read (read-only); notes restored earlier and still hidden: {pending}")
        report(lost, by_snapshot, live)
    finally:
        live.close()
    if not a.apply:
        print("\nDRY RUN: nothing was written. Run again with --apply to restore.")
        return 0
    if not lost and not pending:
        print("nothing to do")
        return 0
    if not a.no_api:
        ok, version = worker_ok(a.api)
        print(f"worker: {version}, ready and has the archive endpoints: {ok}")
        if not ok:
            print("REFUSED: the worker must be running and new enough (v0.21.105.2 or later, no cap), or the notes could be deleted again.")
            return 2
    if lost:
        print("safety snapshot:", safety_snapshot(live_path, a.home))
        added, sessions = insert_hidden(live_path, live_cols, sess_cols, lost)
        print(f"inserted {added} notes (hidden for now) and {sessions} session rows, in one transaction")
    if a.no_api:
        return 0
    done, failed, total = unarchive_all(live_path, a.api, a.pause)
    print(f"unarchived {done} of {total} ({failed} failed); the worker is putting them back in the search index")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
