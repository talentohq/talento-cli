# Talento CLI

`talento` is the native, scriptable TalentoHQ client. It connects only to the generic
Streamable HTTP MCP gateway at `https://mcp.talentohq.com/mcp`, owns a named OAuth grant for
each company profile, and exposes the complete reviewed gateway surface as stable commands.

The CLI does not recreate Talento business logic. Account, module, role, permission, visibility,
tenant scope, calculations, and write semantics remain server-authoritative.

> `0.1.x` releases are previews. Stable `1.0.0` is blocked by the cross-role, signed-platform,
> and packaged-release gates in [docs/release-gates.md](docs/release-gates.md).

## Install

Released builds support macOS, Linux, and Windows on amd64 and arm64.

```sh
brew install talentohq/tap/talento
scoop bucket add talentohq https://github.com/talentohq/scoop-bucket
scoop install talento
go install github.com/talentohq/talento-cli/cmd/talento@latest
```

The release page also contains signed checksums, archives, deb/rpm/apk packages, Nix instructions,
`install.sh`, and `install.ps1`. The direct installers choose the newest stable release when one
exists and otherwise choose the newest preview; set `TALENTO_CHANNEL=preview` or `stable` to pin a
channel, or `TALENTO_VERSION=0.1.2` to install an exact release. Direct installers require `cosign`
and fail closed if the exact release workflow identity cannot be verified. Stable Windows installs
also require the release-stamped Authenticode publisher policy. Existing direct installations are
preserved and restored if candidate or post-install version validation fails. Until the first
release exists, build locally with Go 1.26 or newer:

```sh
go build -o talento ./cmd/talento
```

## Start

```sh
talento setup
talento commands --available
talento people list --query Ana
```

On a terminal, `talento setup` guides profile selection, OAuth, local-agent integration, optional
shell completion guidance, and a final health check. Running bare `talento` offers this flow only
when no usable authenticated profile is selected. You can still run each stage directly:

```sh
talento auth login --profile acme
talento profile set-default acme
talento setup --agent codex --scope user --yes --json
```

OAuth discovers the generic gateway, dynamically registers a public client, opens the browser, and
uses authorization-code PKCE. Tokens go to the operating-system credential store. If a system store
is unavailable, file credentials require explicit opt-in:

```sh
talento auth login --allow-file-credentials
export TALENTO_ALLOW_FILE_CREDENTIALS=1
```

The fallback is reported clearly and stored owner-only. The CLI never accepts an account URL or API
token as an authentication fallback.

### Repository profile selection

A repository can select an existing global profile with one minimal file:

```json
{"profile":"acme"}
```

Save it as `.talento/config.json`. The nearest ancestor wins in nested worktrees. The file may
contain only `profile`; endpoints, tokens, credential paths, commands, environment values, and
extension fields are rejected. The CLI does not follow a symlinked `.talento` directory or config
file.

The first interactive use offers use once, always trust this exact file, or cancel. Persistent trust
is non-secret metadata bound to the canonical project/config path, selected profile, and SHA-256 of
the exact file bytes. Any edit makes the record stale. Non-interactive modes, including `--yes`,
fail closed until automation opts in explicitly:

```sh
talento profile project-status .
talento profile trust-project .
talento profile untrust-project .
```

Selection precedence is `--profile`, `TALENTO_PROFILE`, a trusted nearest-ancestor selector, then
the global default. Explicit flag or environment selection bypasses project discovery. The local
file can name only an already configured global profile and never creates or authenticates one.

## Output and writes

```sh
talento people list --json
talento people list --agent
talento people list --jq '.data.result.content'
talento reports create-changelog --md
```

- Human output is concise by default.
- `--json` returns `{ok, data, summary, breadcrumbs, meta}`.
- `--agent` disables prompts and returns data-only successes plus structured errors.
- `--md` renders presentation-friendly output.
- `--jq` is built in; no external `jq` executable is required.
- `--agent`, `--json`, `--md`, `--jq`, `--yes`, `TALENTO_NONINTERACTIVE=1`, `CI`, and redirected
  input or output disable onboarding and sign-in prompts. They never open a browser unexpectedly.

Each live MCP result determines write behavior. An immediate commit is reported as committed. A
preview is shown but not executed in non-interactive mode; `--yes` confirms only the preview created
by that command. `talento action confirm <preview-id>` confirms an explicit saved preview. Approval
requests remain `submitted_for_approval` and are never described as approved.

## Schema-driven input

Every generated tool command validates its final merged argument object against the complete
embedded Draft 2020-12 JSON Schema before opening an MCP connection. `--input` and `--input-file`
accept one JSON object and are mutually exclusive; explicit schema-derived flags override matching
keys in that object. Invalid types, enums, nested values, required properties, numeric bounds, and
unsupported properties fail locally as usage errors. Talento business rules and permissions remain
server-authoritative.

Object and complex-array flags accept one complete JSON value. Scalar arrays support a repeatable
`--<name>-item` flag; values are never comma-split, so literal commas are preserved. The incumbent
base flag remains the JSON escape hatch and is the way to pass an empty array:

```sh
talento tasks create-task --name "Follow up" --tags-item urgent --tags-item "sales,emea"
talento tasks create-task --name "Follow up" --tags '[]'
talento invoices create --invoice-lines '[{"item_name":"Consulting","quantity":2}]'
```

Enum flags and enum-valued scalar-array item flags expose shell completion candidates. Required
fields are documented in `--help` but may also be satisfied by `--input` or `--input-file`.

## Commands

The generated domains are `people`, `time`, `absences`, `expenses`, `schedules`, `appointments`,
`projects`, `tasks`, `todos`, `documents`, `goals`, `skills`, `evaluations`, `surveys`, `trainings`,
`recruitment`, `onboarding`, `crm`, `customers`, `contacts`, `leads`, `opportunities`, `invoices`,
`purchases`, `providers`, `items`, `views`, and `reports`.

Raw parity and operational commands remain available:

```text
talento tools list|describe|call
talento resources list|read
talento commands [--available]
talento doctor [--verbose]
talento setup [--agent <id>...] [--scope user|project] [--no-open] [--allow-file-credentials]
talento profile trust-project|untrust-project|project-status [path]
talento skill status [--integration <id>...] [--verbose]
talento skill install|update|remove --agent <id>... [--scope user|project]
talento handoff <chatgpt|claude|cursor-cloud|copilot-cloud|generic>
```

Run `talento --agent --help` for machine-readable command discovery. See
[docs/coverage.md](docs/coverage.md) for the schema/manifest contract and
[docs/agent-integration.md](docs/agent-integration.md) for local and hosted agents. Human help is
grouped by workflow; `talento help output|profiles|writes|exit-codes|environment|agents` opens the
operational help topics. The complete command-and-flag compatibility policy lives in
[docs/cli-surface.md](docs/cli-surface.md).

## Development

```sh
go mod verify
go test -race ./...
go vet ./...
go run ./cmd/schemagen -check
go run ./cmd/surfacegen -check
```

No telemetry is collected. This repository is MIT licensed.
