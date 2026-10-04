#!/usr/bin/env python3
"""Check a plugin tree against the limits Claude's upload form enforces, which `claude plugin validate` does not.

Usage: check_plugin_manifest.py <plugin-dir>

Uploading the plugin to a Claude org failed with "Plugin description must be at most 500 characters" while the validator
had accepted a 514-character description. The limits below are the ones that have been reported; add a line when the
form rejects something else, so the build catches it before an upload does.
"""
import json
import os
import sys

# (field in plugin.json, maximum characters, the message the upload form gives)
LIMITS = (("description", 500, "Plugin description must be at most 500 characters"),)


def check(plugin_dir):
    """The problems found, as readable lines (empty when the tree is within the limits)."""
    path = os.path.join(plugin_dir, ".claude-plugin", "plugin.json")
    with open(path, encoding="utf-8") as f:
        manifest = json.load(f)
    problems = []
    for field, maximum, message in LIMITS:
        value = manifest.get(field)
        if isinstance(value, str) and len(value) > maximum:
            problems.append(f"{field} is {len(value)} characters, {len(value) - maximum} over the limit of {maximum} ({message})")
    return problems


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    found = check(sys.argv[1])
    if found:
        print("The plugin would be rejected by the upload form:\n  " + "\n  ".join(found), file=sys.stderr)
        sys.exit(1)
