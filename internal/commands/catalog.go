package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/schema"
)

type commandCatalog struct {
	Profile   string                   `json:"profile,omitempty"`
	Available bool                     `json:"available_only"`
	Commands  []catalogCommand         `json:"commands"`
	Resources []schema.ResourceMapping `json:"resources"`
}

type catalogCommand struct {
	Path        string `json:"path"`
	Tool        string `json:"tool"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
}

func (c commandCatalog) HumanText() string {
	label := "reviewed command catalog"
	if c.Available {
		label = "commands available to profile " + c.Profile
	}
	lines := []string{fmt.Sprintf("%s (%d):", label, len(c.Commands))}
	for _, command := range c.Commands {
		lines = append(lines, fmt.Sprintf("- talento %s — %s", command.Path, command.Description))
	}
	return strings.Join(lines, "\n")
}

func newCommandsCommand(talento *app.App) *cobra.Command {
	var available bool
	command := &cobra.Command{
		Use: "commands", Short: "Show the generated, versioned command catalogue.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			allowed := make(map[string]bool)
			profile := ""
			if available {
				client, selected, err := talento.MCP(cmd.Context())
				if err != nil {
					return err
				}
				defer client.Close()
				tools, err := client.ListTools(cmd.Context())
				if err != nil {
					return err
				}
				for _, tool := range tools {
					allowed[tool.Name] = true
				}
				profile = selected
			}
			catalog := commandCatalog{Profile: profile, Available: available, Resources: talento.Manifest.Resources}
			for _, mapping := range talento.Manifest.Tools {
				if available && !allowed[mapping.Tool] {
					continue
				}
				tool, _ := schema.ToolByName(talento.Snapshot, mapping.Tool)
				catalog.Commands = append(catalog.Commands, catalogCommand{
					Path: mapping.Path(), Tool: mapping.Tool, Description: shortDescription(tool.Description), ReadOnly: mapping.ReadOnly,
				})
			}
			sort.Slice(catalog.Commands, func(i, j int) bool { return catalog.Commands[i].Path < catalog.Commands[j].Path })
			meta := map[string]any{"snapshot_version": talento.Snapshot.SnapshotVersion}
			if profile != "" {
				meta["profile"] = profile
			}
			return talento.Output().Success(catalog, fmt.Sprintf("%d commands.", len(catalog.Commands)), nil, meta)
		},
	}
	command.Flags().BoolVar(&available, "available", false, "show only tools exposed to the selected profile")
	command.Annotations = map[string]string{requiresAuthAnnotation: "when-available"}
	return command
}
