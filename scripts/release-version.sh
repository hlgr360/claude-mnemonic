#!/bin/bash
# Print the version a release of this fork must carry: the highest upstream release tag (vX.Y.Z) merged into HEAD,
# without the leading v. The fork stays in lockstep with the upstream version it has merged, so the number is read from
# the merge state instead of being typed.
#
# Usage: scripts/release-version.sh [--tag]
#   --tag   print the tag (vX.Y.Z) instead of the bare version
#
# Run `git fetch upstream --tags` first so that new upstream releases are known. Only plain vX.Y.Z tags count: the
# in-app updater compares dotted numbers and would mis-read a suffix.
set -euo pipefail

tag="$(git tag --merged HEAD --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)"
if [[ -z "$tag" ]]; then
    echo "No vX.Y.Z release tag is merged into HEAD; run: git fetch upstream --tags" >&2
    exit 1
fi

if [[ "${1:-}" == "--tag" ]]; then
    echo "$tag"
else
    echo "${tag#v}"
fi
