#!/usr/bin/env bash
# The image tags a release puts on ghcr.io/tacplus-dev/doppelganger (.github/workflows/release.yml).
#
# Usage:  scripts/release-tags.sh vX.Y.Z
#         scripts/release-tags.sh --highest
#
# Run in the checkout with its tags fetched. Prints the tags, space-separated: X.Y.Z, and the line tag X.Y
# only when X.Y.Z is the highest release of that line, so the line tag never moves backwards and a patch on
# one line never moves another. A pre-release (vX.Y.Z-rc.N) gets only its own tag and is not a release.
# --highest prints the highest release of all, by version: the one GitHub marks Latest.
# Tested by scripts/release_test.sh.
set -euo pipefail

# The releases (vX.Y.Z, no pre-releases) matching a glob, highest last, by version, not lexically.
releases() {
  git tag -l "$1" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -t. -k1.2,1n -k2,2n -k3,3n || true
}

if [[ "$1" == --highest ]]; then
  releases 'v*' | tail -1
  exit 0
fi

ref="$1"
v="${ref#v}"
if [[ "$ref" == v* && "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.-]+$ ]]; then
  echo "$v"
  exit 0
fi
if [[ "$ref" != v* || ! "$v" =~ ^([0-9]+)\.([0-9]+)\.[0-9]+$ ]]; then
  echo "release-tags.sh: $ref is not vMAJOR.MINOR.PATCH" >&2
  exit 1
fi
line="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"

tags="$v"
[[ "$(releases "v$line.*" | tail -1)" == "$ref" ]] && tags+=" $line"
echo "$tags"
