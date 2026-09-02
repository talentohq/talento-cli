#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repo"

tmp=$(mktemp -d)
uploads=
cleanup() {
  rm -rf "$tmp"
  [ -z "$uploads" ] || rm -f $uploads
}
trap cleanup EXIT HUP INT TERM

fail() {
  echo "test-prepare-staging-evidence: $*" >&2
  exit 1
}

decode_base64_file() {
  base64 -d <"$1" >"$2" 2>/dev/null || base64 -D <"$1" >"$2" 2>/dev/null
}

# Printed `gh secret set … < PATH` must redirect from an out-of-repo file whose
# contents are evidence bytes or the unwrapped base64 of those bytes.
assert_secret_upload() {
  _out=$1
  _evidence=$2
  _secret_line=$(grep -F "gh secret set TALENTO_STAGING_EVIDENCE_BASE64 --env stable-release-gates -R talentohq/talento-cli < " "$_out" || true)
  [ -n "$_secret_line" ] || fail "stdout missing gh secret set command"
  _upload_path=${_secret_line##*< }
  _upload_path=$(printf '%s' "$_upload_path" | tr -d '\r')
  case "$_upload_path" in
    /*) ;;
    *) fail "secret command does not redirect from an absolute path: $_upload_path" ;;
  esac
  [ -f "$_upload_path" ] || fail "upload file does not exist: $_upload_path"
  case "$_upload_path" in
    "$repo"/*) fail "upload file must stay outside the repo: $_upload_path" ;;
  esac
  uploads="$uploads $_upload_path"
  if cmp -s "$_evidence" "$_upload_path"; then
    return 0
  fi
  if decode_base64_file "$_upload_path" "$tmp/upload-decoded"; then
    cmp -s "$_evidence" "$tmp/upload-decoded" || fail "upload file does not decode to evidence bytes"
    return 0
  fi
  fail "upload file is neither evidence bytes nor decodable unwrapped base64"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

command -v jq >/dev/null 2>&1 || fail "jq is required"

captured_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
snapshot_sha=$(sha256_file schemas/gateway.json)
admin_sha=$(sha256_file contracts/roles/admin.json)
manager_sha=$(sha256_file contracts/roles/manager.json)
employee_sha=$(sha256_file contracts/roles/employee.json)
external_sha=$(sha256_file contracts/roles/external.json)
probe_contract_sha=$(sha256_file contracts/staging-evidence-probes.json)

jq -n \
  --arg captured_at "$captured_at" \
  --arg snapshot_sha "$snapshot_sha" \
  --arg admin_sha "$admin_sha" \
  --arg manager_sha "$manager_sha" \
  --arg employee_sha "$employee_sha" \
  --arg external_sha "$external_sha" \
  --arg probe_contract_sha "$probe_contract_sha" \
  --slurpfile admin contracts/roles/admin.json \
  --slurpfile manager contracts/roles/manager.json \
  --slurpfile employee contracts/roles/employee.json \
  --slurpfile external contracts/roles/external.json '
    def capture($contract; $sha): {
      role: $contract.role,
      captured_at: $captured_at,
      contract_sha256: $sha,
      tenant: $contract.tenant,
      modules: $contract.modules,
      tools: [$contract.representative_tools[] | {name: ., expected: "available", actual: "available"}],
      resources: [$contract.representative_resources[] | {name: ., expected: "available", actual: "available"}]
    };
    def probe($id; $kind; $role; $subject; $result): {
      id: $id, kind: $kind, role: $role, subject: $subject, expected: $result, actual: $result
    };
    {
      evidence_version: 1,
      captured_at: $captured_at,
      snapshot_sha256: $snapshot_sha,
      probe_contract_sha256: $probe_contract_sha,
      roles: [
        capture($admin[0]; $admin_sha),
        capture($manager[0]; $manager_sha),
        capture($employee[0]; $employee_sha),
        capture($external[0]; $external_sha)
      ],
      probes: [
        probe("authentication.non_hr"; "authentication"; "employee"; "generic_mcp_endpoint"; "authenticated"),
        probe("tenant.company_selection"; "tenant"; "external"; "staging-company-b-selected"; "selected"),
        probe("resource.read"; "resource"; "admin"; "employee"; "success"),
        probe("tool.read"; "read"; "admin"; "list_employees"; "success"),
        probe("write.immediate"; "write"; "admin"; "auto_clock_ins"; "committed"),
        probe("write.preview"; "write"; "admin"; "create_training"; "preview"),
        probe("write.confirm"; "write"; "admin"; "confirm_action"; "committed"),
        probe("write.expired_preview"; "write"; "admin"; "confirm_action"; "rejected"),
        probe("write.approval_pending"; "write"; "employee"; "create_absence"; "submitted_for_approval"),
        probe("authorization.permission_denied"; "authorization"; "employee"; "create_invoice"; "denied"),
        probe("authorization.module_disabled"; "authorization"; "manager"; "create_invoice"; "unavailable"),
        probe("resolution.ambiguous_entity"; "resolution"; "manager"; "list_employees"; "ambiguous"),
        probe("response.truncation"; "response"; "admin"; "list_employees"; "truncated"),
        probe("response.server_failure"; "response"; "admin"; "list_employees"; "error"),
        probe("tenant.cross_tenant_read"; "tenant"; "external"; "foreign_synthetic_record"; "denied"),
        probe("tenant.cross_tenant_resolve"; "tenant"; "external"; "foreign_synthetic_record"; "denied"),
        probe("tenant.cross_tenant_preview"; "tenant"; "external"; "foreign_synthetic_record"; "denied"),
        probe("tenant.cross_tenant_confirm"; "tenant"; "external"; "foreign_synthetic_preview"; "denied"),
        probe("tenant.cross_tenant_mutation"; "tenant"; "external"; "foreign_synthetic_record"; "denied"),
        probe("skill.employee"; "skill"; "employee"; "employee_evaluation"; "passed"),
        probe("skill.manager_hr"; "skill"; "manager"; "manager_hr_evaluation"; "passed"),
        probe("skill.sales"; "skill"; "admin"; "sales_evaluation"; "passed"),
        probe("skill.finance"; "skill"; "admin"; "finance_evaluation"; "passed"),
        probe("skill.external_user"; "skill"; "external"; "external_user_evaluation"; "passed")
      ]
    }
  ' > "$tmp/valid.json"

valid_sha=$(sha256_file "$tmp/valid.json")

sh ./scripts/prepare-staging-evidence.sh "$tmp/valid.json" >"$tmp/out" 2>"$tmp/err" || {
  cat "$tmp/err" >&2
  fail "prepare-staging-evidence rejected valid evidence"
}

printed_sha=$(awk '/^sha256: / {print $2; exit}' "$tmp/out")
[ -n "$printed_sha" ] || fail "stdout missing sha256 line"
case "$printed_sha" in
  *[!0-9a-f]*|'') fail "printed digest is not lowercase hex" ;;
esac
[ "${#printed_sha}" -eq 64 ] || fail "printed digest is not 64 characters"
[ "$printed_sha" = "$valid_sha" ] || fail "printed digest does not match file SHA-256"

printed_b64=$(awk '/^base64: / {sub(/^base64: /, ""); print; exit}' "$tmp/out")
[ -n "$printed_b64" ] || fail "stdout missing base64 line"
printf '%s' "$printed_b64" | base64 -d >"$tmp/decoded" 2>/dev/null || \
  printf '%s' "$printed_b64" | base64 -D >"$tmp/decoded" 2>/dev/null || \
  fail "printed base64 did not decode"
cmp -s "$tmp/valid.json" "$tmp/decoded" || fail "decoded base64 does not match evidence bytes"

assert_secret_upload "$tmp/out" "$tmp/valid.json"
grep -F "gh variable set TALENTO_STAGING_EVIDENCE_SHA --env stable-release-gates -R talentohq/talento-cli --body $valid_sha" "$tmp/out" >/dev/null \
  || fail "stdout missing gh variable set command with digest"

# Relative REPORT path from a CWD outside the repo must still resolve.
cp "$tmp/valid.json" "$tmp/relative-report.json"
if (
  cd "$tmp"
  sh "$repo/scripts/prepare-staging-evidence.sh" ./relative-report.json >"$tmp/rel.out" 2>"$tmp/rel.err"
); then
  :
else
  cat "$tmp/rel.err" >&2
  fail "prepare-staging-evidence rejected relative evidence path from outside the repo"
fi
rel_sha=$(awk '/^sha256: / {print $2; exit}' "$tmp/rel.out")
[ "$rel_sha" = "$valid_sha" ] || fail "relative-path digest does not match file SHA-256"
rel_b64=$(awk '/^base64: / {sub(/^base64: /, ""); print; exit}' "$tmp/rel.out")
[ -n "$rel_b64" ] || fail "relative-path stdout missing base64 line"
printf '%s' "$rel_b64" | base64 -d >"$tmp/rel-decoded" 2>/dev/null || \
  printf '%s' "$rel_b64" | base64 -D >"$tmp/rel-decoded" 2>/dev/null || \
  fail "relative-path base64 did not decode"
cmp -s "$tmp/valid.json" "$tmp/rel-decoded" || fail "relative-path decoded base64 does not match evidence bytes"
assert_secret_upload "$tmp/rel.out" "$tmp/valid.json"

echo "prepare-staging-evidence tests passed"
