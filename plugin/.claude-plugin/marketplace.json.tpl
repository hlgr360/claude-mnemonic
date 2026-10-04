{
  "$schema": "https://anthropic.com/claude-code/marketplace.schema.json",
  "name": "claude-mnemonic",
  "version": "{{ .Version }}",
  "description": "Persistent memory system for Claude Code - stores observations, session summaries, and user prompts with semantic search",
  "owner": {
    "name": "lukaszraczylo",
    "email": "lukaszraczylo@users.noreply.github.com"
  },
  "plugins": [
    {
      "name": "claude-mnemonic",
      "description": "Persistent memory for Claude Code and Claude Desktop, with a local worker (SQLite and embeddings) and a web dashboard",
      "version": "{{ .Version }}",
      "author": {
        "name": "lukaszraczylo"
      },
      "source": "./",
      "category": "productivity"
    }
  ]
}
