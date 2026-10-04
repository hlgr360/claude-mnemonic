{
  "name": "claude-mnemonic",
  "version": "{{ .Version }}",
  "description": "Persistent memory for Claude Code and Claude Desktop: the decisions, findings and fixes from your earlier sessions, searchable and shared by both, with a local web dashboard. A local worker (SQLite and embeddings) stores it on your computer; the binaries are downloaded from this fork's release on first use and verified. In Claude Desktop chat, paste the instruction from README.md into your personal preferences once, or chat answers from its built-in memory instead. Fork of lukaszraczylo/claude-mnemonic (MIT).",
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
