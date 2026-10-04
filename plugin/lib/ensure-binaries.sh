#!/bin/sh
# Fetch, verify and install the claude-mnemonic binaries that belong to this plugin version into
# ~/.claude-mnemonic/bin, the stable location the hooks and the worker already look in first.
#
# Usage: ensure-binaries.sh [--background]
#   --background  do the work detached and return at once (hooks must not wait for a download)
#
# The plugin carries no binaries: it is one platform-independent zip. Its version (.claude-plugin/plugin.json) is the
# release whose archive is fetched: <release base>/claude-mnemonic_<version>_<os>_<arch>.tar.gz. The archive is checked
# against the release's checksums.txt, and against its cosign signature when cosign is installed (the same check the
# in-app updater makes). A binary that fails a check is never installed.
#
# When to install:
#   - the worker or the MCP server is missing                     -> install
#   - no marker (binaries from `make install` or install.sh)      -> leave them alone
#   - marker older than the plugin version                        -> install (the plugin is the floor)
#   - marker equal or newer (the in-app updater moved on)        -> leave them alone
#
# Environment (for forks and tests):
#   MNEMONIC_REPO          release repository, default below
#   MNEMONIC_RELEASE_BASE  base URL of the release's files, default https://github.com/<repo>/releases/download/v<version>
set -u

DEFAULT_REPO="hlgr360/claude-mnemonic"
REPO="${MNEMONIC_REPO:-$DEFAULT_REPO}"

PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MANIFEST="$PLUGIN_ROOT/.claude-plugin/plugin.json"
DATA="$HOME/.claude-mnemonic"
BIN="$DATA/bin"
MARKER="$BIN/.plugin-version"
LOCK="$DATA/.install.lock"
LOG="$DATA/plugin-install.log"

say() { echo "claude-mnemonic: $*" >&2; }

VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$MANIFEST" 2>/dev/null | head -n 1)"
if [ -z "$VERSION" ]; then
    say "cannot read the plugin version from $MANIFEST"
    exit 1
fi

# true when $1 is a higher dotted version than $2. Up to four numeric parts: the upstream version and this fork's number
# (0.21.95.10 is higher than 0.21.95.9, and 0.21.95.1 is higher than 0.21.95).
version_gt() {
    [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -t. -k1,1n -k2,2n -k3,3n -k4,4n | tail -n 1)" = "$1" ]
}

needs_install() {
    [ -x "$BIN/worker" ] && [ -x "$BIN/mcp-server" ] || return 0
    [ -f "$MARKER" ] || return 1
    installed="$(head -n 1 "$MARKER")"
    version_gt "$VERSION" "$installed"
}

if [ "${1:-}" = "--background" ]; then
    needs_install || exit 0
    mkdir -p "$DATA"
    nohup sh "$0" </dev/null >>"$LOG" 2>&1 &
    exit 0
fi

needs_install || exit 0

case "$(uname -s)-$(uname -m)" in
    Darwin-arm64) PLATFORM="darwin_arm64" ;;
    Linux-x86_64) PLATFORM="linux_amd64" ;;
    *)
        say "no release build for $(uname -s) $(uname -m); use the install script from $REPO instead"
        exit 1
        ;;
esac

ARCHIVE="claude-mnemonic_${VERSION}_${PLATFORM}.tar.gz"
BASE="${MNEMONIC_RELEASE_BASE:-https://github.com/${REPO}/releases/download/v${VERSION}}"

# One installer at a time: the session-start hook and the MCP server both start on a first run. The lock is a
# directory (mkdir is atomic); a lock older than ten minutes is a crashed installer's and is taken over.
mkdir -p "$DATA"
waited=0
while ! mkdir "$LOCK" 2>/dev/null; do
    if [ -n "$(find "$LOCK" -maxdepth 0 -mmin +10 2>/dev/null)" ]; then
        rmdir "$LOCK" 2>/dev/null
        continue
    fi
    waited=$((waited + 1))
    if [ "$waited" -gt 120 ]; then
        say "another installation did not finish in two minutes"
        exit 1
    fi
    sleep 1
done
TMP=""
cleanup() {
    [ -n "$TMP" ] && rm -rf "$TMP"
    rmdir "$LOCK" 2>/dev/null
}
trap cleanup EXIT INT TERM

# The installer that held the lock may have done the work.
needs_install || exit 0

fail() {
    say "$*"
    exit 1
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d ' ' -f 1
    else
        shasum -a 256 "$1" | cut -d ' ' -f 1
    fi
}

TMP="$(mktemp -d "$DATA/.install.XXXXXX")" || fail "cannot create a temporary directory in $DATA"

say "installing ${VERSION} (${PLATFORM}) from ${BASE}"
curl -fsSL --retry 2 -o "$TMP/$ARCHIVE" "$BASE/$ARCHIVE" || fail "download of $ARCHIVE failed"
curl -fsSL --retry 2 -o "$TMP/checksums.txt" "$BASE/checksums.txt" || fail "download of checksums.txt failed"

expected="$(awk -v f="$ARCHIVE" '$2 == f { print $1 }' "$TMP/checksums.txt")"
[ -n "$expected" ] || fail "checksums.txt has no entry for $ARCHIVE"
actual="$(sha256_of "$TMP/$ARCHIVE")"
[ "$expected" = "$actual" ] || fail "checksum mismatch for $ARCHIVE (expected $expected, got $actual); nothing installed"

if command -v cosign >/dev/null 2>&1; then
    curl -fsSL --retry 2 -o "$TMP/checksums.txt.sigstore.json" "$BASE/checksums.txt.sigstore.json" ||
        fail "download of the signature bundle failed"
    cosign verify-blob \
        --bundle "$TMP/checksums.txt.sigstore.json" \
        --certificate-identity-regexp "^https://github\\.com/${REPO}/.*\$" \
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
        "$TMP/checksums.txt" >/dev/null 2>&1 || fail "signature check failed for checksums.txt; nothing installed"
else
    say "cosign is not installed: checked the checksum only, not the signature"
fi

mkdir -p "$TMP/x"
tar -xzf "$TMP/$ARCHIVE" -C "$TMP/x" || fail "cannot unpack $ARCHIVE"
[ -f "$TMP/x/worker" ] && [ -f "$TMP/x/mcp-server" ] || fail "$ARCHIVE has no worker and mcp-server"

# Replace each file by renaming a copy over it, so a running worker or hook is never half-written.
mkdir -p "$BIN/hooks"
install_file() {
    cp "$1" "$2.new" && chmod 755 "$2.new" && mv -f "$2.new" "$2"
}
install_file "$TMP/x/worker" "$BIN/worker" || fail "cannot install the worker into $BIN"
install_file "$TMP/x/mcp-server" "$BIN/mcp-server" || fail "cannot install the MCP server into $BIN"
for f in "$TMP"/x/hooks/*; do
    [ -f "$f" ] || continue
    case "$f" in *.json) continue ;; esac
    install_file "$f" "$BIN/hooks/$(basename "$f")" || fail "cannot install $(basename "$f") into $BIN/hooks"
done
printf '%s\n' "$VERSION" >"$MARKER.new" && mv -f "$MARKER.new" "$MARKER"
say "installed ${VERSION} into $BIN"
