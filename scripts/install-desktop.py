#!/usr/bin/env python3
"""Add claude-mnemonic to (or remove it from) Claude Desktop's MCP server configuration.

Claude Desktop reads claude_desktop_config.json once at startup. This script edits only the
one server entry it owns: every other byte of the file, including your other servers and their
formatting, is left exactly as it was. The edited text is checked structurally before anything is
written, a timestamped backup is taken first, and --dry-run shows the change without making it.

    install-desktop.py                 add or update the entry
    install-desktop.py uninstall       remove it
    install-desktop.py status          show what is configured
    install-desktop.py --dry-run       show the diff only
    install-desktop.py instructions    print the instruction to paste into Claude Desktop (--copy: to the clipboard)

Standard library only (Python 3.8+).
"""
import argparse
import copy
import difflib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

DEFAULT_NAME = "claude-mnemonic"
INSTRUCTIONS_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "desktop-instructions.txt")


# --------------------------------------------------------------------------- locations

def default_config_path():
    home = os.path.expanduser("~")
    if sys.platform == "darwin":
        return os.path.join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
    if sys.platform.startswith("win"):
        return os.path.join(os.environ.get("APPDATA", os.path.join(home, "AppData", "Roaming")), "Claude", "claude_desktop_config.json")
    return os.path.join(os.environ.get("XDG_CONFIG_HOME", os.path.join(home, ".config")), "Claude", "claude_desktop_config.json")


def default_binary_path():
    exe = "mcp-server.exe" if sys.platform.startswith("win") else "mcp-server"
    return os.path.join(os.path.expanduser("~"), ".claude-mnemonic", "bin", exe)


# --------------------------------------------------------------------------- minimal JSON scanner
# Just enough of a scanner to find a member's exact text span, so edits can be surgical.

class ConfigError(Exception):
    pass


def _skip_ws(t, i):
    while i < len(t) and t[i] in " \t\r\n":
        i += 1
    return i


def _skip_string(t, i):
    i += 1
    while t[i] != '"':
        i += 2 if t[i] == "\\" else 1
    return i + 1


def _skip_value(t, i):
    c = t[i]
    if c == '"':
        return _skip_string(t, i)
    if c in "{[":
        depth = 0
        while True:
            ch = t[i]
            if ch == '"':
                i = _skip_string(t, i)
                continue
            if ch in "{[":
                depth += 1
            elif ch in "}]":
                depth -= 1
                if depth == 0:
                    return i + 1
            i += 1
    while i < len(t) and t[i] not in ",}] \t\r\n":
        i += 1
    return i


def _members(t, obj_start):
    """Yield (key, key_start, value_start, value_end) for each member of the object at obj_start."""
    i = obj_start + 1
    while True:
        i = _skip_ws(t, i)
        if t[i] == "}":
            return
        if t[i] == ",":
            i += 1
            continue
        key_start = i
        key_end = _skip_string(t, i)
        key = json.loads(t[key_start:key_end])
        i = _skip_ws(t, key_end)
        i = _skip_ws(t, i + 1)  # past ':'
        value_end = _skip_value(t, i)
        yield key, key_start, i, value_end
        i = value_end


def _find(t, obj_start, key):
    for k, ks, vs, ve in _members(t, obj_start):
        if k == key:
            return ks, vs, ve
    return None


def _root(t):
    i = _skip_ws(t, 0)
    if i >= len(t) or t[i] != "{":
        raise ConfigError("the configuration is not a JSON object")
    return i


def _indent_unit(t):
    """The file's indentation unit: a tab, or the width of its first indented line (default two spaces)."""
    for line in t.splitlines():
        stripped = line.lstrip(" \t")
        if stripped and len(stripped) < len(line):
            lead = line[: len(line) - len(stripped)]
            return "\t" if lead.startswith("\t") else lead
    return "  "


def _line_indent(t, pos):
    start = t.rfind("\n", 0, pos) + 1
    return t[start:pos] if t[start:pos].strip() == "" else ""


def _render(value, indent, unit, nl):
    """Pretty-print value so that it can be placed after a key that sits at `indent`."""
    text = json.dumps(value, indent=unit, ensure_ascii=False)
    return text.replace("\n", nl + indent)


def _newline(t):
    return "\r\n" if "\r\n" in t else "\n"


# --------------------------------------------------------------------------- edits

def set_server(text, name, entry):
    """Return text with mcpServers[name] set to entry, changing nothing else."""
    root = _root(text)
    unit, nl = _indent_unit(text), _newline(text)

    servers = _find(text, root, "mcpServers")
    if servers is None:
        member = f"{json.dumps('mcpServers')}: {_render({name: entry}, unit, unit, nl)}"
        return _insert_member(text, root, member, nl, unit)

    key_start, vs, _ = servers
    if text[vs] != "{":
        raise ConfigError('"mcpServers" is not a JSON object')

    existing = _find(text, vs, name)
    if existing is not None:
        existing_key, evs, eve = existing
        indent = _line_indent(text, existing_key)
        return text[:evs] + _render(entry, indent, unit, nl) + text[eve:]

    first = next(_members(text, vs), None)
    inner = _line_indent(text, first[1]) if first else _line_indent(text, key_start) + unit
    member = f"{json.dumps(name)}: {_render(entry, inner, unit, nl)}"
    return _insert_member(text, vs, member, nl, inner)


def _insert_member(text, obj_start, member, nl, inner):
    """Insert `member`, indented by `inner`, as the first member of the object at obj_start."""
    after = obj_start + 1
    first = next(_members(text, obj_start), None)
    if first is not None:
        # In a compact file the members share the brace's line: keep the new one there too.
        line_start = text.rfind("\n", 0, first[1]) + 1
        inline = text[line_start:first[1]].strip() != ""
        lead = "" if inline else nl + inner
        return text[:after] + lead + member + "," + text[after:]
    # empty object: put the member on its own line and keep the closing brace where it was
    closing = _skip_ws(text, after)
    return text[:after] + nl + inner + member + nl + _line_indent(text, obj_start) + text[closing:]


def remove_server(text, name):
    """Return text with mcpServers[name] removed, changing nothing else."""
    root = _root(text)
    servers = _find(text, root, "mcpServers")
    if servers is None or text[servers[1]] != "{":
        return text
    found = _find(text, servers[1], name)
    if found is None:
        return text
    key_start, _, value_end = found

    line_start = text.rfind("\n", 0, key_start) + 1
    own_line = text[line_start:key_start].strip() == ""
    start = line_start if own_line else key_start

    def through_end_of_line(pos):
        """Extend pos over the rest of its line when that holds nothing else."""
        eol = text.find("\n", pos)
        if own_line and eol != -1 and text[pos:eol].strip() == "":
            return eol + 1
        return pos

    after = _skip_ws(text, value_end)
    if after < len(text) and text[after] == ",":
        return text[:start] + text[through_end_of_line(after + 1):]

    # no trailing comma, so this is the last member: remove the comma that precedes it instead
    j = start
    while j > 0 and text[j - 1] in " \t\r\n":
        j -= 1
    if j > 0 and text[j - 1] == ",":
        return text[: j - 1] + text[value_end:]
    return text[:start] + text[through_end_of_line(value_end):]  # the only member


# --------------------------------------------------------------------------- verification

def _expected(original, name, entry):
    data = json.loads(original) if original.strip() else {}
    out = copy.deepcopy(data)
    if entry is None:
        if isinstance(out.get("mcpServers"), dict):
            out["mcpServers"].pop(name, None)
    else:
        out.setdefault("mcpServers", {})[name] = entry
    return out


def verify(original, edited, name, entry):
    """Refuse to proceed unless the edit changed exactly the one server entry and nothing else."""
    try:
        got = json.loads(edited)
    except ValueError as err:
        raise ConfigError(f"internal error: the edited configuration is not valid JSON ({err}); nothing was written")
    if got != _expected(original, name, entry):
        raise ConfigError("internal error: the edit would change more than the claude-mnemonic entry; nothing was written")


# --------------------------------------------------------------------------- the instruction for Claude Desktop

def render_instructions(name=DEFAULT_NAME, template=None):
    """The instruction to paste into Claude Desktop, with the connector's name filled in.

    It lives in the person's claude.ai account, not in any local file, so it cannot be written
    by this installer; it can only be handed over (printed, or copied to the clipboard).
    """
    if template is None:
        with open(INSTRUCTIONS_FILE, encoding="utf-8") as f:
            template = f.read()
    connector = name if name == DEFAULT_NAME else f'"{name}" (claude-mnemonic)'
    return template.replace("{connector}", connector).replace("{name}", name)


def clipboard_command():
    """The first clipboard tool this machine has, as an argv list, or None."""
    candidates = [["pbcopy"], ["clip"], ["wl-copy"], ["xclip", "-selection", "clipboard"], ["xsel", "--clipboard", "--input"]]
    for argv in candidates:
        if shutil.which(argv[0]):
            return argv
    return None


def copy_to_clipboard(text):
    """Put text on the clipboard. Returns the tool used, or None if there is none or it failed."""
    argv = clipboard_command()
    if argv is None:
        return None
    try:
        subprocess.run(argv, input=text.encode("utf-8"), check=True, timeout=5)
    except (OSError, subprocess.SubprocessError):
        return None
    return argv[0]


def instruction_section(name, copy):
    """What to print after a Desktop install: the instruction itself and where it goes.

    Chat has its own built-in memory and ignores claude-mnemonic for ordinary "memory" wording unless
    told to use it, so this is shown whenever the entry is in place, including when nothing had to change.
    With copy=True it is also put on the clipboard.
    """
    text = render_instructions(name)
    copied = copy_to_clipboard(text) if copy else None
    if copied:
        where = f"It is on your clipboard (copied with {copied}), so just paste it."
    elif copy:
        where = "No clipboard tool was found: select and copy the text below."
    else:
        where = "Copy the text below."
    rule = "-" * 72
    return (
        "\nOne more step, once: Claude Desktop chat has its own built-in memory and ignores claude-mnemonic for\n"
        "ordinary \"memory\" wording unless it is told to use it. Paste this instruction into Claude Desktop\n"
        "(Settings, in the field for personal preferences or custom instructions; it lives in your account, so it\n"
        f"cannot be added automatically). {where}\n"
        f"{rule}\n{text.rstrip(chr(10))}\n{rule}\n"
        "Show it again any time with: python3 scripts/install-desktop.py instructions --copy\n"
    )


# --------------------------------------------------------------------------- actions

def build_entry(binary, args):
    return {"command": binary, "args": list(args)}


def plan(original, name, entry, uninstall):
    """Compute the edited text. An empty or missing file starts from {}."""
    base = original if original.strip() else "{}\n"
    edited = remove_server(base, name) if uninstall else set_server(base, name, entry)
    verify(base, edited, name, None if uninstall else entry)
    if not original.strip() and not uninstall:
        return edited if edited.endswith("\n") else edited + "\n"
    return edited


def write_atomic(path, text):
    directory = os.path.dirname(path) or "."
    os.makedirs(directory, exist_ok=True)
    mode = os.stat(path).st_mode & 0o777 if os.path.exists(path) else 0o600
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=".claude_desktop_config.", suffix=".tmp")
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="") as f:
            f.write(text)
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise


def unique_backup_path(path):
    """A timestamped backup name that never overwrites an earlier backup, even within one second."""
    base = f"{path}.bak-{time.strftime('%Y%m%d-%H%M%S')}"
    candidate, n = base, 1
    while os.path.exists(candidate):
        n += 1
        candidate = f"{base}-{n}"
    return candidate


def read_text(path):
    if not os.path.exists(path):
        return ""
    with open(path, encoding="utf-8", newline="") as f:
        return f.read()


def run(opts, out=None):
    out = out or sys.stdout  # looked up per call so callers can redirect it
    if opts.action == "instructions":
        text = render_instructions(opts.name)
        print(text, end="", file=out)
        if opts.copy:
            tool = copy_to_clipboard(text)
            print(f"\n(copied to the clipboard with {tool})" if tool else "\n(could not copy: no clipboard tool found; select and copy the text above)", file=out)
        return 0
    path = opts.config or default_config_path()
    name = opts.name
    original = read_text(path)
    existing = None
    if original.strip():
        try:
            data = json.loads(original)
        except ValueError as err:
            raise ConfigError(f"{path} is not valid JSON ({err}); fix or move it and run again")
        if not isinstance(data, dict):
            raise ConfigError(f"{path} does not contain a JSON object")
        servers = data.get("mcpServers", {})
        if not isinstance(servers, dict):
            raise ConfigError('"mcpServers" in the configuration is not an object')
        existing = servers.get(name)

    if opts.action == "status":
        print(f"config:  {path}{'' if os.path.exists(path) else '  (does not exist yet)'}", file=out)
        print(f"entry:   {'configured' if existing is not None else 'not configured'} as '{name}'", file=out)
        if existing is not None:
            print(f"command: {existing.get('command')}", file=out)
            print(f"args:    {existing.get('args', [])}", file=out)
            binary = existing.get("command")
            if binary and not os.path.exists(binary):
                print("warning: that binary does not exist", file=out)
        return 0

    uninstall = opts.action == "uninstall"
    args = []
    if opts.project:
        args += ["--project", opts.project]
    if opts.mode and opts.mode != "auto":
        args += ["--mode", opts.mode]
    binary = opts.binary or default_binary_path()
    if not uninstall and not opts.force and not os.path.exists(binary):
        raise ConfigError(f"{binary} does not exist. Run 'make install' first, or pass --binary PATH (or --force to write the entry anyway)")
    entry = build_entry(binary, args)

    if uninstall and existing is None:
        print(f"Nothing to do: '{name}' is not configured in {path}.", file=out)
        return 0
    if not uninstall and existing == entry:
        print(f"Already up to date: '{name}' is configured in {path}.", file=out)
        if not opts.dry_run:
            print(instruction_section(name, not opts.no_copy), end="", file=out)
        return 0

    edited = plan(original, name, entry, uninstall)
    diff = "".join(difflib.unified_diff(original.splitlines(True), edited.splitlines(True), "current", "new"))

    if opts.dry_run:
        print(f"Dry run: {'would remove' if uninstall else 'would write'} '{name}' in {path}\n", file=out)
        print(diff or "(no textual change)", file=out)
        return 0

    backup = None
    if os.path.exists(path):
        backup = unique_backup_path(path)
        shutil.copy2(path, backup)
    write_atomic(path, edited)

    verb = "Removed" if uninstall else ("Updated" if existing is not None else "Added")
    print(f"{verb} '{name}' in {path}", file=out)
    if backup:
        print(f"Backup:  {backup}", file=out)
    if not uninstall:
        print(f"Command: {binary} {' '.join(args)}".rstrip(), file=out)
    print("\nQuit Claude Desktop completely (Cmd-Q / File > Exit) and reopen it; it reads this file only at startup.", file=out)
    if not uninstall:
        print(instruction_section(name, not opts.no_copy), end="", file=out)
    return 0


def parse(argv):
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("action", nargs="?", default="install", choices=["install", "uninstall", "status", "instructions"])
    p.add_argument("--config", help="path to claude_desktop_config.json (default: the standard location for this OS)")
    p.add_argument("--binary", help="path to the mcp-server binary (default: ~/.claude-mnemonic/bin/mcp-server)")
    p.add_argument("--name", default=DEFAULT_NAME, help=f"server name in the configuration (default: {DEFAULT_NAME})")
    p.add_argument("--project", help="pin one project id instead of letting the model choose per conversation")
    p.add_argument("--mode", choices=["auto", "code", "desktop"], default="auto", help="project mode (default: auto, detected from the client)")
    p.add_argument("--dry-run", action="store_true", help="show the change without making it")
    p.add_argument("--force", action="store_true", help="write the entry even if the binary does not exist")
    p.add_argument("--copy", action="store_true", help="with 'instructions': also copy the text to the clipboard")
    p.add_argument("--no-copy", action="store_true", help="install: show the instruction but do not copy it to the clipboard")
    return p.parse_args(argv)


def main(argv=None):
    try:
        return run(parse(argv))
    except ConfigError as err:
        print(f"error: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
