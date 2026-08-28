package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	baseoutput "github.com/basecamp/cli/output"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/auth"
	"github.com/talentohq/talento-cli/internal/config"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/tui"
)

type tuiCommandHarness struct {
	*setupHarness
	root          *cobra.Command
	deps          tuiDependencies
	options       tui.Options
	run           func(context.Context, tui.Options) error
	runCalls      int
	openProfiles  []string
	openAllowFile []bool
	loginOptions  []auth.LoginOptions
	loginFallback []bool
}

func newTUICommandHarness(t *testing.T) *tuiCommandHarness {
	t.Helper()
	for _, name := range []string{"CI", "TALENTO_NONINTERACTIVE", "TALENTO_PROFILE", "TALENTO_ALLOW_FILE_CREDENTIALS"} {
		t.Setenv(name, "")
	}
	t.Setenv("TERM", "xterm-256color")
	h := &tuiCommandHarness{setupHarness: newSetupHarness(t, "")}
	h.talento.InteractiveCheck = func() bool { return true }
	work := t.TempDir()
	h.talento.WorkingDirectory = func() (string, error) { return work, nil }
	h.deps = tuiDependencies{
		Terminal: func(io.Reader, io.Writer) bool { return true },
		Run: func(ctx context.Context, options tui.Options) error {
			h.runCalls++
			h.options = options
			if h.run != nil {
				return h.run(ctx, options)
			}
			return nil
		},
		OpenSession: func(_ context.Context, profile string, allowFile bool) (app.Session, error) {
			h.openProfiles = append(h.openProfiles, profile)
			h.openAllowFile = append(h.openAllowFile, allowFile)
			return nil, nil
		},
		Login: func(_ context.Context, options auth.LoginOptions, allowFile bool) error {
			h.loginOptions = append(h.loginOptions, options)
			h.loginFallback = append(h.loginFallback, allowFile)
			if options.URLSink != nil {
				options.URLSink("https://auth.example.test/authorize")
			}
			return nil
		},
	}
	return h
}

func (h *tuiCommandHarness) execute(t *testing.T, args ...string) error {
	t.Helper()
	h.root = newRootCommand(h.talento, h.assets, h.setupHarness.deps)
	existing, _, err := h.root.Find([]string{"tui"})
	if err != nil || existing == h.root {
		t.Fatalf("TUI is not registered: %v", err)
	}
	h.root.RemoveCommand(existing)
	command := newTUICommandWithDependencies(h.talento, h.deps)
	command.GroupID = groupWork
	h.root.AddCommand(command)
	classifyCobraUsageErrors(command)
	h.root.SetArgs(append([]string{"tui"}, args...))
	return h.root.Execute()
}

func (h *tuiCommandHarness) assertNoAuthentication(t *testing.T) {
	t.Helper()
	if h.loginCalls != 0 || h.statusCalls != 0 || len(h.loginOptions) != 0 || len(h.openProfiles) != 0 {
		t.Fatalf("unexpected authentication: setup login=%d status=%d TUI login=%d open=%v", h.loginCalls, h.statusCalls, len(h.loginOptions), h.openProfiles)
	}
	if len(h.installCalls) != 0 {
		t.Fatalf("TUI installed agents: %#v", h.installCalls)
	}
}

func TestTUIRejectsMachineModesBeforeProfileResolutionOrAuthentication(t *testing.T) {
	for _, args := range [][]string{
		{"--json"}, {"--md"}, {"--agent"}, {"--jq", "."}, {"--jq="}, {"--yes"}, {"-y"},
		{"--json=false"}, {"--md=false"}, {"--agent=false"}, {"--yes=false"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newTUICommandHarness(t)
			h.talento.WorkingDirectory = func() (string, error) {
				t.Fatal("profile resolved before machine-mode gate")
				return "", nil
			}
			err := h.execute(t, args...)
			if err == nil || clioutput.ExitCode(err) != baseoutput.ExitUsage {
				t.Fatalf("error = %v, exit = %d", err, clioutput.ExitCode(err))
			}
			if h.runCalls != 0 {
				t.Fatal("machine mode entered TUI")
			}
			h.assertNoAuthentication(t)
		})
	}
}

func TestTUIRejectsNoninteractiveEnvironmentBeforeProfileResolution(t *testing.T) {
	for _, setting := range []struct{ name, value string }{
		{"CI", "true"}, {"CI", "false"}, {"TALENTO_NONINTERACTIVE", "1"},
		{"TERM", "dumb"}, {"TERM", " DUMB "},
	} {
		t.Run(setting.name+"="+setting.value, func(t *testing.T) {
			h := newTUICommandHarness(t)
			t.Setenv(setting.name, setting.value)
			h.talento.WorkingDirectory = func() (string, error) {
				t.Fatal("profile resolved before environment gate")
				return "", nil
			}
			if err := h.execute(t); err == nil || clioutput.ExitCode(err) != baseoutput.ExitUsage {
				t.Fatalf("error = %v", err)
			}
			if h.runCalls != 0 {
				t.Fatal("noninteractive environment entered TUI")
			}
			h.assertNoAuthentication(t)
		})
	}
}

func TestTUIGatesRedirectedStreamsWithRealTerminalCheck(t *testing.T) {
	for _, redirected := range []string{"stdin", "stdout", "both"} {
		t.Run(redirected, func(t *testing.T) {
			h := newTUICommandHarness(t)
			h.deps.Terminal = tuiTerminal
			switch redirected {
			case "stdin":
				h.talento.Stdout = os.Stdout
			case "stdout":
				h.talento.Stdin = os.Stdin
			}
			h.talento.WorkingDirectory = func() (string, error) {
				t.Fatal("profile resolved for redirected I/O")
				return "", nil
			}
			if err := h.execute(t); err == nil || clioutput.ExitCode(err) != baseoutput.ExitUsage {
				t.Fatalf("error = %v", err)
			}
			if h.runCalls != 0 {
				t.Fatal("redirected stream entered TUI")
			}
			h.assertNoAuthentication(t)
		})
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	if tuiTerminal(devNull, devNull) {
		t.Fatal("character device without a terminal was accepted")
	}
}

func TestTUIHelpDoesNotResolveProfileOrAuthenticate(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--help", "--agent"}, {"--help", "--json"}, {"--help", "--yes"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newTUICommandHarness(t)
			t.Setenv("TERM", "dumb")
			t.Setenv("CI", "true")
			h.deps.Terminal = func(io.Reader, io.Writer) bool {
				t.Fatal("help performed terminal takeover check")
				return false
			}
			h.talento.WorkingDirectory = func() (string, error) {
				t.Fatal("help resolved profile")
				return "", nil
			}
			if err := h.execute(t, args...); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(h.stdout.String(), "tui") || !strings.Contains(h.stdout.String(), "no-open") {
				t.Fatalf("help = %s", h.stdout.String())
			}
			if h.runCalls != 0 {
				t.Fatal("help entered TUI")
			}
			h.assertNoAuthentication(t)
		})
	}
}

func TestTUIInitialEmptyConfigurationIsNotWritten(t *testing.T) {
	h := newTUICommandHarness(t)
	h.run = func(ctx context.Context, options tui.Options) error {
		if options.Profile != "default" {
			t.Fatalf("profile = %q", options.Profile)
		}
		profiles, err := options.Profiles()
		if err != nil || len(profiles) != 0 {
			t.Fatalf("profiles = %v, error = %v", profiles, err)
		}
		if _, err := options.OpenSession(ctx, "default"); err == nil || clioutput.ExitCode(err) != 3 {
			t.Fatalf("prospective profile connection error = %v", err)
		}
		return nil
	}
	if err := h.execute(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.talento.Config.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup created config: %v", err)
	}
	h.assertNoAuthentication(t)
}

func TestTUIExplicitMissingSelectorsNeverBecomeDefault(t *testing.T) {
	for _, store := range []string{"empty", "configured"} {
		for _, source := range []string{"flag", "environment"} {
			t.Run(store+"/"+source, func(t *testing.T) {
				h := newTUICommandHarness(t)
				if store == "configured" {
					if _, err := h.talento.Config.CreateProfile("existing"); err != nil {
						t.Fatal(err)
					}
				}
				var args []string
				if source == "flag" {
					args = []string{"--profile", "missing"}
				} else {
					t.Setenv("TALENTO_PROFILE", "missing")
				}
				if err := h.execute(t, args...); err == nil || clioutput.ExitCode(err) != baseoutput.ExitUsage {
					t.Fatalf("error = %v", err)
				}
				if h.runCalls != 0 {
					t.Fatal("invalid selector entered TUI")
				}
				for _, name := range []string{"missing", "default"} {
					if _, err := h.talento.Config.Profile(name); err == nil {
						t.Fatalf("created %q without user login", name)
					}
				}
				h.assertNoAuthentication(t)
			})
		}
	}
}

func TestTUIRejectsInvalidProfileNames(t *testing.T) {
	h := newTUICommandHarness(t)
	err := h.execute(t, "--profile", "$(invalid)")
	if err == nil || clioutput.ExitCode(err) != baseoutput.ExitUsage || !strings.Contains(err.Error(), "invalid profile name") {
		t.Fatalf("error = %v", err)
	}
	if h.runCalls != 0 {
		t.Fatal("invalid profile name entered TUI")
	}
	h.assertNoAuthentication(t)
}

func TestTUIResolvesProfilePrecedenceWithoutChangingDefault(t *testing.T) {
	for _, source := range []string{"default", "project", "environment", "flag"} {
		t.Run(source, func(t *testing.T) {
			h := newTUICommandHarness(t)
			for _, name := range []string{"default", "project", "environment", "flag"} {
				if _, err := h.talento.Config.CreateProfile(name); err != nil {
					t.Fatal(err)
				}
			}
			var args []string
			if source != "default" {
				project := writeTUIProject(t, h, `{"profile":"project"}`)
				if err := h.talento.Config.TrustProject(project); err != nil {
					t.Fatal(err)
				}
			}
			if source == "environment" || source == "flag" {
				t.Setenv("TALENTO_PROFILE", "environment")
			}
			if source == "flag" {
				args = []string{"--profile", "flag"}
			}
			if err := h.execute(t, args...); err != nil {
				t.Fatal(err)
			}
			if h.options.Profile != source {
				t.Fatalf("selected %q, want %q", h.options.Profile, source)
			}
			_, defaultProfile, err := h.talento.Config.ProfileNames()
			if err != nil || defaultProfile != "default" {
				t.Fatalf("default = %q, error = %v", defaultProfile, err)
			}
			h.assertNoAuthentication(t)
		})
	}
}

func writeTUIProject(t *testing.T, h *tuiCommandHarness, content string) config.ProjectProfile {
	t.Helper()
	work := t.TempDir()
	path := filepath.Join(work, ".talento", "config.json")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	h.talento.WorkingDirectory = func() (string, error) { return work, nil }
	project, err := config.DiscoverProjectProfile(work)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func TestTUITrustIsResolvedBeforeTerminalTakeover(t *testing.T) {
	for _, state := range []config.ProjectTrustState{config.ProjectUntrusted, config.ProjectStale} {
		t.Run(string(state), func(t *testing.T) {
			h := newTUICommandHarness(t)
			if _, err := h.talento.Config.CreateProfile("project"); err != nil {
				t.Fatal(err)
			}
			project := writeTUIProject(t, h, `{"profile":"project"}`)
			if state == config.ProjectStale {
				if err := h.talento.Config.TrustProject(project); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(project.ConfigPath, []byte("{\n\"profile\":\"project\"\n}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			prompted := false
			h.talento.ProjectTrustPrompt = func(_ config.ProjectProfile, got config.ProjectTrustState) (app.ProjectTrustDecision, error) {
				if h.runCalls != 0 || got != state {
					t.Fatalf("trust prompt after takeover or wrong state: runs=%d state=%q", h.runCalls, got)
				}
				prompted = true
				return app.ProjectTrustCancel, nil
			}
			if err := h.execute(t); err == nil || !strings.Contains(err.Error(), "canceled") {
				t.Fatalf("error = %v", err)
			}
			if !prompted || h.runCalls != 0 {
				t.Fatalf("prompted=%v runs=%d", prompted, h.runCalls)
			}
			h.assertNoAuthentication(t)
		})
	}
}

func TestTUIProjectUseOnceDoesNotPersistTrust(t *testing.T) {
	h := newTUICommandHarness(t)
	if _, err := h.talento.Config.CreateProfile("project"); err != nil {
		t.Fatal(err)
	}
	writeTUIProject(t, h, `{"profile":"project"}`)
	h.talento.ProjectTrustPrompt = func(config.ProjectProfile, config.ProjectTrustState) (app.ProjectTrustDecision, error) {
		if h.runCalls != 0 {
			t.Fatal("trust prompt happened after TUI launch")
		}
		return app.ProjectUseOnce, nil
	}
	if err := h.execute(t); err != nil {
		t.Fatal(err)
	}
	state, err := h.talento.Config.Load()
	if err != nil || len(state.ProjectTrust) != 0 || h.options.Profile != "project" {
		t.Fatalf("state = %#v, profile = %q, error = %v", state, h.options.Profile, err)
	}
}

func TestTUIExplicitLoginCreatesOnlyProspectiveProfileAndForwardsOptions(t *testing.T) {
	for _, options := range []struct {
		name      string
		args      []string
		noOpen    bool
		allowFile bool
	}{
		{name: "normal"},
		{name: "manual", args: []string{"--no-open"}, noOpen: true},
		{name: "fallback", args: []string{"--no-open", "--allow-file-credentials"}, noOpen: true, allowFile: true},
	} {
		t.Run(options.name, func(t *testing.T) {
			h := newTUICommandHarness(t)
			var authURL string
			h.run = func(ctx context.Context, opts tui.Options) error {
				if _, err := h.talento.Config.Profile("default"); err == nil {
					t.Fatal("default was created before explicit Login")
				}
				if err := opts.Login(ctx, "unlisted", nil); err == nil {
					t.Fatal("login created arbitrary profile")
				}
				if err := opts.Login(ctx, "default", func(value string) { authURL = value }); err != nil {
					return err
				}
				if _, err := h.talento.Config.Profile("default"); err != nil {
					t.Fatal(err)
				}
				_, err := opts.OpenSession(ctx, "default")
				return err
			}
			if err := h.execute(t, options.args...); err != nil {
				t.Fatal(err)
			}
			if len(h.loginOptions) != 1 || h.loginOptions[0].Profile != "default" || h.loginOptions[0].NoOpen != options.noOpen || h.loginFallback[0] != options.allowFile {
				t.Fatalf("login options=%#v fallback=%v", h.loginOptions, h.loginFallback)
			}
			if authURL != "https://auth.example.test/authorize" || !reflect.DeepEqual(h.openAllowFile, []bool{options.allowFile}) {
				t.Fatalf("URL=%q open fallback=%v", authURL, h.openAllowFile)
			}
			if h.statusCalls != 0 || h.loginCalls != 0 || len(h.installCalls) != 0 {
				t.Fatal("TUI used setup authentication/agent flow")
			}
		})
	}
}

func TestTUIProfileCallbacksNeverChangeDefaultsOrGlobalSelection(t *testing.T) {
	h := newTUICommandHarness(t)
	for _, name := range []string{"acme", "beta"} {
		if _, err := h.talento.Config.CreateProfile(name); err != nil {
			t.Fatal(err)
		}
	}
	h.run = func(ctx context.Context, opts tui.Options) error {
		names, err := opts.Profiles()
		if err != nil || !reflect.DeepEqual(names, []string{"acme", "beta"}) {
			t.Fatalf("profiles=%v error=%v", names, err)
		}
		if _, err := opts.OpenSession(ctx, "beta"); err != nil {
			return err
		}
		if err := opts.Login(ctx, "beta", nil); err != nil {
			return err
		}
		if _, err := opts.OpenSession(ctx, "unknown"); err == nil {
			t.Fatal("opened unconfigured profile")
		}
		return nil
	}
	if err := h.execute(t, "--profile", "acme"); err != nil {
		t.Fatal(err)
	}
	_, defaultProfile, err := h.talento.Config.ProfileNames()
	if err != nil || defaultProfile != "acme" || h.talento.Global.Profile != "acme" {
		t.Fatalf("default=%q global=%q error=%v", defaultProfile, h.talento.Global.Profile, err)
	}
	if !reflect.DeepEqual(h.openProfiles, []string{"beta"}) || len(h.loginOptions) != 1 || h.loginOptions[0].Profile != "beta" {
		t.Fatalf("open=%v login=%v", h.openProfiles, h.loginOptions)
	}
}

func TestTUICanceledLoginDoesNotCreateConfig(t *testing.T) {
	h := newTUICommandHarness(t)
	h.run = func(_ context.Context, opts tui.Options) error {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := opts.OpenSession(ctx, "default"); !errors.Is(err, context.Canceled) {
			t.Fatalf("open error = %v", err)
		}
		return opts.Login(ctx, "default", nil)
	}
	if err := h.execute(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(h.talento.Config.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled login created config: %v", err)
	}
	h.assertNoAuthentication(t)
}

func TestTUIPropagatesRunnerAndCallbackErrorsWithoutOutputEnvelope(t *testing.T) {
	h := newTUICommandHarness(t)
	expected := errors.New("terminal stopped")
	h.run = func(_ context.Context, opts tui.Options) error {
		if opts.Stdin != h.talento.Stdin || opts.Stdout != h.talento.Stdout {
			t.Fatal("runner did not receive app streams")
		}
		return expected
	}
	if err := h.execute(t); !errors.Is(err, expected) {
		t.Fatalf("error = %v", err)
	}
	if h.stdout.Len() != 0 || h.stderr.Len() != 0 {
		t.Fatalf("TUI wrote CLI envelope: stdout=%q stderr=%q", h.stdout.String(), h.stderr.String())
	}
	h.assertNoAuthentication(t)

	h = newTUICommandHarness(t)
	if _, err := h.talento.Config.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	h.deps.OpenSession = func(context.Context, string, bool) (app.Session, error) { return nil, expected }
	h.deps.Login = func(context.Context, auth.LoginOptions, bool) error { return expected }
	h.run = func(ctx context.Context, opts tui.Options) error {
		if _, err := opts.OpenSession(ctx, "acme"); !errors.Is(err, expected) {
			t.Fatalf("open error = %v", err)
		}
		return opts.Login(ctx, "acme", nil)
	}
	if err := h.execute(t); !errors.Is(err, expected) {
		t.Fatalf("login error = %v", err)
	}
}

func TestTUIRegistrationDoesNotRequestLineAuthentication(t *testing.T) {
	h := newTUICommandHarness(t)
	root := newRootCommand(h.talento, h.assets, h.setupHarness.deps)
	command, _, err := root.Find([]string{"tui"})
	if err != nil || command == root || command.GroupID != groupWork {
		t.Fatalf("registered command=%v error=%v", command, err)
	}
	if command.Annotations[requiresAuthAnnotation] != "" {
		t.Fatal("TUI unexpectedly runs the line-based auth hook")
	}
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil || !strings.Contains(output.String(), "tui") {
		t.Fatalf("help=%q error=%v", output.String(), err)
	}
}
