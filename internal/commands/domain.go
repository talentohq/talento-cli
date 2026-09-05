package commands

import (
	"context"
	"fmt"
	"strings"

	baseoutput "github.com/basecamp/cli/output"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	"github.com/talentohq/talento-cli/internal/schema"
)

func newDomainCommand(talento *app.App, domain string, toolNames []string) *cobra.Command {
	command := &cobra.Command{Use: domain, Short: domainDescriptions[domain]}
	command.Example = domainExamples[domain]
	if command.Short == "" {
		command.Short = "Talento " + domain + " operations."
	}
	if len(toolNames) == 0 {
		command.Long = command.Short + " The reviewed gateway snapshot exposes no dedicated tool in this domain; use `talento commands --available` to inspect the current profile."
		command.Args = cobra.NoArgs
		command.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	}
	for _, name := range toolNames {
		tool, ok := schema.ToolByName(talento.Snapshot, name)
		if !ok {
			continue
		}
		mapping := mappingFor(talento, name)
		command.AddCommand(newGeneratedToolCommand(talento, tool, mapping.Command))
	}
	return command
}

func mappingFor(talento *app.App, tool string) schema.ToolMapping {
	for _, mapping := range talento.Manifest.Tools {
		if mapping.Tool == tool {
			return mapping
		}
	}
	return schema.ToolMapping{Tool: tool, Domain: "tools", Command: strings.ReplaceAll(tool, "_", "-")}
}

func newGeneratedToolCommand(talento *app.App, tool schema.Tool, commandName string) *cobra.Command {
	command := &cobra.Command{
		Use:     commandName,
		Short:   shortDescription(tool.Description),
		Long:    strings.TrimSpace(tool.Description),
		Example: toolExamples[tool.Name],
		Args:    cobra.NoArgs,
		Annotations: map[string]string{
			schemaToolAnnotation: tool.Name,
		},
	}
	input := addSchemaFlags(command, tool)
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		execution, err := executeValidatedTool(cmd.Context(), cmd, input, tool, talento.ExecuteTool)
		if err != nil {
			return err
		}
		return writeToolExecution(talento, execution)
	}
	return markRequiresAuth(command)
}

func executeValidatedTool(
	ctx context.Context,
	command *cobra.Command,
	input *schemaInput,
	tool schema.Tool,
	execute func(context.Context, string, map[string]any) (*app.ToolExecution, error),
) (*app.ToolExecution, error) {
	arguments, err := validatedArguments(command, input, tool)
	if err != nil {
		return nil, err
	}
	return execute(ctx, tool.Name, arguments)
}

var domainExamples = map[string]string{
	"people": `  talento people list --name Ana
  talento people get --employee-id 42 --json`,
	"absences": `  talento absences list --start-date 2026-09-01 --end-date 2026-09-30
  talento absences create --input-file request.json`,
	"reports": `  talento reports create-changelog --title "Weekly update" --content "Completed onboarding"`,
	"trainings": `  talento trainings list --name onboarding --json
  talento trainings get --training-id 42`,
	"meetings": `  talento meetings list --for meetings --json
  talento meetings create --name "First interview — company" --show-in-job-step --job-step-template-names-item "First interview"`,
}

var toolExamples = map[string]string{
	"list_employees": `  talento people list --name Ana
  talento people list --team-id 12 --json`,
	"list_absences":           `  talento absences list --start-date 2026-09-01 --end-date 2026-09-30 --status approved`,
	"create_changelog":        `  talento reports create-changelog --title "Weekly update" --content "Completed onboarding"`,
	"list_trainings":          `  talento trainings list --name onboarding --json`,
	"list_meeting_templates":  `  talento meetings list --for meetings --json`,
	"create_meeting_template": `  talento meetings create --name "First interview — company" --show-in-job-step --job-step-template-names-item "First interview"`,
}

func writeToolExecution(talento *app.App, execution *app.ToolExecution) error {
	state := execution.Result.State
	summary := "Talento returned a result."
	var breadcrumbs []baseoutput.Breadcrumb
	switch state {
	case mcpclient.StateCommitted:
		summary = "Action completed and persisted."
	case mcpclient.StateSubmitted:
		summary = "Request submitted; an approval may still be pending."
	case mcpclient.StatePreview:
		summary = "Preview returned; nothing has been executed."
		if execution.Result.PreviewID != "" {
			breadcrumbs = append(breadcrumbs, app.Breadcrumb("confirm", "talento action confirm "+execution.Result.PreviewID, "Confirm this exact preview."))
		}
	}
	meta := map[string]any{"profile": execution.Profile, "state": state}
	return talento.Output().Success(execution, summary, breadcrumbs, meta)
}

func callTool(ctx context.Context, talento *app.App, name string, arguments map[string]any) error {
	execution, err := talento.ExecuteTool(ctx, name, arguments)
	if err != nil {
		return err
	}
	return writeToolExecution(talento, execution)
}

func unavailableToolError(name string) error {
	return fmt.Errorf("tool %q is not available to the selected profile; Talento role, permission, module, and tenant rules are server-authoritative", name)
}
