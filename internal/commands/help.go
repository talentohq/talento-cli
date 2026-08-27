package commands

import (
	"strings"

	"github.com/spf13/cobra"
)

func newHelpTopics() []*cobra.Command {
	topics := []struct {
		use, short, long string
	}{
		{
			use: "output", short: "Output modes and stable machine envelopes.", long: `Talento defaults to concise human output.

--json returns the stable {ok, data, summary, breadcrumbs, meta} envelope.
--agent disables prompts and returns data-only successes plus structured errors.
--md renders presentation-friendly Markdown.
--jq filters the built-in JSON representation without an external jq process.

--json and --md are mutually exclusive. --agent and --md are mutually exclusive.
Use one machine mode consistently in scripts; stdout contains data and stderr contains errors.`,
		},
		{
			use: "profiles", short: "Named profiles, OAuth, and credential storage.", long: `A profile names one company OAuth grant. Select it with --profile, TALENTO_PROFILE,
the nearest trusted project selector, or the global default configured by talento profile set-default.

A repository may contain only this minimal local file:
  .talento/config.json  ->  {"profile":"acme"}

The CLI never reads endpoints or credentials from a project. An untrusted or edited file requires
an interactive use-once/trust/cancel decision, and non-interactive use fails closed. Inspect or
manage the exact-file trust record with:
  talento profile project-status [path]
  talento profile trust-project [path]
  talento profile untrust-project [path]

Create and authenticate a profile with:
  talento auth login --profile acme

Tokens are stored in the operating-system credential store. Owner-only file credentials
require explicit opt-in. Profile and status output never includes tokens.`,
		},
		{
			use: "writes", short: "Preview, confirmation, approval, and committed states.", long: `The gateway is authoritative about whether a tool is a read or write and what state it returns.

preview                  Nothing has executed. Confirm the exact preview interactively, with
                         --yes on the originating command, or with talento action confirm.
submitted_for_approval   A request exists but is not approved or committed yet.
committed                The action completed and persisted.

Non-interactive modes never confirm a preview unless --yes was explicitly supplied.`,
		},
		{
			use: "exit-codes", short: "Stable process exit codes for automation.", long: `Talento uses stable process exit codes:

0 success                 1 usage or invalid input
2 not found               3 authentication required
4 forbidden               5 rate limited
6 network failure         7 API or unexpected failure
8 ambiguous match

With --json or --agent, stderr also contains a structured error code and message.`,
		},
		{
			use: "environment", short: "Environment variables and non-interactive behavior.", long: `TALENTO_PROFILE selects a named profile and bypasses project-config discovery. TALENTO_NONINTERACTIVE=1 and CI disable prompts.
TALENTO_ALLOW_FILE_CREDENTIALS=1 opts in to owner-only file credentials when the system
store is unavailable. TALENTO_CONFIG_DIR and TALENTO_HOME override local paths for isolated
environments and testing.

--agent, --json, --md, --jq, --yes, CI, TALENTO_NONINTERACTIVE, and redirected input or
output prevent setup and sign-in prompts.`,
		},
		{
			use: "agents", short: "Coding-agent setup, health, and hosted handoff.", long: `talento setup detects supported local coding agents and installs only Talento-managed files.
Use talento skill status to inspect runtime and managed-file health, and talento doctor for
the complete CLI health report. Install, update, and remove operations support user or project
scope; modified managed files require --force and receive a backup.

Hosted agents authenticate directly to the TalentoHQ MCP gateway. Run talento handoff --help
for supported hosted-agent guidance; never paste a local CLI token into another product.`,
		},
	}
	commands := make([]*cobra.Command, 0, len(topics))
	for _, topic := range topics {
		commands = append(commands, &cobra.Command{
			Use: topic.use, Short: topic.short, Long: strings.TrimSpace(topic.long),
		})
	}
	return commands
}
