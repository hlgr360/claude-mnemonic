# Claude Mnemonic

**Give Claude Code a memory that actually remembers.**

[![Release](https://img.shields.io/github/v/release/hlgr360/claude-mnemonic?style=flat-square)](https://github.com/hlgr360/claude-mnemonic/releases)
[![License](https://img.shields.io/github/license/hlgr360/claude-mnemonic?style=flat-square)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go)](https://go.dev)

> **This is a fork** of [lukaszraczylo/claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) (MIT). It adds Claude Desktop support ([DESKTOP.md](DESKTOP.md)), project management (duplicates, aliases, merge), a dashboard link and command, a signed release pipeline and a Claude plugin. Its releases are named for the upstream version they contain plus a fork number: `0.21.95.1` is upstream `0.21.95` plus this fork's first release.

---

Claude Code forgets everything when your session ends. Claude Mnemonic fixes that.

It captures what Claude learns during your coding sessions - bug fixes, architecture decisions, patterns that work - and brings that knowledge back in future conversations. No more re-explaining your codebase.

![Claude Mnemonic Dashboard](docs/public/claude-mnemonic.jpg)

## What's New in v0.7

- **Two-Stage Retrieval** - Cross-encoder reranking for dramatically improved search relevance
- **Knowledge Graph** - Automatic relationship detection between observations with visual graph in dashboard
  ![Knowledge Graph](docs/public/observation-relation-graph.jpg)
- **Pattern Detection** - Identifies recurring patterns across sessions and projects
- **Importance Scoring** - Time decay and voting system to surface the most valuable memories
- **Query Expansion** - Reformulates searches to find semantically related content
- **Conflict Detection** - Identifies and resolves contradictory observations
- **Observation Lifecycle** - Memories can be superseded when better information arrives

<details>
<summary>Previous: v0.6</summary>

- **Auto-Updates** - Automatically stays up-to-date with the latest version
- **Slash Commands: `/memory-dashboard` and `/memory-restart`** - Open the dashboard, or restart the worker, directly from Claude Code
- **Local Embeddings** - All semantic search runs locally via ONNX Runtime (no external API calls)
- **Async Queue Processing** - Non-blocking observation capture for faster sessions
- **Smarter Storage** - Filters out system/agent summaries to keep knowledge relevant
- **Improved Reliability** - Better handling of connectivity issues and dead connections
</details>

## One memory for Claude Code and Claude Desktop

Claude Code and Claude Desktop (chat, Cowork and the Code tab) use **one local memory service**: a worker on your computer (port 37777, data in `~/.claude-mnemonic`) with one database and one dashboard. What Claude Code saves in a session, Desktop chat can search, and the other way round. Nothing is sent to a server.

| App | Install | What it gives |
|---|---|---|
| **Claude Code** (the terminal, and the Desktop Code tab) | the **plugin**, from the marketplace | Hooks that save what happens in a session and load the project's memory at the start, the MCP tools (search, related, ...), skills and commands |
| **Claude Desktop chat and Cowork** | the **extension** (a `.mcpb` file) | The memory tools, with a few extra ones for choosing a project (Desktop does not give a plugin's tools to chat, and Cowork starts them where they cannot reach the worker) |

Use the one for the app you use, or both: they share the worker and the database. The extension cannot replace the plugin in Claude Code (the automatic saving comes from the plugin's hooks), and the plugin cannot replace the extension in Desktop.

Details for Desktop (the tools, project handling, what was measured): [DESKTOP.md](DESKTOP.md).

## Requirements

| Dependency | Required | Purpose |
|------------|----------|---------|
| **Claude Code** or **Claude Desktop** | Yes (either, or both) | Where the memory is used |

That's it. No Python. No external services. Everything runs locally.

> **No API keys needed!** Claude Mnemonic uses Claude Code CLI, which works with your existing Claude Pro or Max subscription. No separate API costs.

## Install

Install what you use. Both routes download the binaries of their own release on first use, check them against the release's checksums (and against the cosign signature when cosign is installed) and keep them in `~/.claude-mnemonic/bin`. Supported: macOS on Apple silicon (plugin and extension) and Linux on x86-64 (plugin). Your data (`~/.claude-mnemonic`: the database, settings and embeddings) is never touched by an install.

### Claude Code: the plugin

```
/plugin marketplace add hlgr360/agent-plugins
/plugin install claude-mnemonic@hlgr360
```

Then **start a new Claude Code session**. Installing the plugin starts nothing: the first session downloads the binaries and starts the worker, and only then does the dashboard at **http://localhost:37777** answer (or run **`/claude-mnemonic:memory-dashboard`**). The first session may start without memory until the download has finished.

Claude Code 2.1.224 or later is known to work; an older version fails with `plugins.0.source: Invalid input` on the original catalogue form, and was not tested since the catalogue changed. Do not install it next to another install of claude-mnemonic (upstream's plugin, a build from source): they register the same hooks.

### Claude Desktop: the extension

1. Download `claude-mnemonic-desktop_<version>.mcpb` from the [latest release](https://github.com/hlgr360/claude-mnemonic/releases/latest).
2. In Claude Desktop open **Settings > Extensions > Install Extension** and choose the file (or drag it onto the window), then restart Desktop.
3. Paste the instruction below into your preferences, once.

Chat and Cowork then have the memory tools (`project_suggest`, `remember`, `checkpoint`, `catch_up`, `dashboard`, `restart`, ...), run on your computer and talking to the same worker as Claude Code. Details, the tools and how projects work: [DESKTOP.md](DESKTOP.md).

For an organization: an owner uploads the file at Organization settings > Connectors > Desktop > "Add custom extension", and a new version is uploaded the same way ("Upload new version"; each release has a higher extension version, `0.21.95-fork.3` for release `0.21.95.3`). With the extension allowlist on, people cannot install the file themselves.

### Claude Desktop chat: paste this once

Chat has a built-in memory of its own and answers "search my memory" from that, without calling the memory tools, so it needs one instruction. Paste the text below **once** into Claude Desktop: Settings, in your account's personal preferences (custom instructions) field. (Prefer it only in one place? Put it into the instructions of one Desktop project instead.)

```text
I keep a persistent memory of my project work in the claude-mnemonic connector. It is shared with Claude Code and holds decisions, findings and fixes from earlier sessions.

Use it, in addition to any built-in memory, whenever I ask about my past work, earlier decisions or project history, or when I say "memory" or "remember" about my projects. Tell me which source an answer came from.

How to use it:
1. Call the project_suggest tool of the claude-mnemonic connector with my message and show me the projects it returns by name. Ask me which one I mean, or whether to continue without one. Never pick a project for me.
2. If I choose one, load its context before answering. If I am continuing earlier work, also call catch_up for it. While we work, keep one short checkpoint per thread of work with the checkpoint tool (goal, progress, decisions, next steps) after meaningful progress. Save anything else with remember only when I ask you to.
3. If I decline, search and catch up read-only, and do not save anything and do not checkpoint.
4. If two projects share a name, ask me which one. If the connector says they are probably the same project, tell me and offer to merge them; do not make me choose between copies of one project.
5. If this conversation has been compacted or summarised and you lose track of the project or of what we were doing, call catch_up for the project (ask me which one, as in 1, if you do not know) before carrying on, instead of asking me to repeat it.
6. When I ask how something came about, what led to a decision or whether a problem was ever fixed, find the note with search, then call related with its id and follow the connections it lists.
7. When I ask to see, open or manage my memory in a browser, call dashboard and give me the link.

Do not use it for general questions that do not refer to my own earlier work.
```

It lives in your claude.ai account, so nothing can set it for you, and `python3 scripts/install-desktop.py instructions --copy` (from a clone) puts it on your clipboard. Without it, expect chat to ignore claude-mnemonic for ordinary "memory" wording; Claude Code does not need it.

<details>
<summary>Build from source (developers)</summary>

```bash
git clone https://github.com/hlgr360/claude-mnemonic.git
cd claude-mnemonic
make build && make install
```

Requires: Go 1.24+, Node.js 18+, CGO-compatible compiler. `make install` registers the build as the plugin (for Claude Code); `make install-desktop` registers it as a connector in Claude Desktop's configuration instead of the extension.
</details>

With either route the worker starts on its own: with the first Claude Code session, or the first memory tool call in Desktop. Then open **http://localhost:37777** to see the dashboard (it opens on the **Summaries** tab; **All**, Observations, Prompts, Graph and Conflicts are one click away).

You do not have to remember the address:

- **Claude Code:** run **`/memory-dashboard`**. It opens the dashboard in your browser (it finds a custom `WORKER_PORT` itself) and says so if the worker is not running. The first session after an install or an update also shows you a one-line message with the address (for you only; it is not added to what the model sees).
- **Claude Desktop:** ask for your memory dashboard; the `dashboard` tool gives chat the link to click.

### Verifying Release Signatures

All release checksums are signed with [cosign](https://github.com/sigstore/cosign) using keyless signing (the signature covers the platform archives, the plugin zip and the Desktop extension). To verify:

```bash
# Download checksums.txt and checksums.txt.sigstore.json from the release
cosign verify-blob \
  --certificate-identity-regexp "^https://github.com/hlgr360/claude-mnemonic/.*$" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  --bundle "checksums.txt.sigstore.json" \
  checksums.txt
```

## What it does

| Feature | Description |
|---------|-------------|
| **Persistent Memory** | Observations survive across sessions and restarts |
| **Project Isolation** | Each project has its own knowledge base |
| **Global Patterns** | Best practices are shared across all projects |
| **Semantic Search** | Find relevant context with natural language (local embeddings) |
| **Live Statusline** | Real-time metrics in Claude Code: `[mnemonic] ● served:42 | project:28 memories`. While the worker is ready, the `[mnemonic]` tag is a link to the dashboard (Cmd+click on macOS, Ctrl+click elsewhere, in a terminal that supports hyperlinks such as iTerm2, Kitty or WezTerm; Terminal.app does not). `CLAUDE_MNEMONIC_STATUSLINE_LINK=false` turns the link off |
| **Web Dashboard** | Browse and manage memories at `localhost:37777` (`/memory-dashboard` opens it) |
| **Auto-Updates** | Automatically downloads and applies new versions |
| **Slash Commands** | Control the worker directly from Claude Code |

### How knowledge flows

```
You code with Claude
        ↓
Claude learns something useful
        ↓
Mnemonic captures it automatically
        ↓
Next session: Claude remembers
```

Behind the scenes: hooks capture Claude's observations → SQLite stores with full-text search → sqlite-vec enables semantic search with local embeddings (all-MiniLM-L6-v2) → relevant context is injected at session start.

## Configuration

Config file: `~/.claude-mnemonic/settings.json`

```json
{
  "CLAUDE_MNEMONIC_WORKER_PORT": 37777,
  "CLAUDE_MNEMONIC_CONTEXT_OBSERVATIONS": 100,
  "CLAUDE_MNEMONIC_CONTEXT_FULL_COUNT": 25,
  "CLAUDE_MNEMONIC_RERANKING_ENABLED": true
}
```

### Core Settings

| Variable | Default | What it does |
|----------|---------|--------------|
| `WORKER_PORT` | `37777` | Dashboard & API port |
| `CONTEXT_OBSERVATIONS` | `100` | Max memories per session |
| `CONTEXT_FULL_COUNT` | `25` | Full detail memories (rest are condensed) |
| `CONTEXT_SESSION_COUNT` | `10` | Recent sessions to reference |
| `CONTEXT_RELEVANCE_THRESHOLD` | `0.3` | Minimum similarity score (0.0-1.0) for inclusion |
| `CONTEXT_MAX_PROMPT_RESULTS` | `10` | Max results per prompt search |

### Reranking Settings (Two-Stage Retrieval)

| Variable | Default | What it does |
|----------|---------|--------------|
| `RERANKING_ENABLED` | `true` | Enable cross-encoder reranking |
| `RERANKING_CANDIDATES` | `100` | Candidates to retrieve before reranking |
| `RERANKING_RESULTS` | `10` | Final results after reranking |
| `RERANKING_ALPHA` | `0.7` | Score blend: alpha×rerank + (1-alpha)×original |
| `RERANKING_PURE_MODE` | `false` | Use pure cross-encoder scores only |

### Embedding Settings

| Variable | Default | What it does |
|----------|---------|--------------|
| `EMBEDDING_MODEL` | `bge-v1.5` | Embedding model for semantic search |

### Project Brief (optional, Desktop)

A project brief is a short, dated orientation for a project: what it is, its current state, the key decisions and
why, and the conventions and gotchas, written by Haiku from the project's observations (with the observation ids it
rests on). Claude Desktop receives it first when it calls `context` or `catch_up` for a project, so a fresh chat
does not have to read a pile of raw observations. Claude Code's own context injection is not changed.

It spends Claude usage (about one call per refresh, a few cents for a project of a few hundred observations), so
the automatic briefs are **off by default**. The header says when it was written and from how many observations,
because a brief can lag behind recent work. Its "Open threads" list comes straight from your checkpoint notes, not
from the model. You can always ask for one by hand, whether or not the automatic ones are on:

```sh
curl -s -X POST localhost:37777/api/projects/<project>/brief     # write (or rewrite) it now
curl -s localhost:37777/api/projects/<project>/brief              # read it
```

| Variable | Default | What it does |
|----------|---------|--------------|
| `PROJECT_BRIEF_ENABLED` | `false` | Write and refresh briefs automatically in the background |
| `PROJECT_BRIEF_MIN_NEW_OBSERVATIONS` | `10` | A project gets its first brief at this many observations, and a new one after this many new ones |
| `PROJECT_BRIEF_MAX_AGE_DAYS` | `7` | A brief older than this is also refreshed as soon as one observation is new |
| `PROJECT_BRIEF_MAX_PER_RUN` | `3` | At most this many briefs per pass, the projects with the most new observations first |
| `PROJECT_BRIEF_INTERVAL_MINUTES` | `60` | How often a pass looks for projects that need a brief |

### Conflict Review

Over time notes go out of date: a newer note says the cache now lives for a day, and the older one still says an
hour. The **Conflicts** tab of the dashboard (`http://localhost:37777`) is where you settle such pairs. Each
proposal opens as the older and the newer note side by side, with the words and items that differ marked, and you
choose:

- **Newer replaces older**: the older note is hidden from session context and from search.
- **Older replaces newer**: the same the other way round.
- **Keep both**: the two do not conflict. The pair is remembered and never proposed again.
- **Skip**: decide later.

A decision takes effect at once and an **Undo** is offered right away (and from the *Decided* list at any time).
Nothing is ever hidden without you: the proposals come from Haiku (or from you, below) and a model's say-so never
changes a note. A hidden note stays in the dashboard, marked *Superseded*, and can be fetched by id. Claude Code is
otherwise unchanged; only notes you decided about are left out of its context.

Looking for pairs spends Claude usage: one short Haiku call per new observation that has close older neighbours in
the same project, and at most 20 observations per hourly pass, so an existing archive is worked through over time.
The **proposer is on by default**. To switch it off, put `"CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED": false` in
`~/.claude-mnemonic/settings.json` and restart the worker. In our checks Haiku's strict prompt flagged roughly one
in eight of the pairs it was shown, and only about half of those were real, which is why it only proposes. You can
also propose a pair yourself:

```sh
curl -s -X POST localhost:37777/api/conflicts -H 'Content-Type: application/json' \
  -d '{"older_id": 12, "newer_id": 40, "reason": "the cache lifetime changed"}'
curl -s 'localhost:37777/api/conflicts?status=open'                       # what waits for a decision
curl -s -X POST localhost:37777/api/conflicts/7/resolve -d '{"decision": "supersede_older"}'
curl -s -X POST localhost:37777/api/conflicts/7/undo
```

| Variable | Default | What it does |
|----------|---------|--------------|
| `CONFLICT_PROPOSALS_ENABLED` | `true` | Look for conflicting notes in the background and propose them |
| `CONFLICT_PROPOSALS_MAX_PER_RUN` | `20` | At most this many observations are looked at per pass, newest first |
| `CONFLICT_PROPOSALS_INTERVAL_MINUTES` | `60` | How often a pass runs |
| `CONFLICT_PROPOSALS_MIN_SIMILARITY` | `0.65` | How close an older note must be to the new one to be compared with it |
| `SUPERSEDED_RETENTION_DAYS` | `0` | Delete a note this many days after you superseded it. `0` keeps hidden notes for ever |
| `LLM_BACKEND_CONFLICT` | `claude` | `claude` or `ollama`, for the proposer. No local model passed our checks, so keep `claude` |

An observation is looked at once; if the model call fails it is tried again in the next pass.

### Knowledge Graph

The dashboard's **Graph** tab draws how your notes relate: each note is a circle (bigger when it has more relations,
coloured by type), each relation a line (thicker when the notes are closer). Click a note to read it and see the
notes related to it. You can hide relation types, raise the minimum confidence, and the project filter applies.

Relations are made in the background from what is already stored, so they cost **no model usage**: a note is linked to
at most three older notes of the **same project** that read alike (by the same vector search the rest of the memory
uses). The existing rules may then call a relation *fixes*, *depends on* or *evolves from* when two notes share files
or follow a natural order (a decision, then the feature); otherwise it is *relates to*, and the confidence is how close
the two notes are. A relation is never *supersedes*: replacing a note is something you decide in the Conflicts tab, and
notes you superseded leave the graph. A new note is picked up within about ten minutes, and an existing archive is
worked through a hundred notes at a time after the worker starts. In a real archive of 416 notes this gave about two
relations per note; an all-pairs rule gave about 29 per note, which is not a graph.

| Variable | Default | What it does |
|----------|---------|--------------|
| `GRAPH_ENABLED` | `true` | Build the graph in the background |
| `GRAPH_RELATIONS_MIN_SIMILARITY` | `0.6` | How close an older note must be to be related |
| `GRAPH_RELATIONS_MAX_PER_OBSERVATION` | `3` | At most this many relations from a note to older notes |

After changing the thresholds, start over with `curl -s -X POST localhost:37777/api/relations/rebuild` (the **Rebuild**
button in the Graph tab does the same). `GET /api/graph?project=&min_confidence=&types=&max_nodes=` returns the nodes and
relations as JSON.

Your assistant can read the graph too, not only the dashboard:

- `observation` with `action: related` answers "how is this note connected?": for one note it lists the notes it fixes,
  builds on or evolved from and the notes that came after it, each with its id, the kind of relation, how sure the
  graph is and why. Filter with `types`, `direction` (`older` = what it came from, `newer` = what came after),
  `min_confidence` and `limit`; pass an id from any answer back in to follow the chain.
- `action: relation_types` lists the kinds of relation with what each means and how many there are (in a project,
  around one note, or everywhere); `action: relationships` returns the wider graph around a note (`max_depth`, `types`,
  `min_confidence`).
- In Claude Desktop the same two are tools of their own, `related` (also takes a `query` and a project instead of an id)
  and `relation_types`; see [DESKTOP.md](DESKTOP.md).
- Over HTTP: `GET /api/observations/{id}/connections?direction=&types=&min_confidence=&limit=` and
  `GET /api/relations/types?project=&observation_id=`. Notes you superseded or archived are never listed.

### Duplicate Projects

The same work can end up under two project ids (a moved or renamed folder, a second clone). The worker records the
normalised git remote of each project's folder (no credentials, nothing sent anywhere) and the dashboard's **Manage
projects…** shows **Possible duplicates**: pairs that share a remote, or a name plus other evidence (the same notes, a
folder that is gone). A name alone is never enough and projects with different remotes are never suggested. Merging
uses the existing preview, backup and alias; **Not the same** is remembered per pair. In Claude Desktop use
`project_manage` with `duplicates` and `dismiss`; see [DESKTOP.md](DESKTOP.md).

| Variable | Default | What it does |
|----------|---------|--------------|
| `PROJECT_AUTO_MERGE_ENABLED` | `false` | Merge by itself the pairs with the same remote whose old folder is gone (backup first, alias `auto-merge`, announced in the dashboard) |
| `PROJECT_AUTO_MERGE_INTERVAL_MINUTES` | `30` | How often it looks |

API: `GET /api/projects/duplicates`, `POST /api/projects/duplicates/dismiss` and `/restore` (`{a, b}`).

### Local LLM Settings (Ollama, optional)

Summaries, observation extraction and the stale-observation check run on the Claude CLI by default. Each of
these tasks can instead run on a local [Ollama](https://ollama.com) model, so nothing from a session leaves the
machine. Nothing changes unless you switch a task; `GET /api/llm/status` shows what the worker sees.

| Variable | Default | What it does |
|----------|---------|--------------|
| `LLM_BACKEND_SUMMARY` | `claude` | `claude` or `ollama`, for session summaries |
| `LLM_BACKEND_OBSERVATION` | `claude` | `claude` or `ollama`, for observation extraction |
| `LLM_BACKEND_VERIFY` | `claude` | `claude` or `ollama`, for the stale-observation check |
| `LLM_BACKEND_BRIEF` | `claude` | `claude` or `ollama`, for the project brief (see below) |
| `LLM_BACKEND_CONFLICT` | `claude` | `claude` or `ollama`, for the conflict proposer (see above) |
| `LLM_FALLBACK_TO_CLAUDE` | `true` | Use the Claude CLI when Ollama is unreachable or fails |
| `OLLAMA_MODEL` | *(none)* | Model to use, for example `gemma3:12b`. A task on `ollama` without a model stays on Claude |
| `OLLAMA_URL` | `OLLAMA_HOST`, else `http://localhost:11434` | Where Ollama listens |
| `OLLAMA_NUM_CTX` | `16384` | Context window sent to Ollama (its own default is far too small for long inputs) |
| `OLLAMA_KEEP_ALIVE` | `10m` | How long Ollama keeps the model in memory |
| `OLLAMA_TIMEOUT_SECONDS` | `120` | Per-request timeout |

`make install-ollama` does the setup for you: it finds Ollama, shows which of the models we know are installed,
downloads the one you pick (only after asking), checks that it answers, and saves the choice. It switches no task
over unless you name it (`python3 scripts/setup-ollama.py --model gemma3:12b --task verify --yes` is the
non-interactive form; add `--dry-run` to preview). `make uninstall-ollama` puts every task back on the Claude CLI.

Small models write noticeably worse summaries than Haiku. In a test with `llama3.2:3b` on real turns the
summaries were generic and sometimes wrong, so pick a model by comparing it on your own sessions first.

All variables are prefixed with `CLAUDE_MNEMONIC_` in the config file.

## Project vs Global scope

Every observation has a scope that decides where it is shown to sessions:

- **Project scope** (the usual one): shown only to sessions of its own project.
- **Global scope**: shown to sessions of every project.

A note gets the scope **global only when it is tagged as a general lesson (`best-practice` or `anti-pattern`) and
changed none of the project's files**. Everything else is project scope. Example: a bug fix in your auth module stays
local; "Always validate JWT server-side", saved as a best practice, goes global. (An earlier version also globalized
notes tagged `architecture`, `testing`, `workflow`, `pattern`, `tooling`, `debugging`, `security` or `performance`, which
nearly every note carries: on a real archive 90% of the notes became global and were shown in every project.)

You can see and change this in the dashboard: each note shows a **Project** or **Global** badge, and clicking it switches
the note. The **Scope** filter above the timeline lists one kind, and **Review scopes…** previews what the current rule
would change across the whole archive (how many notes, a sample, how many are kept because you chose their scope) and
applies it after a backup of the database. A scope you chose, by clicking, by editing the note or by saving it with
`remember`, is never changed by a re-scope. The same is available as `GET /api/scope/preview` and
`POST /api/scope/apply {"confirm": "<token of the preview>"}`.

The project brief reads all of a project's own notes, whatever their scope.

## MCP Tools

Four tools are exposed via MCP:

- `search` - semantic search across all memories. Accepts `obs_type` (e.g.
  `decision`, `code_change`, `architecture`) plus filter params (`concepts`,
  `files`, `type`) - these replace the old per-type shortcut tools.
- `timeline` - browse observations around a point in time.
- `observation` - manage individual observations. Set `action` to one of:
  `get`, `edit`, `delete`, `supersede`, `boost`, `merge`, `related`, `relation_types`,
  `similar`, `quality`, `relationships`, `scoring`, `tag`, `by_tag`, `batch_tag`.
- `memory_admin` - administration and analytics. Set `action` to one of:
  `stats`, `health`, `maintenance_stats`, `run_maintenance`, `importance`,
  `search_patterns`, `explain_ranking`, `temporal_trends`, `data_quality`,
  `export`, `suggest_consolidations`, `patterns`.

Using Claude Desktop (chat, Cowork, Code tab)? See [DESKTOP.md](DESKTOP.md): it adds
project selection, explicit `remember`, and project management.

## Slash Commands

Available commands within Claude Code:

| Command | Description |
|---------|-------------|
| `/memory-dashboard` | Open the web dashboard in your browser |
| `/memory-restart` | Restart the worker process when experiencing issues |

The names say what they do and are unique on purpose: where the plugin's name cannot be typed as a prefix (Claude Desktop), the short name is all there is. In Claude Code the full form is `/claude-mnemonic:memory-dashboard` and `/claude-mnemonic:memory-restart`. They were renamed from the shorter `dashboard` and `restart`, which no longer exist.

In Claude Desktop chat and Cowork a shell runs in a sandbox that cannot reach the worker on your computer, so the same two actions are MCP tools there (`dashboard` and `restart`, which run on your side of the sandbox): ask Claude to show your memory dashboard or to restart your memory. The commands tell Claude to use the tool when it has one.

## Auto-Updates

Claude Mnemonic automatically checks for updates and applies them. Updates are downloaded in the background and applied on restart.

- Automatic update checks on startup
- Background downloads (up to 250MB)
- Seamless restart after update
- Manual trigger: `curl -X POST http://127.0.0.1:37777/api/update/apply`

Check update status: `curl http://127.0.0.1:37777/api/update/status`

## Troubleshooting

**The dashboard does not load right after `claude plugin install`?** That is expected until a session has started: installing the plugin starts nothing. Start a new Claude Code session (the first one downloads the binaries and starts the worker), then open http://localhost:37777 or run `/claude-mnemonic:memory-dashboard`. If it still does not load, run `/claude-mnemonic:memory-restart`.

**Worker won't start?**
```bash
lsof -i :37777              # check if port is in use
cat /tmp/claude-mnemonic-worker.log  # view logs
```

**Database locked?**
```bash
rm -f ~/.claude-mnemonic/*.db-wal ~/.claude-mnemonic/*.db-shm
```

**Worker unresponsive?**
```bash
# Restart via API
curl -X POST http://127.0.0.1:37777/api/restart

# Or use the slash command in Claude Code
/memory-restart

# In Claude Desktop chat or Cowork: ask Claude to restart your memory (it uses the restart tool)
```

**Check health status:**
```bash
curl http://127.0.0.1:37777/api/selfcheck
```

## Uninstall

Your data (`~/.claude-mnemonic`: the database, settings and embeddings) is **kept** unless you delete it yourself.

- **Claude Code:** `/plugin uninstall claude-mnemonic@hlgr360`.
- **Claude Desktop:** Settings > Extensions, remove Claude Mnemonic.
- **Everything, including your data** (the database cannot be recovered): stop the worker (`lsof -ti :37777 | xargs kill`) and delete `~/.claude-mnemonic`.

Switching between routes only needs the first two lines; never delete the data for that.

## Architecture

- **SQLite + FTS5** - Full-text search for exact matches
- **sqlite-vec** - Vector database embedded in SQLite
- **Two-Stage Retrieval** - Bi-encoder (embedding) + cross-encoder (reranking) for high accuracy
- **Local Models** - all-MiniLM-L6-v2 for embeddings, BGE reranker for relevance scoring
- **Go** - Single binary, no external dependencies

Everything runs locally. No Python. No external vector database. No API calls.

## Platform support

| Platform | Status |
|----------|--------|
| macOS Intel | Supported |
| macOS Apple Silicon | Supported |
| Linux amd64 | Supported |
| Linux arm64 | Supported |
| Windows amd64 | Supported |

The table is what the worker builds for. Through the plugin and the extension the supported platforms are macOS on Apple silicon (both) and Linux on x86-64 (plugin only); anything else builds from source.

## Development

```bash
make build          # build all
make test           # run tests
make dev            # dev mode with hot reload
make install        # install to Claude plugins
```

How work is done in this fork (ticket, branch, checks, security scanning, reviewing the bots' comments before merging) is in [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT

---

**Links:** [Releases](https://github.com/hlgr360/claude-mnemonic/releases) · [Issues](https://github.com/hlgr360/claude-mnemonic/issues) · [Upstream](https://github.com/lukaszraczylo/claude-mnemonic)
