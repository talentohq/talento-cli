# Talento CLI

`talento` is a native command-line client for TalentoHQ. Use it to find and update people, time,
absences, expenses, projects, tasks, documents, training, recruitment, CRM, invoices, reports, and
the other capabilities available to your TalentoHQ account.

The CLI connects to the fixed TalentoHQ MCP gateway at `https://mcp.talentohq.com/mcp` and signs in
with OAuth. Each named profile represents one company grant, so you never need to enter an account
URL or paste an API token. TalentoHQ remains authoritative for company access, enabled modules,
roles, permissions, visibility, calculations, approvals, and whether a write is previewed or
committed.

## Install

Packaged releases cover macOS and Linux on amd64 and arm64. Windows is not published until Authenticode signing is available; you can still build from source with Go.

### Homebrew (macOS, Linux)

```sh
brew tap talentohq/tap
brew install --cask talento
talento version
```

### Verified script (macOS, Linux)

Install [cosign](https://docs.sigstore.dev/cosign/system_config/installation/), then:

```sh
curl -fsSL https://github.com/talentohq/talento-cli/releases/latest/download/install.sh | sh
talento version
```

The installer verifies the checksum manifest with Sigstore (OIDC identity `https://github.com/talentohq/talento-cli/.github/workflows/release.yml@refs/tags/v<version>`) before replacing the binary. Default destination is `/usr/local/bin`; override with `TALENTO_INSTALL_DIR`. Pin a version with `TALENTO_VERSION=1.0.0`.

### Nix

```sh
nix profile install github:talentohq/talento-cli/v1.0.0
# or one-shot
nix run github:talentohq/talento-cli/v1.0.0 -- version
```

### Go

You need [Go](https://go.dev/doc/install) 1.26.7 or newer.

```sh
go install github.com/talentohq/talento-cli/cmd/talento@v1.0.0
talento version
```

`go install` writes the binary to `GOBIN`, or to `$(go env GOPATH)/bin` when `GOBIN` is unset.

### Linux packages

Debian/Ubuntu (`deb`), Fedora/RHEL (`rpm`), and Alpine (`apk`) packages are attached to each [GitHub release](https://github.com/talentohq/talento-cli/releases).

See [Distribution and verification](docs/distribution.md) for checksum, Sigstore, attestation, and `talento upgrade` behavior.

## Quickstart

Run the interactive setup wizard:

```sh
talento setup
```

Setup creates or selects a named profile, opens TalentoHQ OAuth in your browser, verifies the grant,
offers to configure detected local coding agents, shows optional shell-completion guidance, and
finishes with a health check. It does not ask for a company URL or API token.

After setup, verify the installation and discover what your selected grant can use:

```sh
talento doctor
talento commands --available
talento people list --name Ana
```

`commands --available` is profile-aware: it shows only the reviewed commands backed by tools that
the selected TalentoHQ grant currently exposes.

### Terminal workspace

Open the keyboard-driven workspace with your selected profile:

```sh
talento tui
talento tui --profile acme
```

The TUI uses the same TalentoHQ OAuth profiles as ordinary commands. It does not require an AI
subscription or coding agent. Browse grouped actions, press `/` to search, fill in a form, and use
`Ctrl+S` to run a read or review a write. `Ctrl+P` switches profiles for this session only, `Ctrl+R`
refreshes reads, and `?` shows contextual help.

Writes require a deliberate review before submission; a server preview requires a separate exact
confirmation. Results remain server-authoritative. The workspace is text-first, with a structured
result inspector, and does not parse server prose into editable record tables.

The TUI requires a real terminal and cannot be combined with machine-output flags or `--yes`.
Bare `talento` still shows help or offers setup. See [Terminal workspace](docs/tui.md) for forms,
authentication, write states, and terminal requirements.

## Configure authentication and profiles

### Sign in without the setup wizard

If you only want to configure the CLI and not a coding-agent integration, authenticate a profile
directly:

```sh
talento auth login --profile acme
talento auth status --profile acme
talento profile set-default acme
```

`auth login --profile <name>` creates the named profile when it does not exist. Use profiles for
different companies or grants:

```sh
talento profile list
talento profile show acme
talento people list --profile acme --name Ana
```

Profile selection follows this precedence:

1. `--profile <name>`
2. `TALENTO_PROFILE`
3. The nearest trusted repository selector
4. The global default set with `talento profile set-default <name>`

An explicit flag or environment variable bypasses repository-profile discovery.

### Credential storage

OAuth tokens are stored in the operating-system credential store. If the system store is
unavailable, the CLI fails closed unless you explicitly opt in to an owner-only plaintext file:

```sh
talento auth login --allow-file-credentials
```

Or set the equivalent environment variable:

```sh
export TALENTO_ALLOW_FILE_CREDENTIALS=1
```

The fallback location is reported when it is used. Profile and status output never includes tokens.

### Select a profile per repository

A repository can select an existing global profile with `.talento/config.json`:

```json
{"profile":"acme"}
```

The nearest ancestor selector wins in nested worktrees. The file may contain only `profile`; it
cannot define an endpoint, token, credential path, command, environment value, or extension field.
The CLI does not follow a symlinked `.talento` directory or config file. The selected profile must
already exist in the global configuration; the repository file never creates or authenticates it.

On first interactive use, the CLI offers to use the selector once, trust its exact contents, or
cancel. An edit invalidates persistent trust. Inspect or manage trust explicitly with:

```sh
talento profile project-status .
talento profile trust-project .
talento profile untrust-project .
```

Non-interactive modes fail closed until the exact selector is trusted or a profile is selected with
`--profile` or `TALENTO_PROFILE`.

### Shell completion

Generate completion for Bash, Zsh, Fish, or PowerShell. For the current shell session:

```sh
# Bash
source <(talento completion bash)

# Zsh
source <(talento completion zsh)

# Fish
talento completion fish | source
```

```powershell
# PowerShell
talento completion powershell | Out-String | Invoke-Expression
```

Run `talento completion --help` and follow your shell's documentation to install completion
persistently.

### Environment variables

| Variable | Purpose |
| --- | --- |
| `TALENTO_PROFILE` | Select a named profile and bypass repository-profile discovery. |
| `TALENTO_NONINTERACTIVE=1` | Disable setup, authentication, and confirmation prompts. |
| `TALENTO_ALLOW_FILE_CREDENTIALS=1` | Allow owner-only file credentials when the system store is unavailable. |
| `TALENTO_CONFIG_DIR` | Override the configuration directory for an isolated environment. |
| `TALENTO_HOME` | Override the home directory used for managed agent integrations. |
| `CI` | Disable interactive behavior in continuous integration. |

Non-secret profile and integration metadata is stored under the operating system's user
configuration directory. Run `talento doctor --verbose` to see the resolved path safely.

Machine-output flags, `--yes`, redirected input, and redirected output also disable onboarding and
sign-in prompts. They never open a browser unexpectedly.

## Use the CLI

### Discover commands

Human help is grouped by workflow, and each generated domain documents its available commands and
schema-derived flags:

```sh
talento --help
talento commands
talento commands --available
talento people --help
talento people list --help
```

The generated domains are `people`, `time`, `absences`, `expenses`, `schedules`, `appointments`,
`projects`, `tasks`, `todos`, `documents`, `goals`, `skills`, `evaluations`, `surveys`, `trainings`,
`recruitment`, `onboarding`, `crm`, `customers`, `contacts`, `leads`, `opportunities`, `invoices`,
`purchases`, `providers`, `items`, `views`, and `reports`.

Additional operational help is available through:

```text
talento help output
talento help profiles
talento help writes
talento help exit-codes
talento help environment
talento help agents
```

### Read and search

Generated flags use kebab-case versions of the gateway input names:

```sh
talento people list --name Ana
talento people list --office-id 42 --team-id 7
talento people get --employee-id 123
```

Your grant may expose only part of the complete command catalogue. If a capability is unavailable,
the CLI reports it rather than trying to reproduce TalentoHQ business logic locally.

### Pass structured input

Every generated tool command validates the final input against its embedded JSON Schema before it
authenticates or opens an MCP connection. Pass one JSON object directly or from a file:

```sh
talento tasks create-task --input '{"name":"Follow up","tags":["urgent"]}'
talento tasks create-task --input-file task.json
```

`--input` and `--input-file` are mutually exclusive. Explicit schema-derived flags override matching
keys in that object. Object and complex-array flags accept a complete JSON value; scalar arrays also
provide repeatable item flags:

```sh
talento tasks create-task --name "Follow up" --tags-item urgent --tags-item "sales,emea"
talento tasks create-task --name "Follow up" --tags '[]'
talento invoices create --invoice-lines '[{"item_name":"Consulting","quantity":2}]'
```

Values for repeatable array flags are not comma-split, so literal commas are preserved. Invalid
types, enum values, required properties, nested values, numeric bounds, and unsupported properties
fail locally as usage errors. TalentoHQ still enforces permissions and business rules.

### Choose an output format

```sh
talento people list --json
talento people list --md
talento people list --agent
talento people list --jq '.data'
```

- Human output is concise by default.
- `--json` returns the stable `{ok, data, summary, breadcrumbs, meta}` envelope.
- `--md` renders presentation-friendly Markdown.
- `--agent` disables prompts and returns data-only successes with structured errors.
- `--jq` filters the built-in JSON representation; no external `jq` executable is required.

Use one machine format consistently in scripts. `--json` and `--md` are mutually exclusive, as are
`--agent` and `--md`. Run `talento --agent --help` for machine-readable command discovery.

### Understand writes

The live MCP response determines the state of every write:

- `preview`: nothing has executed. Confirm interactively, pass `--yes` to the originating command,
  or confirm the saved preview explicitly with `talento action confirm <preview-id>`.
- `submitted_for_approval`: a request exists, but it is not approved or committed.
- `committed`: TalentoHQ reports that the action completed and persisted.

Non-interactive modes never confirm a preview unless `--yes` was explicitly supplied. `--yes`
confirms only the preview created by that command; it does not bypass approval or permission checks.

For example, the following command may commit, return a preview, request approval, or fail according
to the server response and selected grant:

```sh
talento reports create-changelog --title "Weekly update" --content "Completed onboarding"
```

### Use raw MCP tools and resources

Generated commands cover the reviewed gateway surface. Raw commands remain available for discovery
and forward-compatible access:

```sh
talento tools list
talento tools describe list_employees
talento tools call list_employees --input '{"name":"Ana"}'

talento resources list
talento resources read '<uri-from-resources-list>'
```

Raw tool calls use the same authentication, schema validation, output, and write-state handling as
generated commands.

## Configure coding agents

Interactive `talento setup` can detect and configure supported local agents. You can also manage
integrations directly:

```sh
talento skill status
talento skill install --agent codex --scope user
talento skill update --agent codex --scope user
talento skill remove --agent codex --scope user
```

Supported IDs are `claude-code`, `codex`, `gemini`, `copilot`, `cursor`, `windsurf`, and `opencode`.
Use `--scope project` to install in the current project instead of the user scope. The CLI changes
only files it owns, preserves user modifications, and requires `--force` before replacing a modified
managed file; forced replacement creates a timestamped backup.

Hosted agents cannot use local files or credentials. Generate product-specific connection guidance
and let the hosted product complete its own OAuth flow:

```sh
talento handoff chatgpt
talento handoff claude
talento handoff generic
```

Never paste a local CLI token into another product. See
[Agent integration](docs/agent-integration.md) for supported adapters, scopes, and hosted-agent
caveats.

## Troubleshooting and maintenance

Start with the health report:

```sh
talento doctor
talento doctor --verbose
```

The verbose report includes safe paths and integrity details, never credentials or prompt content.
These commands help isolate common setup problems:

```sh
talento version
talento auth status
talento profile list
talento profile project-status .
talento commands --available
talento skill status --verbose
```

Automation can rely on these stable exit codes:

| Code | Meaning |
| ---: | --- |
| `0` | Success |
| `1` | Usage or invalid input |
| `2` | Not found |
| `3` | Authentication required |
| `4` | Forbidden |
| `5` | Rate limited |
| `6` | Network failure |
| `7` | API or unexpected failure |
| `8` | Ambiguous match |

With `--json` or `--agent`, errors also include a structured code and message on standard error.

## More documentation

- [Architecture](docs/architecture.md)
- [Agent integration](docs/agent-integration.md)
- [Command-surface compatibility](docs/cli-surface.md)
- [Gateway coverage contract](docs/coverage.md)
- [Distribution and verification](docs/distribution.md)
- [Release gates](docs/release-gates.md)
- [Release runbook](docs/release.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)

## Development

```sh
go mod verify
go test -race ./...
go vet ./...
go run ./cmd/schemagen -check
go run ./cmd/surfacegen -check
```

No telemetry is collected. This repository is [MIT licensed](LICENSE).
