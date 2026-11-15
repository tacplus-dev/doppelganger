#!/usr/bin/env bash
# Assembles the release image (.github/workflows/release.yml) from the release's Dockerfile, once per
# architecture, and publishes them as one image.
#
# Usage:  scripts/release-image.sh SRC BIN VERSION REVISION [TAG...]
#
# The Dockerfile takes its binaries from beside it, as the fleet's image pipeline puts them there: for each of
# ARCHS (default "arm64") this stages a build context with SRC's Dockerfile, BIN/doppelganger-linux-<arch> as
# bin/doppelganger and BIN/launcher-linux-<arch> (scripts/release-build.sh), and builds it labelled with VERSION
# and REVISION (the commit). With TAGs, each architecture is pushed by digest and $IMAGE:<TAG> (each TAG) is
# pointed at all of them. Without, nothing is pushed: the dry run.
set -euo pipefail

src="$1"
bin="$2"
version="$3"
revision="$4"
shift 4
image="${IMAGE:-ghcr.io/tacplus-dev/doppelganger}"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

digests=()
for arch in ${ARCHS:-arm64}; do
  ctx="$stage/$arch"
  mkdir -p "$ctx/bin"
  cp "$src/Dockerfile" "$ctx/"
  cp "$bin/doppelganger-linux-$arch" "$ctx/bin/doppelganger"
  cp "$bin/launcher-linux-$arch" "$ctx/"

  output="type=cacheonly"
  (($#)) && output="type=image,name=$image,push-by-digest=true,name-canonical=true,push=true"
  docker buildx build --platform "linux/$arch" \
    --build-arg "VERSION=$version" --build-arg "VCS_REF=$revision" \
    --output "$output" --metadata-file "$ctx.json" "$ctx"
  if (($#)); then
    digest="$(jq -r '."containerimage.digest"' "$ctx.json")"
    echo "linux/$arch: $image@$digest"
    digests+=("$image@$digest")
  fi
done

(($#)) || exit 0
refs=()
for tag in "$@"; do refs+=(-t "$image:$tag"); done
docker buildx imagetools create "${refs[@]}" "${digests[@]}"
