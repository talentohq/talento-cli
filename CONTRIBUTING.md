# Contributing

Keep the CLI a thin client of the generic TalentoHQ MCP gateway. Do not duplicate authorization,
tenant scoping, module rules, calculations, or write policy locally.

Before proposing a change:

```sh
go mod verify
go fmt ./...
go vet ./...
go test -race ./...
go run ./cmd/schemagen -check
sh scripts/test-installers.sh
skills-ref validate skills/talento
python3 /path/to/plugin-creator/scripts/validate_plugin.py plugins/talento
```

Schema changes require a reviewed gateway snapshot and an explicit mapping for every added, removed,
renamed, or input-incompatible tool/resource. Never hand-edit only the digest in
`coverage/manifest.json`.

Changes to `skills/talento/` must be copied into the Codex and Claude Code plugin packages; the
contract test rejects drift. Keep workflow guidance progressive and capability discovery live.

Tests use local HTTP servers and isolated homes. Never use production grants or customer data in the
unit suite. Staging and stable-release gates are explicit, separately authorized operations.

TUI tests drive the Bubble Tea models with fake sessions; Linux/macOS also run a real Unix PTY helper
with an in-memory backend. Keep both the CLI one-shot behavior and session-bound write safety covered.
Changing a TUI dependency requires regenerating and reviewing `vendor` as well as `go.mod`/`go.sum`.
Run `go run ./cmd/surfacegen -diff`, append the next snapshot with `-next`, and verify with `-check`
when adding a public command or flag; never rewrite an existing surface snapshot.

Release tags are `vVERSION`, while every packaged metadata surface uses `VERSION`. Run
`scripts/stamp-nix-version.sh VERSION` and commit `nix/version.nix` before creating either a preview
or stable tag. The release workflow verifies the tag, Nix stamp, binary provenance, embedded Codex
and Claude Code manifests, and generated Homebrew/Scoop metadata before publishing anything.
Stable release configuration must also define `TALENTO_WINDOWS_AUTHENTICODE_PUBLISHER` as the exact
certificate subject returned by `Get-AuthenticodeSignature` for the protected Windows signing
certificate. `cmd/packageextras` stamps it into the released PowerShell installer; an empty or
unstamped policy is intentionally unusable for stable direct installs. Run
`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-installers.ps1` on Windows
PowerShell 5.1 when changing installer behavior.
