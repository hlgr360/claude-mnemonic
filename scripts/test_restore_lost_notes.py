#!/usr/bin/env python3
"""scripts/restore_lost_notes.py: putting back notes that are in a snapshot but not in the live database.

Run: python3 -m unittest scripts/test_restore_lost_notes.py -v
Everything runs on small made-up databases in a temporary directory with a fake worker; no real data or worker is touched.
"""
import http.server
import os
import socketserver
import sqlite3
import subprocess
import sys
import tempfile
import threading
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "restore_lost_notes.py")

OBS = """CREATE TABLE observations (file_mtimes TEXT, sdk_session_id TEXT NOT NULL, project TEXT NOT NULL, scope TEXT DEFAULT 'project', type TEXT NOT NULL,
 created_at TEXT NOT NULL, facts TEXT, narrative TEXT, concepts TEXT, files_read TEXT, files_modified TEXT, subtitle TEXT, title TEXT, archived_reason TEXT,
 score_updated_at_epoch INTEGER, prompt_number INTEGER, archived_at_epoch INTEGER, last_retrieved_at_epoch INTEGER, id INTEGER PRIMARY KEY AUTOINCREMENT,
 importance_score REAL DEFAULT 1, user_feedback INTEGER DEFAULT 0, retrieval_count INTEGER DEFAULT 0, created_at_epoch INTEGER NOT NULL,
 discovery_tokens INTEGER DEFAULT 0, is_superseded INTEGER DEFAULT 0, is_archived INTEGER DEFAULT 0 %s)"""
SESSIONS = """CREATE TABLE sdk_sessions (claude_session_id TEXT NOT NULL UNIQUE, project TEXT NOT NULL, status TEXT DEFAULT 'active', started_at TEXT NOT NULL,
 sdk_session_id TEXT UNIQUE, user_prompt TEXT, completed_at TEXT, worker_port INTEGER, completed_at_epoch INTEGER, id INTEGER PRIMARY KEY AUTOINCREMENT,
 prompt_counter INTEGER DEFAULT 0, started_at_epoch INTEGER NOT NULL)"""
SCOPE_SOURCE = ", scope_source TEXT NOT NULL DEFAULT ''"

P, OTHER, GONE = "mine_aaaaaa", "shop_bbbbbb", "mine_dead00"


def make_db(path, with_scope_source):
    con = sqlite3.connect(path)
    con.execute(OBS % (SCOPE_SOURCE if with_scope_source else ""))
    con.execute(SESSIONS)
    return con


def note(con, oid, project, session, title, facts="[]", scope_source=None, importance=1.0):
    cols = "id, sdk_session_id, project, type, created_at, created_at_epoch, title, narrative, facts, importance_score"
    vals = [oid, session, project, "discovery", "2026-10-03T10:00:00Z", 1_000_000_000_000, title, "narrative of " + title, facts, importance]
    if scope_source is not None:
        cols += ", scope_source"
        vals.append(scope_source)
    con.execute(f"INSERT INTO observations ({cols}) VALUES ({','.join('?' * len(vals))})", vals)


def session(con, sid, project):
    con.execute("INSERT INTO sdk_sessions (claude_session_id, project, started_at, started_at_epoch, sdk_session_id) VALUES (?,?,?,?,?)",
                (sid, project, "2026-10-03T09:00:00Z", 1_000_000_000_000, sid))


class FakeWorker(http.server.BaseHTTPRequestHandler):
    """Answers like a ready worker that knows the archive endpoints; unarchive clears the flags in the test database."""
    db = ""
    calls = []
    limited = set()

    def log_message(self, *args):
        pass

    def _send(self, code, body=b"{}"):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            self._send(200, b'{"ready":true,"version":"0.21.105.2"}')
        elif self.path.startswith("/api/observations?archived_only"):
            self._send(200, b'{"observations":[],"total":0}')
        else:
            self._send(404)

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))  # read the body, or the client sees a reset
        oid = int(self.path.split("/")[3])
        FakeWorker.calls.append(oid)
        if oid == 3 and oid not in FakeWorker.limited:  # the first call for note 3 is rate limited
            FakeWorker.limited.add(oid)
            return self._send(429)
        con = sqlite3.connect(FakeWorker.db)
        con.execute("UPDATE observations SET is_archived=0, archived_reason=NULL, archived_at_epoch=NULL WHERE id=?", (oid,))
        con.commit()
        con.close()
        self._send(200)


class QuickServer(http.server.HTTPServer):
    """HTTPServer without the reverse-DNS lookup of its own address, which can take seconds on some machines."""

    def server_bind(self):
        socketserver.TCPServer.server_bind(self)
        self.server_name, self.server_port = "localhost", self.socket.getsockname()[1]


class RestoreLostNotes(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="restore-test-")
        os.makedirs(f"{self.home}/backups")
        self.live = f"{self.home}/claude-mnemonic.db"
        # live: two notes of P, one of OTHER; session s1 exists, s2 and s9 do not
        con = make_db(self.live, True)
        session(con, "s1", P)
        session(con, "sO", OTHER)
        note(con, 100, P, "s1", "live one", scope_source="auto")
        note(con, 101, P, "s1", "live two", scope_source="auto")
        note(con, 300, OTHER, "sO", "other live", scope_source="auto")
        con.commit()
        con.close()
        # an older snapshot without scope_source: lost notes 1, 2 (an old copy), 3 (saved explicitly), a note that is live, other project, gone project.
        # facts are stored as bytes in some real rows: note 1 and 3 do that here
        old = make_db(f"{self.home}/backups/snapshot-20261003-184052.111-delete-x_111111.db", False)
        session(old, "s1", P)
        session(old, "s2", P)
        session(old, "sO", OTHER)
        note(old, 1, P, "s1", "lost one", facts=b'["a plain fact kept as bytes"]')
        note(old, 2, P, "s2", "lost two OLD copy")
        note(old, 3, P, "s2", "lost three", facts=b'["Saved explicitly via remember"]')
        note(old, 100, P, "s1", "live one")
        note(old, 400, OTHER, "sO", "other lost")
        note(old, 500, GONE, "sX", "gone project")
        old.commit()
        old.close()
        # a newer snapshot: the newer copy of note 2, and note 4 whose session is in no snapshot
        new = make_db(f"{self.home}/backups/snapshot-20261004-122933.351-rescope.db", True)
        session(new, "s1", P)
        note(new, 2, P, "s2", "lost two NEW copy", scope_source="auto", importance=2.5)
        note(new, 4, P, "s9", "lost four", scope_source="explicit")
        new.commit()
        new.close()

        FakeWorker.db, FakeWorker.calls, FakeWorker.limited = self.live, [], set()
        self.server = QuickServer(("127.0.0.1", 0), FakeWorker)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.api = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def run_script(self, *args):
        r = subprocess.run([sys.executable, SCRIPT, "--home", self.home, "--project-prefix", "mine_", "--api", self.api, "--pause", "0", *args],
                           capture_output=True, text=True)
        return r.returncode, r.stdout + r.stderr

    def q(self, sql, *params):
        con = sqlite3.connect(f"file:{self.live}?mode=ro", uri=True)
        try:
            return con.execute(sql, params).fetchall()
        finally:
            con.close()

    def count(self):
        return self.q("SELECT COUNT(*) FROM observations")[0][0]

    def test_dry_run_reports_counts_never_text_and_writes_nothing(self):
        before = self.count()
        code, out = self.run_script()
        self.assertEqual(code, 0, out)
        self.assertIn("DRY RUN", out)
        self.assertIn("lost notes found: 4", out, "the lost notes of the existing project only: not a live one, another project or a gone project")
        for text in ("lost one", "narrative", "fact"):
            self.assertNotIn(text, out, "a dry run prints counts, never what a note says")
        self.assertEqual(self.count(), before)
        self.assertEqual([f for f in os.listdir(f"{self.home}/backups") if "before-restore" in f], [], "no snapshot for a dry run")

    def test_apply_inserts_the_notes_hidden_faithfully_after_a_safety_snapshot(self):
        before = self.count()
        code, out = self.run_script("--apply", "--no-api")
        self.assertEqual(code, 0, out)
        self.assertIn("inserted 4 notes", out)
        rows = {r[0]: r for r in self.q("SELECT id, is_archived, archived_reason, title, scope_source, importance_score, sdk_session_id FROM observations WHERE id IN (1,2,3,4)")}
        self.assertEqual(len(rows), 4)
        for r in rows.values():
            self.assertEqual(r[1], 1, "hidden until the worker has put it back in the index")
            self.assertTrue(r[2].startswith("restored from snapshot "))
        self.assertEqual((rows[2][3], rows[2][5]), ("lost two NEW copy", 2.5), "the newest snapshot's copy wins")
        self.assertEqual({k: v[4] for k, v in rows.items()}, {1: "auto", 2: "auto", 3: "explicit", 4: "explicit"},
                         "an old snapshot has no scope_source: it is derived, even when facts are bytes; a newer one keeps its own")
        self.assertEqual(self.q("SELECT COUNT(*) FROM observations WHERE id IN (100,101,300)")[0][0], 3, "live notes untouched")
        self.assertEqual(self.q("SELECT COUNT(*) FROM observations WHERE id IN (400,500)")[0][0], 0, "other and gone projects not restored")
        self.assertEqual(self.q("SELECT COUNT(*) FROM sdk_sessions WHERE sdk_session_id IN ('s2','s9')")[0][0], 2,
                         "a missing session is copied from its snapshot, an unknown one is made")
        snaps = [f for f in os.listdir(f"{self.home}/backups") if "before-restore-lost-notes" in f]
        self.assertEqual(len(snaps), 1, "a safety snapshot of the live database came first")
        con = sqlite3.connect(f"file:{self.home}/backups/{snaps[0]}?mode=ro", uri=True)
        self.assertEqual(con.execute("SELECT COUNT(*) FROM observations").fetchone()[0], before, "and holds the database as it was")
        con.close()

    def test_second_run_unarchives_through_the_api_retrying_429_and_is_then_a_no_op(self):
        self.run_script("--apply", "--no-api")
        code, out = self.run_script("--apply")
        self.assertEqual(code, 0, out)
        self.assertIn("unarchived 4 of 4 (0 failed)", out)
        self.assertEqual(self.q("SELECT COUNT(*) FROM observations WHERE id IN (1,2,3,4) AND is_archived=0 AND archived_reason IS NULL")[0][0], 4)
        self.assertEqual(FakeWorker.calls.count(3), 2, "the rate-limited call was repeated")
        after = self.count()
        code, out = self.run_script("--apply")
        self.assertEqual(code, 0, out)
        self.assertIn("nothing to do", out)
        self.assertEqual(self.count(), after)

    def test_apply_in_one_go_is_the_same(self):
        code, out = self.run_script("--apply")
        self.assertEqual(code, 0, out)
        self.assertIn("inserted 4 notes", out)
        self.assertIn("unarchived 4 of 4", out)

    def test_refused_without_a_ready_worker_and_nothing_is_written(self):
        self.server.shutdown()
        self.server.server_close()  # a stopped worker refuses the connection at once
        before = self.count()
        code, out = self.run_script("--apply")
        self.assertEqual(code, 2, out)
        self.assertIn("REFUSED", out)
        self.assertEqual(self.count(), before)
        self.assertEqual([f for f in os.listdir(f"{self.home}/backups") if "before-restore" in f], [])


if __name__ == "__main__":
    unittest.main()
