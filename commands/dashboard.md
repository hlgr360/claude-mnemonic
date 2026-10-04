---
description: Open the Claude Mnemonic web dashboard in your browser
allowed-tools: Bash(curl:*), Bash(open:*), Bash(xdg-open:*), Bash(grep:*), Bash(echo:*)
---

# Open the Claude Mnemonic dashboard

The dashboard shows your saved notes, session summaries, the knowledge graph, conflicts to review, scopes and your projects. It is served by the local worker, so it is only reachable on this computer.

## Instructions

1. Work out the dashboard address and open it with one command. The worker's port is `CLAUDE_MNEMONIC_WORKER_PORT` from the environment, else from `~/.claude-mnemonic/settings.json`, else 37777:

   ```bash
   port="${CLAUDE_MNEMONIC_WORKER_PORT:-$(grep -o '"CLAUDE_MNEMONIC_WORKER_PORT"[[:space:]]*:[[:space:]]*[0-9]*' ~/.claude-mnemonic/settings.json 2>/dev/null | grep -o '[0-9]*$')}"
   url="http://localhost:${port:-37777}"
   if curl -sf "$url/health" >/dev/null; then (open "$url" 2>/dev/null || xdg-open "$url" 2>/dev/null); echo "$url"; else echo "NOT RUNNING $url"; fi
   ```

2. If the output is a URL, tell the user the dashboard is open and give them the URL as a link, so they can open it again.

3. If the output starts with `NOT RUNNING`, the worker is not running. Tell the user, and suggest `/claude-mnemonic:restart`.

Do not read or summarise the dashboard's contents; it is for the user to look at.
