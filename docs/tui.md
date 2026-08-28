# Terminal workspace

`talento tui` is a human-operated front end to the same fixed TalentoHQ MCP gateway as the CLI.
It needs TalentoHQ OAuth, not an AI account, subscription, API key, or coding agent.

```sh
talento tui
talento tui --profile acme
talento tui --no-open
talento tui --allow-file-credentials
```

The selected profile follows the ordinary flag, environment, trusted-project, and global-default
precedence. Project trust is resolved before the full-screen interface starts. Use
`talento auth login --profile <name>` to create a named profile first. With no existing selection,
the workspace offers explicit sign-in for a new `default` profile. It never installs coding agents.

`--no-open` displays the OAuth authorization URL instead of launching a browser. File credentials
still require the existing explicit opt-in. An in-app profile switch is temporary: it does not
change the saved default or project selector. The candidate profile must connect before it replaces
the current one, and switching clears the previous company's results, forms, and recent actions.

## Navigation and discovery

Workspace shortcuts open forms; entering the workspace does not automatically fetch personal or
business records. Available actions are grouped into People & HR, Work, Sales, Finance, and Content
& reports. Empty groups are hidden. Resources and Advanced / Live schema have separate groups.

| Key | Action |
| --- | --- |
| `/` | Search available actions and resources. |
| `Ctrl+P` | Switch an existing profile for this session. |
| `Tab` / `Shift+Tab` | Move focus; move between fields inside a form. |
| `Enter` | Activate the selected control. |
| `Ctrl+S` | Validate a form, then run a read or review a write. |
| `F4` / `Ctrl+J` | Switch a reviewed form to or from exact JSON input. |
| `F2` | Inspect the live schema or resource template. |
| `j` | Toggle the result's source JSON outside text editing. |
| `Ctrl+R` | Refresh the current read or capability catalogue. |
| `Esc` | Go back or cancel a read; edited forms require discard confirmation. |
| `?` | Show help outside text editing. |
| `Ctrl+C` | Exit, with protection for edited forms and in-flight actions. |

Global character shortcuts do not intercept form typing. The footer describes the controls for the
current screen. Results scroll and retain the last successful content when a refresh fails, marked
as stale. Recent actions exist only in memory and are cleared when changing companies.

## Forms and results

Live `tools/list` determines availability. A tool whose schema matches the reviewed snapshot gets
a generated form. New or changed live schemas remain available under **Advanced / Live schema**
through an exact JSON-object editor; these are not presented as reviewed forms. An invalid schema
is inspectable but cannot be executed in the TUI.

Fields preserve the difference between an omitted value and an explicit empty string, empty array,
false, zero, or null. Defaults are suggestions, not silently submitted values. Complex values use
JSON editing. The complete input is validated with the same JSON Schema validator as the CLI.
`Ctrl+O` changes field presence; the form footer shows available value controls. Use `F4` if your
terminal sends Enter for `Ctrl+J`. JSON input rejects duplicate keys and numbers that would lose
precision. Input is bounded to 8 MiB; the JSON editor also has a 10,000-line limit.
Availability and schema revision are checked again immediately before execution; a changed schema
requires another review without discarding the draft.

Results show sanitized server text, with the full source result available for structured inspection.
The TUI does not derive record IDs, pagination, permissions, or editable rows from prose. It does
not recalculate totals or interpret a preview as a committed write.

Resource discovery includes live concrete resources and URI templates. For compatibility with the
current gateway, a live resource whose URI exactly matches a reviewed legacy template is presented
as a template. Nothing in the embedded snapshot alone makes a resource available. Template fields
are expanded into the URI before reading it; resources are not presented as browseable collections.

Attachment-reference fields refer to gateway attachment handles. They are not local filesystem
paths, and this version does not upload local files or fetch arbitrary URLs.

## Write safety and recovery

Every write has a local argument-review screen before its first call. This is **not** a server
preview: submission may execute immediately. High-impact actions are visibly identified.

If Talento returns a preview, review it separately and confirm its exact ID. Confirmation is bound
to the originating session and can be dispatched only once. Missing IDs cannot be confirmed. Editing
the form, switching profiles, reconnecting, or reauthenticating invalidates pending confirmation.
Use the ordinary `talento action confirm` command for an explicit saved-preview workflow outside
this session; the TUI never confirms an unspecified "latest action."

The server decides whether a result is returned, previewed, committed, submitted for approval, or
rejected. If a write response is lost, the TUI reports **outcome unknown**: the server may already
have executed it. Inspect the current state before taking another action. The TUI never retries a
write automatically, and canceling a client request does not guarantee server-side cancellation.

Ordinary read refreshes are explicit. Superseded reads are canceled and late responses cannot replace
the current view. The application performs no background mutations or optimistic record updates.

## Terminal requirements and privacy

Use a real terminal on stdin and stdout, ideally at least 80 columns by 24 rows. Narrow layouts stack
panes; very small terminals show a resize notice. `NO_COLOR` disables color, and status remains
readable without it. The application restores terminal settings and the alternate screen on exit.

The TUI rejects redirected input/output, `TERM=dumb`, `CI`, `TALENTO_NONINTERACTIVE`, and the
`--json`, `--md`, `--agent`, `--jq`, and `--yes` flags before sign-in or screen takeover. Its help is
available without a terminal. Use ordinary CLI commands for automation and machine output.

No result cache, history file, clipboard integration, telemetry, external shell execution, or AI
service is added by the TUI. OAuth secrets stay in the existing credential store. Remote labels,
descriptions, values, and errors are sanitized before terminal styling; source results remain
unchanged for inspection.
