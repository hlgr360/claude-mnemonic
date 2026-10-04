# Claude Mnemonic

Persistent memory for Claude Code and Claude Desktop. It keeps the decisions, findings and fixes from your earlier
sessions, searchable and shared by both, and has a web dashboard (`/memory-dashboard`). This is a fork of
[lukaszraczylo/claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) (MIT); its releases are at
https://github.com/hlgr360/claude-mnemonic.

## What this plugin does

- **In Claude Code**, hooks save what happens in a session and load the project's memory at the start, and the MCP
  server gives Claude the search and related tools.
- **Skills:** `/memory-dashboard` opens the dashboard, `/memory-restart` restarts the local worker,
  and `project-memory` carries the instruction below.
- **The memory is stored on your computer** (`~/.claude-mnemonic`), by a local worker that the hooks and the MCP server
  start when needed.
- **The plugin carries no binaries.** On first use it downloads the binaries of its own version from the release,
  checks them against the release's checksums (and against the cosign signature when cosign is installed), and
  installs them in `~/.claude-mnemonic/bin`. Binaries from `make install` or the install script are never replaced.
  Supported: macOS on Apple silicon and Linux on x86-64. The first session may run without memory until the download
  has finished.
- Do not use it next to another install of claude-mnemonic (`install.sh`, `make install`, upstream's plugin): they
  register the same hooks.

## Claude Desktop chat needs one setting

Chat has a built-in memory of its own and answers "search my memory" from that, without calling this plugin. With only
the `project-memory` skill, chat still did not call the connector (one prompt measured; see DESKTOP.md in the
repository). Paste the text below **once** into Claude Desktop's personal preferences (Settings, in the field for
custom instructions). It lives in your claude.ai account, so a plugin cannot set it for you.

```text
{{ INSTRUCTION }}
```

More about the tools, projects and how this was measured: DESKTOP.md in the repository.
