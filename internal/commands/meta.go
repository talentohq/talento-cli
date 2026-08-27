package commands

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/buildinfo"
	"github.com/talentohq/talento-cli/internal/config"
)

type versionView struct {
	buildinfo.Info
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func (v versionView) HumanText() string {
	return fmt.Sprintf("talento %s (%s/%s, commit %s, built %s)", v.Version, v.OS, v.Arch, v.Commit, v.Date)
}

func newVersionCommand(talento *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Show CLI version and build provenance.", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			view := versionView{Info: buildinfo.Current(), GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
			return talento.Output().Success(view, "Version information.", nil, nil)
		},
	}
}

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	command := &cobra.Command{Use: "completion <bash|zsh|fish|powershell>", Short: "Generate a shell completion script.", Args: cobra.ExactArgs(1)}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		switch strings.ToLower(args[0]) {
		case "bash":
			return root.GenBashCompletion(cmd.OutOrStdout())
		case "zsh":
			return root.GenZshCompletion(cmd.OutOrStdout())
		case "fish":
			return root.GenFishCompletion(cmd.OutOrStdout(), true)
		case "powershell":
			return root.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
		default:
			return fmt.Errorf("unsupported shell %q; choose bash, zsh, fish, or powershell", args[0])
		}
	}
	return command
}

type handoffView struct {
	HostedAgent string   `json:"hosted_agent"`
	Endpoint    string   `json:"endpoint"`
	Transport   string   `json:"transport"`
	OAuth       string   `json:"oauth"`
	Steps       []string `json:"steps"`
	Caveat      string   `json:"caveat,omitempty"`
}

func (v handoffView) HumanText() string {
	lines := []string{
		"Connect " + v.HostedAgent + " directly to TalentoHQ:",
		"Endpoint: " + v.Endpoint,
		"Transport: " + v.Transport,
		"Authentication: " + v.OAuth,
	}
	for index, step := range v.Steps {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, step))
	}
	if v.Caveat != "" {
		lines = append(lines, "Note: "+v.Caveat)
	}
	return strings.Join(lines, "\n")
}

func newHandoffCommand(talento *app.App) *cobra.Command {
	command := &cobra.Command{
		Use: "handoff <hosted-agent>", Short: "Guide a hosted agent through its own direct MCP connection.", Args: cobra.ExactArgs(1),
		ValidArgs: []string{"chatgpt", "claude", "cursor-cloud", "copilot-cloud", "generic"},
		RunE: func(_ *cobra.Command, args []string) error {
			view, err := handoffFor(strings.ToLower(args[0]))
			if err != nil {
				return err
			}
			return talento.Output().Success(view, "Hosted-agent handoff instructions.", nil, nil)
		},
	}
	return command
}

func handoffFor(id string) (handoffView, error) {
	view := handoffView{
		HostedAgent: id, Endpoint: config.Endpoint, Transport: "Streamable HTTP", OAuth: "authorization-code OAuth in the hosted product",
		Steps: []string{
			"Open the product's MCP, connector, or integrations settings.",
			"Add the TalentoHQ endpoint exactly as shown; do not paste a local CLI token.",
			"Complete TalentoHQ OAuth and select the intended company.",
			"Verify the tools exposed to that hosted grant before acting.",
		},
	}
	switch id {
	case "chatgpt":
		view.HostedAgent = "ChatGPT"
		view.Caveat = "Workspace administrators may need to install or approve the connector before members can authenticate."
	case "claude", "claude.ai", "claude-desktop":
		view.HostedAgent = "Claude.ai / Claude Desktop"
		view.Caveat = "Organization policy may require an owner or administrator to approve the integration."
	case "cursor-cloud":
		view.HostedAgent = "Cursor Cloud"
		view.Caveat = "Use the hosted environment's MCP configuration, not the local Cursor wrapper installed by `talento setup`."
	case "copilot-cloud":
		view.HostedAgent = "GitHub Copilot Cloud"
		view.Caveat = "Repository or organization administrators may need to allow the MCP server."
	case "generic":
		view.HostedAgent = "Hosted MCP client"
		view.Caveat = "The client must support Streamable HTTP MCP and OAuth discovery; marketplace or administrator installation may be required."
	default:
		return handoffView{}, fmt.Errorf("unsupported hosted agent %q; choose chatgpt, claude, cursor-cloud, copilot-cloud, or generic", id)
	}
	return view, nil
}
