package managed

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type runnerFunc func(context.Context, string, []string, []string) ([]byte, error)

func (fn runnerFunc) Run(ctx context.Context, executable string, args, environment []string) ([]byte, error) {
	return fn(ctx, executable, args, environment)
}

func TestAdapterCapabilityCatalog(t *testing.T) {
	want := []string{"claude-code", "codex", "copilot", "cursor", "gemini", "grok", "opencode", "windsurf"}
	got := make([]string, 0, len(SupportedAgents))
	for _, agent := range SupportedAgents {
		adapter, ok := AdapterByID(agent.ID)
		if !ok {
			t.Fatalf("adapter missing for %s", agent.ID)
		}
		capabilities := adapter.Capabilities()
		if capabilities.InstallMethod != MethodManagedFile || capabilities.RemoveMethod != MethodManagedFile {
			t.Fatalf("%s mutation capabilities = %#v", agent.ID, capabilities)
		}
		if capabilities.VersionMethod != MethodNativeCommand || !reflect.DeepEqual(capabilities.Scopes, []string{"user", "project"}) {
			t.Fatalf("%s inspection capabilities = %#v", agent.ID, capabilities)
		}
		got = append(got, agent.ID)
	}
	sortStrings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog = %v, want %v", got, want)
	}
	if _, ok := AdapterByID("future-agent"); ok {
		t.Fatal("unknown adapter unexpectedly resolved")
	}
	if _, ok := AgentByID("future-agent"); ok {
		t.Fatal("unknown agent unexpectedly resolved")
	}
}

func TestGrokAdapterUsesNativeSkillDirectories(t *testing.T) {
	agent, ok := AgentByID("grok")
	if !ok {
		t.Fatal("grok adapter is missing")
	}
	if agent.Name != "Grok" || agent.Executable != "grok" || agent.UserPath != ".grok/skills/talento" || agent.ProjectPath != ".grok/skills/talento" || agent.DetectionPath != ".grok" || agent.SingleFile || agent.DependsOnSharedSkill {
		t.Fatalf("grok adapter = %#v", agent)
	}
}

func TestGrokDetectionUsesGrokExecutable(t *testing.T) {
	grok, ok := AdapterByID("grok")
	if !ok {
		t.Fatal("grok adapter is missing")
	}
	environment := AdapterEnvironment{
		Home: t.TempDir(),
		LookPath: func(name string) (string, error) {
			if name != "grok" {
				t.Fatalf("looked up %q", name)
			}
			return "/opt/tools/grok", nil
		},
	}
	detection := grok.Detect(context.Background(), environment)
	if !detection.Detected || detection.ExecutablePath != "/opt/tools/grok" || detection.DetectedBy != "executable" {
		t.Fatalf("grok executable detection = %#v", detection)
	}
}

func TestAdapterDetectionUsesExecutableOrAgentDirectory(t *testing.T) {
	home := t.TempDir()
	codex, _ := AdapterByID("codex")
	environment := AdapterEnvironment{
		Home: home,
		LookPath: func(name string) (string, error) {
			if name != "codex" {
				t.Fatalf("looked up %q", name)
			}
			return "/opt/tools/codex", nil
		},
	}
	detection := codex.Detect(context.Background(), environment)
	if !detection.Detected || detection.ExecutablePath != "/opt/tools/codex" || detection.DetectedBy != "executable" {
		t.Fatalf("executable detection = %#v", detection)
	}

	environment.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if detection := codex.Detect(context.Background(), environment); detection.Detected {
		t.Fatalf("unexpected missing detection = %#v", detection)
	}
	if err := mkdirAll(home + "/.codex/skills/talento"); err != nil {
		t.Fatal(err)
	}
	if detection := codex.Detect(context.Background(), environment); detection.Detected {
		t.Fatalf("managed integration fabricated agent detection = %#v", detection)
	}
	if err := os.WriteFile(home+"/.codex/config.toml", []byte("model = 'test'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	detection = codex.Detect(context.Background(), environment)
	if !detection.Detected || detection.DetectedBy != "agent-directory" || detection.ExecutablePath != "" {
		t.Fatalf("directory detection = %#v", detection)
	}
}

func TestAdapterVersionProbeIsBoundedSanitizedAndSecretFree(t *testing.T) {
	codex, _ := AdapterByID("codex")
	called := false
	probeHome := ""
	environment := AdapterEnvironment{
		Home:        "/safe/home",
		Timeout:     time.Second,
		Environment: []string{"PATH=/safe/bin", "HOME=/safe/home"},
		Runner: runnerFunc(func(_ context.Context, executable string, args, environment []string) ([]byte, error) {
			called = true
			if executable != "/safe/bin/codex" || !reflect.DeepEqual(args, []string{"--version"}) {
				t.Fatalf("command = %q %v", executable, args)
			}
			seenPath, seenConfig, seenTemp := false, false, false
			for _, value := range environment {
				if strings.Contains(value, "TOKEN") || strings.Contains(value, "SECRET") {
					t.Fatalf("secret reached runner: %q", value)
				}
				key, value, _ := strings.Cut(value, "=")
				switch key {
				case "PATH":
					seenPath = value == "/safe/bin"
				case "HOME":
					probeHome = value
				case "XDG_CONFIG_HOME":
					seenConfig = strings.HasPrefix(value, probeHome)
				case "TMPDIR":
					seenTemp = strings.HasPrefix(value, probeHome)
				}
			}
			if !seenPath || probeHome == "" || probeHome == "/safe/home" || !seenConfig || !seenTemp {
				t.Fatalf("probe was not isolated: %v", environment)
			}
			if err := os.WriteFile(filepath.Join(probeHome, "probe-created"), []byte("temporary"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []byte("codex 1.2.3\n\x1b]0;forged\a"), nil
		}),
	}
	version := codex.Version(context.Background(), environment, Detection{Detected: true, ExecutablePath: "/safe/bin/codex"})
	if !called || version.Status != "available" || version.Value != "codex 1.2.3" {
		t.Fatalf("version = %#v called=%v", version, called)
	}
	if _, err := os.Stat(probeHome); !os.IsNotExist(err) {
		t.Fatalf("isolated probe home was not cleaned up: %v", err)
	}
}

func TestDefaultAdapterEnvironmentDoesNotInheritSecrets(t *testing.T) {
	t.Setenv("TALENTO_ACCESS_TOKEN", "secret")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("OPENAI_API_KEY", "secret")
	environment := DefaultAdapterEnvironment("/safe/home")
	for _, value := range environment.Environment {
		if strings.Contains(value, "secret") || strings.HasPrefix(value, "TALENTO_ACCESS_TOKEN=") ||
			strings.HasPrefix(value, "ANTHROPIC_API_KEY=") || strings.HasPrefix(value, "OPENAI_API_KEY=") {
			t.Fatalf("secret environment variable inherited: %q", value)
		}
	}
}

func TestAdapterVersionProbeHandlesMissingMalformedFailureAndTimeout(t *testing.T) {
	codex, _ := AdapterByID("codex")
	if got := codex.Version(context.Background(), AdapterEnvironment{}, Detection{}); got.Status != "unavailable" {
		t.Fatalf("missing = %#v", got)
	}

	tests := []struct {
		name   string
		runner runnerFunc
		want   string
	}{
		{name: "malformed", runner: func(context.Context, string, []string, []string) ([]byte, error) { return []byte("\x1b[2J"), nil }, want: "malformed"},
		{name: "failure", runner: func(context.Context, string, []string, []string) ([]byte, error) {
			return []byte("bad\n\x1b[2Jforged"), errors.New("exit status 2")
		}, want: "error"},
		{name: "timeout", runner: func(ctx context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, want: "timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := AdapterEnvironment{Runner: test.runner, Timeout: 5 * time.Millisecond}
			got := codex.Version(context.Background(), environment, Detection{Detected: true, ExecutablePath: "/codex"})
			if got.Status != test.want || strings.Contains(got.Detail, "\x1b") || strings.Contains(got.Detail, "\n") {
				t.Fatalf("version = %#v", got)
			}
		})
	}
}

func TestManagedInstallNeverInvokesUnsupportedCodexNativeMutation(t *testing.T) {
	manager, _, _ := newLifecycleManager(t)
	manager.Runtime = AdapterEnvironment{
		LookPath: func(string) (string, error) { return "/codex", nil },
		Runner: runnerFunc(func(context.Context, string, []string, []string) ([]byte, error) {
			t.Fatal("managed install invoked a Codex subprocess")
			return nil, nil
		}),
	}
	if _, err := manager.Install(InstallOptions{Agents: []string{"codex"}, Scope: "user"}); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownAdapterMutationFailsClearly(t *testing.T) {
	manager, _, _ := newLifecycleManager(t)
	for _, operation := range []struct {
		name string
		run  func(InstallOptions) (Result, error)
	}{
		{name: "install", run: manager.Install},
		{name: "remove", run: manager.Remove},
	} {
		t.Run(operation.name, func(t *testing.T) {
			_, err := operation.run(InstallOptions{Agents: []string{"future-agent"}, Scope: "user"})
			if err == nil || !strings.Contains(err.Error(), `unsupported agent "future-agent"`) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func sortStrings(values []string) {
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}
