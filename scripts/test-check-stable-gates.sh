#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repo"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

fail() {
  echo "test-check-stable-gates: $*" >&2
  exit 1
}

run_gate() {
  tag=${1:-v1.0.0}
  GITHUB_REF_NAME="$tag" \
    TALENTO_RELEASE_PUBLIC_KEY=test \
    TALENTO_RELEASE_PRIVATE_KEY=test \
    APPLE_CERTIFICATE_P12=test \
    APPLE_CERTIFICATE_PASSWORD=test \
    APPLE_ID=test \
    APPLE_APP_PASSWORD=test \
    APPLE_TEAM_ID=test \
    APPLE_SIGNING_IDENTITY=test \
    sh ./scripts/check-stable-gates.sh
}

run_gate v1.0.0 >/dev/null || fail "v1.0.0 was rejected"
run_gate 'v1.0.0+build-foo' >/dev/null || fail "stable build metadata containing a hyphen was misclassified as prerelease"

if run_gate v1.0.0-rc.1 >"$tmp/prerelease.out" 2>&1; then
  fail "stable prerelease tag unexpectedly passed"
fi
grep -F "reject SemVer prerelease tags" "$tmp/prerelease.out" >/dev/null || fail "stable prerelease rejection was not actionable"

if run_gate v0.1.0 >"$tmp/preview.out" 2>&1; then
  fail "v0.1.0 unexpectedly passed stable gates"
fi
grep -F "Stable gates apply only to v1.x tags" "$tmp/preview.out" >/dev/null || fail "v0 rejection was not actionable"

if GITHUB_REF_NAME=v1.0.0 \
  TALENTO_RELEASE_PUBLIC_KEY=test \
  TALENTO_RELEASE_PRIVATE_KEY=test \
  APPLE_CERTIFICATE_P12=test \
  APPLE_CERTIFICATE_PASSWORD=test \
  APPLE_ID= \
  APPLE_APP_PASSWORD=test \
  APPLE_TEAM_ID=test \
  APPLE_SIGNING_IDENTITY=test \
  sh ./scripts/check-stable-gates.sh >"$tmp/missing.out" 2>&1; then
  fail "missing APPLE_ID unexpectedly passed"
fi
grep -F "APPLE_ID is required for a stable release" "$tmp/missing.out" >/dev/null || fail "missing Apple secret was not actionable"

echo "stable gate tests passed"
