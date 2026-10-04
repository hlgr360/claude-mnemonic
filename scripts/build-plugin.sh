#!/bin/bash
# Build the thin Claude Code plugin: no binaries, one platform-independent tree and zip.
#
# Usage: scripts/build-plugin.sh <version>      (version without the leading v, e.g. 0.21.95)
#   DIST=dist            where the tree (dist/plugin) and the zip (dist/claude-mnemonic-plugin_<version>.zip) go
#   MNEMONIC_REPO=o/n    release repository the plugin downloads its binaries from (default: this fork's)
#   SKIP_VALIDATE=1      do not run `claude plugin validate` (it needs the claude CLI)
#
# The tree holds the manifest (version stamped), the hook definitions and wrappers, the MCP server wrapper, the slash
# commands, the memory skill and lib/ensure-binaries.sh, which fetches and verifies the binaries of this release on first use (see that
# script for the rules). Try it without installing anything:  claude --plugin-dir dist/plugin
# Note that the plugin runs the hooks: they start the worker and write to ~/.claude-mnemonic like any install.
#
# The release workflow publishes the zip next to the platform archives, so one catalogue entry (an `archive` source
# with the zip's URL and sha256) serves every platform.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
    echo "Usage: $0 <version>" >&2
    exit 1
fi
VERSION="${VERSION#v}"
DIST="${DIST:-dist}"
TREE="${DIST}/plugin"
ZIP="${DIST}/claude-mnemonic-plugin_${VERSION}.zip"

echo "==> Assembling the plugin ${VERSION} in ${TREE}"
rm -rf "$TREE"
mkdir -p "$TREE/.claude-plugin" "$TREE/hooks" "$TREE/commands" "$TREE/lib" "$TREE/skills/project-memory"

sed "s/{{ .Version }}/${VERSION}/g; s/{{.Version}}/${VERSION}/g" plugin/.claude-plugin/plugin.json.tpl >"$TREE/.claude-plugin/plugin.json"
cp plugin/hooks/hooks.json "$TREE/hooks/hooks.json"
for hook in session-start user-prompt post-tool-use subagent-stop stop pre-compact statusline; do
    cp "hooks/${hook}" "$TREE/hooks/${hook}"
    chmod 755 "$TREE/hooks/${hook}"
done
cp mcp-server "$TREE/mcp-server"
chmod 755 "$TREE/mcp-server"
cp commands/*.md "$TREE/commands/"
# The memory skill: the instruction pasted into Claude Desktop, as a skill (one source: scripts/desktop-instructions.txt).
python3 scripts/render_skill.py "$TREE/skills/project-memory/SKILL.md"
cp plugin/lib/ensure-binaries.sh "$TREE/lib/ensure-binaries.sh"
chmod 755 "$TREE/lib/ensure-binaries.sh"
cp LICENSE "$TREE/LICENSE"

# A fork of the fork downloads from its own releases.
if [[ -n "${MNEMONIC_REPO:-}" ]]; then
    sed -i.bak "s|^DEFAULT_REPO=.*|DEFAULT_REPO=\"${MNEMONIC_REPO}\"|" "$TREE/lib/ensure-binaries.sh"
    rm -f "$TREE/lib/ensure-binaries.sh.bak"
fi

if [[ "${SKIP_VALIDATE:-}" != "1" ]]; then
    if ! command -v claude >/dev/null 2>&1; then
        echo "The claude CLI is needed to validate the plugin (SKIP_VALIDATE=1 to skip)" >&2
        exit 1
    fi
    echo "==> Validating (claude plugin validate --strict)"
    REPORT="$(mktemp)"
    trap 'rm -f "$REPORT"' EXIT
    # The exit code is ignored on purpose: the report is read below, because one error is known and accepted.
    claude plugin validate "$TREE" --strict --json >"$REPORT" 2>&1 || true
    python3 - "$REPORT" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    text = f.read()
try:
    report = json.loads(text)
except ValueError:
    print("claude plugin validate did not return a JSON report:\n" + text, file=sys.stderr)
    sys.exit(1)

# The validator refuses third-party plugin names that start with "claude-". The name stays: Claude Code installs and
# loads such a plugin all the same (only `validate`, `plugin init` and `plugin tag` check the name), and it is the
# name the existing install and its slash commands use. Every other error and every warning fails the build.
def accepted(item):
    return item.get("path") == "name" and item.get("message", "").startswith('Plugin name "claude-mnemonic" is reserved')

problems = []
def walk(node):
    if isinstance(node, dict):
        for level in ("errors", "warnings"):
            for item in node.get(level) or []:
                if level == "errors" and accepted(item):
                    continue
                problems.append(f"{level[:-1]}: {item.get('path') or '-'}: {item.get('message')}")
        for value in node.values():
            walk(value)
    elif isinstance(node, list):
        for value in node:
            walk(value)

walk(report)
if problems:
    print("Plugin validation failed:\n  " + "\n  ".join(problems), file=sys.stderr)
    sys.exit(1)
print("    ok (the reserved-name error for claude-mnemonic is accepted)")
PY
    # The skill and the commands are checked as components (the plugin report does not list them).
    for component in skills commands; do
        if ! claude plugin validate "$TREE/$component" --strict >"$REPORT" 2>&1; then
            echo "Validation of $component failed:" >&2
            cat "$REPORT" >&2
            exit 1
        fi
    done
    echo "    ok (skills and commands)"
fi

PYTHON="$(command -v python3 || command -v python)"
mkdir -p "$DIST"
rm -f "$ZIP"
"$PYTHON" scripts/package_release.py "$TREE" "$ZIP"
if command -v sha256sum >/dev/null 2>&1; then SUM="$(sha256sum "$ZIP" | cut -d ' ' -f 1)"; else SUM="$(shasum -a 256 "$ZIP" | cut -d ' ' -f 1)"; fi
echo "==> ${ZIP} ($(du -h "$ZIP" | cut -f1))"
echo "    sha256 ${SUM}"
