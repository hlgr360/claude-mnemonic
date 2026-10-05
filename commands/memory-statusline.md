---
description: Show the Claude Mnemonic status line in Claude Code (a plugin cannot turn it on by itself)
allowed-tools: Bash(sh:*)
---

# Set up the Claude Mnemonic status line

The status line is the bar at the bottom of Claude Code that shows `[mnemonic] ● served:42 | project:28 memories`; the `[mnemonic]` tag links to the dashboard. Claude Code only shows a status line that is set in `settings.json`, and a plugin cannot set it, so this command does it, with your say-so.

## Instructions

1. Look at the current state:

   ```bash
   sh "${CLAUDE_PLUGIN_ROOT}/lib/statusline.sh" status
   ```

   If that file does not exist, tell the user this install sets the status line up another way and stop.

2. Act on the first word of the output:
   - `NONE`: no status line is set. Run `sh "${CLAUDE_PLUGIN_ROOT}/lib/statusline.sh" enable`.
   - `OURS`: it is already the Claude Mnemonic status line. Say so and stop.
   - `OTHER <command>`: the user already has a status line, shown after `OTHER`. Show it, say that enabling this one replaces it (the old `settings.json` is kept as `settings.json.mnemonic-backup`), and ask whether to replace it. Only if they say yes, run `sh "${CLAUDE_PLUGIN_ROOT}/lib/statusline.sh" enable --replace`.

3. If the output is `ENABLED ...`, tell the user the status line is on (it may need a new session to appear). If the script says the status line binary is not installed yet, tell them to start a new Claude Code session once (the plugin installs it) and run this command again.

4. If the user asks to turn it off, run `sh "${CLAUDE_PLUGIN_ROOT}/lib/statusline.sh" disable`; it only removes the Claude Mnemonic one.

Do not edit `settings.json` yourself; use the script.
