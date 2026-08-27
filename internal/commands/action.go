package commands

import (
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
)

func newActionCommand(talento *app.App) *cobra.Command {
	command := &cobra.Command{Use: "action", Short: "Confirm an exact server preview."}
	var choice int
	confirm := &cobra.Command{
		Use:   "confirm <preview-id>",
		Short: "Execute a preview returned by Talento.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arguments := map[string]any{"preview_id": args[0]}
			if cmd.Flags().Changed("choice") {
				arguments["choice"] = choice
			}
			return callTool(cmd.Context(), talento, "confirm_action", arguments)
		},
	}
	confirm.Flags().IntVar(&choice, "choice", 0, "select a numbered preview only when Talento reports several")
	command.AddCommand(markRequiresAuth(confirm))
	return command
}
