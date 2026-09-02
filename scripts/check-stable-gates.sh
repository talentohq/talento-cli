#!/bin/sh
set -eu

fail() {
  echo "$*" >&2
  exit 1
}

release_tag=${GITHUB_REF_NAME:-}
release_precedence=${release_tag%%+*}
case "$release_precedence" in
  *-*) fail "Stable gates reject SemVer prerelease tags" ;;
esac
case "$release_tag" in
  v1.*) ;;
  *) fail "Stable gates apply only to v1.x tags" ;;
esac

for variable in \
  TALENTO_RELEASE_PUBLIC_KEY TALENTO_RELEASE_PRIVATE_KEY \
  APPLE_CERTIFICATE_P12 APPLE_CERTIFICATE_PASSWORD APPLE_ID APPLE_APP_PASSWORD APPLE_TEAM_ID APPLE_SIGNING_IDENTITY; do
  eval "value=\${$variable:-}"
  [ -n "$value" ] || fail "$variable is required for a stable release"
done

echo "Stable release prerequisites are valid; environment approval is recorded by GitHub."
