package commands

import (
	"fmt"
	"strings"

	baseoutput "github.com/basecamp/cli/output"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/auth"
)

type authStatus struct{ auth.Status }

func (s authStatus) HumanText() string {
	if !s.Authenticated {
		return fmt.Sprintf("Profile %s is not authenticated.", s.Profile)
	}
	state := "valid"
	if s.Expired {
		state = "expired"
	}
	text := fmt.Sprintf("Profile %s is authenticated (%s).\nExpires: %s\nScope: %s\nCredential storage: %s", s.Profile, state, s.ExpiresAt.Format("2006-01-02 15:04:05Z07:00"), s.Scope, s.Storage)
	if s.Warning != "" {
		text += "\nWarning: " + s.Warning
	}
	return text
}

func newAuthCommand(talento *app.App) *cobra.Command {
	command := &cobra.Command{Use: "auth", Short: "Manage OAuth grants for named profiles."}
	var noOpen, allowFile bool
	login := &cobra.Command{
		Use: "login", Short: "Authenticate a named company profile with OAuth and PKCE.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, err := loginProfile(talento)
			if err != nil {
				return err
			}
			service, err := talento.AuthService(allowFile)
			if err != nil {
				return err
			}
			options := auth.LoginOptions{Profile: profile, NoOpen: noOpen}
			if noOpen {
				options.URLSink = func(value string) { _, _ = fmt.Fprintln(talento.Stderr, "Open this URL to authenticate:\n"+value) }
			}
			status, err := service.Login(cmd.Context(), options)
			if err != nil {
				return err
			}
			return talento.Output().Success(authStatus{status}, "Authenticated.", []baseoutput.Breadcrumb{
				app.Breadcrumb("inspect", "talento commands --available --json", "Inspect commands allowed by this grant."),
			}, map[string]any{"profile": profile})
		},
	}
	login.Flags().BoolVar(&noOpen, "no-open", false, "print the authorization URL instead of opening a browser")
	login.Flags().BoolVar(&allowFile, "allow-file-credentials", false, "opt in to owner-only plaintext credential storage if the system store is unavailable")

	var all bool
	logout := &cobra.Command{
		Use: "logout", Short: "Revoke and remove one or all local OAuth grants.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := talento.AuthService(false)
			if err != nil {
				return err
			}
			if all {
				names, _, err := talento.Config.ProfileNames()
				if err != nil {
					return err
				}
				results := make([]auth.LogoutResult, 0, len(names))
				for _, name := range names {
					result, logoutErr := service.Logout(cmd.Context(), name)
					if logoutErr != nil {
						return logoutErr
					}
					results = append(results, result)
				}
				return talento.Output().Success(results, fmt.Sprintf("Logged out %d profiles.", len(results)), nil, nil)
			}
			profile, err := talento.ResolveProfile(true)
			if err != nil {
				return err
			}
			result, err := service.Logout(cmd.Context(), profile)
			if err != nil {
				return err
			}
			return talento.Output().Success(logoutView{result}, "Logged out.", nil, map[string]any{"profile": profile})
		},
	}
	logout.Flags().BoolVar(&all, "all", false, "log out every configured profile")

	status := &cobra.Command{
		Use: "status", Short: "Show non-secret authentication state.", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			profile, err := talento.ResolveProfile(true)
			if err != nil {
				return err
			}
			service, err := talento.AuthService(false)
			if err != nil {
				return err
			}
			value, err := service.Status(profile)
			if err != nil {
				return err
			}
			return talento.Output().Success(authStatus{value}, "Authentication status.", nil, map[string]any{"profile": profile})
		},
	}
	refresh := &cobra.Command{
		Use: "refresh", Short: "Refresh the selected OAuth grant now.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, err := talento.ResolveProfile(true)
			if err != nil {
				return err
			}
			service, err := talento.AuthService(false)
			if err != nil {
				return err
			}
			value, err := service.Refresh(cmd.Context(), profile)
			if err != nil {
				return err
			}
			return talento.Output().Success(authStatus{value}, "Grant refreshed.", nil, map[string]any{"profile": profile})
		},
	}
	command.AddCommand(login, logout, status, refresh)
	return command
}

func loginProfile(talento *app.App) (string, error) {
	if talento.Global.Profile != "" {
		if _, err := talento.Config.Profile(talento.Global.Profile); err != nil {
			if _, createErr := talento.Config.CreateProfile(talento.Global.Profile); createErr != nil {
				return "", createErr
			}
		}
		return talento.Global.Profile, nil
	}
	name, err := talento.ResolveProfile(false)
	if err != nil {
		return "", err
	}
	if name != "" {
		return name, nil
	}
	if _, err := talento.Config.CreateProfile("default"); err != nil && !strings.Contains(err.Error(), "already exists") {
		return "", err
	}
	return "default", nil
}

type logoutView struct{ auth.LogoutResult }

func (r logoutView) HumanText() string {
	text := "Logged out profile " + r.Profile + "."
	if r.Revoked {
		text += " The server grant was revoked."
	}
	if r.Warning != "" {
		text += "\nWarning: " + r.Warning
	}
	return text
}
