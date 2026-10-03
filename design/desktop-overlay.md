# claude-mnemonic: Claude Desktop overlay (design)

Status: **implemented** on branch `feat/desktop-overlay` (issues #2 to #7). This document keeps the
measurements and decisions; the "What was built" section at the end records where the result differs
from the first sketch. User-facing instructions are in `DESKTOP.md`.

Goal: use one claude-mnemonic memory store from Claude Code (terminal and Desktop Code tab), Cowork and
Desktop chat, with the same project layout, without changing Claude Code's native behaviour.

## 1. Evidence (from the ~/mcp-probe experiments)

| Fact | Consequence |
|---|---|
| Project ID = `basename_sha256(abs path)[:6]` (`pkg/hooks/response.go:23`). Reproduced against the live worker. | Server can compute the same ID from any host path. |
| Chat client = `claude-ai`; Cowork and Desktop Code tab = `local-agent-mode-*`; both reach the server via `initialize.clientInfo.name`. | Mode detection needs no flag. |
| Chat: cwd `/`, no `CLAUDE*` env, no roots, no elicitation, server `instructions` not shown to the model. | Protocol text must live in tool descriptions. |
| Cowork: roots stay `[]` even with a folder; the host path reaches only the model. Agent shell is a cloud container that cannot reach `localhost:37777`. | Only MCP tools (running on the Mac) can reach the worker; the model must pass the path. |
| Cowork and the Code tab share ONE `local-agent-mode-*` process; roots are the union of live sessions. One `claude-ai` process serves all chats (confirmed with a second chat). | No per-process or per-roots state. Every call carries its own project. |
| Desktop Code tab: hooks fire, same ID as terminal Code; instructions are shown. | Code needs no overlay. |
| Desktop Code tab with its worktree option creates `<repo>/.claude/worktrees/<random>`; the plugin recorded a NEW project for it. | Path hashing fragments memory; dirname matching cannot recover it. |
| `tools/list_changed` is honoured by the client but the model does not see new tools mid-conversation. | No dynamic tool gating. |
| The plugin's MCP server is not registered with Claude Code for this user; only hooks run. | Desktop would be its first real use. |

## 2. Principles

1. Additive. Claude Code's hook path keeps working unchanged; new behaviour lives in new files and flags.
2. Stateless server. A project is named by the caller on every call; the server holds no "current project".
3. Server-enforced writes. A write without a valid explicit project is rejected, whatever the model does.
4. Decline is first-class. A declined chat can read across all projects and can never write.
5. Same identity everywhere. All modes resolve to the IDs Claude Code already uses.

## 3. Identity

Canonical project key = hash of the MAIN worktree path:

    main = dirname(git rev-parse --path-format=absolute --git-common-dir)   // else the path itself
    id   = basename(main) + "_" + sha256(main)[:6]

- For an ordinary checkout `main` equals the checkout path, so existing IDs do not change. No migration.
- Worktrees (Desktop, `claude --worktree`) resolve to their main repo. Verified: the Desktop worktree's
  common dir is `<repo>/.git`.
- Outside a git repo, fall back to the path (today's behaviour).
- Fragments that already exist (e.g. `working-directory-setup-fc06bf_e5a4ab`) are merged with an alias.

Alias table (new, additive): `project_aliases(alias TEXT PRIMARY KEY, canonical TEXT, source TEXT, created_at)`.
Resolution order for a caller-supplied project reference:
exact known ID, then alias, then hash of a supplied host path (after common-dir resolution), then unique
dirname match among known IDs (never guesses on ambiguity).

The common-dir rule touches the hook path (`ProjectIDWithName`). Ship it as a separate, backward-compatible PR so
it can be upstreamed or dropped independently of the Desktop overlay.

## 4. MCP surface (proposed)

Mode is taken from `clientInfo.name`. In Code mode nothing changes (project from `CLAUDE_PROJECT_DIR`/cwd).
In Desktop modes the server starts "unbound": no default project.

| Tool | Purpose |
|---|---|
| `project_list` | Known projects with last activity and top concepts. |
| `project_resolve(path?, name?)` | Map a host path or a name to a canonical ID, or return candidates. Used by Cowork-with-folder (model passes the folder path). |
| `project_suggest(opening_text, limit=4)` | Chat/Cowork-without-folder. Embeds the text, aggregates sqlite-vec hits per project, mixes dirname match and recency, returns top candidates with a one-line hint and a confidence. |
| `context(project)` | Same payload the session-start hook injects. Replaces SessionStart for Desktop. |
| `remember(project, text, type?, concepts?)` | Explicit write. `project` is required, no default. Text passes through `StripPrivateTags` (`internal/privacy/stripper.go`). |
| existing `search`, `timeline`, `observation` | In Desktop modes `project` defaults to "all projects", not to a derived ID. |

Writes: the worker has `/api/observations/bulk-import`; verify it fits a single observation, else add a thin
`POST /api/observations` (additive worker change). Do not reuse `/api/sessions/observations`, which queues raw tool
events for LLM processing.

Because chat does not show server `instructions`, the protocol is written into tool descriptions, e.g.
`project_suggest`: "Call this first in a new conversation with the user's opening message, then ask the user to
pick a project or decline." `remember`: "Only call after the user chose a project; never invent one."

## 5. Flows

- Code (terminal / Desktop tab / worktree): hooks as today; canonical ID via common-dir. No overlay.
- Cowork with folder: the model knows the host path (session reminder). It calls `project_resolve(path)`,
  then `context(id)`; writes via `remember(id, ...)`. Mode is recognised by `local-agent-mode-*`.
- Cowork without folder / Chat: `project_suggest(opening_text)`, the model asks the user to pick, then proceeds as
  above. If the user declines: no `project` is ever passed, so `remember` is unusable and `search` stays
  cross-project. Optional later upgrade: an MCP Apps picker (chat advertises `io.modelcontextprotocol/ui`).

## 6. Worker access and install

- Desktop servers are started once at app launch, plus a throwaway spawn closed immediately: keep startup cheap and
  side-effect free before `initialize`.
- The MCP server should call `EnsureWorkerRunning` lazily on first tool call (Code relies on hooks to start it).
- Install: a script merges one `mcpServers` entry into `claude_desktop_config.json` (backup first, text insert,
  validate JSON). A `.mcpb` bundle with a project user-config is the polished route; check the current spec first.
- Cowork's container cannot reach the worker, so nothing in Desktop modes may depend on shell or hook access.

## 7. Rollout (each step independently shippable)

0. Prove the read side: register the real `mcp-server` in Desktop with `--project <id>`; search from chat.
1. Identity PR: common-dir resolution + alias table + `project_list`/`project_resolve`. Hook path change is tiny.
2. Desktop mode: `clientInfo` detection, unbound defaults, `project_suggest`, `context`, worker bootstrap.
3. Writes: `remember` + worker endpoint, with required project and privacy stripping.
4. Installer script / `.mcpb`; docs page.
5. Optional: MCP Apps picker.
6. Open todo: prune / merge projects cleanly (section 9). Worker API first, dashboard second; shares the alias table
   from step 1 and is how existing fragments (e.g. the stray worktree project) get merged.

Keep steps 2-4 in new files (`internal/mcp/desktop*.go`, `scripts/install-desktop.sh`, a docs page) to stay clear
of upstream merge conflicts; only step 1 edits existing code.

## 8. Risks and open questions

- Open todo, not yet scheduled: prune/merge projects (section 9). Until it exists, stray projects can only be
  removed with SQL against `~/.claude-mnemonic/claude-mnemonic.db`.

- `project_suggest` quality depends on embeddings per project; cold-start projects have little to match. Fall back to
  recency plus dirname, and always allow "none of these".
- The model, not the server, must ask the user. Verify with real chats that it does so reliably; descriptions may
  need iteration. A hard rule is server-side: no project, no write.
- Desktop's trust/consent prompts for tools may interrupt flows; test `remember` UX.
- Cowork's shared process mixes roots across sessions: never use roots as identity.
- Windows/Linux Desktop paths and config locations are untested.
- Not yet measured: the real `mcp-server` inside Desktop (rollout step 0); how an MCP Apps UI behaves in chat.

## 9. Open todo: prune / merge projects cleanly (see rollout step 6)

Today a project exists only because a session row references it (`GetAllProjects`, `internal/db/gorm/session_store.go`),
and there is no way to remove or merge one. Found while cleaning up test sessions (stray worktree project with 1 session,
1 prompt, 1 observation).

Proposed shape, one implementation with thin front ends:
1. Worker API (the single source of truth): `GET /api/projects/{id}/stats` (counts per table), then
   `DELETE /api/projects/{id}` and `POST /api/projects/{id}/merge {into}`. Dry-run by default; the real call needs the
   counts back as a confirmation token. One transaction; take a DB snapshot first.
2. Dashboard: "Manage projects" next to `ProjectFilter.vue` (counts, Delete, Merge into...). Thin consumer of 1.
3. Optional later: a `memory_admin` action so Desktop can do it, gated behind the dry-run token (a model must never
   delete in one step). Optionally a CLI subcommand for headless use.

Points to get right:
- Cascade completeness: observations, prompts, sessions, summaries, patterns, relations/graph edges, FTS5 rows AND the
  sqlite-vec vectors. Orphaned vectors would keep surfacing in search.
- Merge = re-point rows to the target project (keeps knowledge such as the worktree observation), then drop the empty
  fragment, and record an alias so the old ID keeps resolving. Prune = delete.
- Offer archive (reversible) before hard delete; observations already have archive/unarchive endpoints.
- `GET /api/projects` is sent with `Cache-Control: max-age=300`, so the dashboard list would look stale after a delete.
  Bust or shorten the cache for this route.
- Consider making the project list explicit (union of sessions and observations) so a project cannot linger or
  disappear depending on which table was cleaned.
- Reuses the alias table from section 3, so build the two together (rollout step 1/5).

## 10. What was built, and where it differs from the sketch

Identity (#2)
- Worktrees resolve to their main repository by reading the `.git` file and its `commondir` (no git
  subprocess). A real-worktree end-to-end run showed git records symlink-resolved paths (macOS `/var` is
  `/private/var`), which split one project in two; the main root is now translated back into the spelling
  the caller used. Plain checkouts keep their ids, so nothing is migrated.
- Alias table, resolver (`internal/projects`) and `/api/projects/{summary,resolve,aliases}` as planned.

Desktop mode (#3, #4)
- Mode is detected from `clientInfo.name`; the project default is computed (never mutated) and the client
  name sits behind a lock, because the existing tests call initialize and tools/list concurrently.
- `project_suggest` is served by the worker (`/api/projects/suggest`): it needs the vector index. With no
  project chosen, `search` uses a new `/api/search/cross-project`, because the existing search endpoint
  requires a project.
- `remember` has its own endpoint (`/api/observations/remember`), not bulk-import: that has a 60-second
  cooldown and a synthetic session per call. The project is mandatory and must exist unless it came from a
  real folder path.
- The protocol text is in the tool descriptions and repeated in tool results, because chat ignores
  initialize instructions.

Prune and merge (#5)
- One transaction covers every table including the sqlite-vec rows. Merge updates the vectors' `project`
  column in place (vec0 supports that; `INSERT OR REPLACE` it does not), so nothing is re-embedded.
- Delete and merge preview first; the confirmation token is derived from the action, the project and its
  current counts. A snapshot (`VACUUM INTO`) is taken first and a failed snapshot aborts the change.
- These are read-then-write transactions. With the worker's asynchronous vector sync writing at the same
  time, SQLite fails them with `SQLITE_BUSY_SNAPSHOT` (the busy timeout does not apply). They now take the
  write lock up front (`immediateTx`). Found by a browser end-to-end run, reproduced by a unit test.
- Dashboard: "Manage projects" in the project dropdown. `/api/projects` is cacheable for five minutes, so the
  dropdown bypasses the cache on every open.
- `project_manage` exposes the same to the model, accepting exact ids only and never resolving aliases or
  names for a destructive action.

Installer (#6)
- `scripts/install-desktop.py` makes surgical text edits so the rest of the config is untouched, verifies
  the result structurally, backs up with unique names, and supports `--dry-run`, `status` and `uninstall`.
  Verified on a copy of a real Desktop config (install then uninstall restores it byte for byte).
- A `.mcpb` bundle was not built; the installer covers the same need.

Not done
- The MCP Apps project picker for chat.
- Windows and Linux paths are written but untested.
- Issue #7 (the real `mcp-server` inside Desktop) needs the user's Desktop and is left for them to run:
  `make install-desktop`, restart Desktop, ask a chat to search memory.

Verification: unit tests in every touched package (race detector on), `npm test` for the dashboard client,
installer tests, and end-to-end runs of the built binaries against an isolated worker: Desktop mode over
stdio, a real worktree, real embeddings, merge and delete, the lazy worker start, and the dashboard in
headless Chrome (30 checks, repeated on fresh data).

Project names (#10)
- Rows from `/api/projects/{summary,suggest}` carry `label` (show) and `use` (pass back): the name when unique among
  real projects (case-insensitively, alias ids not counted as namesakes), otherwise the name plus observation count,
  last use and a sample title, with the id as `use`.
- `/api/projects/resolve` flags an ambiguous name and returns `candidate_details`; remember, context and project_manage
  turn that into an "ask the user which one" message instead of choosing.
- `project_manage` resolves exact names strictly (one real project, never aliases or partial matches) and passes
  everything else through for the worker to refuse. The folder path is not stored, so counts, dates and titles are
  the disambiguators.
