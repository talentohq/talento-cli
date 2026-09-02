package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	baseoutput "github.com/basecamp/cli/output"
	baseprofile "github.com/basecamp/cli/profile"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/auth"
	"github.com/talentohq/talento-cli/internal/managed"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/terminal"
)

type setupStage struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type setupView struct {
	Status     string         `json:"status"`
	Profile    string         `json:"profile,omitempty"`
	Scope      string         `json:"scope,omitempty"`
	Agents     []string       `json:"agents,omitempty"`
	Result     managed.Result `json:"result"`
	Stages     []setupStage   `json:"stages,omitempty"`
	Completion string         `json:"completion,omitempty"`
	Next       string         `json:"next_command,omitempty"`
	Health     *doctorReport  `json:"health,omitempty"`
	Automated  bool           `json:"automated,omitempty"`
}

func (v setupView) HumanText() string {
	if v.Automated {
		return v.Result.HumanText()
	}
	lines := []string{"Talento setup: " + strings.ToUpper(v.Status)}
	for _, stage := range v.Stages {
		lines = append(lines, fmt.Sprintf("[%s] %s — %s", strings.ToUpper(stage.Status), stage.Name, stage.Message))
	}
	if v.Completion != "" {
		lines = append(lines, "Shell completion: "+v.Completion)
	}
	if v.Health != nil {
		lines = append(lines, "Health: "+strings.ToUpper(v.Health.Status))
	}
	if v.Next != "" {
		lines = append(lines, "Next: "+v.Next)
	}
	return strings.Join(lines, "\n")
}

type setupDependencies struct {
	Interactive      func() bool
	Login            func(context.Context, string, bool, bool, func(string)) (auth.Status, error)
	Status           func(string, bool) (auth.Status, error)
	Detect           func(string) []managed.Agent
	Install          func(managed.InstallOptions) (managed.Result, error)
	Doctor           doctorRunner
	WorkingDirectory func() (string, error)
	Shell            func() string
}

type setupOptions struct {
	Agents               []string
	Scope                string
	ScopeExplicit        bool
	NoOpen               bool
	AllowFileCredentials bool
}

var errSetupCanceled = errors.New("setup canceled")

func newSetupCommand(talento *app.App, assets fs.FS) *cobra.Command {
	return newSetupCommandWithDependencies(talento, assets, defaultSetupDependencies(talento, assets))
}

func defaultSetupDependencies(talento *app.App, assets fs.FS) setupDependencies {
	return setupDependencies{
		Interactive: talento.Interactive,
		Login: func(ctx context.Context, profile string, noOpen, allowFile bool, sink func(string)) (auth.Status, error) {
			service, err := talento.AuthService(allowFile)
			if err != nil {
				return auth.Status{}, err
			}
			return service.Login(ctx, auth.LoginOptions{Profile: profile, NoOpen: noOpen, URLSink: sink})
		},
		Status: func(profile string, allowFile bool) (auth.Status, error) {
			service, err := talento.AuthService(allowFile)
			if err != nil {
				return auth.Status{}, err
			}
			return service.Status(profile)
		},
		Detect: managed.Detect,
		Install: func(options managed.InstallOptions) (managed.Result, error) {
			return managed.NewManager(assets, talento.Config, talento.Paths.HomeDir).Install(options)
		},
		Doctor:           runDoctor,
		WorkingDirectory: os.Getwd,
		Shell:            func() string { return os.Getenv("SHELL") },
	}
}

func newSetupCommandWithDependencies(talento *app.App, assets fs.FS, deps setupDependencies) *cobra.Command {
	var agents []string
	var scope string
	var noOpen, allowFile bool
	command := &cobra.Command{
		Use:   "setup",
		Short: "Authenticate Talento and configure local coding agents.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options := setupOptions{
				Agents: agents, Scope: scope, ScopeExplicit: cmd.Flags().Changed("scope"),
				NoOpen: noOpen, AllowFileCredentials: allowFile,
			}
			if !deps.Interactive() {
				return runAutomatedSetup(talento, deps, options)
			}
			return runInteractiveSetup(cmd.Context(), talento, assets, deps, options, newLinePrompter(talento.Stdin, talento.Stderr))
		},
	}
	command.Flags().StringSliceVar(&agents, "agent", nil, "agent id to configure; repeat or pass a comma-separated list")
	command.Flags().StringVar(&scope, "scope", "user", "installation scope: user or project")
	command.Flags().BoolVar(&noOpen, "no-open", false, "print the authorization URL instead of opening a browser")
	command.Flags().BoolVar(&allowFile, "allow-file-credentials", false, "opt in to owner-only plaintext credential storage if the system store is unavailable")
	return command
}

func runAutomatedSetup(talento *app.App, deps setupDependencies, options setupOptions) error {
	selected, err := validateAgentIDs(options.Agents)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		detected := deps.Detect(talento.Paths.HomeDir)
		if talento.Global.Yes && len(detected) > 0 {
			selected = agentIDs(detected)
		} else if len(detected) == 0 {
			return fmt.Errorf("no supported local agent was detected; pass --agent explicitly (%s)", supportedAgentIDs())
		} else {
			return fmt.Errorf("non-interactive setup requires at least one --agent")
		}
	}
	projectDir, err := deps.WorkingDirectory()
	if err != nil {
		return err
	}
	result, err := deps.Install(managed.InstallOptions{Agents: selected, Scope: options.Scope, ProjectDir: projectDir})
	if err != nil {
		return err
	}
	view := setupView{Status: "complete", Scope: options.Scope, Agents: selected, Result: result, Automated: true}
	return talento.Output().Success(view, "Agent integration installed.", []baseoutput.Breadcrumb{
		app.Breadcrumb("authenticate", "talento auth login", "Authenticate a company profile."),
		app.Breadcrumb("verify", "talento doctor", "Verify the CLI and installed agent files."),
	}, nil)
}

func runInteractiveSetup(ctx context.Context, talento *app.App, assets fs.FS, deps setupDependencies, options setupOptions, prompt *linePrompter) error {
	view := setupView{Status: "in_progress", Scope: options.Scope}
	profile, created, err := chooseSetupProfile(talento, prompt)
	if err != nil {
		return setupFailure(view, "profile", err, "talento setup")
	}
	view.Profile = profile
	message := "selected profile " + profile
	if created {
		message = "created profile " + profile
	}
	view.Stages = append(view.Stages, setupStage{Name: "profile", Status: "pass", Message: message})
	// The selected wizard profile applies only to this process. Persistent profile
	// selection is handled by the config store's default profile.
	talento.Global.Profile = profile

	status, statusErr := deps.Status(profile, options.AllowFileCredentials)
	if statusErr != nil {
		return setupFailure(view, "authentication", statusErr, authLoginCommand(profile, options))
	}
	if !status.Authenticated || status.Expired {
		approved, confirmErr := prompt.confirm("Authenticate profile "+profile+" now?", true)
		if confirmErr != nil {
			return setupFailure(view, "authentication", confirmErr, authLoginCommand(profile, options))
		}
		if !approved {
			view.Stages = append(view.Stages, setupStage{Name: "authentication", Status: "skip", Message: "authentication was declined; no grant was changed"})
			view.Status = "incomplete"
			view.Next = authLoginCommand(profile, options)
			return clioutput.WithRenderedData(clioutput.Auth("setup stopped before authentication"), view)
		}
		sink := authLoginURLSink(talento, options.NoOpen)
		if _, err := deps.Login(ctx, profile, options.NoOpen, options.AllowFileCredentials, sink); err != nil {
			return setupFailure(view, "authentication", err, authLoginCommand(profile, options))
		}
		status, statusErr = deps.Status(profile, options.AllowFileCredentials)
		if statusErr != nil {
			return setupFailure(view, "authentication", statusErr, authLoginCommand(profile, options))
		}
	}
	if !status.Authenticated {
		return setupFailure(view, "authentication", errors.New("OAuth completed without a usable local grant"), authLoginCommand(profile, options))
	}
	view.Stages = append(view.Stages, setupStage{Name: "authentication", Status: "pass", Message: "verified OAuth grant for " + profile})

	selected, err := validateAgentIDs(options.Agents)
	if err != nil {
		return setupFailure(view, "agents", err, "talento setup --profile "+profile)
	}
	if len(selected) == 0 {
		detected := deps.Detect(talento.Paths.HomeDir)
		if len(detected) == 0 {
			view.Stages = append(view.Stages, setupStage{Name: "agents", Status: "warn", Message: "no supported local coding agent was detected; integration was skipped"})
		} else {
			selected, err = prompt.selectAgents(detected)
			if err != nil {
				return setupFailure(view, "agents", err, "talento setup --profile "+profile)
			}
			view.Stages = append(view.Stages, setupStage{Name: "agents", Status: "pass", Message: fmt.Sprintf("selected %d detected agent(s)", len(selected))})
		}
	} else {
		view.Stages = append(view.Stages, setupStage{Name: "agents", Status: "pass", Message: fmt.Sprintf("selected %d requested agent(s)", len(selected))})
	}
	view.Agents = selected

	if len(selected) > 0 {
		if !options.ScopeExplicit {
			view.Scope, err = prompt.scope(options.Scope)
			if err != nil {
				return setupFailure(view, "integration", err, setupInstallCommand(profile, selected, options.Scope))
			}
		}
		projectDir, err := deps.WorkingDirectory()
		if err != nil {
			return setupFailure(view, "integration", err, setupInstallCommand(profile, selected, view.Scope))
		}
		result, err := deps.Install(managed.InstallOptions{Agents: selected, Scope: view.Scope, ProjectDir: projectDir})
		if err != nil {
			return setupFailure(view, "integration", err, setupInstallCommand(profile, selected, view.Scope))
		}
		view.Result = result
		view.Stages = append(view.Stages, setupStage{Name: "integration", Status: "pass", Message: fmt.Sprintf("installed managed files for %d agent(s) at %s scope", len(selected), view.Scope)})
	}

	wantsCompletion, err := prompt.confirm("Show shell completion setup guidance?", false)
	if err != nil {
		return setupFailure(view, "completion", err, "talento completion --help")
	}
	if wantsCompletion {
		view.Completion = completionGuidance(deps.Shell())
		view.Stages = append(view.Stages, setupStage{Name: "completion", Status: "pass", Message: "generated guidance for the current shell"})
	} else {
		view.Stages = append(view.Stages, setupStage{Name: "completion", Status: "skip", Message: "completion guidance was skipped"})
	}

	report := deps.Doctor(ctx, talento, assets, false)
	view.Health = &report
	healthStatus := "pass"
	if !report.Healthy || report.Status == "warn" {
		healthStatus = "warn"
	}
	view.Stages = append(view.Stages, setupStage{Name: "health", Status: healthStatus, Message: "doctor completed with status " + report.Status})
	view.Status = "complete"
	view.Next = "talento commands --available --profile " + profile
	if len(selected) == 0 {
		view.Next = "talento setup"
	}
	return talento.Output().Success(view, "Guided setup completed.", []baseoutput.Breadcrumb{
		app.Breadcrumb("next", view.Next, "Continue with the configured profile."),
		app.Breadcrumb("verify", "talento doctor", "Run the full health report again."),
	}, map[string]any{"profile": profile, "healthy": report.Healthy})
}

func setupFailure(view setupView, stage string, cause error, next string) error {
	message := cause.Error()
	if errors.Is(cause, errSetupCanceled) {
		message = "canceled; completed stages were kept"
	}
	view.Status = "incomplete"
	view.Stages = append(view.Stages, setupStage{Name: stage, Status: "fail", Message: message})
	view.Next = next
	return clioutput.WithRenderedData(cause, view)
}

func authLoginCommand(profile string, options setupOptions) string {
	command := "talento auth login --profile " + profile
	if options.NoOpen {
		command += " --no-open"
	}
	if options.AllowFileCredentials {
		command += " --allow-file-credentials"
	}
	return command
}

func setupInstallCommand(profile string, agents []string, scope string) string {
	parts := []string{"talento", "setup", "--profile", profile, "--scope", scope, "--yes"}
	for _, id := range agents {
		parts = append(parts, "--agent", id)
	}
	return strings.Join(parts, " ")
}

func chooseSetupProfile(talento *app.App, prompt *linePrompter) (string, bool, error) {
	names, defaultName, err := talento.Config.ProfileNames()
	if err != nil {
		return "", false, err
	}
	requestedProfile := talento.Global.Profile
	if requestedProfile == "" {
		requestedProfile = os.Getenv("TALENTO_PROFILE")
	}
	if requestedProfile != "" {
		return ensureSetupProfile(talento, requestedProfile, names)
	}
	if len(names) == 0 {
		name, err := prompt.line("Profile name [default]: ")
		if err != nil {
			return "", false, err
		}
		if name == "" {
			name = "default"
		}
		return ensureSetupProfile(talento, name, names)
	}

	_, _ = fmt.Fprintln(prompt.out, "Configured profiles:")
	defaultIndex := 0
	for index, name := range names {
		marker := ""
		if name == defaultName {
			marker = " (default)"
			defaultIndex = index
		}
		_, _ = fmt.Fprintf(prompt.out, "  %d. %s%s\n", index+1, name, marker)
	}
	answer, err := prompt.line(fmt.Sprintf("Select a profile [number, name, or new] [%d]: ", defaultIndex+1))
	if err != nil {
		return "", false, err
	}
	if answer == "" {
		answer = strconv.Itoa(defaultIndex + 1)
	}
	if strings.EqualFold(answer, "new") {
		name, err := prompt.line("New profile name: ")
		if err != nil {
			return "", false, err
		}
		if name == "" {
			return "", false, errors.New("profile name cannot be empty")
		}
		return ensureSetupProfile(talento, name, names)
	}
	if index, parseErr := strconv.Atoi(answer); parseErr == nil {
		if index < 1 || index > len(names) {
			return "", false, fmt.Errorf("invalid profile selection %q", answer)
		}
		answer = names[index-1]
	}
	for _, name := range names {
		if answer == name {
			if err := talento.Config.SetDefault(name); err != nil {
				return "", false, err
			}
			return name, false, nil
		}
	}
	return "", false, fmt.Errorf("profile %q is not configured; choose a listed profile or new", answer)
}

func ensureSetupProfile(talento *app.App, name string, existing []string) (string, bool, error) {
	if err := baseprofile.ValidateName(name); err != nil {
		return "", false, err
	}
	for _, candidate := range existing {
		if candidate == name {
			if err := talento.Config.SetDefault(name); err != nil {
				return "", false, err
			}
			return name, false, nil
		}
	}
	if _, err := talento.Config.CreateProfile(name); err != nil {
		return "", false, err
	}
	return name, true, nil
}

type linePrompter struct {
	reader *bufio.Reader
	out    io.Writer
}

func newLinePrompter(in io.Reader, out io.Writer) *linePrompter {
	return &linePrompter{reader: bufio.NewReader(in), out: out}
}

func (p *linePrompter) line(question string) (string, error) {
	if _, err := fmt.Fprint(p.out, question); err != nil {
		return "", err
	}
	line, err := p.reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			return "", errSetupCanceled
		}
		return "", err
	}
	answer := strings.TrimSpace(line)
	if strings.EqualFold(answer, "cancel") || strings.EqualFold(answer, "quit") || strings.EqualFold(answer, "q") {
		return "", errSetupCanceled
	}
	return answer, nil
}

func (p *linePrompter) confirm(question string, defaultYes bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}
	answer, err := p.line(question + suffix)
	if err != nil {
		return false, err
	}
	if answer == "" {
		return defaultYes, nil
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("answer yes or no")
	}
}

func (p *linePrompter) selectAgents(detected []managed.Agent) ([]string, error) {
	_, _ = fmt.Fprintln(p.out, "Detected local agents:")
	for index, agent := range detected {
		_, _ = fmt.Fprintf(p.out, "  %d. %s (%s)\n", index+1, agent.Name, agent.ID)
	}
	answer, err := p.line("Install for which agents? Enter numbers separated by commas [all]: ")
	if err != nil {
		return nil, err
	}
	if answer == "" || strings.EqualFold(answer, "all") {
		return agentIDs(detected), nil
	}
	seen := make(map[string]bool)
	for _, value := range strings.Split(answer, ",") {
		index, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || index < 1 || index > len(detected) {
			return nil, fmt.Errorf("invalid agent selection %q", value)
		}
		seen[detected[index-1].ID] = true
	}
	selected := make([]string, 0, len(seen))
	for id := range seen {
		selected = append(selected, id)
	}
	sort.Strings(selected)
	return selected, nil
}

func (p *linePrompter) scope(defaultScope string) (string, error) {
	answer, err := p.line("Install integration at user or project scope [" + defaultScope + "]: ")
	if err != nil {
		return "", err
	}
	if answer == "" {
		answer = defaultScope
	}
	answer = strings.ToLower(answer)
	if answer != "user" && answer != "project" {
		return "", errors.New("scope must be user or project")
	}
	return answer, nil
}

func completionGuidance(shellPath string) string {
	shell := strings.ToLower(filepath.Base(shellPath))
	switch shell {
	case "bash":
		return "run `source <(talento completion bash)` now; install `talento completion bash` in your Bash completion directory for future shells"
	case "zsh":
		return "run `source <(talento completion zsh)` now; save the output as `_talento` in a directory on fpath for future shells"
	case "fish":
		return "run `talento completion fish > ~/.config/fish/completions/talento.fish`"
	case "pwsh", "powershell":
		return "run `talento completion powershell | Out-String | Invoke-Expression`; add it to your PowerShell profile for future sessions"
	default:
		return "run `talento completion --help` and install the generated script for your shell"
	}
}

const requiresAuthAnnotation = "talento.requires_auth"

func markRequiresAuth(command *cobra.Command) *cobra.Command {
	if command.Annotations == nil {
		command.Annotations = make(map[string]string)
	}
	command.Annotations[requiresAuthAnnotation] = "true"
	return command
}

func runBareRoot(cmd *cobra.Command, talento *app.App, assets fs.FS, deps setupDependencies) error {
	if !deps.Interactive() {
		return cmd.Help()
	}
	usable, _, err := hasUsableAuthentication(talento, deps)
	if err != nil {
		return err
	}
	if usable {
		return cmd.Help()
	}
	prompt := newLinePrompter(talento.Stdin, talento.Stderr)
	approved, err := prompt.confirm("No usable Talento profile is authenticated. Run guided setup now?", true)
	if err != nil {
		return err
	}
	if !approved {
		_, _ = fmt.Fprintln(talento.Stderr, "Run `talento setup` when you are ready.")
		return cmd.Help()
	}
	return runInteractiveSetup(cmd.Context(), talento, assets, deps, setupOptions{Scope: "user"}, prompt)
}

func maybeOfferAuthentication(cmd *cobra.Command, talento *app.App, deps setupDependencies) error {
	requirement := cmd.Annotations[requiresAuthAnnotation]
	if requirement == "when-available" {
		available, err := cmd.Flags().GetBool("available")
		if err != nil || !available {
			return err
		}
	} else if requirement != "true" {
		return nil
	}
	if !deps.Interactive() {
		return nil
	}
	usable, selectedProfile, err := hasUsableAuthentication(talento, deps)
	if err != nil {
		return err
	}
	if usable {
		return nil
	}
	prompt := newLinePrompter(talento.Stdin, talento.Stderr)
	approved, err := prompt.confirm("This command needs an authenticated Talento profile. Sign in now?", false)
	if err != nil {
		return err
	}
	if !approved {
		// Let the command continue so its established auth error envelope and exit
		// code remain unchanged.
		return nil
	}
	profile := selectedProfile
	if profile == "" {
		profile, _, err = chooseSetupProfile(talento, prompt)
		if err != nil {
			return err
		}
	}
	talento.Global.Profile = profile
	status, err := deps.Status(profile, false)
	if err != nil {
		return err
	}
	if !status.Authenticated || status.Expired {
		if _, err := deps.Login(cmd.Context(), profile, false, false, authLoginURLSink(talento, false)); err != nil {
			return err
		}
		status, err = deps.Status(profile, false)
		if err != nil {
			return err
		}
	}
	if !status.Authenticated {
		return clioutput.Auth(fmt.Sprintf("profile %q is not authenticated", profile))
	}
	_, _ = fmt.Fprintln(talento.Stderr, "Authenticated. Resuming "+cmd.CommandPath()+".")
	return nil
}

func hasUsableAuthentication(talento *app.App, deps setupDependencies) (bool, string, error) {
	profile, err := talento.ResolveProfile(false)
	if err != nil {
		return false, "", err
	}
	if profile == "" {
		return false, "", nil
	}
	status, err := deps.Status(profile, false)
	if err != nil {
		return false, profile, nil
	}
	return status.Authenticated && !status.Expired, profile, nil
}

func newSkillCommand(talento *app.App, assets fs.FS) *cobra.Command {
	command := &cobra.Command{Use: "skill", Short: "Install, inspect, update, or remove managed Talento agent skills."}
	command.AddCommand(newSkillStatusCommand(talento, managed.NewManager(assets, talento.Config, talento.Paths.HomeDir)))
	for _, operation := range []string{"install", "update", "remove"} {
		operation := operation
		var agents []string
		var scope string
		var force bool
		child := &cobra.Command{
			Use:   operation,
			Short: strings.ToUpper(operation[:1]) + operation[1:] + " managed skill files.",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				selected, err := validateAgentIDs(agents)
				if err != nil {
					return err
				}
				if len(selected) == 0 {
					return fmt.Errorf("pass at least one --agent; supported values: %s", supportedAgentIDs())
				}
				projectDir, err := os.Getwd()
				if err != nil {
					return err
				}
				manager := managed.NewManager(assets, talento.Config, talento.Paths.HomeDir)
				options := managed.InstallOptions{Agents: selected, Scope: scope, ProjectDir: projectDir, Force: force}
				var result managed.Result
				if operation == "remove" {
					result, err = manager.Remove(options)
				} else {
					result, err = manager.Install(options)
				}
				if err != nil {
					return err
				}
				view := setupView{Status: "complete", Scope: scope, Agents: selected, Result: result, Automated: true}
				return talento.Output().Success(view, "Managed skill operation completed.", nil, map[string]any{"operation": operation})
			},
		}
		child.Flags().StringSliceVar(&agents, "agent", nil, "agent id; repeat or pass a comma-separated list")
		child.Flags().StringVar(&scope, "scope", "user", "installation scope: user or project")
		child.Flags().BoolVar(&force, "force", false, "back up and replace modified managed files")
		command.AddCommand(child)
	}
	return command
}

type integrationStatusView struct {
	Status       string              `json:"status"`
	Integrations []managed.Diagnosis `json:"integrations"`
	Verbose      bool                `json:"-"`
}

func (v integrationStatusView) HumanText() string {
	lines := []string{"Talento agent integrations: " + terminal.SanitizeLine(strings.ToUpper(v.Status))}
	for _, diagnosis := range v.Integrations {
		lines = append(lines, fmt.Sprintf("[%s] %s (%s) — %s",
			terminal.SanitizeLine(strings.ToUpper(diagnosis.Status)),
			terminal.SanitizeLine(diagnosis.Agent.Name),
			terminal.SanitizeLine(diagnosis.Agent.ID),
			terminal.SanitizeLine(diagnosis.Summary())))
		detected := "not detected"
		if diagnosis.Detection.Detected {
			detected = "detected"
			if diagnosis.Detection.ExecutablePath != "" {
				detected += " at " + diagnosis.Detection.ExecutablePath
			}
		}
		if diagnosis.ExecutableVersion.Value != "" {
			detected += " (" + diagnosis.ExecutableVersion.Value + ")"
		}
		lines = append(lines, "  Runtime: "+terminal.SanitizeLine(detected))
		lines = append(lines, fmt.Sprintf("  Supported: install/remove %s; scopes %s; version probe %s",
			terminal.SanitizeLine(diagnosis.Capabilities.InstallMethod),
			terminal.SanitizeLine(strings.Join(diagnosis.Capabilities.Scopes, ", ")),
			terminal.SanitizeLine(diagnosis.Capabilities.VersionMethod)))
		if diagnosis.Installed {
			lines = append(lines, fmt.Sprintf("  Installed: method %s; scope %s",
				terminal.SanitizeLine(diagnosis.Method), terminal.SanitizeLine(strings.Join(diagnosis.Scopes, ", "))))
			lines = append(lines, fmt.Sprintf("  Integration version: installed %s; expected %s",
				terminal.SanitizeLine(valueOrUnknown(diagnosis.InstalledVersion)), terminal.SanitizeLine(diagnosis.ExpectedVersion)))
		}
		if v.Verbose {
			for _, file := range diagnosis.Files {
				lines = append(lines, fmt.Sprintf("  File [%s]: %s (expected %s, actual %s)",
					terminal.SanitizeLine(file.Status), terminal.SanitizeLine(file.Path),
					terminal.SanitizeLine(file.Expected), terminal.SanitizeLine(valueOrUnknown(file.Actual))))
			}
		}
		if diagnosis.Status != "healthy" && len(diagnosis.RepairCommands) > 0 {
			lines = append(lines, "  Repair: "+terminal.SanitizeLine(diagnosis.RepairCommands[0]))
		}
	}
	return strings.Join(lines, "\n")
}

func newSkillStatusCommand(talento *app.App, manager *managed.Manager) *cobra.Command {
	var agents []string
	var verbose bool
	command := &cobra.Command{
		Use: "status", Short: "Inspect coding-agent integration capabilities and health.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected, err := validateAgentIDs(agents)
			if err != nil {
				return err
			}
			diagnoses, err := manager.DiagnoseAgents(cmd.Context(), selected)
			if err != nil {
				return err
			}
			filter := make(map[string]bool, len(selected))
			for _, id := range selected {
				filter[id] = true
			}
			view := integrationStatusView{Status: "healthy", Verbose: verbose}
			active := false
			for _, diagnosis := range diagnoses {
				if len(filter) > 0 && !filter[diagnosis.Agent.ID] {
					continue
				}
				view.Integrations = append(view.Integrations, diagnosis)
				if diagnosis.Installed || diagnosis.Detection.Detected {
					active = true
				}
				if diagnosis.Status != "healthy" && (diagnosis.Installed || diagnosis.Detection.Detected) {
					view.Status = "attention"
				}
			}
			if !active {
				view.Status = "not-installed"
			}
			return talento.Output().Success(view, "Agent integration inspection completed.", nil, map[string]any{"status": view.Status})
		},
	}
	command.Flags().StringSliceVar(&agents, "integration", nil, "agent id to inspect; repeat or pass a comma-separated list (default: all)")
	command.Flags().BoolVar(&verbose, "verbose", false, "include managed paths and expected/actual digests in human output")
	return command
}

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func validateAgentIDs(values []string) ([]string, error) {
	seen := make(map[string]bool)
	for _, value := range values {
		for _, id := range strings.Split(value, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := managed.AgentByID(id); !ok {
				return nil, fmt.Errorf("unsupported agent %q; supported values: %s", id, supportedAgentIDs())
			}
			seen[id] = true
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func supportedAgentIDs() string {
	return strings.Join(agentIDs(managed.SupportedAgents), ", ")
}

func agentIDs(agents []managed.Agent) []string {
	result := make([]string, 0, len(agents))
	for _, agent := range agents {
		result = append(result, agent.ID)
	}
	sort.Strings(result)
	return result
}
