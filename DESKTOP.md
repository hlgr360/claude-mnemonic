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
