# Using claude-mnemonic with Claude Desktop

Claude Code captures memories automatically through hooks. Claude Desktop has no hooks, and
its chat has no working directory, so it works differently: **you choose the project, and
memories are written on purpose.** Both share one memory store and the same project ids, so
what Code learned is available in Desktop and the other way round.

## Which Desktop mode does what

| Mode | How the project is found | Capture |
|---|---|---|
| **Code tab** | Automatically, as in the terminal (hooks) | Automatic (hooks) |
| **Cowork with a folder** | The folder path is hashed to the id Claude Code uses for the same folder | `remember` |
| **Cowork without a folder, Chat** | `project_suggest` offers likely projects from your first message; you pick one | `remember`, after you pick |
| **Declined** | none | none: read-only search across all projects |

If you decline to pick a project, **nothing can be written**: the server rejects `remember`
without an explicit project, whatever the model does.

## Install

```sh
make install            # builds and installs the worker and MCP server (as before)
python3 scripts/install-desktop.py --dry-run     # preview the change to Claude Desktop's config
make install-desktop    # apply it
```

Then **quit Claude Desktop completely and reopen it**; it reads its configuration only at startup.

The installer edits only its own `claude-mnemonic` entry in `claude_desktop_config.json`. Every
other byte of the file, including your other servers, is left as it was. It checks the result
structurally before writing, keeps a timestamped backup (never overwriting an earlier one), and
`make uninstall-desktop` removes the entry again. Useful options:

- `--project ID` pins one project instead of letting the model choose per conversation
- `--mode desktop|code|auto` forces the mode (default `auto`: detected from the client)
- `--binary PATH`, `--config PATH`, `--name NAME` for non-standard setups

## What the model can do

Desktop mode adds these tools (Claude Code's tool list is unchanged):

| Tool | Purpose |
|---|---|
| `project_suggest` | Ranks projects for the user's first message (content, name, recency) so the model can offer a short list, plus "none" |
| `project_resolve` | Maps a folder path, name or id to the canonical project id |
| `project_list` | All projects with counts and last activity |
| `context` | Loads the saved context of a chosen project |
| `remember` | Saves a decision, finding or fix to a chosen project |
| `checkpoint` | Keeps one living note per line of work (goal, progress, decisions, next steps) in a chosen project; the same thread name updates the note |
| `catch_up` | Returns the project's thread notes (most recently worked on first) and latest decisions, read-only |
| `related` | How a note is connected to others: what it fixes, builds on or evolved from, and what came after it (by note id, or by a query and a project), read-only |
| `relation_types` | The kinds of connection, what each means and how many there are, read-only |
| `project_manage` | Stats, alias, merge and delete projects (previews first, see below) |

`search` and the other existing tools keep working; with no project chosen, `search` covers
every project.

## Making chat use it

Desktop chat has its own built-in memory. Without help it answers "search my memory" from that and never
calls claude-mnemonic. **Add the instruction below to Claude Desktop once**; without it, expect chat to ignore
claude-mnemonic for ordinary "memory" wording.

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

Do not use it for general questions that do not refer to my own earlier work.
```

Get it with the connector's name filled in, and copied to the clipboard:

```sh
python3 scripts/install-desktop.py instructions --copy
```

Paste it into Claude Desktop (Settings, in the field for personal preferences or custom instructions), or into
the instructions of one Desktop project if you only want it there.

**It cannot be added automatically.** The instruction is stored in your claude.ai account, not in a local file,
and `claude_desktop_config.json` has no field for it. The installer can only hand it over: `make install-desktop`
prints it every time it runs (first install, update, or already configured) and copies it to your clipboard, so you
only have to paste it. Use `--no-copy` to skip the clipboard. Server instructions are ignored by chat (measured), and MCP prompts or resources need
an action in every chat, so none of them replaces it.

### What we measured

Five memory prompts that should use claude-mnemonic and three plain prompts that should not, each in a new chat
(the prompts and the script are below):

| Configuration | Memory prompts that used it | Plain prompts that stayed out |
|---|---|---|
| Baseline | 0 of 5 | 3 of 3 |
| Tool descriptions reworded to say "persistent project memory" (server verified to advertise them) | 0 of 5 | not run |
| Connector renamed to `memory` and the instruction added | works; one prompt measured (1 of 1) | not run |
| Default name `claude-mnemonic` and the instruction | observed working in daily use; the full prompt set was not run | not run |

What this tells us, and what it does not:

- In chat, a tool's description appears to be read only after the model decides to look for tools. For a memory
  question it never looks, because it already has a memory of its own, so better descriptions alone changed
  nothing. The instruction acts before that decision.
- Naming the tools in the prompt always worked, so the tools themselves are fine.
- The last row changed two things at once (the connector name and the instruction), so the effect of each is not
  separated. The final setup (default name plus the instruction) was judged by daily use and not measured with the
  full set, so there is no number for it; run the prompts below to measure it on your own machine.
- Chat is a model: no wording can force it to call a tool. The server enforces the rules that matter regardless
  (no project, no write; a shared name is never guessed).

### Checking that it works

In real Desktop, open a **new chat** for each prompt. Mark each one just before sending it:

```sh
python3 scripts/desktop-calls.py clear
python3 scripts/desktop-calls.py mark "P1 search my memory" --expect call
python3 scripts/desktop-calls.py mark "N1 explain worktrees" --expect none
python3 scripts/desktop-calls.py report
```

If the connector has a different name, Desktop logs it under that name, so add
`--desktop-log ~/Library/Logs/Claude/mcp-server-<name>.log` to `report`.

| Prompt | Expect |
|---|---|
| P1 "Search my memory for what we decided about project identity." | call |
| P2 "What did we decide earlier about git worktrees?" | call |
| P3 "Remember that we merged the project-names change today." | call (it should ask which project, not save) |
| P4 "Do you remember what I was working on in claude-mnemonic last week?" | call |
| P5 "Pick up where we left off on the Desktop overlay." | call |
| N1 "Explain how git worktrees work." | none |
| N2 "Write a haiku about autumn." | none |
| N3 "What is 17 times 23?" | none |

The verdict per prompt comes from Desktop's own count of tool calls between your marks. Tool names are inferred
from the worker's log, which `make start-worker` truncates, so they are only available for the current worker run.

## Recovering after a compaction

Claude Code re-injects the saved context after a compaction by itself (its session-start hook fires then), and
its pre-compact hook asks the worker to summarise the conversation just before it is compacted, so decisions that
only lived in the conversation are stored first.
Desktop chat has no hooks, so a long chat that gets summarised can lose which project it was in and what it was
doing. Two tools cover that:

- `checkpoint` writes the state of one thread of work (for example "Overlay design") as a single note that is
  replaced each time it is called, so a long chat leaves one current note per thread instead of a pile of copies.
  The notes are stored with Claude Code's session summaries, searchable like them, and private parts are stripped.
- `catch_up` reads them back: the threads most recently worked on first, plus the project's latest decisions.

With the instruction above, chat checkpoints as it goes once you have chosen a project, calls `catch_up` when you
continue earlier work (also in a new chat), and calls it again when it notices it has lost the thread after a
compaction. Nothing is checkpointed in a chat where you declined to choose a project. To recover by hand, say
"catch up on <project>".

## Following how notes are connected

The knowledge graph links notes that read alike (see the README). `related` shows the links of one note: the notes it
fixes, builds on or evolved from (older) and the notes that came after it (newer), each with its id, how sure the
graph is and why, so chat can answer "what led to this decision?" or "was this ever fixed?" and then follow the chain
by passing a returned id back in. It takes a note id (search results show them) or, instead, a `query` and the project
to find the note in. `relation_types` lists the kinds of relation with what each means and how many there are. Both
are read-only and work in a chat where you declined to save anything. Notes you superseded or archived are not listed.

## Project briefs

`context` and `catch_up` start with the project's **brief** when there is one: a short, dated orientation (what the
project is, its current state, the key decisions and why, conventions and gotchas) written by Haiku from the
project's observations, so a fresh chat does not begin with a pile of raw observations. It says when it was written
and from how many observations, because it can lag behind recent work, and its "Open threads" list comes from your
`checkpoint` notes. The brief is for Desktop only; Claude Code's session-start context is unchanged.

Briefs spend Claude usage, so the automatic ones are off by default (`PROJECT_BRIEF_ENABLED`, see the README). You
can write one by hand any time with `POST /api/projects/<project>/brief`.

## Superseded notes

When you decide in the dashboard's **Conflicts** tab that a newer note replaces an older one, the older note is no
longer returned by `context` or the searches, in Desktop and in Claude Code alike, and later briefs are written
without it (a brief written before your decision can still mention it until it is refreshed). It stays in the
dashboard, marked, and an *Undo* brings it back. Nothing is hidden without that decision. See "Conflict Review" in
the README.

## Project names

The model shows and uses project **names** (`claude-mnemonic`), not ids. Behind each name is an id
(the folder name plus a hash of its full path), which stays the stable key.

Two different folders can have the same name, for example `~/work/app` and `~/work/clients/app`. When that
happens the projects are listed with what tells them apart (how much each holds, when it was last used, a
sample of its content), and the model asks you which one you mean. It never picks one silently: `remember`,
`context` and `project_manage` all refuse a shared name and say so. A namesake can always be reached by its id.

`project_manage` accepts a name too, strictly: an exact name (any capitalisation) that identifies exactly one
real project. Aliases and partial names are never accepted for delete or merge, and a name does not skip the
preview.

## Privacy

- Text inside `<private>...</private>` is never stored, and text that is entirely private is refused.
- Injected memory context is stripped, so recalled memories are not saved again as new ones.
- Nothing is written to a project the user did not choose, and an invented project id is refused.

## Git worktrees

Worktrees (Claude Desktop's worktree option, `claude --worktree`) now belong to their main
repository's project instead of creating a new project each. Ordinary checkouts keep exactly the
ids they had; nothing is migrated.

## Managing projects

Open the project dropdown in the dashboard and choose **Manage projects…**: merge a project into
another, delete it, or remove an alias. The same is available to the model as `project_manage`.

The dropdown and the panel list the same projects: every project that has any data (a session, an
observation or a session summary), by name. The hash is shown only when two projects share a name
(and as a tooltip), and a pasted hash still finds its project in the search. A project that only has
summaries is marked "summaries only".

Every delete and merge is **previewed first**; only a second call carrying the preview's
confirmation token does anything. The token is tied to the project's current contents, so if it
changed after the preview the action is refused and you review again. A backup of the whole
database is taken before each change (kept in `~/.claude-mnemonic/backups`, the newest ten); if
the backup fails, nothing is changed.

A merge moves sessions, observations and summaries into the target, keeps the embeddings as they
are, and makes the old id an alias that keeps resolving. A delete removes everything the project
owns, including its search vectors.

To restore from a backup: stop the worker (`make stop-worker`), replace
`~/.claude-mnemonic/claude-mnemonic.db` with the snapshot (and delete the `-wal`/`-shm` files
next to it), then `make start-worker`.

## Projects that are really one

A project's id is the folder name plus a hash of the full path, so the same work can show up under two ids: a folder
that was moved or renamed, a second clone, a checkout somewhere else. Its memory is then split. The project manager
finds such pairs and helps you merge them, always with the merge above (preview, backup, alias). It never merges on a
name alone.

- **What is recorded.** When a folder path reaches the worker (Claude Code's session start and prompt search, or a path
  resolved by Desktop) the worker asks git, read-only, for the folder's remote (`origin`, otherwise the first by
  name) and the checkout's root, and keeps them per project. The remote is normalised, so the ssh, scp-like and https
  forms of one repository are the same, and credentials in a URL are dropped before anything is stored. Nothing is sent
  anywhere. A project gets this the next time it is used.
- **What is suggested.** The same name or the same remote finds candidate pairs; evidence confirms them. *Same remote*
  is strong. *The same notes* (the same titles) or *a folder that no longer exists* is medium. *One side holds only a
  few notes* is only a hint. Two projects that share just a name are not suggested, and two with different remotes
  are real namesakes and never are.
- **Where.** In the dashboard, **Manage projects…** has a **Possible duplicates** section: the evidence, a merge button
  (bigger project survives; **Keep … instead** reverses it) that opens the usual preview, and **Not the same**. A
  dismissal applies to that pair only, is remembered, and can be taken back ("Suggest again"). In Desktop,
  `project_manage` with `action: duplicates` lists the pairs and `dismiss` records that two are not the same;
  `project_suggest` and `project_resolve` say "probably the same project" for candidates that are, so you are not
  asked to choose between copies of one project.
- **Automatic merge (off by default).** With `CLAUDE_MNEMONIC_PROJECT_AUTO_MERGE_ENABLED` set to `true` in
  `~/.claude-mnemonic/settings.json`, the worker merges by itself only the pairs with the strongest evidence: the same
  remote **and** every recorded folder of the smaller project gone (a checkout that moved or was deleted, not a second
  live clone). It takes a backup first, leaves an alias marked `auto-merge`, and announces the merge in the dashboard
  with the backup's path. To undo one, restore that backup (see Managing projects above). Anything less certain is only suggested.

## Troubleshooting and limits

- **Tools do not appear in Desktop.** Restart Desktop fully, then check the entry with
  `python3 scripts/install-desktop.py status`.
- **The worker is not running.** The MCP server starts it on the first Desktop tool call. If that
  fails, run `make start-worker` and check `/tmp/claude-mnemonic-worker.log`.
- **Cowork cannot run hooks or scripts against the worker.** Its agent shell runs in a sandbox
  that cannot reach `localhost:37777`; only the MCP tools, which run on your computer, can. This
  is why capture there is explicit.
- **Chat ignores server instructions.** The "ask first, never guess" protocol is therefore also
  written into each tool's description.
- **`search` across all projects needs the vector index.** Without it the call fails clearly
  instead of returning nothing.
- **Windows and Linux Desktop** are supported by the installer but have not been tested.

Design notes and the measurements behind these choices are in `design/desktop-overlay.md`.
