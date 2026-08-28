package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	baseprofile "github.com/basecamp/cli/profile"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/auth"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/tui"
)

// Dependencies are constructor-injected for command tests. Production always
// checks actual terminal descriptors; App.InteractiveCheck alone cannot make a
// redirected stream eligible for terminal takeover.
type tuiDependencies struct {
	Terminal    func(io.Reader, io.Writer) bool
	Run         func(context.Context, tui.Options) error
	OpenSession func(context.Context, string, bool) (app.Session, error)
	Login       func(context.Context, auth.LoginOptions, bool) error
}

func defaultTUIDependencies(talento *app.App) tuiDependencies {
	return tuiDependencies{
		Terminal:    tuiTerminal,
		Run:         tui.Run,
		OpenSession: talento.OpenSession,
		Login: func(ctx context.Context, options auth.LoginOptions, allowFile bool) error {
			service, err := talento.AuthService(allowFile)
			if err != nil {
				return err
			}
			_, err = service.Login(ctx, options)
			return err
		},
	}
}

func newTUICommand(talento *app.App) *cobra.Command {
	return newTUICommandWithDependencies(talento, defaultTUIDependencies(talento))
}

func newTUICommandWithDependencies(talento *app.App, deps tuiDependencies) *cobra.Command {
	var noOpen, allowFile bool
	command := &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive Talento workspace in your terminal.",
		Long:  "Browse available Talento actions and resources in a keyboard-driven terminal workspace. Requires TalentoHQ OAuth, not an AI subscription.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := tuiPreflight(cmd, talento, deps); err != nil {
				return err
			}
			// Resolve project trust before entering the alternate screen. Resolution
			// retains the CLI's flag, environment, project, and default precedence.
			profile, prospective, err := tuiInitialProfile(talento)
			if err != nil {
				return err
			}
			return deps.Run(cmd.Context(), tui.Options{
				Profile: profile, Stdin: talento.Stdin, Stdout: talento.Stdout,
				Profiles: func() ([]string, error) {
					names, _, err := talento.Config.ProfileNames()
					return names, err
				},
				OpenSession: func(ctx context.Context, name string) (app.Session, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					snapshot, err := talento.Config.SnapshotProfile(name)
					if err != nil {
						return nil, err
					}
					if !snapshot.Exists {
						if prospective && name == profile {
							return nil, clioutput.Auth("Sign in to create your first Talento profile.")
						}
						return nil, tuiMissingProfile(name)
					}
					return deps.OpenSession(ctx, name, allowFile)
				},
				Login: func(ctx context.Context, name string, sink func(string)) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					snapshot, err := talento.Config.SnapshotProfile(name)
					if err != nil {
						return err
					}
					if !snapshot.Exists {
						if !prospective || name != profile {
							return tuiMissingProfile(name)
						}
						// The UI invokes this callback only after explicit sign-in.
						// Merely opening or closing the TUI never writes config.
						if _, err := talento.Config.CreateProfile(name); err != nil {
							return err
						}
					}
					return deps.Login(ctx, auth.LoginOptions{Profile: name, NoOpen: noOpen, URLSink: sink}, allowFile)
				},
			})
		},
	}
	command.Flags().BoolVar(&noOpen, "no-open", false, "show the authorization URL instead of opening a browser")
	command.Flags().BoolVar(&allowFile, "allow-file-credentials", false, "opt in to owner-only plaintext credential storage if the system store is unavailable")
	return command
}

func tuiPreflight(command *cobra.Command, talento *app.App, deps tuiDependencies) error {
	for _, name := range []string{"json", "md", "agent", "jq", "yes"} {
		if command.Flags().Changed(name) {
			return clioutput.Usage("talento tui cannot be combined with --"+name, "Use `talento tui` in an interactive terminal, or run a CLI command with machine-output flags.")
		}
	}
	if !talento.Interactive() || !deps.Terminal(talento.Stdin, talento.Stdout) || strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return clioutput.Usage("talento tui requires an interactive terminal on stdin and stdout", "Use a terminal without CI or TALENTO_NONINTERACTIVE, TERM=dumb, redirects, or machine-output/--yes flags.")
	}
	return nil
}

func tuiTerminal(stdin io.Reader, stdout io.Writer) bool {
	in, inOK := stdin.(*os.File)
	out, outOK := stdout.(*os.File)
	return inOK && outOK && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

func tuiInitialProfile(talento *app.App) (name string, prospective bool, err error) {
	selected := talento.Global.Profile
	if selected == "" {
		selected = os.Getenv("TALENTO_PROFILE")
	}
	// The shared resolver supports profile-less commands and consequently
	// returns no selection for an empty store even with an explicit selector.
	// A full-screen session must not silently replace that selector with default.
	if selected != "" {
		snapshot, err := talento.Config.SnapshotProfile(selected)
		if err != nil {
			return "", false, err
		}
		if !snapshot.Exists {
			return "", false, tuiMissingProfile(selected)
		}
	}
	name, err = talento.ResolveProfile(false)
	if err != nil {
		return "", false, err
	}
	if name == "" {
		return "default", true, nil
	}
	return name, false, nil
}

func tuiMissingProfile(name string) error {
	if err := baseprofile.ValidateName(name); err != nil {
		return clioutput.Usage(err.Error(), "Use an existing profile name, or create one with `talento auth login --profile NAME`.")
	}
	return clioutput.Usage(fmt.Sprintf("profile %q is not configured", name), fmt.Sprintf("Run `talento auth login --profile %q` to create and authenticate it.", name))
}
