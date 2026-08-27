# Staging role and tenant contracts

Stable release validation uses dedicated staging users for `admin`, `manager`, `employee`, and
`external`, with module/permission combinations recorded under `contracts/roles/`. Fixture entries
contain only tool/resource names and synthetic record labels—never tokens, customer data, or internal
production IDs.

Each role capture must record its timestamp, gateway snapshot digest, tenant label, enabled modules,
available tools/resources, and representative read/write probes. External users select exactly one
company. Cross-tenant probes use disposable records in two staging companies and expect denial or
not-found without revealing whether the foreign record exists.

The checked-in role files are the reviewable contract shape; their nullable `captured_at` field is not
accepted as proof that a current live run succeeded. Current live captures and write probes belong in
the versioned report shape defined by `contracts/staging-evidence.schema.json`, not in unit-test
inputs or checked-in passing evidence. Each role capture in that report includes the SHA-256 of its
reviewed contract file, so a contract edit invalidates previously captured evidence. `1.0.0` remains
blocked until a current report is installed in the protected release environment with its matching
SHA-256.
