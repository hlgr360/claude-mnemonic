{
  "name": "claude-mnemonic",
  "version": "{{ .Version }}",
  "description": "Persistent memory for Claude Code: decisions, findings and fixes from earlier sessions, saved and loaded automatically, searchable, with a local web dashboard. Binaries are downloaded and verified from this fork's release on first use. Claude Desktop is a separate install: the Desktop extension on the same release. Fork of lukaszraczylo/claude-mnemonic (MIT).",
  "author": {
    "name": "hlgr360",
    "url": "https://github.com/hlgr360"
  },
  "homepage": "https://github.com/hlgr360/claude-mnemonic",
  "repository": "https://github.com/hlgr360/claude-mnemonic",
  "license": "MIT",
  "keywords": ["memory", "persistence", "search", "context"],
  "mcpServers": {
    "claude-mnemonic": {
      "command": "${CLAUDE_PLUGIN_ROOT}/mcp-server",
      "env": {}
    }
  }
}
