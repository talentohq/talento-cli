# Release gates

`0.1.x` is a preview line. A `1.0.0` tag is blocked until the `stable-release-gates` GitHub
environment is approved and every item below has current evidence attached to the release run.

- A non-HR user authenticates through the generic endpoint, selects a company, and receives only
  tenant-scoped capabilities.
- Current admin, manager, employee, and external staging profiles match the reviewed role contracts.
- Separate company profiles cannot read, resolve, confirm, or mutate each other's records/previews.
- Representative reads and writes cover every domain; immediate commits, previews, confirmations,
  expired previews, approval-pending requests, permission denials, disabled modules, ambiguous
  entities, truncation, and server failures retain their correct states.
- Employee, manager/HR, sales, finance, and external-user skill evaluations pass.
- Packaged archives—not repository builds—pass install, auth, `doctor`, completion, credential,
  adapter, and verified-upgrade smoke tests on macOS and Linux.
- Packaged `talento tui` passes a real-terminal smoke test on macOS and Linux: launch/sign-in, read,
  form review, exact preview, profile isolation, resize, and clean exit.
- macOS binaries are Developer ID signed and notarized. Packaged Windows archives are not published.
- Checksums, Ed25519 signature, Sigstore bundle, SBOMs, and GitHub attestations verify.
- The artifact allowlist contains every published asset and no private repository content or secret.

The staging smoke portion is intentionally not run against production by unit tests. The release
workflow requires separately configured staging profiles and manual approval. A successful staging
run produces one JSON report matching `contracts/staging-evidence.schema.json`. The report contains
only synthetic tenant labels, role/tool/resource names, and expected/actual result states; it must
never contain access tokens, refresh tokens, customer data, record contents, or production IDs.
`contracts/staging-evidence-probes.json` is the reviewable producer contract for the exact probe IDs,
roles, kinds, and expected results; the report includes that file's byte SHA-256.

The protected `stable-release-gates` environment stores the base64-encoded report as the
`TALENTO_STAGING_EVIDENCE_BASE64` secret and its lowercase byte SHA-256 as the
`TALENTO_STAGING_EVIDENCE_SHA` variable. The workflow decodes the report only into the runner's
temporary directory. The gate verifies the digest before parsing the report, binds the report to
the exact byte SHA-256 of `schemas/gateway.json`, the probe contract, and the reviewed role-contract
files, and rejects role, tool, resource, or probe drift. A report must include every required
scenario with its exact expected result: representative resource/read/write behavior, preview and
confirmation states, approval-pending writes, role and skill coverage, cross-tenant denial,
permission/module denial, ambiguity, truncation, and server failures.

Evidence expires 24 hours after `captured_at` and timestamps more than five minutes in the future
are rejected. `TALENTO_STAGING_EVIDENCE_MAX_AGE_SECONDS` may configure a shorter release-environment
window, but the gate rejects zero, non-integers, and values above 86,400 seconds. Updating the evidence
secret without updating its protected SHA variable, or vice versa, fails closed. Failed, stale,
missing, duplicate, unexpected, or snapshot-inconsistent evidence blocks stable publication.

Stable assets are assembled into a private workflow artifact after platform signing. Every packaged
archive in that artifact must pass the Linux and macOS smoke matrices before a separate job
can create the public GitHub release. Homebrew publication starts only after that GitHub
release job succeeds. Preview releases retain their prerelease-first workflow.
