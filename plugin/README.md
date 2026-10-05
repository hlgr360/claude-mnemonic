# Claude Mnemonic

Persistent memory for Claude Code. It keeps the decisions, findings and fixes from your earlier sessions, saves them
automatically, loads the project's memory at the start of a session, makes them searchable, and has a web dashboard
(`/memory-dashboard`). This is a fork of [lukaszraczylo/claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic)
(MIT); its releases are at https://github.com/hlgr360/claude-mnemonic.

## What this plugin does

- Hooks save what happens in a session and load the project's memory at the start, and the MCP server gives Claude the
  search and related tools.
- `/memory-dashboard` opens the dashboard and `/memory-restart` restarts the local worker.
- `/memory-statusline` turns on the Claude Mnemonic status line (`[mnemonic] ● served:42 | project:28 memories`, the tag
  links to the dashboard). A plugin cannot set a status line itself, so this asks first and never replaces yours silently.
- **The memory is stored on your computer** (`~/.claude-mnemonic`), by a local worker that the hooks and the MCP server
  start when needed.
- **The plugin carries no binaries.** On first use it downloads the binaries of its own version from the release,
  checks them against the release's checksums (and against the cosign signature when cosign is installed), and
  installs them in `~/.claude-mnemonic/bin`. Binaries from `make install` are never replaced. Supported: macOS on
  Apple silicon and Linux on x86-64. The first session may run without memory until the download has finished.
- Do not use it next to another install of claude-mnemonic (`make install`, upstream's plugin): they register the same
  hooks.

## Claude Desktop is a separate install

This plugin is for Claude Code. Claude Desktop does not give a plugin's tools to chat, and Cowork starts them where they
cannot reach the worker, so chat and Cowork get the memory tools from the **Desktop extension**,
`claude-mnemonic-desktop_<version>.mcpb` on the same release (Settings > Extensions > Install Extension), plus one
instruction pasted into your preferences. Both talk to the same local worker. See the project's README and DESKTOP.md.
