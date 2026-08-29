package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/buildinfo"
	"github.com/talentohq/talento-cli/internal/upgrade"
)

func newUpgradeCommand(talento *app.App) *cobra.Command {
	return &cobra.Command{
		Use: "upgrade", Short: "Install the latest authentic Talento CLI release transactionally.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			if buildinfo.Source == "go-install" && upgradeProgressEnabled(talento) {
				_, _ = fmt.Fprintln(talento.Stderr, "Updating talento with the installed Go toolchain...")
			}
			result, err := upgrade.NewClient().InstallLatest(cmd.Context(), buildinfo.Version, executable, buildinfo.ReleasePublicKey)
			if err != nil {
				return err
			}
			return talento.Output().Success(result, "Upgrade check completed.", nil, nil)
		},
	}
}

func upgradeProgressEnabled(talento *app.App) bool {
	options := talento.Global
	return options == nil || (!options.JSON && !options.Markdown && !options.Agent && options.JQ == "")
}
