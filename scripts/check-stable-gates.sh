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

release_tag=${GITHUB_REF_NAME:-}
release_precedence=${release_tag%%+*}
case "$release_precedence" in
  *-*) fail "Stable gates reject SemVer prerelease tags" ;;
esac
case "$release_tag" in
  v1.*) ;;
  *) fail "Stable gates apply only to v1.x tags" ;;
esac

command -v jq >/dev/null 2>&1 || fail "jq is required to validate staging evidence"

for variable in \
  TALENTO_RELEASE_PUBLIC_KEY TALENTO_RELEASE_PRIVATE_KEY TALENTO_STAGING_EVIDENCE_SHA TALENTO_STAGING_EVIDENCE_FILE \
  APPLE_CERTIFICATE_P12 APPLE_CERTIFICATE_PASSWORD APPLE_ID APPLE_APP_PASSWORD APPLE_TEAM_ID APPLE_SIGNING_IDENTITY \
  WINDOWS_CERTIFICATE_PFX WINDOWS_CERTIFICATE_PASSWORD TALENTO_WINDOWS_AUTHENTICODE_PUBLISHER; do
  eval "value=\${$variable:-}"
  [ -n "$value" ] || fail "$variable is required for a stable release"
done

evidence_file=$TALENTO_STAGING_EVIDENCE_FILE
[ -f "$evidence_file" ] || fail "TALENTO_STAGING_EVIDENCE_FILE does not identify a regular file"

case "$TALENTO_STAGING_EVIDENCE_SHA" in
  *[!0-9a-f]*|'') fail "TALENTO_STAGING_EVIDENCE_SHA must be a lowercase SHA-256 digest" ;;
esac
[ "${#TALENTO_STAGING_EVIDENCE_SHA}" -eq 64 ] || fail "TALENTO_STAGING_EVIDENCE_SHA must be a lowercase SHA-256 digest"

evidence_sha=$(sha256_file "$evidence_file")
[ "$evidence_sha" = "$TALENTO_STAGING_EVIDENCE_SHA" ] || fail "staging evidence SHA-256 does not match TALENTO_STAGING_EVIDENCE_SHA"
jq -e . "$evidence_file" >/dev/null 2>&1 || fail "staging evidence is not valid JSON"

jq -e '
  type == "object" and
  (keys == ["captured_at", "evidence_version", "probe_contract_sha256", "probes", "roles", "snapshot_sha256"]) and
  .evidence_version == 1 and
  (.captured_at | type == "string") and
  (.probe_contract_sha256 | type == "string") and
  (.snapshot_sha256 | type == "string") and
  (.roles | type == "array") and
  (.probes | type == "array")
' "$evidence_file" >/dev/null || fail "staging evidence does not match evidence version 1"

if ! captured_epoch=$(jq -er '.captured_at | fromdateiso8601' "$evidence_file" 2>/dev/null); then
  fail "staging evidence captured_at must be an RFC 3339 UTC timestamp"
fi
captured_at=$(jq -r '.captured_at' "$evidence_file")
case "$captured_at" in
  ????-??-??T??:??:??Z) ;;
  *) fail "staging evidence captured_at must use YYYY-MM-DDTHH:MM:SSZ" ;;
esac

max_age=${TALENTO_STAGING_EVIDENCE_MAX_AGE_SECONDS:-86400}
case "$max_age" in
  *[!0-9]*|'') fail "TALENTO_STAGING_EVIDENCE_MAX_AGE_SECONDS must be a positive integer" ;;
esac
[ "$max_age" -gt 0 ] || fail "TALENTO_STAGING_EVIDENCE_MAX_AGE_SECONDS must be a positive integer"
[ "$max_age" -le 86400 ] || fail "TALENTO_STAGING_EVIDENCE_MAX_AGE_SECONDS must not exceed 86400"
now_epoch=$(date +%s)
age=$((now_epoch - captured_epoch))
[ "$age" -ge -300 ] || fail "staging evidence captured_at is more than five minutes in the future"
[ "$age" -le "$max_age" ] || fail "staging evidence is stale (maximum age is $max_age seconds)"

gateway_sha=$(sha256_file schemas/gateway.json)
evidence_snapshot=$(jq -r '.snapshot_sha256' "$evidence_file")
[ "$evidence_snapshot" = "$gateway_sha" ] || fail "staging evidence snapshot does not match schemas/gateway.json"
manifest_snapshot=$(jq -er '.snapshot_sha256' coverage/manifest.json) || fail "coverage manifest has no snapshot digest"
[ "$manifest_snapshot" = "$gateway_sha" ] || fail "coverage manifest snapshot does not match schemas/gateway.json"

probe_contract=contracts/staging-evidence-probes.json
jq -e '
  type == "object" and
  (keys == ["probe_contract_version", "probes"]) and
  .probe_contract_version == 1 and
  (.probes | type == "array" and length > 0) and
  ([.probes[].id] | unique | length) == (.probes | length) and
  all(.probes[];
    (keys == ["expected", "id", "kind", "role"]) and
    all(.[]; type == "string" and length > 0)
  )
' "$probe_contract" >/dev/null || fail "required staging probe contract is invalid"
probe_contract_sha=$(sha256_file "$probe_contract")
evidence_probe_contract_sha=$(jq -r '.probe_contract_sha256' "$evidence_file")
[ "$evidence_probe_contract_sha" = "$probe_contract_sha" ] || fail "staging evidence probe contract does not match the reviewed requirements"

jq -e '
  (.roles | length) == 4 and
  ([.roles[].role] | sort) == ["admin", "employee", "external", "manager"] and
  ([.roles[].role] | unique | length) == 4
' "$evidence_file" >/dev/null || fail "staging evidence must contain exactly one capture for every required role"

for role in admin manager employee external; do
  contract="contracts/roles/$role.json"
  contract_snapshot=$(jq -er '.snapshot_sha256' "$contract") || fail "$contract has no snapshot digest"
  [ "$contract_snapshot" = "$gateway_sha" ] || fail "$contract snapshot does not match schemas/gateway.json"
  contract_sha=$(sha256_file "$contract")

  jq -e \
    --arg role "$role" \
    --arg captured_at "$captured_at" \
    --arg contract_sha "$contract_sha" \
    --slurpfile contract "$contract" '
      .roles[] | select(.role == $role) |
      (keys == ["captured_at", "contract_sha256", "modules", "resources", "role", "tenant", "tools"]) and
      .captured_at == $captured_at and
      .contract_sha256 == $contract_sha and
      .tenant == $contract[0].tenant and
      .modules == $contract[0].modules and
      (.tools | type == "array") and
      (.resources | type == "array") and
      ([.tools[].name] | sort) == ($contract[0].representative_tools | sort) and
      ([.tools[].name] | unique | length) == (.tools | length) and
      ([.resources[].name] | sort) == ($contract[0].representative_resources | sort) and
      ([.resources[].name] | unique | length) == (.resources | length) and
      all(.tools[];
        (keys == ["actual", "expected", "name"]) and
        .expected == "available" and .actual == .expected
      ) and
      all(.resources[];
        (keys == ["actual", "expected", "name"]) and
        .expected == "available" and .actual == .expected
      )
    ' "$evidence_file" >/dev/null || fail "$role staging capture does not exactly match its reviewed role contract"
done

required_probes=$(jq -c '.probes' "$probe_contract")

jq -e --argjson required "$required_probes" '
  (.probes | length) == ($required | length) and
  ([.probes[].id] | unique | length) == (.probes | length) and
  ([.probes[] | {id, kind, role, expected}] | sort_by(.id)) == ($required | sort_by(.id)) and
  all(.probes[];
    (keys == ["actual", "expected", "id", "kind", "role", "subject"]) and
    (.subject | type == "string" and length > 0) and
    .actual == .expected
  )
' "$evidence_file" >/dev/null || fail "staging evidence has missing, failed, duplicate, or unexpected required probes"

jq -e --slurpfile gateway schemas/gateway.json '
  def known_tool($name): any($gateway[0].tools[]; .name == $name);
  def read_tool($name): any($gateway[0].tools[]; .name == $name and .annotations.readOnlyHint == true);
  def write_tool($name): any($gateway[0].tools[]; .name == $name and .annotations.readOnlyHint == false);
  def known_resource($name): any($gateway[0].resources[]; .name == $name);
  ([.roles[].role] | unique) as $roles |
  all(.probes[]; .role as $role | $roles | index($role)) and
  all(.probes[] | select(.id == "resource.read"); known_resource(.subject)) and
  all(.probes[] | select(.id == "tool.read"); read_tool(.subject)) and
  all(.probes[] | select(.kind == "write"); write_tool(.subject)) and
  all(.probes[] | select(
    .id == "authorization.permission_denied" or
    .id == "authorization.module_disabled" or
    .id == "resolution.ambiguous_entity" or
    .id == "response.truncation" or
    .id == "response.server_failure"
  ); known_tool(.subject))
' "$evidence_file" >/dev/null || fail "staging evidence references an inconsistent role, tool, or resource"

echo "Stable release prerequisites and protected staging evidence are valid; environment approval is recorded by GitHub."
