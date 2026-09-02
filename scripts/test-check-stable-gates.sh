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

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

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

run_gate() {
  evidence=$1
  digest=$2
  tag=${3:-v1.0.0}
  GITHUB_REF_NAME="$tag" \
    TALENTO_RELEASE_PUBLIC_KEY=test \
    TALENTO_RELEASE_PRIVATE_KEY=test \
    TALENTO_STAGING_EVIDENCE_FILE="$evidence" \
    TALENTO_STAGING_EVIDENCE_SHA="$digest" \
    APPLE_CERTIFICATE_P12=test \
    APPLE_CERTIFICATE_PASSWORD=test \
    APPLE_ID=test \
    APPLE_APP_PASSWORD=test \
    APPLE_TEAM_ID=test \
    APPLE_SIGNING_IDENTITY=test \
    sh ./scripts/check-stable-gates.sh
}

expect_failure() {
  name=$1
  evidence=$2
  digest=$3
  message=$4
  if run_gate "$evidence" "$digest" >"$tmp/$name.out" 2>&1; then
    fail "$name unexpectedly passed"
  fi
  grep -F "$message" "$tmp/$name.out" >/dev/null || {
    sed -n '1,120p' "$tmp/$name.out" >&2
    fail "$name did not report the expected failure"
  }
}

valid_sha=$(sha256_file "$tmp/valid.json")
run_gate "$tmp/valid.json" "$valid_sha" >/dev/null || fail "valid evidence was rejected"
if run_gate "$tmp/valid.json" "$valid_sha" v1.0.0-rc.1 >"$tmp/prerelease.out" 2>&1; then
  fail "stable prerelease tag unexpectedly passed"
fi
grep -F "reject SemVer prerelease tags" "$tmp/prerelease.out" >/dev/null || fail "stable prerelease rejection was not actionable"
run_gate "$tmp/valid.json" "$valid_sha" 'v1.0.0+build-foo' >/dev/null || fail "stable build metadata containing a hyphen was misclassified as prerelease"

expect_failure wrong_sha "$tmp/valid.json" "0000000000000000000000000000000000000000000000000000000000000000" "staging evidence SHA-256 does not match"

jq '(.captured_at, .roles[].captured_at) = "2020-01-01T00:00:00Z"' "$tmp/valid.json" > "$tmp/stale.json"
expect_failure stale "$tmp/stale.json" "$(sha256_file "$tmp/stale.json")" "staging evidence is stale"

jq '.snapshot_sha256 = "0000000000000000000000000000000000000000000000000000000000000000"' "$tmp/valid.json" > "$tmp/snapshot.json"
expect_failure snapshot "$tmp/snapshot.json" "$(sha256_file "$tmp/snapshot.json")" "snapshot does not match schemas/gateway.json"

jq '(.probes[] | select(.id == "write.preview") | .actual) = "committed"' "$tmp/valid.json" > "$tmp/failed-probe.json"
expect_failure failed_probe "$tmp/failed-probe.json" "$(sha256_file "$tmp/failed-probe.json")" "missing, failed, duplicate, or unexpected required probes"

jq '.probes |= map(select(.id != "resource.read"))' "$tmp/valid.json" > "$tmp/missing-probe.json"
expect_failure missing_probe "$tmp/missing-probe.json" "$(sha256_file "$tmp/missing-probe.json")" "missing, failed, duplicate, or unexpected required probes"

echo "stable gate evidence tests passed"
