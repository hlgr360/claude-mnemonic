#!/bin/bash
# Print the version of this fork's release, which is the upstream version it contains plus a fork number: v0.21.95.1,
# v0.21.95.2, ... The upstream part is read from the merge state (the highest upstream release tag, vX.Y.Z, merged into
# HEAD); the fork number counts this fork's releases of that upstream version, from 1.
#
# Usage: scripts/release-version.sh [--tag | --next]
#   (none)  the upstream version merged into HEAD, without the leading v (0.21.95)
#   --tag   the same as a tag (v0.21.95)
#   --next  the tag of the next fork release: vX.Y.Z.N, with N one more than the highest N among the tags vX.Y.Z.* on
#           origin and in this clone (1 when there is none)
#
# Run `git fetch upstream --tags` first so that new upstream releases are known. A fork tag has a fourth number, so it
# never clashes with upstream's tag of the same version, and the in-app updater orders it correctly (a letter suffix
# such as 0.21.95a it would mis-read).
set -euo pipefail

base="$(git tag --merged HEAD --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)"
if [[ -z "$base" ]]; then
    echo "No vX.Y.Z release tag is merged into HEAD; run: git fetch upstream --tags" >&2
    exit 1
fi

case "${1:-}" in
    "")
        echo "${base#v}"
        ;;
    --tag)
        echo "$base"
        ;;
    --next)
        if ! remote="$(git ls-remote --tags origin "refs/tags/${base}.*" 2>&1)"; then
            echo "Cannot read the tags of origin, so the next fork number is unknown: $remote" >&2
            exit 1
        fi
        pattern="^${base//./\\.}\\.([0-9]+)$"
        highest=0
        while read -r name; do
            if [[ "$name" =~ $pattern ]] && (( BASH_REMATCH[1] > highest )); then
                highest="${BASH_REMATCH[1]}"
            fi
        done < <({ git tag --list "${base}.*"; printf '%s\n' "$remote" | awk '{ sub("^refs/tags/", "", $2); print $2 }'; })
        echo "${base}.$((highest + 1))"
        ;;
    *)
        echo "Usage: $0 [--tag | --next]" >&2
        exit 2
        ;;
esac
