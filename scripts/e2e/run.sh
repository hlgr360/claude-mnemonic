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
#   E2E_ONLY=Dashboard scripts/e2e/run.sh   run only the suites whose name contains the text
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
  kill $(lsof -ti ":$WORKER_PORT") $(lsof -ti ":37998") $(lsof -ti ":${E2E_OLLAMA_PORT:-37996}") $(lsof -ti ":$UI_PORT") $(lsof -ti ":9333") 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then echo "kept: $WORK"; else rm -rf "$WORK" "${TMPDIR:-/tmp}"/e2e-* "${TMPDIR:-/tmp}"/e2e-admin-* "${TMPDIR:-/tmp}"/e2e-names-* "${TMPDIR:-/tmp}"/e2e-threads-* "${TMPDIR:-/tmp}"/e2e-brief-* "${TMPDIR:-/tmp}"/e2e-conflicts-* "${TMPDIR:-/tmp}"/e2e-relations-* "${TMPDIR:-/tmp}"/e2e-scope-* "${TMPDIR:-/tmp}"/ui-e2e-* 2>/dev/null; fi
}
trap cleanup EXIT

stop_worker() { kill $(lsof -ti ":$WORKER_PORT") 2>/dev/null; sleep 1; }

# The conflict proposer is on by default and would call a model for the notes the suites write. Only the conflict suite
# wants it, with a fake claude; every other suite runs with it switched off.
# Also no regular snapshot: it would put a copy of the database into backups/ half a minute after every worker start, where
# the suites count the snapshots they cause. Only the snapshot suite switches it on.
NO_PROPOSALS='"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED": false, "CLAUDE_MNEMONIC_SNAPSHOT_INTERVAL_HOURS": 0'
base_settings() {
  mkdir -p "$WORK/home/.claude-mnemonic"
  printf '{%s}\n' "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
}
fresh_worker() {
  stop_worker
  rm -rf "$WORK/home/.claude-mnemonic/claude-mnemonic.db"* "$WORK/home/.claude-mnemonic/backups"
  # Hooks cache the worker's pid for 10 s and skip themselves when it is dead; forget the old worker's.
  rm -f "$WORK/home/.claude-mnemonic/.worker-cache"
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
  # E2E_ONLY="graph" runs only the suites whose name contains it (the others still start a fresh worker).
  if [ -n "${E2E_ONLY:-}" ] && [[ "$name" != *"$E2E_ONLY"* ]]; then return 0; fi
  echo; echo "######## $name"
  "$@" || { echo "######## $name: FAILED"; failed=$((failed + 1)); }
}

echo "building into $WORK"
(cd "$ROOT" && go build -tags fts5 -ldflags "-s -w" -buildvcs=false -o "$WORK/bin/worker" ./cmd/worker \
  && go build -tags fts5 -ldflags "-s -w" -buildvcs=false -o "$WORK/bin/mcp-server" ./cmd/mcp \
  && go build -tags fts5 -ldflags "-s -w" -buildvcs=false -o "$WORK/bin/pre-compact" ./cmd/hooks/pre-compact \
  && go build -tags fts5 -ldflags "-s -w -X github.com/lukaszraczylo/claude-mnemonic/pkg/hooks.Version=e2e" -buildvcs=false -o "$WORK/bin/session-start" ./cmd/hooks/session-start) || { echo "build failed"; exit 1; }

base_settings
fresh_worker || exit 1
suite "Desktop mode over stdio (chat, Cowork, worktrees, aliases)" python3 "$HERE/drive_mcp.py"
suite "Code mode unchanged, pinned project, lazy worker start"        python3 "$HERE/drive_modes.py"

fresh_worker || exit 1
suite "Prune and merge with real embeddings and snapshots"             python3 "$HERE/drive_admin.py"

fresh_worker || exit 1
suite "Notes are kept, and an archived note is out of search"          python3 "$HERE/drive_cap.py"

fresh_worker || exit 1
suite "A search scoped to a project finds that project's notes"        python3 "$HERE/drive_project_search.py"

# The regular snapshot: switched on here only, it runs half a minute after the worker starts.
printf '{"CLAUDE_MNEMONIC_SNAPSHOT_INTERVAL_HOURS": 24, "CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED": false}\n' > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "A regular snapshot of the database"                             python3 "$HERE/drive_snapshots.py"
base_settings

fresh_worker || exit 1
suite "Project names, and projects that share a name"                  python3 "$HERE/drive_names.py"

fresh_worker || exit 1
suite "Thread checkpoints and catch-up (recovering a compacted chat)"  python3 "$HERE/drive_threads.py"

# The PreCompact hook makes the worker run a summary. Point the worker at a fake `claude` that records the prompt
# it gets and answers with a canned summary, so this never reaches a real model.
cat > "$WORK/fake-claude" <<FAKE
#!/bin/sh
for a in "\$@"; do last="\$a"; done
printf '%s\n=====END=====\n' "\$last" >> "$WORK/fake-claude-prompts.log"
case "\$last" in
  *"PROJECT BRIEF REQUEST"*)
    # a brief: cite the first observation of the request, and include what the worker must clean up
    first=\$(printf '%s' "\$last" | sed -n '/^OBSERVATIONS (/,\$p' | grep -o '\[#[0-9]*\]' | head -1)
    printf '## What this is\nA small tool that remembers things %s and a made-up citation [#999999]. Contact me@example.com for details.\n\n## Current state\nIt works.\n\n## Open items\n- an invented open item\n' "\$first"
    exit 0 ;;
  *"ROLL-UP REQUEST"*)
    # a roll-up: unless the suite asked this model to fail, echo the subject of the first note, plus what the worker must clean up
    [ -f "$WORK/fake-claude-rollup-fail" ] && exit 1
    line=\$(printf '%s' "\$last" | grep -m1 '^\[#[0-9]*\] (')
    id=\$(printf '%s' "\$line" | grep -o '^\[#[0-9]*\]')
    subject=\$(printf '%s' "\$line" | sed 's/^\[#[0-9]*\] ([^)]*) //')
    printf 'TITLE: Condensed notes\n\n- Earlier work: %s %s and a made-up citation [#999999]. Mail me@example.com about it.\n- A second point that keeps the roll-up long enough to be stored.\n' "\$subject" "\$id"
    exit 0 ;;
  *"CONFLICT CHECK REQUEST"*)
    # a conflict check: say the first older candidate has been superseded by the newer note
    first=\$(printf '%s' "\$last" | sed -n '/^OLDER NOTES:/,\$p' | grep -o '\[#[0-9]*\]' | head -1 | tr -d '[#]')
    printf '[{"older_id": %s, "relation": "supersedes", "confidence": "high", "reason": "The newer note changes the value the older one states."}]\n' "\$first"
    exit 0 ;;
esac
cat <<'XML'
<summary>
  <request>Compaction test summary: Desktop support and thread checkpoints</request>
  <investigated>How chat loses context at a compaction.</investigated>
  <learned>Projects are addressed by name; checkpoints keep one note per thread.</learned>
  <completed>Edited the desktop tools and added the checkpoint endpoint.</completed>
  <next_steps>Decide how long a note may be.</next_steps>
  <notes>Written by the fake summariser of the end-to-end suite.</notes>
</summary>
XML
FAKE
chmod +x "$WORK/fake-claude"
mkdir -p "$WORK/home/.claude-mnemonic"
printf '{"CLAUDE_CODE_PATH": "%s/fake-claude", '"$NO_PROPOSALS"'}\n' "$WORK" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "PreCompact hook summarises the conversation before a compaction" python3 "$HERE/drive_precompact.py"

# Where the dashboard is: the real session-start hook, the real MCP server in both modes.
fresh_worker || exit 1
suite "Dashboard link: session-start message, Desktop tool, Code unchanged" python3 "$HERE/drive_dashboard_link.py"

# Local LLM: the summary task on a fake Ollama (the suite plays Ollama itself and switches it off midway),
# with the fake claude from above as the fallback.
OLLAMA_E2E_PORT="${E2E_OLLAMA_PORT:-37996}"
printf '{"CLAUDE_CODE_PATH": "%s/fake-claude", "CLAUDE_MNEMONIC_OLLAMA_URL": "http://127.0.0.1:%s", "CLAUDE_MNEMONIC_OLLAMA_MODEL": "gemma3:12b", "CLAUDE_MNEMONIC_LLM_BACKEND_SUMMARY": "ollama", %s}\n' "$WORK" "$OLLAMA_E2E_PORT" "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "Local LLM backend: Ollama for summaries, fallback to the CLI"   env E2E_OLLAMA_PORT="$OLLAMA_E2E_PORT" python3 "$HERE/drive_llm.py"
base_settings

# Project briefs: switched on with low thresholds, written by the fake claude above. The first automatic pass runs
# 30 s after the worker starts, so the suite seeds its data straight away and then waits for it.
printf '{"CLAUDE_CODE_PATH": "%s/fake-claude", "CLAUDE_MNEMONIC_PROJECT_BRIEF_ENABLED": true, "CLAUDE_MNEMONIC_PROJECT_BRIEF_MIN_NEW_OBSERVATIONS": 3, "CLAUDE_MNEMONIC_PROJECT_BRIEF_INTERVAL_MINUTES": 1, "CLAUDE_MNEMONIC_PROJECT_BRIEF_MAX_PER_RUN": 3, %s}\n' "$WORK" "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "Project briefs: automatic and on request, shown first to Desktop"  python3 "$HERE/drive_brief.py"
base_settings

# Roll-ups, on request: the fake claude above condenses old notes. Switched off for the automatic pass, so the suite
# drives it through the tools.
printf '{"CLAUDE_CODE_PATH": "%s/fake-claude", "CLAUDE_MNEMONIC_ROLLUP_MIN_GROUP_SIZE": 8, "CLAUDE_MNEMONIC_ROLLUP_KEEP_NEWEST": 0, %s}\n' "$WORK" "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "Roll-ups: old notes condensed, originals archived, restorable"    python3 "$HERE/drive_rollup.py"
base_settings

# Roll-ups, automatic: switched on, a pass every minute (the first one 45 s after the worker starts), with a target of 4 live notes
# per project so that the pressure ladder is exercised.
printf '{"CLAUDE_CODE_PATH": "%s/fake-claude", "CLAUDE_MNEMONIC_ROLLUP_ENABLED": true, "CLAUDE_MNEMONIC_ROLLUP_INTERVAL_MINUTES": 1, "CLAUDE_MNEMONIC_ROLLUP_MIN_GROUP_SIZE": 8, "CLAUDE_MNEMONIC_ROLLUP_KEEP_NEWEST": 0, "CLAUDE_MNEMONIC_ROLLUP_TARGET_LIVE_NOTES": 4, %s}\n' "$WORK" "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "Roll-ups: the worker's own pass"                                  python3 "$HERE/drive_rollup_auto.py"
base_settings

# Consolidation: switched on with a lower similarity bar and a pass every minute (the first one 45 s after the worker starts).
printf '{"CLAUDE_MNEMONIC_CONSOLIDATION_ENABLED": true, "CLAUDE_MNEMONIC_CONSOLIDATION_INTERVAL_MINUTES": 1, "CLAUDE_MNEMONIC_CONSOLIDATION_MIN_SIMILARITY": 0.8, %s}\n' "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "Consolidation: near-duplicates folded, by hand and by the worker"  python3 "$HERE/drive_consolidate.py"
base_settings

# Conflict proposals: switched on with a low similarity bar, answered by the fake claude above. The first automatic
# pass runs 45 s after the worker starts, so the suite seeds its data straight away and then waits for it.
printf '{"CLAUDE_CODE_PATH": "%s/fake-claude", "CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED": true, "CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_INTERVAL_MINUTES": 1, "CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_MIN_SIMILARITY": 0.6, "CLAUDE_MNEMONIC_SNAPSHOT_INTERVAL_HOURS": 0}\n' "$WORK" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "Conflict review: proposals, decisions, hiding, undo"            python3 "$HERE/drive_conflicts.py"
base_settings

# The knowledge graph is built in the background from the vector index (no model); its first pass runs a minute
# after the worker starts, so the suite seeds straight away and then waits for it.
fresh_worker || exit 1
suite "Knowledge graph: relations, filters, hiding, rebuild"           python3 "$HERE/drive_relations.py"

# Scope: notes imported without a scope get the rule's; an archive written by the old rule is re-scoped through the API.
fresh_worker || exit 1
suite "Scope: the rule, the re-scope, notes chosen by hand"            python3 "$HERE/drive_scope.py"

# Projects that are really one: the git remote recorded per project (real repositories), suggestions, dismissals, and the
# existing safe merge. Automatic merging is off here, as by default.
fresh_worker || exit 1
suite "Duplicate projects: identity, suggestions, dismissals, merge"   python3 "$HERE/drive_duplicates.py"

# Automatic merging switched on: its first pass runs 45 s after the worker starts, then every minute.
printf '{"CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_ENABLED": true, "CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_INTERVAL_MINUTES": 1, %s}\n' "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "Automatic project merge (switched on)"                          python3 "$HERE/drive_automerge.py"
base_settings

# Prompt retention: a month, so the maintenance run through the tool deletes the prompts older than that (after a snapshot).
printf '{"CLAUDE_MNEMONIC_PROMPT_RETENTION_DAYS": 30, %s}\n' "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
fresh_worker || exit 1
suite "Prompt retention: old prompts deleted after a snapshot"          python3 "$HERE/drive_retention.py"
base_settings

if [ "$RUN_UI" = "1" ]; then
  if [ ! -d "$ROOT/ui/dist" ] || ! command -v node >/dev/null; then
    echo; echo "######## dashboard: skipped (needs ui/dist from 'npm run build' in ui/, and node)"
  else
    # The Roll-ups tab writes a roll-up through the fake claude above.
    printf '{"CLAUDE_CODE_PATH": "%s/fake-claude", "CLAUDE_MNEMONIC_ROLLUP_MIN_GROUP_SIZE": 8, "CLAUDE_MNEMONIC_ROLLUP_KEEP_NEWEST": 0, %s}\n' "$WORK" "$NO_PROPOSALS" > "$WORK/home/.claude-mnemonic/settings.json"
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
