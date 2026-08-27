package commands

import (
	"fmt"
	"sort"
	"strings"

	baseoutput "github.com/basecamp/cli/output"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/config"
)

type profileList struct {
	Default  string           `json:"default,omitempty"`
	Profiles []config.Profile `json:"profiles"`
}

func (l profileList) HumanText() string {
	if len(l.Profiles) == 0 {
		return "No profiles configured."
	}
	lines := []string{"Profiles:"}
	for _, profile := range l.Profiles {
		marker := ""
		if profile.Name == l.Default {
			marker = " (default)"
		}
		lines = append(lines, "- "+profile.Name+marker)
	}
	return strings.Join(lines, "\n")
}

type profileView struct {
	Profile config.Profile `json:"profile"`
	Default bool           `json:"default"`
}

type projectProfileView struct {
	Project           string                   `json:"project"`
	ConfigPath        string                   `json:"config_path"`
	Profile           string                   `json:"profile,omitempty"`
	SHA256            string                   `json:"sha256,omitempty"`
	Status            config.ProjectTrustState `json:"status"`
	Message           string                   `json:"message,omitempty"`
	ProfileConfigured bool                     `json:"profile_configured"`
}

func (v projectProfileView) HumanText() string {
	lines := []string{
		"Project profile: " + strings.ToUpper(string(v.Status)),
		"Project: " + v.Project,
		"Config: " + v.ConfigPath,
	}
	if v.Profile != "" {
		lines = append(lines, "Profile: "+v.Profile)
		configured := "no"
		if v.ProfileConfigured {
			configured = "yes"
		}
		lines = append(lines, "Profile configured globally: "+configured)
	}
	if v.SHA256 != "" {
		lines = append(lines, "SHA-256: "+v.SHA256)
	}
	if v.Message != "" {
		lines = append(lines, v.Message)
	}
	return strings.Join(lines, "\n")
}

func (v profileView) HumanText() string {
	text := fmt.Sprintf("Profile: %s\nEndpoint: %s", v.Profile.Name, v.Profile.Endpoint)
	if v.Default {
		text += "\nDefault: yes"
	}
	if v.Profile.ClientID != "" {
		text += "\nOAuth client registered: yes"
	}
	return text
}

func newProfileCommand(talento *app.App) *cobra.Command {
	command := &cobra.Command{Use: "profile", Short: "Manage named company grants without exposing secrets."}
	list := &cobra.Command{
		Use: "list", Short: "List configured profiles.", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := talento.Config.Load()
			if err != nil {
				return err
			}
			data := profileList{Default: cfg.DefaultProfile}
			for _, profile := range cfg.Profiles {
				data.Profiles = append(data.Profiles, profile)
			}
			sort.Slice(data.Profiles, func(i, j int) bool { return data.Profiles[i].Name < data.Profiles[j].Name })
			return talento.Output().Success(data, fmt.Sprintf("%d profiles.", len(data.Profiles)), nil, nil)
		},
	}
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a named profile for the fixed TalentoHQ gateway.", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			profile, err := talento.Config.CreateProfile(args[0])
			if err != nil {
				return err
			}
			cfg, err := talento.Config.Load()
			if err != nil {
				return err
			}
			return talento.Output().Success(profileView{Profile: profile, Default: cfg.DefaultProfile == profile.Name}, "Profile created.", []baseoutput.Breadcrumb{
				app.Breadcrumb("authenticate", "talento auth login --profile "+profile.Name, "Authenticate this profile."),
			}, nil)
		},
	}
	show := &cobra.Command{
		Use: "show [name]", Short: "Show non-secret profile metadata.", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := ""
			var err error
			if len(args) == 1 {
				name = args[0]
			} else {
				name, err = talento.ResolveProfile(true)
				if err != nil {
					return err
				}
			}
			cfg, err := talento.Config.Load()
			if err != nil {
				return err
			}
			profile, ok := cfg.Profiles[name]
			if !ok {
				return fmt.Errorf("profile %q not found", name)
			}
			return talento.Output().Success(profileView{Profile: profile, Default: cfg.DefaultProfile == name}, "Profile metadata.", nil, nil)
		},
	}
	setDefault := func(use string) *cobra.Command {
		return &cobra.Command{
			Use: use + " <name>", Short: "Select the default profile.", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				if err := talento.Config.SetDefault(args[0]); err != nil {
					return err
				}
				return talento.Output().Success("Default profile: "+args[0], "Default profile changed.", nil, nil)
			},
		}
	}
	deleteCommand := &cobra.Command{
		Use: "delete <name>", Short: "Delete logged-out profile metadata only.", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			service, err := talento.AuthService(false)
			if err != nil {
				return err
			}
			status, err := service.Status(args[0])
			if err != nil {
				return err
			}
			if status.Authenticated {
				return fmt.Errorf("profile %q is still authenticated; run `talento auth logout --profile %s` first", args[0], args[0])
			}
			if err := talento.Config.DeleteProfile(args[0]); err != nil {
				return err
			}
			return talento.Output().Success("Deleted profile metadata: "+args[0], "Profile deleted.", nil, nil)
		},
	}
	projectCommand := func(operation string) *cobra.Command {
		child := &cobra.Command{
			Use: operation + " [path]", Args: cobra.MaximumNArgs(1),
		}
		switch operation {
		case "trust-project":
			child.Short = "Trust the nearest project profile selector's exact current contents."
			child.RunE = func(_ *cobra.Command, args []string) error {
				project, err := discoverProjectTarget(talento, args)
				if err != nil {
					return err
				}
				if err := talento.Config.TrustProject(project); err != nil {
					return err
				}
				return talento.Output().Success(projectProfileView{
					Project: project.ProjectDir, ConfigPath: project.ConfigPath, Profile: project.Profile,
					SHA256: project.Digest, Status: config.ProjectTrusted, ProfileConfigured: true,
				}, "Project profile trusted.", nil, nil)
			}
		case "untrust-project":
			child.Short = "Remove trust for the nearest project profile selector."
			child.RunE = func(_ *cobra.Command, args []string) error {
				target, err := projectTargetPath(talento, args)
				if err != nil {
					return err
				}
				location, err := config.LocateProjectConfig(target)
				if err != nil {
					return err
				}
				removed, err := talento.Config.UntrustProject(location.ConfigPath)
				if err != nil {
					return err
				}
				summary := "Project profile was already untrusted."
				if removed {
					summary = "Project profile trust removed."
				}
				return talento.Output().Success(projectProfileView{
					Project: location.ProjectDir, ConfigPath: location.ConfigPath, Status: config.ProjectUntrusted,
					Message: summary,
				}, summary, nil, nil)
			}
		case "project-status":
			child.Short = "Inspect nearest project profile trust without exposing credentials."
			child.RunE = func(_ *cobra.Command, args []string) error {
				project, err := discoverProjectTarget(talento, args)
				if err != nil {
					return err
				}
				cfg, err := talento.Config.Load()
				if err != nil {
					return err
				}
				_, profileConfigured := cfg.Profiles[project.Profile]
				return talento.Output().Success(projectProfileView{
					Project: project.ProjectDir, ConfigPath: project.ConfigPath, Profile: project.Profile,
					SHA256: project.Digest, Status: config.ProjectTrustStatus(project, cfg.ProjectTrust),
					ProfileConfigured: profileConfigured,
				}, "Project profile trust status.", nil, nil)
			}
		}
		return child
	}
	command.AddCommand(
		list, create, show, setDefault("use"), deleteCommand, setDefault("set-default"),
		projectCommand("trust-project"), projectCommand("untrust-project"), projectCommand("project-status"),
	)
	return command
}

func discoverProjectTarget(talento *app.App, args []string) (config.ProjectProfile, error) {
	target, err := projectTargetPath(talento, args)
	if err != nil {
		return config.ProjectProfile{}, err
	}
	return config.DiscoverProjectProfile(target)
}

func projectTargetPath(talento *app.App, args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	workingDirectory := talento.WorkingDirectory
	if workingDirectory == nil {
		return ".", nil
	}
	directory, err := workingDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	return directory, nil
}
