# Architecture

The executable embeds a reviewed MCP schema snapshot, coverage manifest, canonical Agent Skill, and
native plugin packages. Cobra commands are registered from the manifest and their public flags are
derived from JSON Schema. Curated domain names improve discovery; raw tools/resources preserve full
gateway parity.

The snapshot parser preserves each input schema losslessly and resolves every schema with the
standards-compliant Draft 7 / Draft 2020-12 validator while the command tree is built. At execution,
JSON input sources and explicit flags are merged first, then the complete schema is validated before
the MCP client is created. Local validation is syntactic only; business rules, tenant boundaries,
permissions, preview/approval state, and live tool availability remain gateway decisions. Validation
diagnostics expose a deterministic JSON path and constraint without echoing rejected values.

The public Cobra tree is also serialized as an append-only `.surface` history. CI regenerates the
live tree in memory and compares it with the indexed snapshot. Compatible additions require a new
snapshot; removals and semantic changes require exact, fingerprint-bound approvals. See
[cli-surface.md](cli-surface.md).

Runtime data flows through one path:

```text
command -> selected profile -> OAuth access token -> official MCP Go SDK -> generic gateway
```

Profile selection short-circuits on explicit `--profile` and `TALENTO_PROFILE`, then considers the
nearest trusted `.talento/config.json`, then the global default. Project discovery starts from the
canonical working directory, refuses symlinked marker/config entries, reads one bounded stable file,
and accepts only a strict `{"profile":"name"}` object. Interactive trust is either process-only or
persisted as canonical project/config paths plus the exact-byte SHA-256 and profile. Any mismatch is
stale; every non-interactive mode fails closed before credential access or MCP connection. Project
files and trust records contain no endpoint or secret material.

OAuth is intentionally outside the MCP SDK. Discovery, Dynamic Client Registration, loopback
callback, state validation, PKCE, refresh, and revocation live under `internal/auth`. Secret material
is isolated behind the credential-store interface; the owner-only JSON config contains only
non-secret profile metadata, project trust bindings, and managed-file digests.

Machine output retains the SDK's complete MCP result, including content blocks, structured content,
metadata, errors, and resource bodies. The presentation layer adds state and summaries without
replacing the source result.

Configuration updates use a lock file, same-directory temporary file, sync, atomic rename, and
Windows rollback path. Agent files have the same transactional model. Only tracked files are updated
or removed; modified files require `--force` and receive timestamped backups.

Coding-agent integrations are capability adapters rather than command-specific conditionals. An
adapter owns detection, an atomic managed-file install/remove plan, diagnosis, and a bounded
executable version probe isolated in a disposable home/config/temp directory. Adapter records store
only non-secret method, scope, canonical path, version, digest, and update time. Native mutation is
permitted only when a stable vendor contract is
source-backed; otherwise the adapter uses the managed-file path.
