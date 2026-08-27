#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
checker=$root/scripts/check-workflow-policy.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' 0 INT TERM HUP

fail() {
  echo "workflow policy test: $*" >&2
  exit 1
}

write_good() {
  directory=$1
  mkdir -p "$directory"
  cat > "$directory/test.yml" <<'YAML'
name: Test
on: [push]
permissions: {}
jobs:
  test:
    permissions:
      contents: read
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789abcdef0123456789abcdef01234567 # v1.2.3
        with:
          persist-credentials: false
      - uses: example/action@abcdef0123456789abcdef0123456789abcdef01 # v2
YAML
}

good=$work/good
write_good "$good"
"$checker" "$good" >/dev/null || fail "valid pinned workflow was rejected"

mutable=$work/mutable
write_good "$mutable"
sed 's#example/action@abcdef0123456789abcdef0123456789abcdef01 #example/action@v2 #' "$mutable/test.yml" > "$mutable/bad.yml"
rm "$mutable/test.yml"
if "$checker" "$mutable" >/dev/null 2>&1; then fail "mutable action reference was accepted"; fi

unlabelled=$work/unlabelled
write_good "$unlabelled"
sed 's/ # v2$//' "$unlabelled/test.yml" > "$unlabelled/bad.yml"
rm "$unlabelled/test.yml"
if "$checker" "$unlabelled" >/dev/null 2>&1; then fail "action SHA without a version comment was accepted"; fi

credentials=$work/credentials
write_good "$credentials"
sed '/persist-credentials: false/d' "$credentials/test.yml" > "$credentials/bad.yml"
rm "$credentials/test.yml"
if "$checker" "$credentials" >/dev/null 2>&1; then fail "checkout without disabled credential persistence was accepted"; fi

container=$work/container
write_good "$container"
awk '{ print } /runs-on: ubuntu-latest/ { print "    container: golang:1.26" }' "$container/test.yml" > "$container/bad.yml"
rm "$container/test.yml"
if "$checker" "$container" >/dev/null 2>&1; then fail "mutable container image was accepted"; fi

permissions=$work/permissions
write_good "$permissions"
sed '/^permissions: {}/d' "$permissions/test.yml" > "$permissions/bad.yml"
rm "$permissions/test.yml"
if "$checker" "$permissions" >/dev/null 2>&1; then fail "workflow without default-deny permissions was accepted"; fi

echo "Workflow policy regression tests passed."
