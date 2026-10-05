{
  "manifest_version": "0.3",
  "name": "claude-mnemonic",
  "display_name": "Claude Mnemonic",
  "version": "{{ .Version }}",
  "description": "Persistent memory for Claude Desktop chat and Cowork, shared with Claude Code through one local memory service on this computer.",
  "long_description": "Gives Claude Desktop (chat and Cowork) the claude-mnemonic memory tools: search, projects, remember, checkpoint, catch_up, dashboard and restart. The tools run on this computer and talk to the local claude-mnemonic worker, which also serves Claude Code. On first use the binaries of this release are downloaded and verified (checksums, and the cosign signature when cosign is installed). Chat has a memory of its own, so paste the instruction from the project's README into your personal preferences once. Fork of lukaszraczylo/claude-mnemonic (MIT).",
  "author": {
    "name": "hlgr360",
    "url": "https://github.com/hlgr360"
  },
  "repository": {
    "type": "git",
    "url": "https://github.com/hlgr360/claude-mnemonic.git"
  },
  "homepage": "https://github.com/hlgr360/claude-mnemonic",
  "documentation": "https://github.com/hlgr360/claude-mnemonic/blob/main/DESKTOP.md",
  "support": "https://github.com/hlgr360/claude-mnemonic/issues",
  "license": "MIT",
  "keywords": ["memory", "persistence", "search", "context"],
  "server": {
    "type": "binary",
    "entry_point": "server/mcp-server",
    "mcp_config": {
      "command": "/bin/sh",
      "args": ["${__dirname}/server/mcp-server"],
      "env": {}
    }
  },
  "tools_generated": true,
  "compatibility": {
    "claude_desktop": ">=0.8.0",
    "platforms": ["darwin"]
  }
}
