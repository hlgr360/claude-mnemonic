#!/usr/bin/env python3
"""The /memory-restart command: its shell snippet finds the worker's port (environment, then settings.json, then 37777),
posts the restart and reports the version only when the worker answers again.

The plugin is for Claude Code only. Claude Desktop's chat and Cowork get `dashboard` and `restart` as tools from the
Desktop extension, because their shell is a sandbox that cannot reach this computer."""
import json
import os
import re
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RESTART = os.path.join(ROOT, "commands", "memory-restart.md")
DASHBOARD = os.path.join(ROOT, "commands", "memory-dashboard.md")


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def snippet():
    return re.search(r"```bash\n(.*?)```", read(RESTART), re.S).group(1)


def worker(version_answers):
    posted = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            posted.append(self.path)
            self.send_response(200 if self.path == "/api/restart" else 404)
            self.end_headers()
            self.wfile.write(b'{"success":true}')

        def do_GET(self):
            ok = self.path == "/api/version" and version_answers
            self.send_response(200 if ok else 404)
            self.end_headers()
            self.wfile.write(b'{"version":"9.9.9"}')

        def log_message(self, *args):
            pass

    server = HTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, posted


class RestartCommand(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="restart-cmd-")
        os.makedirs(os.path.join(self.home, ".claude-mnemonic"))

    def run_command(self, env_port=None, settings=None):
        if settings is not None:
            with open(os.path.join(self.home, ".claude-mnemonic", "settings.json"), "w") as f:
                f.write(settings)
        env = {"HOME": self.home, "PATH": os.environ["PATH"]}
        if env_port is not None:
            env["CLAUDE_MNEMONIC_WORKER_PORT"] = str(env_port)
        out = subprocess.run(["bash", "-c", snippet()], env=env, capture_output=True, text=True, timeout=60)
        self.assertEqual(out.returncode, 0, out.stderr)
        return out.stdout.strip()

    def test_the_port_from_the_environment_is_restarted_and_the_version_is_reported(self):
        server, posted = worker(True)
        self.addCleanup(server.shutdown)
        out = self.run_command(env_port=server.server_address[1])
        self.assertEqual(out, 'RESTARTED {"version":"9.9.9"}')
        self.assertEqual(posted, ["/api/restart"])

    def test_the_port_from_settings_json_is_used_and_the_environment_wins(self):
        server, posted = worker(True)
        self.addCleanup(server.shutdown)
        port = server.server_address[1]
        self.assertTrue(self.run_command(settings=json.dumps({"CLAUDE_MNEMONIC_WORKER_PORT": port})).startswith("RESTARTED"))
        self.assertTrue(self.run_command(env_port=port, settings='{"CLAUDE_MNEMONIC_WORKER_PORT": 1}').startswith("RESTARTED"))
        self.assertEqual(posted, ["/api/restart", "/api/restart"])

    def test_a_worker_that_is_not_running_is_reported_and_nothing_is_claimed(self):
        server, _ = worker(True)
        port = server.server_address[1]
        server.shutdown()
        server.server_close()
        self.assertEqual(self.run_command(env_port=port), f"NOT RUNNING http://127.0.0.1:{port}")

    def test_a_worker_that_does_not_come_back_is_not_reported_as_restarted(self):
        server, posted = worker(False)  # accepts the restart, never answers the version again
        self.addCleanup(server.shutdown)
        port = server.server_address[1]
        self.assertEqual(self.run_command(env_port=port), f"NOT BACK http://127.0.0.1:{port}")
        self.assertEqual(posted, ["/api/restart"])

    def test_the_default_port_is_37777_not_a_hard_coded_url(self):
        text = read(RESTART)
        self.assertIn("${port:-37777}", text)
        self.assertNotIn("curl -X POST http://127.0.0.1:37777", text, "the port was hard-coded before")


class Allowed(unittest.TestCase):
    def test_the_restart_command_only_allows_the_tools_it_needs(self):
        front = read(RESTART).split("---")[1]
        allowed = re.search(r"allowed-tools: (.+)", front).group(1)
        self.assertEqual(sorted(re.findall(r"Bash\((\w[\w-]*):\*\)", allowed)), ["curl", "echo", "grep", "sleep"])


if __name__ == "__main__":
    unittest.main()
