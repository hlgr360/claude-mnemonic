#!/bin/sh
# Set up (or remove) the claude-mnemonic status line in Claude Code's settings.json.
#
# Usage: statusline.sh status | enable [--replace] | disable
#   status   prints NONE (no status line is set), OURS (it is claude-mnemonic's) or OTHER <command>
#   enable   sets "statusLine" to the status line binary the plugin installed (~/.claude-mnemonic/bin/hooks/statusline)
#            when none is set, and leaves someone else's status line alone (exit 3) unless --replace is given, in which
#            case the old settings.json is kept next to it as settings.json.mnemonic-backup
#   disable  removes the status line, only when it is claude-mnemonic's
#
# A plugin cannot set the status line itself (Claude Code applies only "agent" and "subagentStatusLine" from a plugin's
# settings), so this is the one step a plugin install cannot do. It writes settings.json atomically and keeps every other
# key; CLAUDE_CONFIG_DIR (default ~/.claude) says where the file is. Needs python3.
set -u

ACTION="${1:-status}"
REPLACE=0
[ "${2:-}" = "--replace" ] && REPLACE=1
CONFIG_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
SETTINGS="$CONFIG_DIR/settings.json"
COMMAND="$HOME/.claude-mnemonic/bin/hooks/statusline"

command -v python3 >/dev/null 2>&1 || { echo "statusline: python3 is needed to edit settings.json" >&2; exit 1; }

exec python3 - "$ACTION" "$REPLACE" "$SETTINGS" "$COMMAND" <<'PY'
import json
import os
import shutil
import sys

action, replace, path, command = sys.argv[1], sys.argv[2] == "1", sys.argv[3], sys.argv[4]

def load():
    if not os.path.exists(path):
        return {}
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    except ValueError as e:
        sys.exit(f"statusline: {path} is not valid JSON ({e}); not touching it")
    if not isinstance(data, dict):
        sys.exit(f"statusline: {path} is not a JSON object; not touching it")
    return data

def save(data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".mnemonic-tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
        f.write("\n")
    os.replace(tmp, path)

def state(data):
    current = data.get("statusLine")
    if not current:
        return "NONE", ""
    cmd = current.get("command", "") if isinstance(current, dict) else str(current)
    return ("OURS", cmd) if "claude-mnemonic" in cmd else ("OTHER", cmd)

data = load()
kind, existing = state(data)

if action == "status":
    print(kind if kind != "OTHER" else f"OTHER {existing}")
elif action == "enable":
    if kind == "OTHER" and not replace:
        print(f"OTHER {existing}")
        print("statusline: another status line is set; not changed (run enable --replace to replace it)", file=sys.stderr)
        sys.exit(3)
    if not os.access(command, os.X_OK):
        print(f"statusline: {command} is not installed yet: start a Claude Code session once so the plugin installs it", file=sys.stderr)
        sys.exit(4)
    if kind == "OTHER" and os.path.exists(path):
        shutil.copyfile(path, path + ".mnemonic-backup")
    data["statusLine"] = {"type": "command", "command": command, "padding": 0}
    save(data)
    print("ENABLED " + command)
elif action == "disable":
    if kind == "OURS":
        del data["statusLine"]
        save(data)
        print("DISABLED")
    elif kind == "OTHER":
        print(f"OTHER {existing}")
        print("statusline: the status line is not claude-mnemonic's; not removed", file=sys.stderr)
        sys.exit(3)
    else:
        print("NONE")
else:
    sys.exit("usage: statusline.sh status | enable [--replace] | disable")
PY
