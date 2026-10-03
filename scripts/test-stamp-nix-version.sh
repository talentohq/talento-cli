#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
stamp=$root/scripts/stamp-nix-version.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' 0 INT TERM HUP

fail() {
  echo "stamp-nix-version test: $*" >&2
  exit 1
}

write_fixture() {
  directory=$1
  version=$2
  changelog=$3
  mkdir -p "$directory/nix"
  printf '%s\n' "# stamp" "\"$version\"" > "$directory/nix/version.nix"
  printf '%s\n' "$changelog" > "$directory/CHANGELOG.md"
}

notes='# Changelog

## [Unreleased]

### Added

- Ship the next CLI notes.

## [1.0.8] - 2026-10-03

### Added

- Previous release.
'

empty='# Changelog

## [Unreleased]

## [1.0.8] - 2026-10-03

### Added

- Previous release.
'

dated='# Changelog

## [1.0.9] - 2026-10-01

### Fixed

- Keep an already dated section.

## [1.0.8] - 2026-10-03

### Added

- Previous release.
'

run_stamp() {
  directory=$1
  shift
  (cd "$directory" && "$stamp" "$@")
}

today=$(date +%Y-%m-%d)

release=$work/release
write_fixture "$release" "1.0.9-dev" "$notes"
run_stamp "$release" 1.0.9 >/dev/null || fail "release stamp was rejected"
grep -q '^"1.0.9"$' "$release/nix/version.nix" || fail "nix stamp was not updated"
grep -q "^## \\[1.0.9\\] - $today\$" "$release/CHANGELOG.md" || fail "changelog was not dated $today"
if grep -q '^## \[Unreleased\]$' "$release/CHANGELOG.md"; then
  fail "Unreleased heading survived the release stamp"
fi
run_stamp "$release" --check 1.0.9 >/dev/null || fail "check rejected a dated release"

idempotent=$work/idempotent
write_fixture "$idempotent" "1.0.9" "$dated"
before=$(cksum "$idempotent/CHANGELOG.md")
run_stamp "$idempotent" 1.0.9 >/dev/null || fail "idempotent stamp was rejected"
after=$(cksum "$idempotent/CHANGELOG.md")
[ "$before" = "$after" ] || fail "idempotent stamp rewrote a dated changelog"
run_stamp "$idempotent" --check 1.0.9 >/dev/null || fail "check rejected an existing dated section"

missing=$work/missing
write_fixture "$missing" "1.0.9-dev" "# Changelog

## [1.0.8] - 2026-10-03

### Added

- Previous release.
"
if run_stamp "$missing" 1.0.9 >/dev/null 2>&1; then
  fail "stamp without Unreleased notes was accepted"
fi
grep -q '^"1.0.9-dev"$' "$missing/nix/version.nix" || fail "failed stamp changed the nix version"

blank=$work/blank
write_fixture "$blank" "1.0.9-dev" "$empty"
if run_stamp "$blank" 1.0.9 >/dev/null 2>&1; then
  fail "stamp of an empty Unreleased section was accepted"
fi
grep -q '^"1.0.9-dev"$' "$blank/nix/version.nix" || fail "empty changelog stamp changed the nix version"

nocheck=$work/nocheck
write_fixture "$nocheck" "1.0.9" "# Changelog

## [1.0.8] - 2026-10-03

### Added

- Previous release.
"
if run_stamp "$nocheck" --check 1.0.9 >/dev/null 2>&1; then
  fail "check accepted a release version with no changelog section"
fi

leftover=$work/leftover
write_fixture "$leftover" "1.0.9" "# Changelog

## [Unreleased]

### Added

- Notes for the next version.

## [1.0.9] - 2026-10-03

### Added

- This release.
"
if run_stamp "$leftover" --check 1.0.9 >/dev/null 2>&1; then
  fail "check accepted Unreleased notes on a release version"
fi

dev=$work/dev
write_fixture "$dev" "1.0.8" "$notes"
before=$(cksum "$dev/CHANGELOG.md")
run_stamp "$dev" 1.0.9-dev >/dev/null || fail "dev stamp was rejected"
grep -q '^"1.0.9-dev"$' "$dev/nix/version.nix" || fail "dev stamp did not update nix"
after=$(cksum "$dev/CHANGELOG.md")
[ "$before" = "$after" ] || fail "dev stamp rewrote the changelog"
run_stamp "$dev" --check 1.0.9-dev >/dev/null || fail "check rejected a dev stamp"

echo "stamp-nix-version changelog regression tests passed."
