#!/usr/bin/env bash
# The binaries of a release (.github/workflows/release.yml), built in the build image the release's own
# tacplus.lock pins, by digest: the gates on the release's checkout first, then for each architecture the
# static doppelganger and the launcher every service image runs under (Dockerfile).
#
# Usage:  scripts/release-build.sh SRC LAUNCHER OUT
#
# SRC is the checkout of the tag, LAUNCHER a checkout of github.com/tacplus-dev/launcher. Writes
# OUT/doppelganger-linux-<arch> and OUT/launcher-linux-<arch> for each of ARCHS (default "arm64"). Needs docker
# and no credential.
set -euo pipefail

src="$(cd "$1" && pwd)"
launcher="$(cd "$2" && pwd)"
mkdir -p "$3"
out="$(cd "$3" && pwd)"
archs="${ARCHS:-arm64}"

lock="$src/tacplus.lock"
[[ -f "$lock" ]] || { echo "release-build.sh: $src has no tacplus.lock" >&2; exit 1; }
image="$(sed -n -E 's/^build_image:[[:space:]]*"?([^"[:space:]]+)"?.*$/\1/p' "$lock" | head -1)"
[[ "$image" == *@sha256:* ]] || { echo "release-build.sh: tacplus.lock names no build_image by digest" >&2; exit 1; }
for arch in $archs; do
  case "$arch" in amd64|arm64) ;; *) echo "release-build.sh: unsupported architecture $arch" >&2; exit 1 ;; esac
done

home="$(mktemp -d)"
trap 'rm -rf "$home"' EXIT
# shellcheck disable=SC2016 # expanded in the container
docker run --rm -u "$(id -u):$(id -g)" -e HOME="$home" -e ARCHS="$archs" -e OUT="$out" -e LAUNCHER="$launcher" \
  -v "$src:$src" -v "$launcher:$launcher" -v "$out:$out" -v "$home:$home" -w "$src" \
  "$image" bash -euo pipefail -c '
    just gates
    for arch in $ARCHS; do
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w" \
        -o "$OUT/doppelganger-linux-$arch" ./cmd/doppelganger
      (cd "$LAUNCHER" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w" \
        -o "$OUT/launcher-linux-$arch" .)
    done'
ls -l "$out"
