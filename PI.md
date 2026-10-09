# Using claude-mnemonic with pi

[pi](https://github.com/earendil-works/pi) has no command hooks like Claude Code, but its extensions get the same
moments of a session. The extension in [`pi-extension/`](pi-extension) runs claude-mnemonic's hook binaries on those
events, with the input Claude Code would give them, and registers the MCP server. So pi, Claude Code and Claude Desktop
share one memory, and a folder is the same project in all of them: the project id is computed by the same binaries.

## Install

1. Install claude-mnemonic's binaries. Any route works: the Claude Code plugin (it downloads them on its first
   session), `install.sh` from the release, or `make install` from a checkout. They end up in `~/.claude-mnemonic/bin`.
2. Add the extension to pi, pinned to the release whose binaries you have:

   ```bash
   pi install git:github.com/hlgr360/claude-mnemonic@v<version>
   ```

   Use the tag of the release you installed; v0.21.105.4 is the first release with the extension. The tag pins the extension; to move to a newer release, install again with
   the new tag. To try it for one run without installing: `pi -e git:github.com/hlgr360/claude-mnemonic@<tag>`. From a
   checkout of this repository, `pi install /path/to/claude-mnemonic` loads it in place.

   The repository root's `package.json` is what makes it a pi package: it points pi at `pi-extension/index.ts`. It is
   private and has no dependencies, so the install fetches nothing else.

Without the binaries the extension says so once per session and does nothing else.

## What happens when

| pi event | Hook binary | Effect |
|---|---|---|
| `session_start` | `session-start` | Loads the project's saved context; it is given to the model with the first prompt |
| `session_start`, `agent_settled`, a timer | `statusline` | The footer status |
| `before_agent_start` | `user-prompt` | Adds notes relevant to the prompt and starts the session in the worker |
| `tool_result` | `post-tool-use` | Sends the tool call to the worker, which turns the useful ones into observations |
| `agent_settled` | `stop` | Asks for a session summary |
| `session_before_compact` | `pre-compact` | Summarises the conversation before compaction drops it |

The context reaches the model as a hidden custom message (`customType: "claude-mnemonic"`), the equivalent of a Claude
Code hook's `additionalContext`. Pi's built-in tools are reported under the names the worker knows from Claude Code
(`bash` as `Bash`, `edit` as `Edit`, `find` as `Glob`, ...), so its filters for trivial calls apply to them too. Pi has
no transcript in Claude Code's format, so the `stop` and `pre-compact` hooks get the recent user and assistant turns
inline, in a `messages` field.

Sessions appear in the dashboard with ids starting with `pi-`.

## Tools and commands

The extension registers the MCP server as `claude-mnemonic` each time pi loads it; nothing is written to `mcp.json`, so
`pi remove` takes it away too. It has the same tools as in Claude Code, declared to the model directly (`exposure:
"direct"`) rather than only reachable from codemode scripts: `mcp__claude_mnemonic__search`, `..._timeline`,
`..._observation` and `..._memory_admin`. Its project is the folder pi was started in. A `claude-mnemonic` entry in your
own `mcp.json` takes precedence (`/mcp` shows the override), and the binaries must be installed before pi starts (or run
`/reload`).

`/memory-dashboard` shows the dashboard's address, or says that the worker is not running. `/memory-statusline` is
described below.

## Status line

Pi's footer shows the same status as Claude Code's status line, `[mnemonic] ● served:42 | injected:5 | project:28 memories`,
from the same binary. Nothing has to be set up: the extension sets it under the key `claude-mnemonic` and refreshes it
after every prompt and every 15 seconds. `CLAUDE_MNEMONIC_STATUSLINE_FORMAT` (`default`, `compact`, `minimal`) and
`CLAUDE_MNEMONIC_STATUSLINE_COLORS` work as in Claude Code. The `[mnemonic]` tag is not a link to the dashboard in pi; use
`/memory-dashboard`.

`/memory-statusline` is the same command as in Claude Code. In Claude Code it is needed to turn the status line on (a
plugin cannot set it); in pi the status is on from the start, and the command turns it off or on again:

- `/memory-statusline` or `/memory-statusline on`: show it
- `/memory-statusline off`: hide it
- `/memory-statusline status`: say whether it is on

The choice is kept in `~/.claude-mnemonic/pi.json` for every pi session.

A custom footer (`ctx.ui.setFooter`) shows extension statuses only if it renders them: it reads them with
`footerData.getExtensionStatuses()`, and the one from this extension is under `claude-mnemonic`. The command warns when
an extension in pi's extension folders (`~/.pi/agent/extensions`, `.pi/extensions`) sets its own footer; footers from
installed packages are not detected.

## Notes

- The summaries need binaries that know the `messages` field (this version or later). Older binaries ignore it and
  summarise without the conversation; everything else works with them.
- Hooks that pi need not wait for (observations, summaries) run detached, so quitting pi does not cut them off.
