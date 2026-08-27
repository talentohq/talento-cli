package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/schema"
)

type toolsList struct {
	Profile string      `json:"profile"`
	Tools   []*mcp.Tool `json:"tools"`
}

func (l toolsList) HumanText() string {
	lines := []string{fmt.Sprintf("Available tools for profile %s (%d):", l.Profile, len(l.Tools))}
	for _, tool := range l.Tools {
		lines = append(lines, fmt.Sprintf("- %s — %s", tool.Name, shortDescription(tool.Description)))
	}
	return strings.Join(lines, "\n")
}

func newToolsCommand(talento *app.App) *cobra.Command {
	command := &cobra.Command{Use: "tools", Short: "Inspect and call raw MCP tools."}
	list := &cobra.Command{
		Use: "list", Short: "List tools exposed to the selected profile.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, profile, err := talento.MCP(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()
			tools, err := client.ListTools(cmd.Context())
			if err != nil {
				return err
			}
			sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
			data := toolsList{Profile: profile, Tools: tools}
			return talento.Output().Success(data, fmt.Sprintf("%d tools available.", len(tools)), nil, map[string]any{"profile": profile})
		},
	}
	describe := &cobra.Command{
		Use: "describe <tool>", Short: "Show one live tool schema.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, profile, err := talento.MCP(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()
			tools, err := client.ListTools(cmd.Context())
			if err != nil {
				return err
			}
			for _, tool := range tools {
				if tool.Name == args[0] {
					return talento.Output().Success(toolDescription{Tool: tool}, "Live tool schema.", nil, map[string]any{"profile": profile})
				}
			}
			return unavailableToolError(args[0])
		},
	}
	callToolSchema := schema.Tool{Name: "raw_call", InputSchema: schema.JSONSchema{Properties: map[string]schema.Property{}}}
	call := &cobra.Command{
		Use: "call <tool>", Short: "Call any available MCP tool with a JSON object.", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{schemaToolAnnotation: dynamicSchemaTool},
	}
	input := addSchemaFlags(call, callToolSchema)
	call.RunE = func(cmd *cobra.Command, args []string) error {
		validationSchema := callToolSchema
		if embedded, ok := schema.ToolByName(talento.Snapshot, args[0]); ok {
			validationSchema = embedded
		}
		arguments, err := validatedArguments(cmd, input, validationSchema)
		if err != nil {
			return err
		}
		return callTool(cmd.Context(), talento, args[0], arguments)
	}
	command.AddCommand(markRequiresAuth(list), markRequiresAuth(describe), markRequiresAuth(call))
	return command
}

type toolDescription struct {
	Tool *mcp.Tool `json:"tool"`
}

func (d toolDescription) HumanText() string {
	data, _ := json.MarshalIndent(d.Tool.InputSchema, "", "  ")
	return fmt.Sprintf("%s\n\n%s\n\nInput schema:\n%s", d.Tool.Name, strings.TrimSpace(d.Tool.Description), data)
}
