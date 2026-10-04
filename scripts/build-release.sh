#!/bin/bash
# Build the release archive for the platform this script runs on.
#
# Usage: scripts/build-release.sh <version>      (version without the leading v, e.g. 1.2.3)
#   DIST=dist      where the archive is written
#   SKIP_UI=1      do not rebuild the dashboard (internal/worker/static must already hold it)
#
# The build uses CGO (SQLite, ONNX), so each platform is built on a machine of that platform: the release workflow runs
# this on a macOS arm64, a Linux amd64 and a Windows amd64 runner. The archive has the layout the updater and the
# install scripts expect, claude-mnemonic_<version>_<os>_<arch>.tar.gz (.zip on Windows):
#   worker, mcp-server, hooks/<hook binaries>, hooks/hooks.json, commands/*.md, .claude-plugin/{plugin,marketplace}.json
#
# It rewrites ui/package.json from its template (as `make dashboard` does) and replaces internal/worker/static; restore
# ui/package.json with `git checkout -- ui/package.json` before committing.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
    echo "Usage: $0 <version>" >&2
    exit 1
fi
VERSION="${VERSION#v}"
DIST="${DIST:-dist}"

GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"
case "${GOOS}-${GOARCH}" in
    darwin-arm64|linux-amd64|windows-amd64) ;;
    *) echo "Unsupported build platform: ${GOOS}-${GOARCH} (releases cover darwin-arm64, linux-amd64, windows-amd64)" >&2; exit 1 ;;
esac
EXE=""
ARCHIVE_EXT="tar.gz"
if [[ "$GOOS" == "windows" ]]; then
    EXE=".exe"
    ARCHIVE_EXT="zip"
fi

ARCHIVE="${DIST}/claude-mnemonic_${VERSION}_${GOOS}_${GOARCH}.${ARCHIVE_EXT}"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

echo "==> Building claude-mnemonic ${VERSION} for ${GOOS}-${GOARCH}"

# ONNX runtime libraries for this platform (embedded into the binaries); a no-op when they are already there.
bash scripts/download-onnx-libs.sh "${GOOS}-${GOARCH}"

# The dashboard, embedded into the worker.
if [[ "${SKIP_UI:-}" != "1" ]]; then
    echo "==> Building the dashboard"
    sed "s/{{ .Version }}/${VERSION}/g" ui/package.json.tpl > ui/package.json
    (cd ui && npm ci --silent && npm run build)
    rm -rf internal/worker/static
    mkdir -p internal/worker/static
    cp -r ui/dist/* internal/worker/static/
fi
if [[ ! -f internal/worker/static/index.html ]]; then
    echo "internal/worker/static has no dashboard (index.html); build it or unset SKIP_UI" >&2
    exit 1
fi

LDFLAGS="-s -w -X main.Version=${VERSION} -X github.com/lukaszraczylo/claude-mnemonic/pkg/hooks.Version=${VERSION}"

build() { # <package> <output path inside the archive, without .exe>
    echo "    $2"
    CGO_ENABLED=1 go build -tags fts5 -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "${STAGE}/$2${EXE}" "$1"
}

mkdir -p "${STAGE}/hooks"
build ./cmd/worker worker
build ./cmd/mcp mcp-server
for hook in session-start user-prompt post-tool-use subagent-stop stop pre-compact statusline; do
    build "./cmd/hooks/${hook}" "hooks/${hook}"
done

# Plugin files: hooks definition, slash commands and the generated manifests.
cp plugin/hooks/hooks.json "${STAGE}/hooks/hooks.json"
mkdir -p "${STAGE}/commands"
cp commands/*.md "${STAGE}/commands/"
VERSION="$VERSION" OUTPUT_DIR="${STAGE}/.claude-plugin" bash scripts/generate-plugin-config.sh

PYTHON="$(command -v python3 || command -v python || true)"
if [[ -z "$PYTHON" ]]; then
    echo "python3 is needed to pack the archive" >&2
    exit 1
fi
mkdir -p "$DIST"
"$PYTHON" scripts/package_release.py "$STAGE" "$ARCHIVE"
echo "==> ${ARCHIVE} ($(du -h "$ARCHIVE" | cut -f1))"
