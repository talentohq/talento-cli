package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/config"
	"github.com/talentohq/talento-cli/internal/managed"
)

type statusRunner func(context.Context, string, []string, []string) ([]byte, error)

func (fn statusRunner) Run(ctx context.Context, executable string, args, environment []string) ([]byte, error) {
	return fn(ctx, executable, args, environment)
}

func TestIntegrationStatusHumanJSONAndAgentOutput(t *testing.T) {
	for _, mode := range []string{"human", "json", "agent"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			configPath := filepath.Join(t.TempDir(), "config.json")
			store := config.NewStore(configPath)
			assets := fstest.MapFS{"skills/talento/SKILL.md": &fstest.MapFile{Data: []byte("skill\n")}}
			manager := managed.NewManager(assets, store, home)
			if _, err := manager.Install(managed.InstallOptions{Agents: []string{"codex"}, Scope: "user"}); err != nil {
				t.Fatal(err)
			}
			manager.Runtime = managed.AdapterEnvironment{
				Home: home,
				LookPath: func(name string) (string, error) {
					if name == "codex" {
						return "/safe/codex", nil
					}
					return "", exec.ErrNotFound
				},
				Runner: statusRunner(func(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
					if len(args) != 1 || args[0] != "--version" {
						t.Fatalf("unexpected probe = %v", args)
					}
					return []byte("codex 1.0.0\n"), nil
				}),
			}
			var stdout, stderr bytes.Buffer
			global := &app.GlobalOptions{}
			if mode == "json" {
				global.JSON = true
			} else if mode == "agent" {
				global.Agent = true
			}
			talento := &app.App{
				Paths: config.Paths{HomeDir: home, ConfigFile: configPath}, Config: store,
				Global: global, Stdout: &stdout, Stderr: &stderr,
			}
			command := newSkillStatusCommand(talento, manager)
			command.SetArgs([]string{"--integration", "codex"})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
			if mode == "human" {
				for _, want := range []string{"Talento agent integrations: HEALTHY", "[HEALTHY] Codex (codex)", "managed-file", "installed", "expected"} {
					if !strings.Contains(stdout.String(), want) {
						t.Fatalf("human output %q lacks %q", stdout.String(), want)
					}
				}
				return
			}
			var payload map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("invalid %s output %q: %v", mode, stdout.String(), err)
			}
			data := payload
			if mode == "json" {
				var ok bool
				data, ok = payload["data"].(map[string]any)
				if !ok || payload["ok"] != true {
					t.Fatalf("JSON envelope = %#v", payload)
				}
			}
			if data["status"] != "healthy" {
				t.Fatalf("%s status data = %#v", mode, data)
			}
			integrations, ok := data["integrations"].([]any)
			if !ok || len(integrations) != 1 {
				t.Fatalf("%s integrations = %#v", mode, data["integrations"])
			}
		})
	}
}

func TestIntegrationStatusHumanTextContainsTerminalRecords(t *testing.T) {
	view := integrationStatusView{Status: "attention", Integrations: []managed.Diagnosis{
		{Agent: managed.Agent{ID: "codex", Name: "Codex\nforged"}, Status: "stale", Method: managed.MethodManagedFile, Installed: true, InstalledVersion: "1.0.0", ExpectedVersion: "2.0.0", Capabilities: managed.Capabilities{Scopes: []string{"user", "project"}}, RepairCommands: []string{"talento skill update --agent codex --scope user"}},
		{Agent: managed.Agent{ID: "cursor", Name: "Cursor"}, Status: "modified", Method: managed.MethodManagedFile, Installed: true, ExpectedVersion: "2.0.0", Capabilities: managed.Capabilities{Scopes: []string{"user"}}},
		{Agent: managed.Agent{ID: "gemini", Name: "Gemini CLI"}, Status: "missing", Method: managed.MethodManagedFile, Installed: true, ExpectedVersion: "2.0.0", Capabilities: managed.Capabilities{Scopes: []string{"user"}}},
	}}
	text := view.HumanText()
	for _, want := range []string{"[STALE] Codex forged", "[MODIFIED] Cursor", "[MISSING] Gemini CLI"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text %q lacks %q", text, want)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("status text contains escape: %q", text)
	}
}
