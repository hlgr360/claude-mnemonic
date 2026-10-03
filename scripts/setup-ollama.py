#!/usr/bin/env python3
"""Choose a local Ollama model for claude-mnemonic and set it up.

claude-mnemonic can run its summaries, observation extraction and stale-observation check on a local
Ollama model instead of the Claude CLI. This script finds your Ollama, shows which of the models we know
are installed, offers to download the one you pick, checks that it answers, and records the choice in
~/.claude-mnemonic/settings.json. It never downloads without asking, never changes anything when a step
fails, backs the settings file up first, and by default switches no task over: you name the tasks.

    setup-ollama.py                                interactive: choose, download, test, save
    setup-ollama.py --model gemma3:12b --task verify --yes
    setup-ollama.py --dry-run --model gemma3:12b   show what would happen, change nothing
    setup-ollama.py status                         what Ollama has and what is configured
    setup-ollama.py disable                        put every task back on the Claude CLI

Exit codes: 0 done (or nothing to do), 1 a step failed, 2 a decision is needed (see the message).
Standard library only (Python 3.8+).
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

DEFAULT_URL = "http://localhost:11434"
TASKS = ("summary", "observation", "verify")
PREFIX = "CLAUDE_MNEMONIC_"
MODELS_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "ollama-models.json")
SMALL_MODEL_WARNING = ("Small local models write noticeably worse summaries than Haiku; compare on your own sessions "
                       "before moving 'summary' or 'observation'. 'verify' (a yes/no check) is the safest to start with.")

ask = input  # replaced in tests


class SetupError(Exception):
    pass


# --------------------------------------------------------------------------- locations and settings

def settings_path():
    return os.path.join(os.path.expanduser("~"), ".claude-mnemonic", "settings.json")


def normalize_url(raw):
    raw = (raw or "").strip()
    if not raw:
        return DEFAULT_URL
    if "://" not in raw:
        raw = "http://" + raw
    return raw.rstrip("/")


def resolve_url(cli_url, settings, environ):
    """--url, then the setting, then OLLAMA_HOST, then the default: the worker's own order."""
    if cli_url:
        return normalize_url(cli_url)
    configured = settings.get(PREFIX + "OLLAMA_URL")
    if isinstance(configured, str) and configured.strip():
        return normalize_url(configured)
    return normalize_url(environ.get("OLLAMA_HOST", ""))


def read_settings(path):
    """The settings as a dict; a missing file is empty. A file that is not a JSON object is an error."""
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except FileNotFoundError:
        return {}
    if not text.strip():
        return {}
    try:
        data = json.loads(text)
    except ValueError as e:
        raise SetupError(f"{path} is not valid JSON ({e}); fix or move it first, nothing was changed")
    if not isinstance(data, dict):
        raise SetupError(f"{path} does not hold a JSON object; nothing was changed")
    return data


def backup_path(path):
    stamp = time.strftime("%Y%m%d-%H%M%S")
    candidate = f"{path}.bak-{stamp}"
    n = 1
    while os.path.exists(candidate):
        n += 1
        candidate = f"{path}.bak-{stamp}-{n}"
    return candidate


def write_settings(path, settings):
    """Back the file up (if it exists) and replace it atomically. Returns the backup path or None."""
    os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
    backup = None
    if os.path.exists(path):
        backup = backup_path(path)
        with open(path, "rb") as src, open(backup, "wb") as dst:
            dst.write(src.read())
        os.chmod(backup, 0o600)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(settings, f, indent=2)
        f.write("\n")
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)
    return backup


def plan_changes(current, model, tasks, url=None):
    """The settings keys that would change, as {key: (old, new)}. Unchanged keys are left out."""
    wanted = {PREFIX + "OLLAMA_MODEL": model}
    if url:
        wanted[PREFIX + "OLLAMA_URL"] = url
    for task in tasks:
        wanted[PREFIX + "LLM_BACKEND_" + task.upper()] = "ollama"
    return {k: (current.get(k), v) for k, v in wanted.items() if current.get(k) != v}


def plan_disable(current):
    return {PREFIX + "LLM_BACKEND_" + t.upper(): (current[PREFIX + "LLM_BACKEND_" + t.upper()], None)
            for t in TASKS if PREFIX + "LLM_BACKEND_" + t.upper() in current}


def apply_changes(current, changes):
    out = dict(current)
    for key, (_, new) in changes.items():
        if new is None:
            out.pop(key, None)
        else:
            out[key] = new
    return out


# --------------------------------------------------------------------------- Ollama

def http_json(method, url, body=None, timeout=10):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as e:
        try:
            message = json.loads(e.read()).get("error") or f"HTTP {e.code}"
        except ValueError:
            message = f"HTTP {e.code}"
        raise SetupError(f"Ollama: {message}")
    except (urllib.error.URLError, OSError, ValueError) as e:
        raise SetupError(f"Ollama is not reachable at {url.split('/api/')[0]}: {getattr(e, 'reason', e)}")


def get_state(base):
    """What Ollama offers: {'version', 'installed': [...], 'loaded': [...]}. Raises SetupError when it is down."""
    version = http_json("GET", base + "/api/version", timeout=5).get("version", "")
    installed = http_json("GET", base + "/api/tags", timeout=10).get("models", [])
    try:
        loaded = http_json("GET", base + "/api/ps", timeout=10).get("models", [])
    except SetupError:
        loaded = []
    return {"version": version, "installed": installed, "loaded": loaded}


def has_model(installed, name):
    if not name:
        return False
    want = name if ":" in name else name + ":latest"
    return any(m.get("name") in (name, want) for m in installed)


def pull(base, name, progress=None):
    """Download a model, calling progress(status, completed, total) for each update."""
    req = urllib.request.Request(base + "/api/pull", data=json.dumps({"name": name, "stream": True}).encode(),
                                 method="POST", headers={"Content-Type": "application/json"})
    succeeded = False
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            for raw in resp:
                raw = raw.strip()
                if not raw:
                    continue
                try:
                    line = json.loads(raw)
                except ValueError:
                    continue
                if line.get("error"):
                    raise SetupError(f"Ollama could not pull {name}: {line['error']}")
                if progress:
                    progress(line.get("status", ""), line.get("completed", 0), line.get("total", 0))
                if line.get("status") == "success":
                    succeeded = True
    except urllib.error.HTTPError as e:
        raise SetupError(f"Ollama could not pull {name}: HTTP {e.code}")
    except (urllib.error.URLError, OSError) as e:
        raise SetupError(f"pulling {name} was interrupted: {getattr(e, 'reason', e)}")
    if not succeeded:
        raise SetupError(f"pulling {name} ended without success")


def smoke_test(base, name, timeout=300):
    """Ask the model for one word. Returns (seconds, answer); raises SetupError if it cannot answer."""
    started = time.time()
    resp = http_json("POST", base + "/api/chat", {
        "model": name, "stream": False,
        "messages": [{"role": "user", "content": "Reply with the single word: ready"}],
        "options": {"num_predict": 16, "temperature": 0},
    }, timeout=timeout)
    answer = (resp.get("message") or {}).get("content", "").strip()
    if not answer:
        raise SetupError(f"{name} loaded but gave an empty answer")
    return time.time() - started, answer


class ProgressPrinter:
    """Prints pull progress: one updating line on a terminal, a line per 10% otherwise."""

    def __init__(self, out=sys.stdout, tty=None):
        self.out = out
        self.tty = out.isatty() if tty is None else tty
        self.last = {}

    def __call__(self, status, completed, total):
        if not total:
            if status and status not in self.last:
                self.last[status] = True
                self._line(status)
            return
        pct = int(completed * 100 / total)
        key = status
        if self.tty:
            self.out.write(f"\r  {status[:40]:<40} {pct:3d}% of {total / 1e9:.1f} GB")
            self.out.flush()
            if completed >= total:
                self.out.write("\n")
        elif pct // 10 > self.last.get(key, -1) // 10 or key not in self.last:
            self.last[key] = pct
            self._line(f"{status[:40]} {pct}% of {total / 1e9:.1f} GB")

    def _line(self, text):
        self.out.write("  " + text + "\n")
        self.out.flush()


# --------------------------------------------------------------------------- models list

def load_models(path=MODELS_FILE):
    with open(path, encoding="utf-8") as f:
        return json.load(f).get("models", [])


def installed_as(model, installed):
    """The name under which the model is installed (its own or an alias such as llama3.2:latest), or None."""
    for name in [model["name"]] + model.get("aliases", []):
        want = name if ":" in name else name + ":latest"
        for m in installed:
            if m.get("name") in (name, want):
                return m["name"]
    return None


def describe(model, installed):
    mark = "installed" if installed_as(model, installed) else f"about {model['size_gb']:g} GB"
    status = model.get("status", "")
    return f"{model['name']:<16} {mark:<14} {model.get('license', ''):<26} {status}"


def known(models, name):
    return next((m for m in models if name == m["name"] or name in m.get("aliases", [])), None)


def parse_tasks(raw):
    if not raw:
        return []
    out = []
    for part in raw.replace(" ", "").split(","):
        if not part:
            continue
        if part not in TASKS:
            raise SetupError(f"unknown task {part!r}; the tasks are {', '.join(TASKS)}")
        if part not in out:
            out.append(part)
    return out


# --------------------------------------------------------------------------- commands

def cmd_status(args, out):
    settings = read_settings(args.settings)
    base = resolve_url(args.url, settings, os.environ)
    models = load_models(args.models_file)
    try:
        state = get_state(base)
    except SetupError as e:
        out(f"{e}")
        out("Install Ollama from https://ollama.com and start it, or pass --url.")
        return 1
    out(f"Ollama {state['version']} at {base}")
    out(f"Loaded in memory: {', '.join(m['name'] for m in state['loaded']) or 'nothing'}")
    out("Models we know:")
    for m in models:
        out("  " + describe(m, state["installed"]))
    others = [m["name"] for m in state["installed"] if not known(models, m["name"])]
    if others:
        out(f"Also installed (not tested with claude-mnemonic): {', '.join(others)}")
    chosen = settings.get(PREFIX + "OLLAMA_MODEL") or "(none)"
    on_local = [t for t in TASKS if settings.get(PREFIX + "LLM_BACKEND_" + t.upper()) == "ollama"]
    out(f"Configured model: {chosen}" + ("" if chosen == "(none)" or has_model(state["installed"], chosen) else "  (NOT installed)"))
    out(f"Tasks on Ollama: {', '.join(on_local) or 'none (everything runs on the Claude CLI)'}")
    return 0


def cmd_disable(args, out):
    settings = read_settings(args.settings)
    changes = plan_disable(settings)
    if not changes:
        out("No task is set to run on Ollama; nothing to change.")
        return 0
    for key, (old, _) in changes.items():
        out(f"  {key}: {old} -> (removed, back on the Claude CLI)")
    if args.dry_run:
        out("Dry run: nothing was written.")
        return 0
    backup = write_settings(args.settings, apply_changes(settings, changes))
    out(f"Saved. Backup: {backup}")
    out("The worker reloads its settings by itself.")
    return 0


def choose_model_interactively(models, installed, out):
    out("Models for the local backend:")
    for i, m in enumerate(models, 1):
        out(f"  {i}. " + describe(m, installed))
    out("  o. another model (type its name)")
    out(f"  (sizes are about; none of these is recommended yet, see {os.path.basename(MODELS_FILE)})")
    while True:
        answer = ask("Model [number, name, or Enter to cancel]: ").strip()
        if not answer:
            return None
        if answer.isdigit() and 1 <= int(answer) <= len(models):
            chosen = models[int(answer) - 1]
            return installed_as(chosen, installed) or chosen["name"]  # use the name it is installed under
        if answer.lower() == "o":
            answer = ask("Model name: ").strip()
            if answer:
                return answer
            continue
        if ":" in answer or "/" in answer or answer.isalnum():
            return answer
        out("Not a model number or name.")


def choose_tasks_interactively(out):
    out("Which tasks should run on this model? " + ", ".join(TASKS))
    out("  " + SMALL_MODEL_WARNING)
    while True:
        try:
            return parse_tasks(ask("Tasks (comma-separated, Enter for none): "))
        except SetupError as e:
            out(str(e))


def cmd_setup(args, out):
    settings = read_settings(args.settings)
    base = resolve_url(args.url, settings, os.environ)
    models = load_models(args.models_file)
    state = get_state(base)  # raises SetupError with the install hint when Ollama is down
    interactive = sys.stdin.isatty() and not args.yes and not args.model

    model = args.model
    if not model:
        if not interactive:
            out(f"Ollama {state['version']} at {base} is running. Pass --model NAME to choose one:")
            for m in models:
                out("  " + describe(m, state["installed"]))
            out("Example: setup-ollama.py --model gemma3:12b --task verify --yes   (or run it in a terminal to be asked)")
            return 2
        model = choose_model_interactively(models, state["installed"], out)
        if not model:
            out("Cancelled; nothing was changed.")
            return 0

    entry = known(models, model)
    if entry is None:
        out(f"Note: {model} is not one of the models we have tried with claude-mnemonic.")
    elif entry.get("status") == "poor":
        out(f"Warning: {entry['notes']}")

    tasks = parse_tasks(args.task)  # a bad --task is refused before anything is downloaded

    installed = has_model(state["installed"], model)
    size = f"about {entry['size_gb']:g} GB" if entry else "size unknown"
    if not installed:
        command = f"ollama pull {model}"
        if args.dry_run:
            out(f"Dry run: would download {model} ({size}).")
        elif args.no_pull:
            out(f"{model} is not installed and --no-pull was given. Run: {command}")
            return 2
        else:
            if interactive:
                if ask(f"{model} is not installed. Download it now ({size})? [y/N] ").strip().lower() not in ("y", "yes"):
                    out(f"Not downloaded; nothing was changed. To do it yourself: {command}")
                    return 0
            elif not args.yes:
                out(f"{model} is not installed ({size}). Re-run with --yes to download it, or run: {command}")
                return 2
            out(f"Downloading {model} ({size}) ...")
            pull(base, model, ProgressPrinter())
            state = get_state(base)
            if not has_model(state["installed"], model):
                raise SetupError(f"{model} was pulled but Ollama does not list it")
    else:
        out(f"{model} is installed.")

    if not args.dry_run and not args.skip_test:
        out("Checking that the model answers (the first call loads it, which can take a while) ...")
        seconds, answer = smoke_test(base, model)
        out(f"  answered {answer!r} in {seconds:.1f}s")

    if interactive and not args.task:
        tasks = choose_tasks_interactively(out)

    url = base if (args.url or settings.get(PREFIX + "OLLAMA_URL")) else None
    changes = plan_changes(settings, model, tasks, url)
    if not changes:
        out("Settings already say this; nothing to change.")
        return 0
    for key, (old, new) in changes.items():
        out(f"  {key}: {old!r} -> {new!r}")
    if any(t in tasks for t in ("summary", "observation")):
        out(SMALL_MODEL_WARNING)
    if args.dry_run:
        out("Dry run: nothing was written.")
        return 0
    backup = write_settings(args.settings, apply_changes(settings, changes))
    out(f"Saved to {args.settings}" + (f" (backup: {backup})" if backup else ""))
    out("The worker reloads its settings by itself. Check with: curl -s localhost:37777/api/llm/status")
    if not tasks:
        out("No task was switched over; add --task summary,observation,verify when you want one on the local model.")
    return 0


def parse(argv):
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("command", nargs="?", choices=["setup", "status", "disable"], default="setup")
    p.add_argument("--model", help="model to use (installed, or to be downloaded with --yes)")
    p.add_argument("--task", help="comma-separated tasks to run on it: " + ", ".join(TASKS) + " (default: none)")
    p.add_argument("--yes", action="store_true", help="allow downloading a missing model without asking")
    p.add_argument("--no-pull", action="store_true", help="never download; stop if the model is missing")
    p.add_argument("--dry-run", action="store_true", help="show what would happen and change nothing")
    p.add_argument("--skip-test", action="store_true", help="do not run the one-word answer check")
    p.add_argument("--url", help="Ollama address (default: the setting, then OLLAMA_HOST, then localhost:11434)")
    p.add_argument("--settings", default=settings_path(), help="settings file (default ~/.claude-mnemonic/settings.json)")
    p.add_argument("--models-file", default=MODELS_FILE, help=argparse.SUPPRESS)
    return p.parse_args(argv)


def main(argv=None, out=print):
    args = parse(argv)
    try:
        return {"setup": cmd_setup, "status": cmd_status, "disable": cmd_disable}[args.command](args, out)
    except SetupError as e:
        out(f"error: {e}")
        if "not reachable" in str(e):
            out("Install Ollama from https://ollama.com and start it (ollama serve), or pass --url. Nothing was changed.")
        else:
            out("Nothing was changed.")
        return 1


if __name__ == "__main__":
    sys.exit(main())
