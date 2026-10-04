{
  "name": "claude-mnemonic",
  "version": "{{ .Version }}",
  "description": "Persistent memory for Claude Code and Claude Desktop: decisions, findings and fixes from earlier sessions, searchable and shared by both, with a local web dashboard. Binaries are downloaded and verified from this fork's release on first use. Desktop chat needs the instruction from README.md pasted into your preferences once. Fork of lukaszraczylo/claude-mnemonic (MIT).",
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
