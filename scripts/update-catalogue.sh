#!/bin/bash
# Point the plugin catalogue (hlgr360/agent-plugins) at a claude-mnemonic release, and open the pull request.
#
# Usage: scripts/update-catalogue.sh [<tag>] [--dry-run] [--skip-install-test] [--trailer TEXT] [--footer TEXT]
#   <tag>                The release, e.g. v0.21.95.3. Without it the latest release is used.
#   --dry-run            Do every check and show the change, but commit, push and open nothing.
#   --skip-install-test  Do not install the edited catalogue in an isolated Claude config (needs the claude CLI).
#   --trailer TEXT       A trailer line for the end of the commit message (Co-Authored-By: ...).
#   --footer TEXT        A line for the end of the pull request body.
# Environment: MNEMONIC_REPO (release repository, default hlgr360/claude-mnemonic),
#              CATALOGUE_REPO (default hlgr360/agent-plugins).
#
# It refuses unless every check passes, because a wrong sha256 in the catalogue makes the install fail for everyone:
#   1. the release is published (not a draft or a pre-release) and has the plugin zip, checksums.txt and the signature
#   2. the zip's sha256 is the one in checksums.txt (it is computed from the downloaded file, not copied)
#   3. cosign verifies checksums.txt with the in-app updater's arguments (a warning when cosign is not installed)
#   4. the zip's plugin.json carries the tag's version and is within the upload form's limits
#   5. the version is higher than the catalogue's (the same version is a no-op; no downgrades)
#   6. the edited catalogue installs the plugin in an isolated Claude config (empty HOME and CLAUDE_CONFIG_DIR)
# The commit is made with the git identity configured in THIS checkout, never the machine's global one. It never merges.
set -euo pipefail

RELEASE_REPO="${MNEMONIC_REPO:-hlgr360/claude-mnemonic}"
CATALOGUE_REPO="${CATALOGUE_REPO:-hlgr360/agent-plugins}"
PLUGIN="claude-mnemonic"

TAG="" DRY_RUN=false SKIP_INSTALL=false TRAILER="" FOOTER=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=true ;;
        --skip-install-test) SKIP_INSTALL=true ;;
        --trailer) TRAILER="${2:?--trailer needs a value}"; shift ;;
        --footer) FOOTER="${2:?--footer needs a value}"; shift ;;
        -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
        -*) echo "Unknown option: $1 (see --help)" >&2; exit 2 ;;
        *) [[ -z "$TAG" ]] || { echo "Only one tag can be given" >&2; exit 2; }; TAG="$1" ;;
    esac
    shift
done

cd "$(dirname "$0")/.."
fail() { echo "update-catalogue: $*" >&2; exit 1; }
say() { echo "==> $*"; }

GIT_NAME="$(git config user.name || true)"
GIT_EMAIL="$(git config user.email || true)"
[[ -n "$GIT_NAME" && -n "$GIT_EMAIL" ]] || fail "no git identity is configured in this checkout (git config user.name / user.email); the commit must not use a global one"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

sha256_of() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d ' ' -f 1; else shasum -a 256 "$1" | cut -d ' ' -f 1; fi; }

# 1. The release
if [[ -z "$TAG" ]]; then
    TAG="$(gh release view --repo "$RELEASE_REPO" --json tagName --jq .tagName)" || fail "cannot read the latest release of $RELEASE_REPO"
fi
VERSION="${TAG#v}"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "$TAG is not a fork release tag (vX.Y.Z.N)"
ZIP="claude-mnemonic-plugin_${VERSION}.zip"
say "Release $TAG of $RELEASE_REPO"
META="$(gh release view "$TAG" --repo "$RELEASE_REPO" --json tagName,isDraft,isPrerelease,assets)" || fail "release $TAG not found in $RELEASE_REPO"
python3 - "$META" "$ZIP" <<'PY' || exit 1
import json, sys
meta, zip_name = json.loads(sys.argv[1]), sys.argv[2]
if meta.get("isDraft") or meta.get("isPrerelease"):
    sys.exit("update-catalogue: the release is a draft or a pre-release; the catalogue only points at published releases")
names = {a["name"] for a in meta.get("assets", [])}
missing = [n for n in (zip_name, "checksums.txt", "checksums.txt.sigstore.json") if n not in names]
if missing:
    sys.exit("update-catalogue: the release lacks " + ", ".join(missing))
PY

mkdir -p "$WORK/rel"
gh release download "$TAG" --repo "$RELEASE_REPO" --dir "$WORK/rel" --pattern "$ZIP" --pattern checksums.txt --pattern checksums.txt.sigstore.json >/dev/null \
    || fail "cannot download the release files"

# 2. The checksum, computed from the file and compared with the signed list
EXPECTED="$(awk -v f="$ZIP" '$2 == f { print $1 }' "$WORK/rel/checksums.txt")"
[[ -n "$EXPECTED" ]] || fail "checksums.txt has no entry for $ZIP"
SHA="$(sha256_of "$WORK/rel/$ZIP")"
[[ "$SHA" == "$EXPECTED" ]] || fail "the zip's sha256 ($SHA) is not the one in checksums.txt ($EXPECTED)"
say "sha256 $SHA matches checksums.txt"

# 3. The signature
SIGNATURE="cosign verify-blob passed (the in-app updater's arguments)"
if command -v cosign >/dev/null 2>&1; then
    cosign verify-blob \
        --bundle "$WORK/rel/checksums.txt.sigstore.json" \
        --certificate-identity-regexp "^https://github\\.com/${RELEASE_REPO}/.*\$" \
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
        "$WORK/rel/checksums.txt" >/dev/null 2>&1 || fail "the signature of checksums.txt does not verify"
    say "signature verified"
else
    SIGNATURE="signature NOT checked: cosign is not installed here"
    echo "warning: cosign is not installed, so the signature was not checked" >&2
fi

# 4. The zip's contents
mkdir -p "$WORK/zip"
unzip -q "$WORK/rel/$ZIP" -d "$WORK/zip" || fail "cannot unpack $ZIP"
python3 - "$WORK/zip" "$VERSION" <<'PY' || exit 1
import json, os, sys
root, version = sys.argv[1], sys.argv[2]
found = json.load(open(os.path.join(root, ".claude-plugin", "plugin.json"), encoding="utf-8"))["version"]
if found != version:
    sys.exit(f"update-catalogue: the zip's plugin.json says version {found}, the tag says {version}")
PY
python3 scripts/check_plugin_manifest.py "$WORK/zip" || fail "the plugin would be rejected by the upload form"
say "plugin.json version $VERSION, within the upload form's limits"

# 5. The catalogue
gh repo clone "$CATALOGUE_REPO" "$WORK/cat" -- -q >/dev/null 2>&1 || fail "cannot clone $CATALOGUE_REPO"
MANIFEST="$WORK/cat/.claude-plugin/marketplace.json"
URL="https://github.com/${RELEASE_REPO}/releases/download/${TAG}/${ZIP}"
CHANGE="$(python3 - "$MANIFEST" "$URL" "$SHA" "$PLUGIN" <<'PY'
import json, re, sys
path, url, sha, plugin = sys.argv[1:5]
with open(path, encoding="utf-8") as f:
    catalogue = json.load(f)
entry = next((p for p in catalogue["plugins"] if p["name"] == plugin), None)
if entry is None:
    sys.exit(f"update-catalogue: the catalogue has no {plugin} entry to update")
if entry["source"].get("source") != "archive":
    sys.exit("update-catalogue: the entry is not an archive source")
def version(u):
    m = re.search(r"claude-mnemonic-plugin_(\d+(?:\.\d+){3})\.zip$", u)
    return tuple(int(x) for x in m.group(1).split(".")) if m else None
old, new = version(entry["source"]["url"]), version(url)
if old is None:
    sys.exit("update-catalogue: cannot read the current version from the catalogue entry")
if new == old:
    print("SAME")
    sys.exit(0)
if new < old:
    sys.exit(f"update-catalogue: {'.'.join(map(str, new))} is lower than the catalogue's {'.'.join(map(str, old))}; no downgrades")
entry["source"]["url"], entry["source"]["sha256"] = url, sha
with open(path, "w", encoding="utf-8") as f:
    f.write(json.dumps(catalogue, indent=2) + "\n")
print("CHANGED " + ".".join(map(str, old)))
PY
)" || exit 1
if [[ "$CHANGE" == "SAME" ]]; then
    say "The catalogue already points at $TAG; nothing to do."
    exit 0
fi
OLD_VERSION="${CHANGE#CHANGED }"
MARKETPLACE="$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['name'])" "$MANIFEST")"
say "Catalogue entry: $OLD_VERSION -> $VERSION"
git -C "$WORK/cat" --no-pager diff --stat

# 6. Install from the edited catalogue in an isolated Claude config
INSTALL="not run (--skip-install-test)"
if [[ "$SKIP_INSTALL" != "true" ]]; then
    command -v claude >/dev/null 2>&1 || fail "the claude CLI is needed for the install test (--skip-install-test to skip it)"
    mkdir -p "$WORK/home/.claude"
    (
        export HOME="$WORK/home" CLAUDE_CONFIG_DIR="$WORK/home/.claude"
        claude plugin marketplace add "$WORK/cat" >/dev/null 2>&1 || exit 1
        claude plugin install "${PLUGIN}@${MARKETPLACE}" >/dev/null 2>&1 || exit 2
        claude plugin list 2>&1 | grep -A3 "${PLUGIN}@${MARKETPLACE}" | grep -q "Version: ${VERSION}" || exit 3
    ) || fail "the edited catalogue does not install ${PLUGIN}@${MARKETPLACE} at ${VERSION} in an isolated Claude config (step $?)"
    INSTALL="installed as ${PLUGIN}@${MARKETPLACE} ${VERSION} in an isolated Claude config"
    say "$INSTALL"
fi

if [[ "$DRY_RUN" == "true" ]]; then
    git -C "$WORK/cat" --no-pager diff
    say "Dry run: nothing was committed, pushed or opened."
    exit 0
fi

# 7. Commit, push, open the pull request
BRANCH="chore/${PLUGIN}-${VERSION}"
MESSAGE="Point ${PLUGIN} at ${TAG}"
BODY_COMMIT="The entry now points at the plugin zip of the release ${TAG}, pinned by sha256.

Checked by scripts/update-catalogue.sh: the sha256 equals the one in checksums.txt, ${SIGNATURE}, plugin.json says ${VERSION} and is within the upload form's limits, and the edited catalogue ${INSTALL}."
[[ -z "$TRAILER" ]] || BODY_COMMIT="${BODY_COMMIT}

${TRAILER}"
git -C "$WORK/cat" checkout -q -b "$BRANCH"
git -C "$WORK/cat" add .claude-plugin/marketplace.json
git -C "$WORK/cat" -c user.name="$GIT_NAME" -c user.email="$GIT_EMAIL" commit -q -m "$MESSAGE" -m "$BODY_COMMIT"
git -C "$WORK/cat" push -q -u origin "$BRANCH"
PR_BODY="## What changed
The \`${PLUGIN}\` entry now points at the plugin zip of [\`${TAG}\`](https://github.com/${RELEASE_REPO}/releases/tag/${TAG}), sha256 \`${SHA}\` (was ${OLD_VERSION}).

## Checked by \`scripts/update-catalogue.sh\` before this PR was opened
- The release is published (not a draft or a pre-release) and has the zip, \`checksums.txt\` and the signature bundle.
- The zip's sha256 equals the one in \`checksums.txt\`.
- ${SIGNATURE}.
- The zip's \`plugin.json\` says ${VERSION} and is within the upload form's limits.
- The edited catalogue ${INSTALL}.

Nothing is merged by the script."
[[ -z "$FOOTER" ]] || PR_BODY="${PR_BODY}

${FOOTER}"
gh pr create --repo "$CATALOGUE_REPO" --base main --head "$BRANCH" --title "$MESSAGE" --body "$PR_BODY"
