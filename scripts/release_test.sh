#!/usr/bin/env bash
# Tests of the release scripts (.github/workflows/release.yml): scripts/release-tags.sh and
# scripts/release-notes.sh, on a throwaway repository with its own tags. Run by `just gates`.
#
#   scripts/release_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(mktemp -d)"
trap 'rm -rf "$repo"' EXIT
fails=0

check() { # name got want
  if [[ "$2" == "$3" ]]; then
    echo "ok   $1"
  else
    echo "FAIL $1: got [$2], want [$3]"
    fails=$((fails + 1))
  fi
}
refused() { # name command...
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then
    echo "FAIL $name: not refused"
    fails=$((fails + 1))
  else
    echo "ok   $name"
  fi
}

cd "$repo"
git init -q -b main
git config user.email test@example.invalid
git config user.name test
commit() { git commit -q --allow-empty -m "$1"; }
tags() { "$here/release-tags.sh" "$@"; }
notes() { "$here/release-notes.sh" "$@"; }

commit "first"
commit "second"
git tag -a v0.1.0 -m v0.1.0
check "first release: version and line" "$(tags v0.1.0)" "0.1.0 0.1"
check "first release: every commit" "$(notes v0.1.0 | sed 's/ ([0-9a-f]*)$//')" "$(printf 'Changes:\n\n- second\n- first')"

commit "a fix"
git tag v0.1.1
check "a patch moves its line" "$(tags v0.1.1)" "0.1.1 0.1"
check "the older patch no longer moves the line" "$(tags v0.1.0)" "0.1.0"
check "a patch's notes" "$(notes v0.1.1 | sed 's/ ([0-9a-f]*)$//')" "$(printf 'Changes since v0.1.0:\n\n- a fix')"

commit "a feature"
git tag v0.2.0
git checkout -q -b release/0.1 v0.1.1
commit "a fix on 0.1"
git tag v0.1.2
check "a patch on an older line moves that line" "$(tags v0.1.2)" "0.1.2 0.1"
check "and not the newer line" "$(tags v0.2.0)" "0.2.0 0.2"
check "an older line's notes: since its own previous patch" "$(notes v0.1.2 | sed 's/ ([0-9a-f]*)$//')" "$(printf 'Changes since v0.1.1:\n\n- a fix on 0.1')"

git checkout -q main
for i in $(seq 2 10); do commit "fix $i"; git tag "v0.2.$i"; done
check "by version, not lexically: 0.2.10 is the line's highest" "$(tags v0.2.10)" "0.2.10 0.2"
check "0.2.9 is not" "$(tags v0.2.9)" "0.2.9"
check "the highest release, by version" "$("$here/release-tags.sh" --highest)" "v0.2.10"

git tag v0.3.0-rc.1
check "a pre-release: only its own tag" "$(tags v0.3.0-rc.1)" "0.3.0-rc.1"
check "a pre-release is not the highest release" "$("$here/release-tags.sh" --highest)" "v0.2.10"
check "a pre-release's notes: since the release below it" "$(notes v0.3.0-rc.1 | head -1)" "Changes since v0.2.10:"

refused "not a version: refused (tags)" tags 0.1.0
refused "not a version: refused (tags, v1.2)" tags v1.2
refused "not a version: refused (notes)" notes main

if ((fails)); then
  echo "release_test.sh: $fails failed"
  exit 1
fi
echo "release_test.sh: all passed"
