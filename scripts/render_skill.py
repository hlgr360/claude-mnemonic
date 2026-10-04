#!/usr/bin/env python3
"""Render the plugin's memory skill (SKILL.md) from the instruction text that is pasted into Claude Desktop.

Usage: render_skill.py <output-file>

The instruction (scripts/desktop-instructions.txt) is the one source: the person pastes it into Claude Desktop, and
the plugin carries the same words as a skill, so the two cannot drift apart. A skill's name and description are what
the model sees up front; its body is read when the model decides the skill applies.
"""
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))

NAME = "project-memory"

# The description is what the model reads in every conversation, so it carries the trigger: the user's own earlier
# work, and the words "memory" and "remember" that otherwise select the built-in memory.
DESCRIPTION = (
    "The user's persistent memory of their own project work (decisions, findings and fixes from earlier sessions, "
    "shared with Claude Code), kept in the claude-mnemonic connector. Use it, in addition to any built-in memory, "
    "whenever the user asks about their past work, earlier decisions or project history, or says \"memory\" or "
    "\"remember\" about their projects. Not for general questions that do not refer to their own earlier work."
)

# The skill is background knowledge for the model, not something a person runs: `user-invocable: false` keeps it out of the
# `/` menu (so the plugin's slash commands are only /claude-mnemonic:dashboard and :restart) and keeps its description in
# the model's context, which is the part that matters.

# In Claude Code the hooks already load the project's context and capture what happens, and the tools below that
# belong to Claude Desktop (project_suggest, catch_up, checkpoint, ...) are not offered, so the skill steps aside.
CLAUDE_CODE_NOTE = (
    "If the claude-mnemonic connector has no project_suggest tool, you are in Claude Code: its hooks already load "
    "this project's memory and save what happens, so do not follow the steps below; use the connector's search "
    "tool when the user asks about earlier work.\n\n"
)


def load_installer():
    spec = importlib.util.spec_from_file_location("install_desktop", os.path.join(HERE, "install-desktop.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def render():
    body = load_installer().render_instructions()
    return f"---\nname: {NAME}\nuser-invocable: false\ndescription: {json.dumps(DESCRIPTION)}\n---\n\n# Using the claude-mnemonic memory\n\n{CLAUDE_CODE_NOTE}{body}"


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    os.makedirs(os.path.dirname(os.path.abspath(sys.argv[1])), exist_ok=True)
    with open(sys.argv[1], "w", encoding="utf-8") as f:
        f.write(render())
