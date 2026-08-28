package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	baseoutput "github.com/basecamp/cli/output"
	baseprofile "github.com/basecamp/cli/profile"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/talentohq/talento-cli/internal/auth"
	"github.com/talentohq/talento-cli/internal/config"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/schema"
	"github.com/talentohq/talento-cli/internal/terminal"
)

type GlobalOptions struct {
	Profile  string
	JSON     bool
	Markdown bool
	Agent    bool
	JQ       string
	Yes      bool
}

type App struct {
	Paths        config.Paths
	Config       *config.Store
	Snapshot     schema.Snapshot
	Manifest     schema.Manifest
	SnapshotData []byte
	Global       *GlobalOptions
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	// InteractiveCheck is a test seam for terminal-only command flows. Output
	// modes and explicit non-interactive controls are always enforced before it.
	InteractiveCheck   func() bool
	WorkingDirectory   func() (string, error)
	ProjectTrustPrompt func(config.ProjectProfile, config.ProjectTrustState) (ProjectTrustDecision, error)
	projectOnceMu      sync.Mutex
	projectOnce        map[string]string
}

type ProjectTrustDecision string

const (
	ProjectUseOnce     ProjectTrustDecision = "once"
	ProjectTrustAlways ProjectTrustDecision = "always"
	ProjectTrustCancel ProjectTrustDecision = "cancel"
)

func New(snapshotData, manifestData []byte, global *GlobalOptions) (*App, error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return nil, err
	}
	snapshot, err := schema.ParseSnapshot(snapshotData)
	if err != nil {
		return nil, err
	}
	manifest, err := schema.ParseManifest(manifestData)
	if err != nil {
		return nil, err
	}
	if err := schema.ValidateCoverage(snapshot, snapshotData, manifest); err != nil {
		return nil, err
	}
	return &App{
		Paths: paths, Config: config.NewStore(paths.ConfigFile), Snapshot: snapshot,
		Manifest: manifest, SnapshotData: snapshotData, Global: global,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		WorkingDirectory: os.Getwd, projectOnce: make(map[string]string),
	}, nil
}

func (a *App) Output() *clioutput.Writer {
	return clioutput.New(clioutput.Options{
		JSON: a.Global.JSON, Markdown: a.Global.Markdown, Agent: a.Global.Agent,
		JQ: a.Global.JQ, Writer: a.Stdout, ErrWriter: a.Stderr,
	})
}

func (a *App) Interactive() bool {
	if a.Global.Agent || a.Global.JSON || a.Global.Markdown || a.Global.JQ != "" || a.Global.Yes ||
		os.Getenv("TALENTO_NONINTERACTIVE") != "" || os.Getenv("CI") != "" {
		return false
	}
	if a.InteractiveCheck != nil {
		return a.InteractiveCheck()
	}
	inFile, inOK := a.Stdin.(*os.File)
	outFile, outOK := a.Stdout.(*os.File)
	if !inOK || !outOK {
		return false
	}
	in, inErr := inFile.Stat()
	out, outErr := outFile.Stat()
	return inErr == nil && outErr == nil && in.Mode()&os.ModeCharDevice != 0 && out.Mode()&os.ModeCharDevice != 0
}

func (a *App) ResolveProfile(required bool) (string, error) {
	cfg, err := a.Config.Load()
	if err != nil {
		return "", err
	}
	profiles := make(map[string]*baseprofile.Profile, len(cfg.Profiles))
	for name := range cfg.Profiles {
		profiles[name] = &baseprofile.Profile{Name: name, BaseURL: config.Endpoint}
	}
	environmentProfile := os.Getenv("TALENTO_PROFILE")
	if a.Global.Profile != "" || environmentProfile != "" {
		name, err := baseprofile.Resolve(baseprofile.ResolveOptions{
			FlagValue: a.Global.Profile, EnvVar: environmentProfile,
			DefaultProfile: cfg.DefaultProfile, Profiles: profiles, Interactive: false,
		})
		return requireProfile(name, required, err)
	}

	workingDirectory := a.WorkingDirectory
	if workingDirectory == nil {
		workingDirectory = os.Getwd
	}
	cwd, err := workingDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	project, projectErr := config.DiscoverProjectProfile(cwd)
	if projectErr == nil {
		if _, ok := cfg.Profiles[project.Profile]; !ok {
			return "", fmt.Errorf("project config %s selects profile %q, which is not configured globally", project.ConfigPath, project.Profile)
		}
		state := config.ProjectTrustStatus(project, cfg.ProjectTrust)
		if state != config.ProjectTrusted && !a.hasOnceTrust(project) {
			if !a.Interactive() {
				return "", clioutput.Usage(
					fmt.Sprintf("project config %s is %s and was not used", project.ConfigPath, state),
					"Run `talento profile trust-project` from this project to trust its exact current contents.",
				)
			}
			decision, err := a.promptProjectTrust(project, state)
			if err != nil {
				return "", err
			}
			switch decision {
			case ProjectUseOnce:
				a.rememberOnceTrust(project)
			case ProjectTrustAlways:
				if err := a.Config.TrustProject(project); err != nil {
					return "", err
				}
			case ProjectTrustCancel:
				return "", clioutput.Usage("project profile selection canceled", "Pass --profile, set TALENTO_PROFILE, or explicitly trust the project config.")
			default:
				return "", fmt.Errorf("invalid project trust decision %q", decision)
			}
		}
		return project.Profile, nil
	}
	if !errors.Is(projectErr, config.ErrProjectConfigNotFound) {
		return "", projectErr
	}
	name, err := baseprofile.Resolve(baseprofile.ResolveOptions{
		DefaultProfile: cfg.DefaultProfile,
		Profiles:       profiles,
		Interactive:    false,
	})
	return requireProfile(name, required, err)
}

func requireProfile(name string, required bool, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if required && name == "" {
		return "", fmt.Errorf("no profile configured; run `talento profile create <name>` or `talento auth login --profile <name>`")
	}
	return name, nil
}

func (a *App) hasOnceTrust(project config.ProjectProfile) bool {
	a.projectOnceMu.Lock()
	defer a.projectOnceMu.Unlock()
	return a.projectOnce != nil && a.projectOnce[project.ConfigPath] == project.Profile+":"+project.Digest
}

func (a *App) rememberOnceTrust(project config.ProjectProfile) {
	a.projectOnceMu.Lock()
	defer a.projectOnceMu.Unlock()
	if a.projectOnce == nil {
		a.projectOnce = make(map[string]string)
	}
	a.projectOnce[project.ConfigPath] = project.Profile + ":" + project.Digest
}

func (a *App) promptProjectTrust(project config.ProjectProfile, state config.ProjectTrustState) (ProjectTrustDecision, error) {
	if a.ProjectTrustPrompt != nil {
		return a.ProjectTrustPrompt(project, state)
	}
	_, _ = fmt.Fprintf(a.Stderr,
		"Project profile request (%s):\n  Project: %s\n  Config: %s\n  Profile: %s\n",
		terminal.SanitizeLine(string(state)), terminal.SanitizeLine(project.ProjectDir),
		terminal.SanitizeLine(project.ConfigPath), terminal.SanitizeLine(project.Profile))
	_, _ = fmt.Fprint(a.Stderr, "Use once, always trust this exact file, or cancel? [o/a/C] ")
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "o", "once":
		return ProjectUseOnce, nil
	case "a", "always":
		return ProjectTrustAlways, nil
	case "", "c", "cancel":
		return ProjectTrustCancel, nil
	default:
		return "", fmt.Errorf("choose once, always, or cancel")
	}
}

func (a *App) AuthService(allowFileCredentials bool) (*auth.Service, error) {
	credentials, err := auth.NewCredentialStore(a.Paths, allowFileCredentials)
	if err != nil {
		return nil, err
	}
	return auth.NewService(a.Config, credentials), nil
}

func (a *App) MCP(ctx context.Context) (*mcpclient.Client, string, error) {
	profile, err := a.ResolveProfile(true)
	if err != nil {
		return nil, "", err
	}
	authService, err := a.AuthService(false)
	if err != nil {
		return nil, "", err
	}
	token, err := authService.AccessToken(ctx, profile)
	if err != nil {
		if auth.IsMissingCredentials(err) {
			return nil, "", clioutput.Auth(fmt.Sprintf("profile %q is not authenticated", profile))
		}
		return nil, "", fmt.Errorf("authenticate profile %q: %w", profile, err)
	}
	client, err := mcpclient.Connect(ctx, token)
	if err != nil {
		return nil, "", err
	}
	return client, profile, nil
}

type ToolExecution struct {
	Profile       string                 `json:"profile"`
	Preview       *mcpclient.ToolOutcome `json:"preview,omitempty"`
	Confirmation  *mcpclient.ToolOutcome `json:"confirmation,omitempty"`
	Result        *mcpclient.ToolOutcome `json:"result"`
	PreviewHandle PreviewHandle          `json:"-"`
}

func (e *ToolExecution) HumanText() string {
	if e == nil || e.Result == nil {
		return ""
	}
	return e.Result.HumanText()
}

func (a *App) ExecuteTool(ctx context.Context, name string, arguments map[string]any) (*ToolExecution, error) {
	client, profile, err := a.MCP(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return a.executeTool(ctx, client, profile, name, arguments)
}

type toolClient interface {
	ListTools(context.Context) ([]*mcp.Tool, error)
	CallTool(context.Context, string, map[string]any) (*mcpclient.ToolOutcome, error)
}

func (a *App) executeTool(ctx context.Context, client toolClient, profile, name string, arguments map[string]any) (*ToolExecution, error) {
	tools, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	available := false
	for _, tool := range tools {
		if tool.Name == name {
			available = true
			break
		}
	}
	if !available {
		return nil, unavailableTool(name, profile)
	}
	execution, err := callToolExecution(ctx, client, profile, name, arguments)
	if err != nil {
		return nil, err
	}
	outcome := execution.Result
	if !outcome.IsPreview() {
		return execution, nil
	}
	execution.Preview = outcome
	shouldConfirm := a.Global.Yes
	if !shouldConfirm && a.Interactive() {
		if err := a.writePreview(outcome.HumanText()); err != nil {
			return nil, err
		}
		shouldConfirm, err = a.promptConfirmation()
		if err != nil {
			return nil, err
		}
	}
	if !shouldConfirm {
		return execution, nil
	}
	if outcome.PreviewID == "" {
		return nil, clioutput.WithData(clioutput.API("The server returned a preview without an explicit preview_id; it was not confirmed", nil), execution)
	}
	execution, err = confirmToolExecution(ctx, client, execution, outcome.PreviewID)
	if err != nil {
		return nil, err
	}
	return execution, nil
}

func unavailableTool(name, profile string) error {
	return clioutput.Forbidden(fmt.Sprintf("tool %q is not available to profile %q; Talento role, permission, module, visibility, and tenant rules are server-authoritative", name, profile))
}

// Both the one-shot CLI and the persistent session share the original server
// outcome interpretation. Session callers retain executions on server rejection
// for rendering; the CLI wrapper keeps its existing nil/error envelope behavior.
func callToolExecution(ctx context.Context, client toolClient, profile, name string, arguments map[string]any) (*ToolExecution, error) {
	outcome, err := client.CallTool(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	if outcome == nil || outcome.Result == nil {
		return nil, errors.New("gateway returned an empty tool result")
	}
	execution := &ToolExecution{Profile: profile, Result: outcome}
	if outcome.IsError() {
		return execution, clioutput.WithData(clioutput.API("Talento rejected the tool call", nil), execution)
	}
	if outcome.IsPreview() {
		execution.Preview = outcome
	}
	return execution, nil
}

func confirmToolExecution(ctx context.Context, client toolClient, execution *ToolExecution, previewID string) (*ToolExecution, error) {
	confirmation, err := client.CallTool(ctx, "confirm_action", map[string]any{"preview_id": previewID})
	if err != nil {
		return execution, err
	}
	if confirmation == nil || confirmation.Result == nil {
		return execution, errors.New("gateway returned an empty confirmation result")
	}
	execution.Confirmation = confirmation
	execution.Result = confirmation
	if confirmation.IsError() {
		return execution, clioutput.WithData(clioutput.API("Talento rejected the preview confirmation", nil), execution)
	}
	return execution, nil
}

func (a *App) writePreview(text string) error {
	_, err := fmt.Fprintln(a.Stderr, terminal.Sanitize(strings.TrimSpace(text)))
	return err
}

func (a *App) promptConfirmation() (bool, error) {
	if _, err := fmt.Fprint(a.Stderr, "Confirm this preview? [y/N] "); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func Breadcrumb(action, command, description string) baseoutput.Breadcrumb {
	return baseoutput.Breadcrumb{Action: action, Cmd: command, Description: description}
}
