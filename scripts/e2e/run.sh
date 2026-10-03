#!/usr/bin/env bash
# End-to-end checks of the Claude Desktop features against the real binaries.
#
# Builds the worker and MCP server, runs them in an isolated HOME on private ports (your own
# ~/.claude-mnemonic and its worker are never touched), drives them over stdio and HTTP the way
# Claude Desktop does, and cleans up. The first run downloads the embedding model into a cache
# that later runs reuse (E2E_CACHE, default $TMPDIR/mnemonic-e2e-cache).
#
#   scripts/e2e/run.sh            everything (the dashboard check needs ui/dist and Chrome)
#   scripts/e2e/run.sh --no-ui    skip the dashboard check
#   KEEP=1 scripts/e2e/run.sh     keep the work directory and logs
#
# Needs: go (CGO), python3, curl, lsof; node and Chrome for the dashboard check.
set -uo pipefail

# An idle Mac that sleeps mid-run trips the worker's start deadlines; stay awake for the duration.
if [ -z "${E2E_AWAKE:-}" ] && command -v caffeinate >/dev/null 2>&1; then
  E2E_AWAKE=1 exec caffeinate -i "$0" "$@"
fi

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
HERE="$ROOT/scripts/e2e"
WORKER_PORT="${E2E_PORT:-37999}"
UI_PORT="${E2E_UI_PORT:-8099}"
RUN_UI=1
[ "${1:-}" = "--no-ui" ] && RUN_UI=0

WORK="$(mktemp -d "${TMPDIR:-/tmp}/mnemonic-e2e.XXXXXX")"
mkdir -p "$WORK/bin" "$WORK/home"

# The embedding model (about 130 MB) is downloaded into the isolated home's user cache on first use.
# Keep that cache between runs so only the very first run downloads it.
CACHE="${E2E_CACHE:-${TMPDIR:-/tmp}/mnemonic-e2e-cache}"
mkdir -p "$CACHE"
if [ "$(uname)" = "Darwin" ]; then
  mkdir -p "$WORK/home/Library" && ln -s "$CACHE" "$WORK/home/Library/Caches"
else
  ln -s "$CACHE" "$WORK/home/.cache"
fi
export E2E_DIR="$WORK" E2E_PORT="$WORKER_PORT" DO_NOT_TRACK=1 CGO_ENABLED=1

cleanup() {
  kill $(lsof -ti ":$WORKER_PORT") $(lsof -ti ":37998") $(lsof -ti ":$UI_PORT") $(lsof -ti ":9333") 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then echo "kept: $WORK"; else rm -rf "$WORK" "${TMPDIR:-/tmp}"/e2e-* "${TMPDIR:-/tmp}"/e2e-admin-* "${TMPDIR:-/tmp}"/e2e-names-* "${TMPDIR:-/tmp}"/e2e-threads-* "${TMPDIR:-/tmp}"/ui-e2e-* 2>/dev/null; fi
}
trap cleanup EXIT

stop_worker() { kill $(lsof -ti ":$WORKER_PORT") 2>/dev/null; sleep 1; }
fresh_worker() {
  stop_worker
  rm -rf "$WORK/home/.claude-mnemonic/claude-mnemonic.db"* "$WORK/home/.claude-mnemonic/backups"
  (cd "$WORK" && HOME="$WORK/home" CLAUDE_MNEMONIC_WORKER_PORT="$WORKER_PORT" nohup ./bin/worker > worker.log 2>&1 &)
  for _ in $(seq 1 90); do
    curl -s -m 2 "http://localhost:$WORKER_PORT/health" | grep -q '"ready":true' && return 0
    sleep 2
  done
  echo "worker did not become ready; see $WORK/worker.log" >&2
  return 1
}

failed=0
suite() { # name, command...
  local name="$1"; shift
  echo; echo "######## $name"
  "$@" || { echo "######## $name: FAILED"; failed=$((failed + 1)); }
}

echo "building into $WORK"
(cd "$ROOT" && go build -tags fts5 -ldflags "-s -w" -buildvcs=false -o "$WORK/bin/worker" ./cmd/worker \
  && go build -tags fts5 -ldflags "-s -w" -buildvcs=false -o "$WORK/bin/mcp-server" ./cmd/mcp) || { echo "build failed"; exit 1; }

fresh_worker || exit 1
suite "Desktop mode over stdio (chat, Cowork, worktrees, aliases)" python3 "$HERE/drive_mcp.py"
suite "Code mode unchanged, pinned project, lazy worker start"        python3 "$HERE/drive_modes.py"

fresh_worker || exit 1
suite "Prune and merge with real embeddings and snapshots"             python3 "$HERE/drive_admin.py"

fresh_worker || exit 1
suite "Project names, and projects that share a name"                  python3 "$HERE/drive_names.py"

fresh_worker || exit 1
suite "Thread checkpoints and catch-up (recovering a compacted chat)"  python3 "$HERE/drive_threads.py"

if [ "$RUN_UI" = "1" ]; then
  if [ ! -d "$ROOT/ui/dist" ] || ! command -v node >/dev/null; then
    echo; echo "######## dashboard: skipped (needs ui/dist from 'npm run build' in ui/, and node)"
  else
    fresh_worker || exit 1
    (nohup python3 "$HERE/serve_ui.py" "$ROOT/ui/dist" "http://localhost:$WORKER_PORT" "$UI_PORT" > "$WORK/ui.log" 2>&1 &)
    sleep 1
    IDS="$(python3 "$HERE/seed_ui.py")" || { echo "seeding failed"; failed=$((failed + 1)); IDS=""; }
    if [ -n "$IDS" ]; then
      sleep 3 # asynchronous vector sync
      suite "Dashboard in headless Chrome" node "$HERE/ui_e2e.mjs" "http://127.0.0.1:$UI_PORT/" "http://127.0.0.1:$UI_PORT" "$IDS"
    fi
  fi
fi

echo
if [ "$failed" -eq 0 ]; then echo "ALL END-TO-END SUITES PASSED"; else echo "$failed SUITE(S) FAILED"; fi
exit "$failed"
