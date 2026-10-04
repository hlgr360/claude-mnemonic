#!/usr/bin/env python3
"""The thin plugin: scripts/build-plugin.sh, plugin/lib/ensure-binaries.sh and the hook / MCP wrappers.

Run: python3 -m unittest scripts/test_plugin.py -v
Everything runs in temporary directories against a fixture "release" served over file://; HOME is a temporary
directory, so the real ~/.claude-mnemonic is never touched, and no real binary is started. cosign and claude are
replaced by small fakes on PATH, so the results do not depend on what is installed on the machine.
"""
import hashlib
import json
import os
import platform
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)

import package_release  # noqa: E402

VERSION = "1.2.3"
SYSTEM = {("Darwin", "arm64"): "darwin_arm64", ("Linux", "x86_64"): "linux_amd64"}.get((platform.system(), platform.machine()))
ARCHIVE = f"claude-mnemonic_{VERSION}_{SYSTEM}.tar.gz"
HOOKS = ["session-start", "user-prompt", "post-tool-use", "subagent-stop", "stop", "pre-compact", "statusline"]


def write(path, text, mode=0o755):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    os.chmod(path, mode)


def sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def fake_cosign(directory, exit_code=0):
    """A cosign that records its arguments and exits with exit_code."""
    write(os.path.join(directory, "cosign"), f'#!/bin/sh\necho "$@" >> "{directory}/cosign.calls"\nexit {exit_code}\n')


class Fixture:
    """A temporary HOME, a fixture release directory, a fake-tool directory and a built plugin tree."""

    def __init__(self, test, version=VERSION):
        self.tmp = tempfile.TemporaryDirectory()
        test.addCleanup(self.tmp.cleanup)
        root = self.tmp.name
        self.home = os.path.join(root, "home")
        self.release = os.path.join(root, "release")
        self.tools = os.path.join(root, "tools")
        self.dist = os.path.join(root, "dist")
        for d in (self.home, self.release, self.tools):
            os.makedirs(d)
        fake_cosign(self.tools)
        self.bin = os.path.join(self.home, ".claude-mnemonic", "bin")
        self.tree = self.build(version)

    def build(self, version, **extra_env):
        env = dict(os.environ, DIST=self.dist, SKIP_VALIDATE="1", **extra_env)
        subprocess.run(["bash", os.path.join(HERE, "build-plugin.sh"), version], cwd=REPO_ROOT, env=env, check=True, capture_output=True)
        return os.path.join(self.dist, "plugin")

    def make_release(self, version=VERSION, wrong_checksum=False, missing_checksum=False):
        stage = os.path.join(self.tmp.name, "stage")
        shutil.rmtree(stage, ignore_errors=True)
        write(os.path.join(stage, "worker"), "#!/bin/sh\necho fixture worker\n")
        write(os.path.join(stage, "mcp-server"), '#!/bin/sh\necho "mcp-server fixture $@"\n')
        for h in HOOKS:
            write(os.path.join(stage, "hooks", h), f"#!/bin/sh\necho fixture {h}\n")
        write(os.path.join(stage, "hooks", "hooks.json"), "{}", 0o644)
        write(os.path.join(stage, "commands", "memory-dashboard.md"), "x", 0o644)
        archive = os.path.join(self.release, f"claude-mnemonic_{version}_{SYSTEM}.tar.gz")
        package_release.pack(stage, archive)
        digest = "0" * 64 if wrong_checksum else sha256(archive)
        lines = [] if missing_checksum else [f"{digest}  {os.path.basename(archive)}"]
        write(os.path.join(self.release, "checksums.txt"), "\n".join(lines) + "\n", 0o644)
        write(os.path.join(self.release, "checksums.txt.sigstore.json"), "{}", 0o644)

    def env(self, base=None, path=None):
        return dict(
            os.environ,
            HOME=self.home,
            PATH=(self.tools + os.pathsep + os.environ["PATH"]) if path is None else path,
            MNEMONIC_RELEASE_BASE=base or ("file://" + self.release),
        )

    def ensure(self, *args, **kw):
        return subprocess.run(["sh", os.path.join(self.tree, "lib", "ensure-binaries.sh"), *args], env=self.env(**kw), capture_output=True, text=True, timeout=60)

    def preinstall(self, marker=None):
        write(os.path.join(self.bin, "worker"), "#!/bin/sh\necho existing worker\n")
        write(os.path.join(self.bin, "mcp-server"), "#!/bin/sh\necho existing mcp\n")
        if marker:
            write(os.path.join(self.bin, ".plugin-version"), marker + "\n", 0o644)

    def installed(self):
        return os.path.isfile(os.path.join(self.bin, "worker"))

    def leftovers(self):
        data = os.path.join(self.home, ".claude-mnemonic")
        return [n for n in os.listdir(data) if n.startswith(".install")] if os.path.isdir(data) else []


@unittest.skipUnless(SYSTEM and shutil.which("bash") and shutil.which("curl") and shutil.which("tar"), "needs a supported platform, bash, curl and tar")
class EnsureBinaries(unittest.TestCase):
    def test_installs_the_verified_binaries_and_records_the_version(self):
        f = Fixture(self)
        f.make_release()
        out = f.ensure()
        self.assertEqual(out.returncode, 0, out.stderr)
        for name in ("worker", "mcp-server", *[f"hooks/{h}" for h in HOOKS]):
            self.assertTrue(os.access(os.path.join(f.bin, name), os.X_OK), name)
        self.assertFalse(os.path.exists(os.path.join(f.bin, "hooks", "hooks.json")), "the hook definitions stay in the plugin")
        with open(os.path.join(f.bin, ".plugin-version"), encoding="utf-8") as fh:
            self.assertEqual(fh.read().strip(), VERSION)
        self.assertEqual(f.leftovers(), [], "no temporary directory or lock is left behind")
        with open(os.path.join(f.tools, "cosign.calls"), encoding="utf-8") as fh:
            calls = fh.read()
        self.assertIn("verify-blob", calls)
        self.assertIn("^https://github\\.com/hlgr360/claude-mnemonic/.*$", calls)
        self.assertIn("https://token.actions.githubusercontent.com", calls)

    def test_a_wrong_checksum_is_refused(self):
        f = Fixture(self)
        f.make_release(wrong_checksum=True)
        out = f.ensure()
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("checksum mismatch", out.stderr)
        self.assertFalse(f.installed(), "nothing is installed from an archive that fails its checksum")
        self.assertFalse(os.path.exists(os.path.join(f.bin, ".plugin-version")))
        self.assertEqual(f.leftovers(), [])

    def test_an_archive_missing_from_the_checksums_is_refused(self):
        f = Fixture(self)
        f.make_release(missing_checksum=True)
        out = f.ensure()
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("no entry", out.stderr)
        self.assertFalse(f.installed())

    def test_a_failed_signature_check_is_refused(self):
        f = Fixture(self)
        f.make_release()
        fake_cosign(f.tools, exit_code=1)
        out = f.ensure()
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("signature check failed", out.stderr)
        self.assertFalse(f.installed())

    @unittest.skipIf(shutil.which("cosign"), "a real cosign is installed; the no-cosign path cannot be set up")
    def test_without_cosign_only_the_checksum_is_checked_and_it_says_so(self):
        f = Fixture(self)
        f.make_release()
        os.remove(os.path.join(f.tools, "cosign"))
        out = f.ensure()
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertIn("checksum only", out.stderr)
        self.assertTrue(f.installed())

    def test_binaries_from_make_install_are_never_replaced(self):
        f = Fixture(self)
        f.make_release()
        f.preinstall()  # no marker: a developer build or install.sh
        out = f.ensure(base="file:///nonexistent")
        self.assertEqual(out.returncode, 0, out.stderr)
        with open(os.path.join(f.bin, "worker"), encoding="utf-8") as fh:
            self.assertIn("existing worker", fh.read())

    def test_an_older_installation_is_brought_up_to_the_plugin_version(self):
        f = Fixture(self)
        f.make_release()
        f.preinstall(marker="1.0.0")
        out = f.ensure()
        self.assertEqual(out.returncode, 0, out.stderr)
        with open(os.path.join(f.bin, "worker"), encoding="utf-8") as fh:
            self.assertIn("fixture worker", fh.read())
        with open(os.path.join(f.bin, ".plugin-version"), encoding="utf-8") as fh:
            self.assertEqual(fh.read().strip(), VERSION)

    def test_a_newer_or_equal_installation_is_left_alone_without_any_download(self):
        for marker in ("1.2.3", "2.0.0", "1.10.0"):
            with self.subTest(marker=marker):
                f = Fixture(self)
                f.preinstall(marker=marker)
                out = f.ensure(base="file:///nonexistent")
                self.assertEqual(out.returncode, 0, out.stderr)
                with open(os.path.join(f.bin, ".plugin-version"), encoding="utf-8") as fh:
                    self.assertEqual(fh.read().strip(), marker)

    def test_versions_compare_numerically(self):
        f = Fixture(self)
        f.make_release()
        f.preinstall(marker="1.2.10")  # higher than 1.2.3 numerically, lower as text
        out = f.ensure(base="file:///nonexistent")
        self.assertEqual(out.returncode, 0, out.stderr)

    def test_fork_numbers_compare_numerically_and_upgrade_from_the_plain_upstream_version(self):
        # This fork's releases are the upstream version plus a number: 0.21.95.1, 0.21.95.2, ... (installed, plugin, reinstall?)
        for installed, plugin, reinstall in (
            ("0.21.95.9", "0.21.95.10", True),   # 10 is higher than 9, as numbers
            ("0.21.95", "0.21.95.1", True),      # the first fork release is above upstream's plain version
            ("0.21.95.1", "0.21.95.2", True),
            ("0.21.95.10", "0.21.95.9", False),  # never a downgrade
            ("0.21.95.1", "0.21.95", False),
            ("0.21.95.4", "0.21.95.4", False),
            ("0.21.95.4", "0.21.96.1", True),    # a new upstream version starts again at .1
        ):
            with self.subTest(installed=installed, plugin=plugin):
                f = Fixture(self, version=plugin)
                f.make_release(version=plugin)
                f.preinstall(marker=installed)
                out = f.ensure() if reinstall else f.ensure(base="file:///nonexistent")
                self.assertEqual(out.returncode, 0, out.stderr)
                with open(os.path.join(f.bin, ".plugin-version"), encoding="utf-8") as fh:
                    self.assertEqual(fh.read().strip(), plugin if reinstall else installed)

    def test_two_installers_at_once_install_once_and_both_succeed(self):
        f = Fixture(self)
        f.make_release()
        procs = [subprocess.Popen(["sh", os.path.join(f.tree, "lib", "ensure-binaries.sh")], env=f.env(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) for _ in range(2)]
        results = [p.communicate(timeout=60) + (p.returncode,) for p in procs]
        self.assertEqual([r[2] for r in results], [0, 0], [r[1] for r in results])
        self.assertTrue(f.installed())
        self.assertEqual(sum("installing" in r[1] for r in results), 1, "only one of them downloads")
        self.assertEqual(f.leftovers(), [])

    def test_a_platform_without_a_release_build_is_refused_with_a_hint(self):
        f = Fixture(self)
        f.make_release()
        write(os.path.join(f.tools, "uname"), '#!/bin/sh\ncase "$1" in -s) echo MINGW64_NT;; *) echo x86_64;; esac\n')
        out = f.ensure()
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("no release build", out.stderr)
        self.assertFalse(f.installed())


@unittest.skipUnless(SYSTEM and shutil.which("bash") and shutil.which("curl") and shutil.which("tar"), "needs a supported platform, bash, curl and tar")
class Wrappers(unittest.TestCase):
    def run_wrapper(self, f, *argv, **kw):
        return subprocess.run([os.path.join(f.tree, *argv[0].split("/")), *argv[1:]], env=f.env(**kw), capture_output=True, text=True, timeout=60, stdin=subprocess.DEVNULL)

    def test_a_hook_without_binaries_returns_at_once_and_the_download_happens_in_the_background(self):
        f = Fixture(self)
        f.make_release()
        started = time.time()
        out = self.run_wrapper(f, "hooks/post-tool-use")
        self.assertEqual((out.returncode, out.stdout), (0, ""), out.stderr)
        self.assertLess(time.time() - started, 5)
        deadline = time.time() + 30
        while time.time() < deadline and not os.path.exists(os.path.join(f.bin, ".plugin-version")):
            time.sleep(0.2)
        self.assertTrue(f.installed(), "the background installer finished")

    def test_a_hook_runs_the_installed_binary_with_its_arguments(self):
        f = Fixture(self)
        write(os.path.join(f.bin, "hooks", "stop"), '#!/bin/sh\necho "hook stop got $1"\n')
        out = self.run_wrapper(f, "hooks/stop", "arg1")
        self.assertEqual(out.stdout.strip(), "hook stop got arg1", out.stderr)

    def test_the_session_start_hook_does_not_download_when_the_installation_is_current(self):
        f = Fixture(self)
        f.preinstall(marker=VERSION)
        write(os.path.join(f.bin, "hooks", "session-start"), "#!/bin/sh\necho started\n")
        out = self.run_wrapper(f, "hooks/session-start", base="file:///nonexistent")
        self.assertEqual(out.stdout.strip(), "started")
        self.assertEqual(f.leftovers(), [])

    def test_the_mcp_server_waits_for_the_install_and_then_runs(self):
        f = Fixture(self)
        f.make_release()
        out = self.run_wrapper(f, "mcp-server", "--stdio")
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertEqual(out.stdout.strip(), "mcp-server fixture --stdio", "stdout carries only the server's own output")
        self.assertIn("installed", out.stderr)

    def test_the_mcp_server_fails_with_a_message_when_the_download_fails(self):
        f = Fixture(self)
        out = self.run_wrapper(f, "mcp-server", base="file:///nonexistent")
        self.assertEqual(out.returncode, 1)
        self.assertIn("mcp-server not found", out.stderr)


class BuildPlugin(unittest.TestCase):
    def test_the_tree_is_complete_stamped_and_has_no_binaries(self):
        f = Fixture(self, version="3.4.5")
        files = sorted(os.path.relpath(os.path.join(r, n), f.tree) for r, _d, ns in os.walk(f.tree) for n in ns)
        self.assertEqual(
            files,
            sorted([".claude-plugin/plugin.json", "LICENSE", "README.md", "hooks/hooks.json", "lib/ensure-binaries.sh", "mcp-server", "skills/memory-dashboard/SKILL.md", "skills/memory-restart/SKILL.md", "skills/project-memory/SKILL.md"] + [f"hooks/{h}" for h in HOOKS]),
        )
        with open(os.path.join(f.tree, ".claude-plugin", "plugin.json"), encoding="utf-8") as fh:
            manifest = json.load(fh)
        self.assertEqual(manifest["version"], "3.4.5")
        self.assertEqual(manifest["name"], "claude-mnemonic")
        self.assertEqual(manifest["mcpServers"]["claude-mnemonic"]["command"], "${CLAUDE_PLUGIN_ROOT}/mcp-server")
        # A `commands` list in the manifest replaces the default commands/ scan, which hid the dashboard command.
        self.assertNotIn("commands", manifest)
        # hooks/hooks.json is the default location; declaring it as well made Desktop count the hooks twice (12 for 6).
        self.assertNotIn("hooks", manifest)
        # commands/ is the older format: the slash commands ship as skills, so there is no commands/ directory.
        self.assertFalse(os.path.exists(os.path.join(f.tree, "commands")))
        for h in HOOKS:
            self.assertTrue(os.access(os.path.join(f.tree, "hooks", h), os.X_OK), h)
        # Cowork does not install a plugin that has a top-level bin/ directory.
        self.assertFalse(os.path.exists(os.path.join(f.tree, "bin")))
        self.assertLess(os.path.getsize(os.path.join(f.dist, "claude-mnemonic-plugin_3.4.5.zip")), 1_000_000)

    def test_every_hook_command_points_at_a_wrapper_in_the_tree(self):
        f = Fixture(self)
        with open(os.path.join(f.tree, "hooks", "hooks.json"), encoding="utf-8") as fh:
            hooks = json.load(fh)["hooks"]
        commands = [h["command"] for groups in hooks.values() for g in groups for h in g["hooks"]]
        self.assertEqual(len(commands), 6)
        for c in commands:
            rel = c.replace('"${CLAUDE_PLUGIN_ROOT}/', "").rstrip('"')
            self.assertTrue(os.access(os.path.join(f.tree, rel), os.X_OK), c)

    def test_the_zip_is_the_same_bytes_every_time(self):
        f = Fixture(self)
        zip_path = os.path.join(f.dist, f"claude-mnemonic-plugin_{VERSION}.zip")
        first = sha256(zip_path)
        time.sleep(1.1)
        f.build(VERSION)
        self.assertEqual(sha256(zip_path), first)

    def test_a_fork_of_the_fork_downloads_from_its_own_releases(self):
        f = Fixture(self)
        f.build(VERSION, MNEMONIC_REPO="someone/else")
        with open(os.path.join(f.tree, "lib", "ensure-binaries.sh"), encoding="utf-8") as fh:
            self.assertIn('DEFAULT_REPO="someone/else"', fh.read())
        with open(os.path.join(f.tree, ".claude-plugin", "plugin.json"), encoding="utf-8") as fh:
            manifest = json.load(fh)
        self.assertEqual((manifest["homepage"], manifest["repository"]), ("https://github.com/someone/else",) * 2)
        with open(os.path.join(f.tree, "README.md"), encoding="utf-8") as fh:
            readme = fh.read()
        self.assertIn("https://github.com/someone/else.", readme)
        self.assertIn("github.com/lukaszraczylo/claude-mnemonic", readme, "the credit to upstream is not rewritten")
        with open(os.path.join(REPO_ROOT, "plugin", "lib", "ensure-binaries.sh"), encoding="utf-8") as fh:
            self.assertIn('DEFAULT_REPO="hlgr360/claude-mnemonic"', fh.read(), "the source file is not changed")


class MemorySkill(unittest.TestCase):
    def skill(self):
        f = Fixture(self)
        with open(os.path.join(f.tree, "skills", "project-memory", "SKILL.md"), encoding="utf-8") as fh:
            return fh.read()

    def test_the_skill_carries_the_instruction_that_is_pasted_into_desktop(self):
        text = self.skill()
        with open(os.path.join(HERE, "desktop-instructions.txt"), encoding="utf-8") as fh:
            template = fh.read()
        rendered = template.replace("{connector}", "claude-mnemonic").replace("{name}", "claude-mnemonic")
        self.assertIn(rendered, text, "one source: the skill body contains the pasted instruction word for word")
        for tool in ("project_suggest", "catch_up", "checkpoint", "related", "dashboard"):
            self.assertIn(tool, text)

    def test_the_description_triggers_on_the_users_own_work_and_the_words_that_select_built_in_memory(self):
        text = self.skill()
        front = text.split("---")[1]
        self.assertTrue(front.startswith("\nname: project-memory\n"))
        description = json.loads(front.split("description: ", 1)[1])
        for phrase in ("past work", "earlier decisions", '"memory"', '"remember"', "claude-mnemonic", "Not for general questions"):
            self.assertIn(phrase, description)
        self.assertLess(len(description), 1024, "the description is read in every conversation: keep it short")

    def test_the_skill_is_background_knowledge_not_a_slash_command(self):
        # Without this a person could run /claude-mnemonic:project-memory by hand; the plugin's slash commands are only
        # memory-dashboard and memory-restart. The model still sees the description (that is what makes it use the skill).
        front = self.skill().split("---")[1]
        self.assertIn("\nuser-invocable: false\n", front)
        self.assertNotIn("disable-model-invocation", front, "that would take the description out of the model's context")

    def test_only_the_two_memory_commands_can_be_run_by_hand(self):
        f = Fixture(self)
        runnable = []
        for name in sorted(os.listdir(os.path.join(f.tree, "skills"))):
            with open(os.path.join(f.tree, "skills", name, "SKILL.md"), encoding="utf-8") as fh:
                front = fh.read().split("---")[1]
            if "user-invocable: false" not in front:
                runnable.append(name)
        self.assertEqual(runnable, ["memory-dashboard", "memory-restart"])

    def test_claude_code_is_told_to_step_aside(self):
        text = self.skill()
        self.assertIn("no project_suggest tool, you are in Claude Code", text)

    def test_the_front_matter_is_valid_json_quoted_yaml(self):
        front = self.skill().split("---")[1]
        self.assertEqual(front.count("\n"), 4, "name, user-invocable and description, each on one line")


class UploadLimits(unittest.TestCase):
    """Claude's upload form rejects what `claude plugin validate` accepts: "Plugin description must be at most 500
    characters". The build checks the limits the form is known to enforce."""

    def tree_with_description(self, text):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        os.makedirs(os.path.join(tmp.name, ".claude-plugin"))
        with open(os.path.join(tmp.name, ".claude-plugin", "plugin.json"), "w", encoding="utf-8") as fh:
            json.dump({"name": "x", "version": "1.0.0", "description": text}, fh)
        return tmp.name

    def run_check(self, tree):
        return subprocess.run([sys.executable, os.path.join(HERE, "check_plugin_manifest.py"), tree], capture_output=True, text=True)

    def test_exactly_500_characters_pass_and_501_fail(self):
        ok = self.run_check(self.tree_with_description("x" * 500))
        self.assertEqual(ok.returncode, 0, ok.stderr)
        bad = self.run_check(self.tree_with_description("x" * 501))
        self.assertEqual(bad.returncode, 1)
        self.assertIn("501 characters, 1 over the limit of 500", bad.stderr)
        self.assertIn("Plugin description must be at most 500 characters", bad.stderr, "the form's own message is quoted")

    def test_characters_are_counted_not_bytes(self):
        # The form counts characters: 500 two-byte characters are fine.
        self.assertEqual(self.run_check(self.tree_with_description("é" * 500)).returncode, 0)

    def test_the_built_plugin_is_within_the_limit_and_the_build_runs_the_check(self):
        f = Fixture(self)
        with open(os.path.join(f.tree, ".claude-plugin", "plugin.json"), encoding="utf-8") as fh:
            description = json.load(fh)["description"]
        self.assertLessEqual(len(description), 500, f"the description is {len(description)} characters")
        self.assertIn('check_plugin_manifest.py "$TREE"', read_file(os.path.join(HERE, "build-plugin.sh")), "the build must run the check")

    def test_the_check_refuses_the_description_that_was_released_too_long(self):
        # v0.21.95.1 shipped a 514-character description and could not be uploaded to an org.
        released = (
            "Persistent memory for Claude Code and Claude Desktop: the decisions, findings and fixes from your earlier sessions, "
            "searchable and shared by both, with a local web dashboard. A local worker (SQLite and embeddings) stores it on your "
            "computer; the binaries are downloaded from this fork's release on first use and verified. In Claude Desktop chat, "
            "paste the instruction from README.md into your personal preferences once, or chat answers from its built-in memory "
            "instead. Fork of lukaszraczylo/claude-mnemonic (MIT)."
        )
        self.assertEqual(len(released), 514)
        self.assertEqual(self.run_check(self.tree_with_description(released)).returncode, 1)


def read_file(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()


class Overview(unittest.TestCase):
    """What Claude Desktop's plugin page shows: the manifest's description, author and links, and the README."""

    def built(self):
        f = Fixture(self)
        with open(os.path.join(f.tree, ".claude-plugin", "plugin.json"), encoding="utf-8") as fh:
            manifest = json.load(fh)
        with open(os.path.join(f.tree, "README.md"), encoding="utf-8") as fh:
            readme = fh.read()
        return manifest, readme

    def test_the_description_is_current_and_points_at_the_one_setting_chat_needs(self):
        manifest, _ = self.built()
        description = manifest["description"]
        self.assertNotIn("ChromaDB", description)
        # Short on purpose (the upload form allows 500 characters); the README carries the explanation.
        for phrase in ("Claude Code and Claude Desktop", "README.md", "Desktop chat", "lukaszraczylo/claude-mnemonic"):
            self.assertIn(phrase, description)

    def test_the_author_is_the_fork_and_upstream_stays_credited(self):
        manifest, readme = self.built()
        self.assertEqual(manifest["author"]["name"], "hlgr360")
        self.assertEqual(manifest["license"], "MIT")
        self.assertIn("fork of lukaszraczylo/claude-mnemonic", manifest["description"].replace("Fork of", "fork of"))
        self.assertIn("(MIT)", readme)
        with open(os.path.join(REPO_ROOT, "LICENSE"), encoding="utf-8") as fh:
            self.assertIn("Lukasz Raczylo", fh.read(), "upstream's copyright notice is kept")

    def test_the_readme_carries_the_pasted_instruction_word_for_word(self):
        _, readme = self.built()
        with open(os.path.join(HERE, "desktop-instructions.txt"), encoding="utf-8") as fh:
            template = fh.read()
        rendered = template.replace("{connector}", "claude-mnemonic").replace("{name}", "claude-mnemonic").rstrip("\n")
        self.assertIn("```text\n" + rendered + "\n```", readme, "one source: the README shows exactly the text to paste")
        self.assertNotIn("{{", readme, "no placeholder is left")

    def test_the_readme_says_what_it_installs_and_what_chat_needs(self):
        _, readme = self.built()
        for phrase in ("carries no binaries", "cosign", "never replaced", "Apple silicon", "built-in memory", "once", "lukaszraczylo/claude-mnemonic"):
            self.assertIn(phrase, readme)

    def test_the_renderer_refuses_a_template_without_the_placeholder(self):
        import render_readme

        with self.assertRaises(ValueError):
            render_readme.render("# no placeholder here\n")


class CommandsAsSkills(unittest.TestCase):
    """The slash commands ship as skills (commands/ is the older format); /claude-mnemonic:<name> does not change."""

    def front_and_body(self, text):
        _, front, body = text.split("---\n", 2)
        return dict(line.split(": ", 1) for line in front.strip().split("\n")), body

    def test_every_command_becomes_a_user_only_skill_with_the_same_body(self):
        f = Fixture(self)
        names = sorted(n[:-3] for n in os.listdir(os.path.join(REPO_ROOT, "commands")) if n.endswith(".md"))
        self.assertEqual(names, ["memory-dashboard", "memory-restart"])
        for name in names:
            with open(os.path.join(REPO_ROOT, "commands", name + ".md"), encoding="utf-8") as fh:
                source_front, source_body = self.front_and_body(fh.read())
            with open(os.path.join(f.tree, "skills", name, "SKILL.md"), encoding="utf-8") as fh:
                front, body = self.front_and_body(fh.read())
            self.assertEqual(front["name"], name, "the name is the directory, so the slash command is unchanged")
            self.assertEqual(front["disable-model-invocation"], "true", f"{name}: the model must not run it on its own")
            self.assertEqual(body, source_body)
            for key, value in source_front.items():
                self.assertEqual(front[key], value, f"{name}: {key} is kept")

    def test_the_converter_refuses_a_command_it_would_change_the_meaning_of(self):
        import commands_to_skills

        for text in ("no front matter\n", "---\ndescription: x\nno closing line\n", "---\nname: other\ndescription: x\n---\nbody\n", "---\ndisable-model-invocation: false\n---\nbody\n"):
            with self.subTest(text=text):
                with self.assertRaises(ValueError):
                    commands_to_skills.convert(text, "x")

    def test_the_converter_command_line_writes_one_skill_per_command(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        write(os.path.join(tmp.name, "commands", "hello.md"), "---\ndescription: Say hello\n---\n\n# Hello\n", 0o644)
        subprocess.run([sys.executable, os.path.join(HERE, "commands_to_skills.py"), os.path.join(tmp.name, "commands"), os.path.join(tmp.name, "skills")], check=True)
        with open(os.path.join(tmp.name, "skills", "hello", "SKILL.md"), encoding="utf-8") as fh:
            self.assertEqual(fh.read(), "---\nname: hello\ndisable-model-invocation: true\ndescription: Say hello\n---\n\n# Hello\n")


class ValidationFilter(unittest.TestCase):
    """The build accepts the reserved-name error and nothing else (the claude CLI is replaced by a fake)."""

    NAME_ERROR = {"path": "name", "message": 'Plugin name "claude-mnemonic" is reserved: it passes as one of Anthropic\'s own.', "code": None}

    def build_with(self, report, component_exit=0):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        tools = os.path.join(tmp.name, "tools")
        body = report if isinstance(report, str) else json.dumps(report)
        # The plugin report is asked for with --json; the skills and commands are validated without it.
        write(
            os.path.join(tools, "claude"),
            f"#!/bin/sh\ncase \"$*\" in *--json*) cat <<'EOF'\n{body}\nEOF\nexit 1;; *) echo 'component problem: bad frontmatter'; exit {component_exit};; esac\n",
        )
        env = dict(os.environ, DIST=os.path.join(tmp.name, "dist"), PATH=tools + os.pathsep + os.environ["PATH"])
        env.pop("SKIP_VALIDATE", None)
        return subprocess.run(["bash", os.path.join(HERE, "build-plugin.sh"), VERSION], cwd=REPO_ROOT, env=env, capture_output=True, text=True)

    def test_only_the_reserved_name_error_is_accepted(self):
        out = self.build_with({"manifest": {"errors": [self.NAME_ERROR], "warnings": [], "notes": []}, "contents": []})
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertIn("accepted", out.stdout)

    def test_any_other_error_fails_the_build(self):
        other = {"path": "hooks", "message": "Path not found", "code": None}
        out = self.build_with({"manifest": {"errors": [self.NAME_ERROR, other], "warnings": []}, "contents": []})
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("Path not found", out.stderr)

    def test_a_name_error_about_another_plugin_is_not_accepted(self):
        wrong = {"path": "name", "message": 'Plugin name "claude-other" is reserved: it passes as one of Anthropic\'s own.'}
        out = self.build_with({"manifest": {"errors": [wrong], "warnings": []}, "contents": []})
        self.assertNotEqual(out.returncode, 0)

    def test_a_warning_fails_the_build(self):
        out = self.build_with({"manifest": {"errors": [self.NAME_ERROR], "warnings": [{"path": "version", "message": "missing"}]}, "contents": []})
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("warning", out.stderr)

    def test_a_problem_in_the_skills_fails_the_build(self):
        out = self.build_with({"manifest": {"errors": [self.NAME_ERROR], "warnings": []}, "contents": []}, component_exit=1)
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("component problem", out.stderr)

    def test_a_report_that_is_not_json_fails_the_build(self):
        out = self.build_with("this is not json")
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("did not return a JSON report", out.stderr)

    @unittest.skipUnless(shutil.which("claude"), "needs the claude CLI")
    def test_the_real_validator_accepts_a_fork_release_version(self):
        # The plugin's version is not checked against semver (the docs say so); a four-part version must not warn.
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        env = dict(os.environ, DIST=tmp.name)
        env.pop("SKIP_VALIDATE", None)
        out = subprocess.run(["bash", os.path.join(HERE, "build-plugin.sh"), "0.21.95.1"], cwd=REPO_ROOT, env=env, capture_output=True, text=True)
        self.assertEqual(out.returncode, 0, out.stderr)
        with open(os.path.join(tmp.name, "plugin", ".claude-plugin", "plugin.json"), encoding="utf-8") as fh:
            self.assertEqual(json.load(fh)["version"], "0.21.95.1")

    @unittest.skipUnless(shutil.which("claude"), "needs the claude CLI")
    def test_the_real_validator_accepts_the_built_tree(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        env = dict(os.environ, DIST=tmp.name)
        env.pop("SKIP_VALIDATE", None)
        out = subprocess.run(["bash", os.path.join(HERE, "build-plugin.sh"), VERSION], cwd=REPO_ROOT, env=env, capture_output=True, text=True)
        self.assertEqual(out.returncode, 0, out.stderr)


if __name__ == "__main__":
    unittest.main()
