---
description: Restart the Claude Mnemonic worker process
allowed-tools: Bash(curl:*), Bash(grep:*), Bash(echo:*), Bash(sleep:*)
---

# Restart Claude Mnemonic Worker

Restart the claude-mnemonic worker process. Use this command when experiencing issues with the memory system.

## Instructions

1. **If you have the claude-mnemonic `restart` tool** (Claude Desktop chat and Cowork), call it and report what it says. Do not use a shell for this: in those apps the shell runs in a sandbox that cannot reach the worker on this computer, so the commands below would fail even though the worker is fine.

2. Otherwise (Claude Code) restart it with one command. The worker's port is `CLAUDE_MNEMONIC_WORKER_PORT` from the environment, else from `~/.claude-mnemonic/settings.json`, else 37777:

   ```bash
   port="${CLAUDE_MNEMONIC_WORKER_PORT:-$(grep -o '"CLAUDE_MNEMONIC_WORKER_PORT"[[:space:]]*:[[:space:]]*[0-9]*' ~/.claude-mnemonic/settings.json 2>/dev/null | grep -o '[0-9]*$')}"
   url="http://127.0.0.1:${port:-37777}"
   v=""
   if curl -sf -X POST "$url/api/restart" >/dev/null; then
     sleep 2
     for i in 1 2 3 4 5 6 7 8 9 10; do v=$(curl -sf "$url/api/version") && break; sleep 1; done
     if [ -n "$v" ]; then echo "RESTARTED $v"; else echo "NOT BACK $url"; fi
   else
     echo "NOT RUNNING $url"
   fi
   ```

3. Report the result to the user. `RESTARTED` is followed by the worker's version. `NOT RUNNING` means the worker did not answer (nothing to restart: starting a new Claude Code session starts it). `NOT BACK` means it was asked to restart and has not come back within about 12 seconds.

If the restart fails, suggest the user check `/tmp/claude-mnemonic-worker.log` for errors.
