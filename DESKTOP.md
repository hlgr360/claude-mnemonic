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
2. If I choose one, load its context before answering, and save to it only when I ask you to remember something.
3. If I decline, search read-only and do not save anything.
4. If two projects share a name, ask me which one.

Do not use it for general questions that do not refer to my own earlier work.
```

Get it with the connector's name filled in, and copied to the clipboard:

```sh
python3 scripts/install-desktop.py instructions --copy
```

Paste it into Claude Desktop (Settings, in the field for personal preferences or custom instructions), or into
the instructions of one Desktop project if you only want it there.

**It cannot be added automatically.** The instruction is stored in your claude.ai account, not in a local file,
and `claude_desktop_config.json` has no field for it. The installer can only hand it over, and reminds you at the
end of `install-desktop`. Server instructions are ignored by chat (measured), and MCP prompts or resources need
an action in every chat, so none of them replaces it.

### What we measured

Five memory prompts that should use claude-mnemonic and three plain prompts that should not, each in a new chat
(the prompts and the script are below):

| Configuration | Memory prompts that used it | Plain prompts that stayed out |
|---|---|---|
| Baseline | 0 of 5 | 3 of 3 |
| Tool descriptions reworded to say "persistent project memory" (server verified to advertise them) | 0 of 5 | not run |
| Connector renamed to `memory` and the instruction added | works; one prompt measured so far (1 of 1) | not run |

What this tells us, and what it does not:

- In chat, a tool's description appears to be read only after the model decides to look for tools. For a memory
  question it never looks, because it already has a memory of its own, so better descriptions alone changed
  nothing. The instruction acts before that decision.
- Naming the tools in the prompt always worked, so the tools themselves are fine.
- The last row changed two things at once (the connector name and the instruction), so the effect of each is not
  separated. The full set with the default connector name and the instruction is the number that matters; it is
  recorded in issue #16 when measured.
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
