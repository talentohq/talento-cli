# Gateway coverage

`schemas/gateway.json` is the reviewed versioned gateway snapshot. `coverage/manifest.json` records a
stable domain/command decision for all 151 tools and a raw resource decision for all 17 resources in
snapshot version 1. The manifest includes the exact snapshot SHA-256 digest.

`go run ./cmd/schemagen -check` fails if the digest, required inputs, duplicate command paths, tool
set, or resource set drifts. The root command contract test also resolves every mapped tool path.

The live gateway may expose a smaller subset for a selected company grant. `talento commands
--available`, `tools list`, and `resources list` are authoritative for that profile. A live entry
unknown to the embedded manifest makes `doctor` fail; a missing entry does not, because role,
permission, visibility, module, and tenant rules intentionally reduce availability.

Features not exposed through MCP are explicit out-of-scope gaps, not client-side reimplementations.
Refreshes must come from a reviewed generic-gateway or staging snapshot with all supported modules,
then regenerate the manifest and review every mapping before merge.
