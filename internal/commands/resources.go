package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
)

type resourcesList struct {
	Profile   string          `json:"profile"`
	Resources []*mcp.Resource `json:"resources"`
}

func (l resourcesList) HumanText() string {
	lines := []string{fmt.Sprintf("Available resources for profile %s (%d):", l.Profile, len(l.Resources))}
	for _, resource := range l.Resources {
		lines = append(lines, fmt.Sprintf("- %s — %s", resource.URI, resource.Description))
	}
	return strings.Join(lines, "\n")
}

func newResourcesCommand(talento *app.App) *cobra.Command {
	command := &cobra.Command{Use: "resources", Short: "Inspect and read raw MCP resources."}
	list := &cobra.Command{
		Use: "list", Short: "List resources exposed to the selected profile.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, profile, err := talento.MCP(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()
			resources, err := client.ListResources(cmd.Context())
			if err != nil {
				return err
			}
			sort.Slice(resources, func(i, j int) bool { return resources[i].URI < resources[j].URI })
			data := resourcesList{Profile: profile, Resources: resources}
			return talento.Output().Success(data, fmt.Sprintf("%d resources available.", len(resources)), nil, map[string]any{"profile": profile})
		},
	}
	read := &cobra.Command{
		Use: "read <uri>", Short: "Read an MCP resource by URI.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, profile, err := talento.MCP(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()
			result, err := client.ReadResource(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return talento.Output().Success(result, "Resource read.", nil, map[string]any{"profile": profile})
		},
	}
	command.AddCommand(markRequiresAuth(list), markRequiresAuth(read))
	return command
}
