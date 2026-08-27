package commands

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/auth"
	"github.com/talentohq/talento-cli/internal/config"
	"github.com/talentohq/talento-cli/internal/managed"
	clioutput "github.com/talentohq/talento-cli/internal/output"
)

type setupHarness struct {
	talento       *app.App
	assets        fs.FS
	deps          setupDependencies
	stdout        *bytes.Buffer
	stderr        *bytes.Buffer
	authenticated map[string]bool
	loginCalls    int
	statusCalls   int
	installCalls  []managed.InstallOptions
	doctorCalls   int
	loginErr      error
	detected      []managed.Agent
}

func newSetupHarness(t *testing.T, input string) *setupHarness {
	t.Helper()
	root := t.TempDir()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	h := &setupHarness{
		stdout: stdout, stderr: stderr, authenticated: make(map[string]bool),
		detected: []managed.Agent{
			{ID: "codex", Name: "Codex"},
			{ID: "claude-code", Name: "Claude Code"},
		},
		assets: fstest.MapFS{"skills/talento/SKILL.md": &fstest.MapFile{Data: []byte("skill")}},
	}
	paths := config.Paths{
		ConfigDir: root, ConfigFile: filepath.Join(root, "config.json"),
		CredentialDir: filepath.Join(root, "secrets"), HomeDir: filepath.Join(root, "home"),
	}
	h.talento = &app.App{
		Paths: paths, Config: config.NewStore(paths.ConfigFile), Global: &app.GlobalOptions{},
		Stdin: strings.NewReader(input), Stdout: stdout, Stderr: stderr,
	}
	h.deps = setupDependencies{
		Interactive: func() bool { return true },
		Status: func(profile string, _ bool) (auth.Status, error) {
			h.statusCalls++
			return auth.Status{Profile: profile, Authenticated: h.authenticated[profile]}, nil
		},
		Login: func(_ context.Context, profile string, _, _ bool, _ func(string)) (auth.Status, error) {
			h.loginCalls++
			if h.loginErr != nil {
				return auth.Status{}, h.loginErr
			}
			h.authenticated[profile] = true
			return auth.Status{Profile: profile, Authenticated: true}, nil
		},
		Detect: func(string) []managed.Agent { return append([]managed.Agent(nil), h.detected...) },
		Install: func(options managed.InstallOptions) (managed.Result, error) {
			h.installCalls = append(h.installCalls, options)
			return managed.Result{Installed: []string{"managed-skill"}}, nil
		},
		Doctor: func(context.Context, *app.App, fs.FS, bool) doctorReport {
			h.doctorCalls++
			return doctorReport{Status: "pass", Healthy: true, Checks: []doctorCheck{{Name: "authentication", Status: "pass", Message: "grant is present"}}}
		},
		WorkingDirectory: func() (string, error) { return root, nil },
		Shell:            func() string { return "/bin/zsh" },
	}
	return h
}

func TestInteractiveSetupFreshProfileOAuthMultiAgentAndCompletion(t *testing.T) {
	h := newSetupHarness(t, "\n\n1,2\nproject\ny\n")
	command := newSetupCommandWithDependencies(h.talento, h.assets, h.deps)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if h.loginCalls != 1 || h.statusCalls != 2 || h.doctorCalls != 1 {
		t.Fatalf("calls login=%d status=%d doctor=%d", h.loginCalls, h.statusCalls, h.doctorCalls)
	}
	if len(h.installCalls) != 1 {
		t.Fatalf("install calls = %#v", h.installCalls)
	}
	got := h.installCalls[0]
	if got.Scope != "project" || strings.Join(got.Agents, ",") != "claude-code,codex" {
		t.Fatalf("install options = %#v", got)
	}
	profile, err := h.talento.Config.Profile("default")
	if err != nil || profile.Endpoint != config.Endpoint {
		t.Fatalf("profile = %#v, err = %v", profile, err)
	}
	output := h.stdout.String()
	if !strings.Contains(output, "Talento setup: COMPLETE") || !strings.Contains(output, "talento completion zsh") || !strings.Contains(output, "talento commands --available --profile default") {
		t.Fatalf("setup output = %q", output)
	}
}

func TestInteractiveSetupSelectsExistingAuthenticatedProfile(t *testing.T) {
	h := newSetupHarness(t, "2\n\n\n\n")
	if _, err := h.talento.Config.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.talento.Config.CreateProfile("beta"); err != nil {
		t.Fatal(err)
	}
	h.authenticated["beta"] = true
	command := newSetupCommandWithDependencies(h.talento, h.assets, h.deps)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if h.loginCalls != 0 {
		t.Fatalf("existing grant unexpectedly replaced: %d login calls", h.loginCalls)
	}
	_, defaultProfile, err := h.talento.Config.ProfileNames()
	if err != nil || defaultProfile != "beta" {
		t.Fatalf("default profile = %q, err = %v", defaultProfile, err)
	}
}

func TestInteractiveSetupAuthenticationRefusalKeepsCreatedProfile(t *testing.T) {
	h := newSetupHarness(t, "acme\nn\n")
	err := newSetupCommandWithDependencies(h.talento, h.assets, h.deps).Execute()
	if err == nil || clioutput.ExitCode(err) != 3 {
		t.Fatalf("error = %v, exit = %d", err, clioutput.ExitCode(err))
	}
	if _, profileErr := h.talento.Config.Profile("acme"); profileErr != nil {
		t.Fatalf("completed profile stage was not kept: %v", profileErr)
	}
	if h.loginCalls != 0 || len(h.installCalls) != 0 {
		t.Fatalf("calls login=%d install=%d", h.loginCalls, len(h.installCalls))
	}
}

func TestInteractiveSetupOAuthFailureReportsRecoverablePartialState(t *testing.T) {
	h := newSetupHarness(t, "\n\n")
	h.loginErr = errors.New("authorization was denied")
	err := newSetupCommandWithDependencies(h.talento, h.assets, h.deps).Execute()
	if err == nil || !strings.Contains(err.Error(), "authorization was denied") {
		t.Fatalf("error = %v", err)
	}
	if _, profileErr := h.talento.Config.Profile("default"); profileErr != nil {
		t.Fatalf("profile should remain for idempotent resume: %v", profileErr)
	}
	var rendered interface{ RenderedErrorData() any }
	if !errors.As(err, &rendered) {
		t.Fatalf("partial setup error has no rendered progress: %T", err)
	}
	view, ok := rendered.RenderedErrorData().(setupView)
	if !ok || view.Status != "incomplete" || !strings.Contains(view.Next, "auth login") {
		t.Fatalf("partial setup view = %#v", rendered.RenderedErrorData())
	}
}

func TestInteractiveSetupForwardsNoOpenAndCredentialFallbackExplicitly(t *testing.T) {
	h := newSetupHarness(t, "\n\n\n")
	h.detected = nil
	var gotNoOpen, gotAllowFile bool
	h.deps.Login = func(_ context.Context, profile string, noOpen, allowFile bool, sink func(string)) (auth.Status, error) {
		h.loginCalls++
		gotNoOpen, gotAllowFile = noOpen, allowFile
		if sink == nil {
			t.Fatal("--no-open login did not receive an authorization URL sink")
		}
		sink("https://auth.example.test/authorize")
		h.authenticated[profile] = true
		return auth.Status{Profile: profile, Authenticated: true}, nil
	}
	command := newSetupCommandWithDependencies(h.talento, h.assets, h.deps)
	command.SetArgs([]string{"--no-open", "--allow-file-credentials"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !gotNoOpen || !gotAllowFile || !strings.Contains(h.stderr.String(), "https://auth.example.test/authorize") {
		t.Fatalf("noOpen=%v allowFile=%v stderr=%q", gotNoOpen, gotAllowFile, h.stderr.String())
	}
}

func TestInteractiveSetupHandlesNoDetectedAgents(t *testing.T) {
	h := newSetupHarness(t, "\n\n\n")
	h.detected = nil
	if err := newSetupCommandWithDependencies(h.talento, h.assets, h.deps).Execute(); err != nil {
		t.Fatal(err)
	}
	if len(h.installCalls) != 0 {
		t.Fatalf("install should be skipped: %#v", h.installCalls)
	}
	if !strings.Contains(h.stdout.String(), "no supported local coding agent") || !strings.Contains(h.stdout.String(), "Next: talento setup") {
		t.Fatalf("output = %q", h.stdout.String())
	}
}

func TestInteractiveSetupCancellationKeepsCompletedAuthentication(t *testing.T) {
	h := newSetupHarness(t, "\n\nq\n")
	err := newSetupCommandWithDependencies(h.talento, h.assets, h.deps).Execute()
	if !errors.Is(err, errSetupCanceled) {
		t.Fatalf("error = %v", err)
	}
	if !h.authenticated["default"] || len(h.installCalls) != 0 {
		t.Fatalf("authenticated=%v install=%#v", h.authenticated, h.installCalls)
	}
}

func TestAutomatedSetupNeverTouchesAuthenticationOrPrompts(t *testing.T) {
	tests := []struct {
		name   string
		global app.GlobalOptions
		env    map[string]string
	}{
		{name: "json", global: app.GlobalOptions{JSON: true}},
		{name: "agent", global: app.GlobalOptions{Agent: true}},
		{name: "markdown", global: app.GlobalOptions{Markdown: true}},
		{name: "jq", global: app.GlobalOptions{JQ: "."}},
		{name: "yes", global: app.GlobalOptions{Yes: true}},
		{name: "noninteractive env", env: map[string]string{"TALENTO_NONINTERACTIVE": "1"}},
		{name: "ci", env: map[string]string{"CI": "true"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newSetupHarness(t, "this input must remain unread\n")
			*h.talento.Global = test.global
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			h.talento.InteractiveCheck = func() bool { return true }
			h.deps.Interactive = h.talento.Interactive
			command := newSetupCommandWithDependencies(h.talento, h.assets, h.deps)
			command.SetArgs([]string{"--agent", "codex", "--scope", "user"})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if h.loginCalls != 0 || h.statusCalls != 0 || h.doctorCalls != 0 || len(h.installCalls) != 1 {
				t.Fatalf("login=%d status=%d doctor=%d install=%d", h.loginCalls, h.statusCalls, h.doctorCalls, len(h.installCalls))
			}
			if names, _, err := h.talento.Config.ProfileNames(); err != nil || len(names) != 0 {
				t.Fatalf("automation mutated profiles: names=%v err=%v", names, err)
			}
			if strings.Contains(h.stderr.String(), "Profile name") || strings.Contains(h.stderr.String(), "Authenticate") {
				t.Fatalf("automation prompted: %q", h.stderr.String())
			}
		})
	}
}

func TestRedirectedSetupIsNonInteractive(t *testing.T) {
	h := newSetupHarness(t, "prompt data\n")
	h.deps.Interactive = h.talento.Interactive
	command := newSetupCommandWithDependencies(h.talento, h.assets, h.deps)
	command.SetArgs([]string{"--agent", "codex"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if h.loginCalls != 0 || h.statusCalls != 0 || len(h.installCalls) != 1 {
		t.Fatalf("login=%d status=%d install=%d", h.loginCalls, h.statusCalls, len(h.installCalls))
	}
}

func TestBareRootOffersSetupOnlyWithoutUsableAuthentication(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		h := newSetupHarness(t, "n\n")
		command := &cobra.Command{Use: "talento"}
		command.SetHelpFunc(func(*cobra.Command, []string) {})
		if err := runBareRoot(command, h.talento, h.assets, h.deps); err != nil {
			t.Fatal(err)
		}
		if h.loginCalls != 0 || !strings.Contains(h.stderr.String(), "Run guided setup") || !strings.Contains(h.stderr.String(), "talento setup") {
			t.Fatalf("login=%d stderr=%q", h.loginCalls, h.stderr.String())
		}
	})

	t.Run("authenticated", func(t *testing.T) {
		h := newSetupHarness(t, "")
		if _, err := h.talento.Config.CreateProfile("acme"); err != nil {
			t.Fatal(err)
		}
		h.authenticated["acme"] = true
		command := &cobra.Command{Use: "talento"}
		command.SetHelpFunc(func(*cobra.Command, []string) {})
		if err := runBareRoot(command, h.talento, h.assets, h.deps); err != nil {
			t.Fatal(err)
		}
		if h.loginCalls != 0 || h.stderr.Len() != 0 {
			t.Fatalf("authenticated root prompted: %q", h.stderr.String())
		}
	})
}

func TestLoggedOutCommandGuidanceDeclinePreservesCommandLifecycle(t *testing.T) {
	h := newSetupHarness(t, "n\n")
	command := markRequiresAuth(&cobra.Command{Use: "list"})
	if err := maybeOfferAuthentication(command, h.talento, h.deps); err != nil {
		t.Fatal(err)
	}
	if h.loginCalls != 0 || !strings.Contains(h.stderr.String(), "Sign in now") {
		t.Fatalf("login=%d stderr=%q", h.loginCalls, h.stderr.String())
	}
	if names, _, err := h.talento.Config.ProfileNames(); err != nil || len(names) != 0 {
		t.Fatalf("declining guidance mutated config: names=%v err=%v", names, err)
	}
}

func TestLoggedOutCommandCanAuthenticateAndResumeNaturally(t *testing.T) {
	h := newSetupHarness(t, "y\nacme\n")
	command := markRequiresAuth(&cobra.Command{Use: "list"})
	if err := maybeOfferAuthentication(command, h.talento, h.deps); err != nil {
		t.Fatal(err)
	}
	if h.loginCalls != 1 || h.talento.Global.Profile != "acme" || !strings.Contains(h.stderr.String(), "Resuming list") {
		t.Fatalf("login=%d profile=%q stderr=%q", h.loginCalls, h.talento.Global.Profile, h.stderr.String())
	}
}
