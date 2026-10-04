#!/usr/bin/env python3
"""The /claude-mnemonic:dashboard command: its shell snippet really finds the worker's port (environment, then
settings.json, then 37777), opens the dashboard only when the worker answers, and the command is listed in the plugin
manifest. The browser opener is a stub, so nothing is opened."""
import json
import os
import re
import stat
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
COMMAND = os.path.join(ROOT, "commands", "dashboard.md")


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def snippet():
    return re.search(r"```bash\n(.*?)```", read(COMMAND), re.S).group(1)


class Health(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200 if self.path == "/health" else 404)
        self.end_headers()
        self.wfile.write(b'{"ready":true}')

    def log_message(self, *args):
        pass


class DashboardCommand(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="dashboard-cmd-")
        self.bin = os.path.join(self.home, "bin")
        os.makedirs(self.bin)
        self.opened = os.path.join(self.home, "opened.txt")
        for name in ("open", "xdg-open"):
            path = os.path.join(self.bin, name)
            with open(path, "w") as f:
                f.write(f'#!/bin/sh\necho "$1" >> "{self.opened}"\n')
            os.chmod(path, os.stat(path).st_mode | stat.S_IEXEC)
        self.server = HTTPServer(("127.0.0.1", 0), Health)
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.shutdown)

    def run_command(self, env_port=None, settings=None):
        os.makedirs(os.path.join(self.home, ".claude-mnemonic"), exist_ok=True)
        if settings is not None:
            with open(os.path.join(self.home, ".claude-mnemonic", "settings.json"), "w") as f:
                f.write(settings)
        env = {"HOME": self.home, "PATH": self.bin + ":" + os.environ["PATH"]}
        if env_port is not None:
            env["CLAUDE_MNEMONIC_WORKER_PORT"] = str(env_port)
        out = subprocess.run(["bash", "-c", snippet()], env=env, capture_output=True, text=True, timeout=30)
        self.assertEqual(out.returncode, 0, out.stderr)
        opened = read(self.opened).split() if os.path.exists(self.opened) else []
        return out.stdout.strip(), opened

    def test_the_port_from_the_environment_is_opened_when_the_worker_answers(self):
        out, opened = self.run_command(env_port=self.port)
        self.assertEqual(out, f"http://localhost:{self.port}")
        self.assertEqual(opened, [f"http://localhost:{self.port}"])

    def test_the_port_from_settings_json_is_used_without_the_environment(self):
        out, opened = self.run_command(settings=json.dumps({"CLAUDE_MNEMONIC_WORKER_PORT": self.port, "OTHER": 1}))
        self.assertEqual(out, f"http://localhost:{self.port}")
        self.assertEqual(opened, [out])

    def test_the_environment_wins_over_settings_json(self):
        out, _ = self.run_command(env_port=self.port, settings='{"CLAUDE_MNEMONIC_WORKER_PORT": 1}')
        self.assertEqual(out, f"http://localhost:{self.port}")

    def test_a_worker_that_is_not_running_is_reported_and_nothing_is_opened(self):
        self.server.shutdown()
        self.server.server_close()
        out, opened = self.run_command(env_port=self.port)
        self.assertEqual(out, f"NOT RUNNING http://localhost:{self.port}")
        self.assertEqual(opened, [])

    def test_the_default_port_is_37777(self):
        out, _ = self.run_command()
        self.assertTrue(out.endswith("http://localhost:37777"), out)


class Manifest(unittest.TestCase):
    def test_the_command_is_listed_in_the_plugin_manifest_and_exists(self):
        manifest = json.loads(read(os.path.join(ROOT, ".claude-plugin", "plugin.json")))
        self.assertIn("./commands/dashboard.md", manifest["commands"])
        self.assertIn("./commands/restart.md", manifest["commands"], "the existing command is still there")
        for rel in manifest["commands"]:
            self.assertTrue(os.path.isfile(os.path.join(ROOT, rel)), rel)

    def test_the_command_has_a_description_and_only_the_tools_it_needs(self):
        text = read(COMMAND)
        front = text.split("---")[1]
        self.assertRegex(front, r"description: .+dashboard")
        allowed = re.search(r"allowed-tools: (.+)", front).group(1)
        self.assertEqual(sorted(re.findall(r"Bash\((\w[\w-]*):\*\)", allowed)), ["curl", "echo", "grep", "open", "xdg-open"])

    def test_make_install_ships_every_command_and_prints_the_url(self):
        makefile = read(os.path.join(ROOT, "Makefile"))
        self.assertIn("cp commands/*.md", makefile)
        self.assertRegex(makefile, r"Dashboard: http://localhost:\$\$\{CLAUDE_MNEMONIC_WORKER_PORT:-37777\}")


if __name__ == "__main__":
    unittest.main()
