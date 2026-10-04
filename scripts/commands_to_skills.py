#!/usr/bin/env python3
"""Turn slash-command files (commands/<name>.md) into skills (skills/<name>/SKILL.md) for the plugin.

Usage: commands_to_skills.py <commands-dir> <skills-dir>

Claude Code and Claude Desktop treat commands/ as the older format and ask for skills instead. A plugin skill is
/<plugin>:<directory>, so the slash names do not change. Each skill keeps the command's body and front matter and gains
`name` and `disable-model-invocation: true`: these are things the user runs on purpose (restart the worker, open a
browser), and the model must not start them on its own, as before.
"""
import os
import sys


def convert(text, name):
    """The skill file for one command file: front matter with name and disable-model-invocation, then the body."""
    if not text.startswith("---\n"):
        raise ValueError("a command file must start with front matter")
    end = text.find("\n---\n", 4)
    if end < 0:
        raise ValueError("front matter is not closed")
    lines = text[4:end].split("\n")
    for line in lines:
        key = line.split(":", 1)[0].strip()
        if key in ("name", "disable-model-invocation"):
            raise ValueError(f"front matter already sets {key}")
    front = [f"name: {name}", "disable-model-invocation: true", *lines]
    return "---\n" + "\n".join(front) + text[end:]


def main(commands_dir, skills_dir):
    names = sorted(f[:-3] for f in os.listdir(commands_dir) if f.endswith(".md"))
    if not names:
        raise SystemExit(f"no command files in {commands_dir}")
    for name in names:
        with open(os.path.join(commands_dir, name + ".md"), encoding="utf-8") as f:
            skill = convert(f.read(), name)
        target = os.path.join(skills_dir, name)
        os.makedirs(target, exist_ok=True)
        with open(os.path.join(target, "SKILL.md"), "w", encoding="utf-8") as f:
            f.write(skill)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit(__doc__)
    main(sys.argv[1], sys.argv[2])
