package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/talentohq/talento-cli/internal/config"
	"github.com/talentohq/talento-cli/internal/mcpclient"
)

func TestInteractiveHardGuardsCannotBeOverridden(t *testing.T) {
	tests := []struct {
		name    string
		options GlobalOptions
		env     string
	}{
		{name: "agent", options: GlobalOptions{Agent: true}},
		{name: "json", options: GlobalOptions{JSON: true}},
		{name: "markdown", options: GlobalOptions{Markdown: true}},
		{name: "jq", options: GlobalOptions{JQ: "."}},
		{name: "yes", options: GlobalOptions{Yes: true}},
		{name: "noninteractive environment", env: "TALENTO_NONINTERACTIVE"},
		{name: "CI", env: "CI"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.env != "" {
				t.Setenv(test.env, "1")
			}
			checked := false
			application := &App{Global: &test.options, InteractiveCheck: func() bool {
				checked = true
				return true
			}}
			if application.Interactive() {
				t.Fatal("non-interactive control was ignored")
			}
			if checked {
				t.Fatal("test seam bypassed a hard non-interactive control")
			}
		})
	}
}

func TestProjectProfilePrecedenceAndExplicitBypass(t *testing.T) {
	projectDir := writeAppProjectConfig(t, `{"profile":"project"}`)
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	for _, name := range []string{"flag", "environment", "project", "global"} {
		if _, err := store.CreateProfile(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetDefault("global"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, flag, environment, want string
	}{
		{name: "flag", flag: "flag", environment: "environment", want: "flag"},
		{name: "environment", environment: "environment", want: "environment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TALENTO_PROFILE", test.environment)
			cwdCalls := 0
			application := &App{
				Config: store, Global: &GlobalOptions{Profile: test.flag},
				WorkingDirectory: func() (string, error) { cwdCalls++; return projectDir, nil },
			}
			got, err := application.ResolveProfile(true)
			if err != nil || got != test.want {
				t.Fatalf("profile=%q err=%v", got, err)
			}
			if cwdCalls != 0 {
				t.Fatalf("project config was read for explicit selection")
			}
		})
	}

	// Even a malformed project file cannot block an explicit selector.
	if err := os.WriteFile(filepath.Join(projectDir, ".talento", "config.json"), []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	application := &App{Config: store, Global: &GlobalOptions{Profile: "flag"}, WorkingDirectory: func() (string, error) { return projectDir, nil }}
	if got, err := application.ResolveProfile(true); err != nil || got != "flag" {
		t.Fatalf("explicit bypass profile=%q err=%v", got, err)
	}
	t.Setenv("TALENTO_PROFILE", "environment")
	application.Global.Profile = ""
	if got, err := application.ResolveProfile(true); err != nil || got != "environment" {
		t.Fatalf("environment bypass profile=%q err=%v", got, err)
	}
	t.Setenv("TALENTO_PROFILE", "")

	if err := os.WriteFile(filepath.Join(projectDir, ".talento", "config.json"), []byte(`{"profile":"project"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := config.DiscoverProjectProfile(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TrustProject(project); err != nil {
		t.Fatal(err)
	}
	trustedProject := projectTestApp(t, store, projectDir)
	trustedProject.Global.Agent = true
	if got, err := trustedProject.ResolveProfile(true); err != nil || got != "project" {
		t.Fatalf("trusted project profile=%q err=%v", got, err)
	}
	globalFallback := projectTestApp(t, store, t.TempDir())
	globalFallback.Global.Agent = true
	if got, err := globalFallback.ResolveProfile(true); err != nil || got != "global" {
		t.Fatalf("global profile=%q err=%v", got, err)
	}
}

func TestProjectProfileTrustOnceAlwaysCancelAndStale(t *testing.T) {
	projectDir := writeAppProjectConfig(t, `{"profile":"acme"}`)
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}

	promptCalls := 0
	application := projectTestApp(t, store, projectDir)
	application.ProjectTrustPrompt = func(_ config.ProjectProfile, state config.ProjectTrustState) (ProjectTrustDecision, error) {
		promptCalls++
		if state != config.ProjectUntrusted {
			t.Fatalf("state = %s", state)
		}
		return ProjectUseOnce, nil
	}
	for range 2 {
		if got, err := application.ResolveProfile(true); err != nil || got != "acme" {
			t.Fatalf("once profile=%q err=%v", got, err)
		}
	}
	if promptCalls != 1 {
		t.Fatalf("once prompt calls = %d", promptCalls)
	}
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProjectTrust) != 0 {
		t.Fatalf("once persisted trust: %#v", cfg.ProjectTrust)
	}

	always := projectTestApp(t, store, projectDir)
	always.ProjectTrustPrompt = func(_ config.ProjectProfile, _ config.ProjectTrustState) (ProjectTrustDecision, error) {
		return ProjectTrustAlways, nil
	}
	if _, err := always.ResolveProfile(true); err != nil {
		t.Fatal(err)
	}
	noninteractive := projectTestApp(t, store, projectDir)
	noninteractive.Global.Agent = true
	if got, err := noninteractive.ResolveProfile(true); err != nil || got != "acme" {
		t.Fatalf("persisted profile=%q err=%v", got, err)
	}

	if err := os.WriteFile(filepath.Join(projectDir, ".talento", "config.json"), []byte("{\n\"profile\":\"acme\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := noninteractive.ResolveProfile(true); err == nil || !strings.Contains(err.Error(), "stale") || !strings.Contains(err.Error(), "trust-project") {
		t.Fatalf("stale error = %v", err)
	}

	cancel := projectTestApp(t, store, projectDir)
	cancel.ProjectTrustPrompt = func(_ config.ProjectProfile, state config.ProjectTrustState) (ProjectTrustDecision, error) {
		if state != config.ProjectStale {
			t.Fatalf("state = %s", state)
		}
		return ProjectTrustCancel, nil
	}
	if _, err := cancel.ResolveProfile(true); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestProjectTrustAlwaysBindsBytesReadBeforeThePrompt(t *testing.T) {
	projectDir := writeAppProjectConfig(t, `{"profile":"acme"}`)
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	for _, name := range []string{"acme", "beta"} {
		if _, err := store.CreateProfile(name); err != nil {
			t.Fatal(err)
		}
	}
	application := projectTestApp(t, store, projectDir)
	application.ProjectTrustPrompt = func(project config.ProjectProfile, state config.ProjectTrustState) (ProjectTrustDecision, error) {
		if project.Profile != "acme" || state != config.ProjectUntrusted {
			t.Fatalf("prompt project=%#v state=%s", project, state)
		}
		if err := os.WriteFile(filepath.Join(projectDir, ".talento", "config.json"), []byte(`{"profile":"beta"}`), 0o600); err != nil {
			return "", err
		}
		return ProjectTrustAlways, nil
	}
	selected, err := application.ResolveProfile(true)
	if err != nil || selected != "acme" {
		t.Fatalf("selected=%q err=%v", selected, err)
	}
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	current, err := config.DiscoverProjectProfile(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if current.Profile != "beta" || config.ProjectTrustStatus(current, cfg.ProjectTrust) != config.ProjectStale {
		t.Fatalf("current=%#v trust=%#v", current, cfg.ProjectTrust)
	}
}

func TestProjectProfileNonInteractiveModesNeverPrompt(t *testing.T) {
	projectDir := writeAppProjectConfig(t, `{"profile":"acme"}`)
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		options GlobalOptions
		env     string
	}{
		{name: "agent", options: GlobalOptions{Agent: true}},
		{name: "json", options: GlobalOptions{JSON: true}},
		{name: "markdown", options: GlobalOptions{Markdown: true}},
		{name: "jq", options: GlobalOptions{JQ: "."}},
		{name: "yes", options: GlobalOptions{Yes: true}},
		{name: "noninteractive environment", env: "TALENTO_NONINTERACTIVE"},
		{name: "CI", env: "CI"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.env != "" {
				t.Setenv(test.env, "1")
			}
			prompted := false
			application := projectTestApp(t, store, projectDir)
			application.Global = &test.options
			application.ProjectTrustPrompt = func(_ config.ProjectProfile, _ config.ProjectTrustState) (ProjectTrustDecision, error) {
				prompted = true
				return ProjectTrustAlways, nil
			}
			if _, err := application.ResolveProfile(true); err == nil || !strings.Contains(err.Error(), "trust-project") {
				t.Fatalf("error = %v", err)
			}
			if prompted {
				t.Fatal("non-interactive mode prompted")
			}
		})
	}

	redirected := projectTestApp(t, store, projectDir)
	redirected.InteractiveCheck = nil
	redirected.Stdin = strings.NewReader("a\n")
	redirected.Stdout = &bytes.Buffer{}
	if _, err := redirected.ResolveProfile(true); err == nil || !strings.Contains(err.Error(), "trust-project") {
		t.Fatalf("redirected error = %v", err)
	}
}

func TestProjectProfileMissingGlobalProfileFailsBeforeCredentialOrMCPUse(t *testing.T) {
	projectDir := writeAppProjectConfig(t, `{"profile":"missing"}`)
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	application := projectTestApp(t, store, projectDir)
	application.Global.Agent = true
	application.Paths.CredentialDir = filepath.Join(t.TempDir(), "must-not-be-used")
	if _, _, err := application.MCP(context.Background()); err == nil || !strings.Contains(err.Error(), `profile "missing"`) {
		t.Fatalf("MCP error = %v", err)
	}
}

func TestUntrustedProjectFailsBeforeCredentialOrMCPUse(t *testing.T) {
	projectDir := writeAppProjectConfig(t, `{"profile":"acme"}`)
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	application := projectTestApp(t, store, projectDir)
	application.Global.Agent = true
	application.Paths.CredentialDir = filepath.Join(t.TempDir(), "must-not-be-used")
	if _, _, err := application.MCP(context.Background()); err == nil || !strings.Contains(err.Error(), "trust-project") {
		t.Fatalf("MCP error = %v", err)
	}
}

func TestProjectTrustPromptSanitizesFilesystemText(t *testing.T) {
	var stderr bytes.Buffer
	application := &App{Stdin: strings.NewReader("o\n"), Stderr: &stderr}
	decision, err := application.promptProjectTrust(config.ProjectProfile{
		ProjectDir: "/safe/\x1b]8;;https://evil.example\x1b\\project",
		ConfigPath: "/safe/\x1b[31mconfig.json",
		Profile:    "acme",
	}, config.ProjectUntrusted)
	if err != nil || decision != ProjectUseOnce {
		t.Fatalf("decision=%q err=%v", decision, err)
	}
	if strings.Contains(stderr.String(), "\x1b") || !strings.Contains(stderr.String(), "/safe/project") {
		t.Fatalf("prompt = %q", stderr.String())
	}
}

func projectTestApp(t *testing.T, store *config.Store, projectDir string) *App {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("TALENTO_NONINTERACTIVE", "")
	return &App{
		Config: store, Global: &GlobalOptions{}, Stdin: strings.NewReader(""),
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, InteractiveCheck: func() bool { return true },
		WorkingDirectory: func() (string, error) { return projectDir, nil },
		projectOnce:      make(map[string]string),
	}
}

func writeAppProjectConfig(t *testing.T, content string) string {
	t.Helper()
	projectDir := t.TempDir()
	marker := filepath.Join(projectDir, ".talento")
	if err := os.MkdirAll(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, "config.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return projectDir
}

func TestInteractiveRequiresTerminalInputAndOutput(t *testing.T) {
	application := &App{
		Global: &GlobalOptions{}, Stdin: strings.NewReader("yes\n"),
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}
	if application.Interactive() {
		t.Fatal("redirected streams were treated as interactive")
	}
}

func TestWritePreviewContainsServerText(t *testing.T) {
	var stderr bytes.Buffer
	application := &App{Stderr: &stderr}
	if err := application.writePreview("safe \x1b[31mred\x1b[0m \x1b]0;title\x07 invoice\u202efdp.exe pay\u200bpal \u009b31mend"); err != nil {
		t.Fatal(err)
	}
	if got, want := stderr.String(), "safe red  invoicefdp.exe paypal end\n"; got != want {
		t.Fatalf("preview output = %q, want %q", got, want)
	}
}

type fakeToolClient struct {
	tools     []*mcp.Tool
	results   map[string][]*mcpclient.ToolOutcome
	calls     []string
	arguments []map[string]any
}

func (f *fakeToolClient) ListTools(context.Context) ([]*mcp.Tool, error) { return f.tools, nil }

func (f *fakeToolClient) CallTool(_ context.Context, name string, arguments map[string]any) (*mcpclient.ToolOutcome, error) {
	f.calls = append(f.calls, name)
	f.arguments = append(f.arguments, arguments)
	results := f.results[name]
	result := results[0]
	f.results[name] = results[1:]
	return result, nil
}

func TestWriteResultDeterminesConfirmationBehavior(t *testing.T) {
	committed := outcome("create_absence", mcpclient.StateCommitted, "")
	client := &fakeToolClient{tools: []*mcp.Tool{{Name: "create_absence"}}, results: map[string][]*mcpclient.ToolOutcome{"create_absence": {committed}}}
	application := testApp(true)
	execution, err := application.executeTool(context.Background(), client, "acme", "create_absence", map[string]any{"reason": "Holiday"})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Result.State != mcpclient.StateCommitted || len(client.calls) != 1 {
		t.Fatalf("immediate write requested extra confirmation: execution=%#v calls=%#v", execution, client.calls)
	}

	preview := outcome("create_absence", mcpclient.StatePreview, "preview-1")
	confirmed := outcome("confirm_action", mcpclient.StateCommitted, "")
	client = &fakeToolClient{
		tools:   []*mcp.Tool{{Name: "create_absence"}, {Name: "confirm_action"}},
		results: map[string][]*mcpclient.ToolOutcome{"create_absence": {preview}, "confirm_action": {confirmed}},
	}
	execution, err = application.executeTool(context.Background(), client, "acme", "create_absence", nil)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Preview == nil || execution.Confirmation == nil || execution.Result.State != mcpclient.StateCommitted {
		t.Fatalf("preview confirmation was not preserved: %#v", execution)
	}
	if len(client.calls) != 2 || client.calls[1] != "confirm_action" || client.arguments[1]["preview_id"] != "preview-1" {
		t.Fatalf("confirmation calls = %#v arguments = %#v", client.calls, client.arguments)
	}
}

func TestNonInteractivePreviewAndApprovalRemainUncommitted(t *testing.T) {
	preview := outcome("create_expense", mcpclient.StatePreview, "preview-2")
	client := &fakeToolClient{tools: []*mcp.Tool{{Name: "create_expense"}}, results: map[string][]*mcpclient.ToolOutcome{"create_expense": {preview}}}
	execution, err := testApp(false).executeTool(context.Background(), client, "acme", "create_expense", nil)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Result.State != mcpclient.StatePreview || execution.Confirmation != nil || len(client.calls) != 1 {
		t.Fatalf("preview unexpectedly committed: %#v calls=%#v", execution, client.calls)
	}

	submitted := outcome("create_absence", mcpclient.StateSubmitted, "")
	client = &fakeToolClient{tools: []*mcp.Tool{{Name: "create_absence"}}, results: map[string][]*mcpclient.ToolOutcome{"create_absence": {submitted}}}
	execution, err = testApp(true).executeTool(context.Background(), client, "acme", "create_absence", nil)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Result.State != mcpclient.StateSubmitted || len(client.calls) != 1 {
		t.Fatalf("approval request was misreported or confirmed: %#v", execution)
	}
}

func TestUnavailableToolAndExpiredPreviewFailClearly(t *testing.T) {
	client := &fakeToolClient{tools: []*mcp.Tool{{Name: "list_employees"}}, results: make(map[string][]*mcpclient.ToolOutcome)}
	if _, err := testApp(false).executeTool(context.Background(), client, "employee", "create_invoice", nil); err == nil {
		t.Fatal("expected unavailable tool error")
	}
	preview := outcome("create_absence", mcpclient.StatePreview, "expired")
	expired := outcome("confirm_action", mcpclient.StateError, "")
	client = &fakeToolClient{
		tools:   []*mcp.Tool{{Name: "create_absence"}, {Name: "confirm_action"}},
		results: map[string][]*mcpclient.ToolOutcome{"create_absence": {preview}, "confirm_action": {expired}},
	}
	execution, err := testApp(true).executeTool(context.Background(), client, "acme", "create_absence", nil)
	if err == nil || execution != nil {
		t.Fatalf("expected confirmation failure, execution=%#v err=%v", execution, err)
	}
}

func outcome(tool string, state mcpclient.ToolState, previewID string) *mcpclient.ToolOutcome {
	return &mcpclient.ToolOutcome{Tool: tool, State: state, PreviewID: previewID, Result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(state)}}, IsError: state == mcpclient.StateError}}
}

func testApp(yes bool) *App {
	return &App{Global: &GlobalOptions{Yes: yes, Agent: true}, Stdin: bytes.NewReader(nil), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
}
