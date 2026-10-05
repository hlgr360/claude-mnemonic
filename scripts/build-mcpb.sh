#!/bin/bash
# Build the Claude Desktop extension (.mcpb): the same thin wrapper and downloader as the plugin, packed the way
# Desktop's Settings > Extensions installs it. It gives Desktop's chat and Cowork the memory tools (Desktop does not
# attach a plugin's local MCP server to chat, and Cowork starts it inside its VM).
#
# Usage: scripts/build-mcpb.sh <version>      (e.g. 0.21.95.3, 0.0.0-ci.15; with or without the leading v)
#   DIST=dist            where the tree (dist/desktop-extension) and the file go
#   MNEMONIC_REPO=o/n    release repository the binaries are downloaded from (default: this fork's)
#   SKIP_VALIDATE=1      do not run `mcpb validate` (it needs node/npx and the network)
#
# The manifest's version must be semver, and a fork release is the upstream version plus a fork number: 0.21.95.3 becomes
# 0.21.95-fork.3 (0.21.95.1-rc1 becomes 0.21.95-fork.1.rc1, and 0.0.0-ci.15 stays). That increases with every release (what an organization's "Upload new version" needs), and a new
# upstream version (0.21.96-fork.1) is higher than every fork release of the one before. The downloader reads the plain
# release version from server/.claude-plugin/plugin.json, so the extension fetches the binaries of its own release.
#
# Output: dist/claude-mnemonic-desktop_<version>.mcpb (a deterministic zip). Install: Settings > Extensions >
# Install Extension, or drag the file onto Claude Desktop.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
    echo "Usage: $0 <version>" >&2
    exit 1
fi
VERSION="${VERSION#v}"
# X.Y.Z, X.Y.Z.N (a fork release), and either with a pre-release suffix (0.0.0-ci.15 on pull requests, 0.21.95.1-rc1).
if [[ ! "$VERSION" =~ ^([0-9]+\.[0-9]+\.[0-9]+)(\.([0-9]+))?(-([0-9A-Za-z.-]+))?$ ]]; then
    echo "build-mcpb: $VERSION is not a release version (X.Y.Z or X.Y.Z.N, optionally with a -suffix)" >&2
    exit 1
fi
BASE="${BASH_REMATCH[1]}" FORK="${BASH_REMATCH[3]}" PRE="${BASH_REMATCH[5]}"
MANIFEST_VERSION="$BASE"
if [[ -n "$FORK" ]]; then MANIFEST_VERSION="${MANIFEST_VERSION}-fork.${FORK}${PRE:+.${PRE}}"; elif [[ -n "$PRE" ]]; then MANIFEST_VERSION="${MANIFEST_VERSION}-${PRE}"; fi

DIST="${DIST:-dist}"
TREE="${DIST}/desktop-extension"
FILE="${DIST}/claude-mnemonic-desktop_${VERSION}.mcpb"

echo "==> Assembling the Desktop extension ${VERSION} (manifest version ${MANIFEST_VERSION}) in ${TREE}"
rm -rf "$TREE"
mkdir -p "$TREE/server/lib" "$TREE/server/.claude-plugin"

sed "s/{{ .Version }}/${MANIFEST_VERSION}/g; s/{{.Version}}/${MANIFEST_VERSION}/g" desktop-extension/manifest.json.tpl >"$TREE/manifest.json"
cp mcp-server "$TREE/server/mcp-server"
chmod 755 "$TREE/server/mcp-server"
cp plugin/lib/ensure-binaries.sh "$TREE/server/lib/ensure-binaries.sh"
chmod 755 "$TREE/server/lib/ensure-binaries.sh"
# The downloader takes the release to fetch from the plugin.json next to the server (name and version are all it reads).
printf '{"name": "claude-mnemonic", "version": "%s"}\n' "$VERSION" >"$TREE/server/.claude-plugin/plugin.json"
cp LICENSE "$TREE/LICENSE"

# A fork of the fork downloads from its own releases, and links to them.
if [[ -n "${MNEMONIC_REPO:-}" ]]; then
    sed -i.bak "s|^DEFAULT_REPO=.*|DEFAULT_REPO=\"${MNEMONIC_REPO}\"|" "$TREE/server/lib/ensure-binaries.sh"
    sed -i.bak "s|github.com/hlgr360/claude-mnemonic|github.com/${MNEMONIC_REPO}|g" "$TREE/manifest.json"
    rm -f "$TREE/server/lib/ensure-binaries.sh.bak" "$TREE/manifest.json.bak"
fi

if [[ "${SKIP_VALIDATE:-}" != "1" ]]; then
    command -v npx >/dev/null 2>&1 || { echo "build-mcpb: npx is needed to validate the manifest (SKIP_VALIDATE=1 to skip)" >&2; exit 1; }
    echo "==> Validating the manifest (mcpb validate)"
    (cd "$TREE" && npx --yes @anthropic-ai/mcpb validate manifest.json)
fi

PYTHON="$(command -v python3 || command -v python)"
mkdir -p "$DIST"
rm -f "$FILE"
"$PYTHON" scripts/package_release.py "$TREE" "$FILE"
if command -v sha256sum >/dev/null 2>&1; then SUM="$(sha256sum "$FILE" | cut -d ' ' -f 1)"; else SUM="$(shasum -a 256 "$FILE" | cut -d ' ' -f 1)"; fi
echo "==> ${FILE} ($(du -h "$FILE" | cut -f1))"
echo "    sha256 ${SUM}"
