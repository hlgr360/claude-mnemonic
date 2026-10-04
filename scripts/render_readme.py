#!/usr/bin/env python3
"""Render the plugin's README.md from plugin/README.md.tpl, with the Claude Desktop instruction filled in.

Usage: render_readme.py <template> <output-file>

The instruction (scripts/desktop-instructions.txt) is the one source, as for the skill: the person pastes it into
Claude Desktop, and the README shows exactly that text, so the two cannot drift apart.
"""
import os
import sys

import render_skill

PLACEHOLDER = "{{ INSTRUCTION }}"


def render(template):
    if PLACEHOLDER not in template:
        raise ValueError(f"the template has no {PLACEHOLDER}")
    return template.replace(PLACEHOLDER, render_skill.load_installer().render_instructions().rstrip("\n"))


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit(__doc__)
    with open(sys.argv[1], encoding="utf-8") as f:
        text = render(f.read())
    os.makedirs(os.path.dirname(os.path.abspath(sys.argv[2])), exist_ok=True)
    with open(sys.argv[2], "w", encoding="utf-8") as f:
        f.write(text)
