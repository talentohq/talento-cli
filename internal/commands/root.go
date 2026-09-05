package commands

import (
	"bytes"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/talentohq/talento-cli/internal/app"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/surface"
	"github.com/talentohq/talento-cli/internal/terminal"
)

const (
	groupGettingStarted = "getting-started"
	groupWork           = "work"
	groupDiscovery      = "discovery"
	groupIntegrations   = "integrations"
	groupMaintenance    = "maintenance"
)

var plannedDomains = []string{
	"people", "time", "absences", "expenses", "schedules", "appointments",
	"projects", "tasks", "todos", "documents", "goals", "skills", "evaluations",
	"surveys", "trainings", "recruitment", "onboarding", "meetings", "crm", "customers",
	"contacts", "leads", "opportunities", "invoices", "purchases", "providers",
	"items", "views", "reports",
}

var domainDescriptions = map[string]string{
	"people":        "Find employees and people-related reference data.",
	"time":          "Work with attendance, clock-ins, and live activities.",
	"absences":      "List, request, and update time off.",
	"expenses":      "List and file employee expenses.",
	"schedules":     "Inspect schedules, assignments, reschedules, and swaps.",
	"appointments":  "Work with booking calendars and appointments.",
	"projects":      "Discover project capabilities exposed by the current Talento profile.",
	"tasks":         "List and update project tasks and checklists.",
	"todos":         "Manage the current user's private personal todos.",
	"documents":     "Create documents and inspect document categories.",
	"goals":         "List goals, comments, and status updates.",
	"skills":        "Work with skills and competency frameworks.",
	"evaluations":   "Create evaluations and inspect results.",
	"surveys":       "Create surveys and inspect results.",
	"trainings":     "Author and manage training content and lifecycle.",
	"recruitment":   "Manage job offers and candidates.",
	"onboarding":    "Work with onboarding records, templates, and actions.",
	"meetings":      "Manage reusable question templates for 1:1s and hiring interviews.",
	"crm":           "Work with CRM configuration, queues, and commercial actions.",
	"customers":     "Find and maintain customer records.",
	"contacts":      "Find and maintain customer contacts.",
	"leads":         "Find, maintain, and convert leads.",
	"opportunities": "Manage the sales pipeline and opportunities.",
	"invoices":      "Prepare and manage sales invoices.",
	"purchases":     "Manage purchase orders and purchase invoices.",
	"providers":     "Find and maintain supplier records.",
	"items":         "Maintain reusable catalogue items.",
	"views":         "Edit and preview customizable public-page versions.",
	"reports":       "Run report-supporting reads and changelog operations.",
}

func NewRoot(snapshotData, manifestData []byte, managedFS fs.FS) (*cobra.Command, *app.App, error) {
	global := &app.GlobalOptions{}
	talento, err := app.New(snapshotData, manifestData, global)
	if err != nil {
		return nil, nil, err
	}
	root := newRootCommand(talento, managedFS, defaultSetupDependencies(talento, managedFS))
	return root, talento, nil
}

func newRootCommand(talento *app.App, managedFS fs.FS, setupDeps setupDependencies) *cobra.Command {
	global := talento.Global
	root := &cobra.Command{
		Use:   "talento",
		Short: "TalentoHQ from the command line",
		Long:  "Talento is a native, scriptable client for the TalentoHQ MCP gateway.",
		Example: strings.Join([]string{
			"  talento setup",
			"  talento commands --available",
			"  talento people list --name Ana",
			"  talento people list --name Ana --json",
			`  talento reports create-changelog --title "Weekly update" --content "Completed onboarding"`,
		}, "\n"),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddGroup(
		&cobra.Group{ID: groupGettingStarted, Title: "Get started:"},
		&cobra.Group{ID: groupWork, Title: "Talento work:"},
		&cobra.Group{ID: groupDiscovery, Title: "Discovery and raw MCP:"},
		&cobra.Group{ID: groupIntegrations, Title: "Coding-agent integration:"},
		&cobra.Group{ID: groupMaintenance, Title: "Maintenance and shell support:"},
	)
	root.SetHelpCommandGroupID(groupMaintenance)
	root.SetOut(talento.Stdout)
	root.SetErr(talento.Stderr)
	flags := root.PersistentFlags()
	flags.StringVar(&global.Profile, "profile", "", "named company grant (or TALENTO_PROFILE)")
	flags.BoolVar(&global.JSON, "json", false, "return the stable JSON envelope")
	flags.BoolVar(&global.Markdown, "md", false, "render presentation-friendly Markdown")
	flags.BoolVar(&global.Agent, "agent", false, "non-interactive, data-only success output with structured errors")
	flags.StringVar(&global.JQ, "jq", "", "filter built-in JSON with a jq expression")
	flags.BoolVarP(&global.Yes, "yes", "y", false, "confirm a preview returned by the current command")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if global.JSON && global.Markdown {
			return clioutput.Usage("--json and --md are mutually exclusive", fmt.Sprintf("Run `%s --help` for usage.", cmd.CommandPath()))
		}
		if global.Agent && global.Markdown {
			return clioutput.Usage("--agent and --md are mutually exclusive", fmt.Sprintf("Run `%s --help` for usage.", cmd.CommandPath()))
		}
		if err := preflightSchemaArguments(cmd, args, talento); err != nil {
			return err
		}
		return maybeOfferAuthentication(cmd, talento, setupDeps)
	}

	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if global.Agent {
			_ = writeAgentHelp(cmd, talento)
			return
		}
		writer := cmd.OutOrStdout()
		var rendered bytes.Buffer
		cmd.SetOut(&rendered)
		defaultHelp(cmd, args)
		cmd.SetOut(writer)
		_, _ = fmt.Fprint(writer, terminal.Sanitize(rendered.String()))
	})

	root.RunE = func(cmd *cobra.Command, _ []string) error {
		return runBareRoot(cmd, talento, managedFS, setupDeps)
	}
	setupCommand := newSetupCommandWithDependencies(talento, managedFS, setupDeps)
	addGroupedCommands(root, groupGettingStarted, setupCommand, newAuthCommand(talento), newProfileCommand(talento))
	addGroupedCommands(root, groupWork, newTUICommand(talento), newActionCommand(talento))
	addGroupedCommands(root, groupDiscovery, newCommandsCommand(talento), newToolsCommand(talento), newResourcesCommand(talento))
	addGroupedCommands(root, groupIntegrations, newSkillCommand(talento, managedFS), newHandoffCommand(talento))
	addGroupedCommands(root, groupMaintenance, newDoctorCommand(talento, managedFS), newUpgradeCommand(talento), newCompletionCommand(root), newVersionCommand(talento))
	root.AddCommand(newHelpTopics()...)
	addDomainCommands(root, talento)
	classifyCobraUsageErrors(root)
	return root
}

func classifyCobraUsageErrors(root *cobra.Command) {
	usageError := func(command *cobra.Command, err error) error {
		return clioutput.Usage(err.Error(), fmt.Sprintf("Run `%s --help` for usage.", command.CommandPath()))
	}
	root.SetFlagErrorFunc(usageError)
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Args != nil {
			surface.PreserveArgumentContract(command)
			validate := command.Args
			command.Args = func(command *cobra.Command, args []string) error {
				if err := validate(command, args); err != nil {
					return usageError(command, err)
				}
				return nil
			}
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
}

func addGroupedCommands(root *cobra.Command, groupID string, commands ...*cobra.Command) {
	for _, command := range commands {
		command.GroupID = groupID
		root.AddCommand(command)
	}
}

func addDomainCommands(root *cobra.Command, talento *app.App) {
	grouped := make(map[string][]string)
	for _, mapping := range talento.Manifest.Tools {
		if mapping.Domain == "action" {
			continue
		}
		grouped[mapping.Domain] = append(grouped[mapping.Domain], mapping.Tool)
	}
	known := make(map[string]bool)
	for _, domain := range plannedDomains {
		known[domain] = true
		command := newDomainCommand(talento, domain, grouped[domain])
		command.GroupID = groupWork
		root.AddCommand(command)
	}
	extra := make([]string, 0)
	for domain := range grouped {
		if !known[domain] {
			extra = append(extra, domain)
		}
	}
	sort.Strings(extra)
	for _, domain := range extra {
		command := newDomainCommand(talento, domain, grouped[domain])
		command.GroupID = groupWork
		root.AddCommand(command)
	}
}

func writeAgentHelp(cmd *cobra.Command, talento *app.App) error {
	type flag struct {
		Name        string `json:"name"`
		Shorthand   string `json:"shorthand,omitempty"`
		Description string `json:"description"`
		Default     string `json:"default,omitempty"`
	}
	type subcommand struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	data := struct {
		Name        string       `json:"name"`
		Path        string       `json:"path"`
		Usage       string       `json:"usage"`
		Description string       `json:"description"`
		Flags       []flag       `json:"flags"`
		Commands    []subcommand `json:"commands"`
	}{Name: cmd.Name(), Path: cmd.CommandPath(), Usage: cmd.UseLine(), Description: cmd.Long}
	if data.Description == "" {
		data.Description = cmd.Short
	}
	cmd.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		data.Flags = append(data.Flags, flag{Name: f.Name, Shorthand: f.Shorthand, Description: f.Usage, Default: f.DefValue})
	})
	for _, child := range cmd.Commands() {
		if child.IsAvailableCommand() && !child.Hidden {
			data.Commands = append(data.Commands, subcommand{Name: child.Name(), Description: child.Short})
		}
	}
	return clioutput.WriteJSON(talento.Stdout, data)
}

func shortDescription(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	if len(value) > 120 {
		value = value[:117] + "..."
	}
	return value
}
