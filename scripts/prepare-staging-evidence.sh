#!/bin/sh
set -eu

fail() {
  echo "$*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "sha256sum or shasum is required"
  fi
}

encode_base64() {
  # GNU: base64 -w0 FILE. macOS/BSD lack -w0 on file args; stdin + tr is portable.
  if (base64 -w0 "$1") >/dev/null 2>&1; then
    base64 -w0 "$1"
  else
    base64 <"$1" | tr -d '\n'
  fi
}

repo=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)

[ "$#" -eq 1 ] || fail "usage: scripts/prepare-staging-evidence.sh REPORT.json"
evidence_file=$1
[ -f "$evidence_file" ] || fail "evidence file is not a regular file: $evidence_file"

command -v jq >/dev/null 2>&1 || fail "jq is required to validate staging evidence"

# Dummy gate values only when unset — never overwrite operator-exported secrets.
: "${TALENTO_RELEASE_PUBLIC_KEY:=test}"
: "${TALENTO_RELEASE_PRIVATE_KEY:=test}"
: "${APPLE_CERTIFICATE_P12:=test}"
: "${APPLE_CERTIFICATE_PASSWORD:=test}"
: "${APPLE_ID:=test}"
: "${APPLE_APP_PASSWORD:=test}"
: "${APPLE_TEAM_ID:=test}"
: "${APPLE_SIGNING_IDENTITY:=test}"
: "${WINDOWS_CERTIFICATE_PFX:=test}"
: "${WINDOWS_CERTIFICATE_PASSWORD:=test}"
: "${TALENTO_WINDOWS_AUTHENTICODE_PUBLISHER:=CN=TalentoHQ Test}"

export TALENTO_RELEASE_PUBLIC_KEY TALENTO_RELEASE_PRIVATE_KEY \
  APPLE_CERTIFICATE_P12 APPLE_CERTIFICATE_PASSWORD APPLE_ID APPLE_APP_PASSWORD \
  APPLE_TEAM_ID APPLE_SIGNING_IDENTITY WINDOWS_CERTIFICATE_PFX \
  WINDOWS_CERTIFICATE_PASSWORD TALENTO_WINDOWS_AUTHENTICODE_PUBLISHER

evidence_sha=$(sha256_file "$evidence_file")
export GITHUB_REF_NAME=v1.0.0
export TALENTO_STAGING_EVIDENCE_FILE=$evidence_file
export TALENTO_STAGING_EVIDENCE_SHA=$evidence_sha

(
  cd "$repo"
  sh ./scripts/check-stable-gates.sh >&2
)

evidence_b64=$(encode_base64 "$evidence_file")

printf 'sha256: %s\n' "$evidence_sha"
printf 'base64: %s\n' "$evidence_b64"
printf '%s\n' \
  "gh secret set TALENTO_STAGING_EVIDENCE_BASE64 --env stable-release-gates -R talentohq/talento-cli < report.b64" \
  "gh variable set TALENTO_STAGING_EVIDENCE_SHA --env stable-release-gates -R talentohq/talento-cli --body $evidence_sha"
