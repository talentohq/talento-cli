package commands_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	talentocli "github.com/talentohq/talento-cli"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/commands"
	clioutput "github.com/talentohq/talento-cli/internal/output"
)

func newTestRoot(t *testing.T) (*cobra.Command, *app.App) {
	t.Helper()
	t.Setenv("TALENTO_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("TALENTO_HOME", filepath.Join(t.TempDir(), "home"))
	snapshot, err := fs.ReadFile(talentocli.Content, "schemas/gateway.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := fs.ReadFile(talentocli.Content, "coverage/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	root, talento, err := commands.NewRoot(snapshot, manifest, talentocli.Content)
	if err != nil {
		t.Fatal(err)
	}
	return root, talento
}

func TestEveryManifestToolHasAStableCommand(t *testing.T) {
	root, talento := newTestRoot(t)
	for _, mapping := range talento.Manifest.Tools {
		command, remaining, err := root.Find([]string{mapping.Domain, mapping.Command})
		if err != nil || len(remaining) != 0 || command.Name() != mapping.Command {
			t.Fatalf("missing command for %s at %s %s: command=%v remaining=%v err=%v", mapping.Tool, mapping.Domain, mapping.Command, command, remaining, err)
		}
	}
}

func TestAgentHelpAndOfflineCatalog(t *testing.T) {
	root, talento := newTestRoot(t)
	var stdout, stderr bytes.Buffer
	talento.Stdout, talento.Stderr = &stdout, &stderr
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--agent", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"commands"`) || !strings.Contains(stdout.String(), `"profile"`) {
		t.Fatalf("agent help = %s", stdout.String())
	}

	root, talento = newTestRoot(t)
	stdout.Reset()
	talento.Stdout = &stdout
	root.SetOut(&stdout)
	root.SetArgs([]string{"commands", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"ok": true`) || !strings.Contains(stdout.String(), `"commands"`) {
		t.Fatalf("catalog = %s", stdout.String())
	}
}

func TestHelpGroupsTopicsAndExamples(t *testing.T) {
	root, talento := newTestRoot(t)
	var stdout bytes.Buffer
	talento.Stdout = &stdout
	root.SetOut(&stdout)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Get started:", "Talento work:", "Discovery and raw MCP:",
		"Coding-agent integration:", "Maintenance and shell support:",
		"Additional help topics:", "talento output", "talento exit-codes",
		"talento people list --name Ana", "projects      Discover project capabilities",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("root help lacks %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "talento projects    Discover") {
		t.Fatalf("projects domain was presented as a help topic:\n%s", stdout.String())
	}

	root, talento = newTestRoot(t)
	stdout.Reset()
	talento.Stdout = &stdout
	root.SetOut(&stdout)
	root.SetArgs([]string{"help", "writes"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"preview", "submitted_for_approval", "committed", "Non-interactive modes never confirm"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("writes topic lacks %q:\n%s", want, stdout.String())
		}
	}

	root, talento = newTestRoot(t)
	stdout.Reset()
	talento.Stdout = &stdout
	root.SetOut(&stdout)
	root.SetArgs([]string{"people", "list", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "talento people list --team-id 12 --json") {
		t.Fatalf("tool help lacks example:\n%s", stdout.String())
	}
}

func TestHelpSanitizesTerminalControlsAndAgentHelpRemainsJSON(t *testing.T) {
	root, talento := newTestRoot(t)
	people, _, err := root.Find([]string{"people"})
	if err != nil {
		t.Fatal(err)
	}
	people.Short = "safe\x1b]8;;https://evil.example\x1b\\label\x1b]8;;\x1b\\"
	var stdout bytes.Buffer
	talento.Stdout = &stdout
	root.SetOut(&stdout)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "\x1b") || !strings.Contains(stdout.String(), "safelabel") {
		t.Fatalf("unsafe help output = %q", stdout.String())
	}

	root, talento = newTestRoot(t)
	stdout.Reset()
	talento.Stdout = &stdout
	root.SetOut(&stdout)
	root.SetArgs([]string{"--agent", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Name     string `json:"name"`
		Commands []any  `json:"commands"`
		Flags    []any  `json:"flags"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("agent help is not JSON: %v\n%s", err, stdout.String())
	}
	if payload.Name != "talento" || len(payload.Commands) == 0 || len(payload.Flags) == 0 {
		t.Fatalf("agent help contract = %#v", payload)
	}
}

func TestCobraParseLookupAndArgumentFailuresAreStructuredUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown command", args: []string{"--agent", "does-not-exist"}},
		{name: "unknown flag in JSON mode", args: []string{"--json", "version", "--does-not-exist"}},
		{name: "invalid boolean flag", args: []string{"--agent", "version", "--yes=not-a-boolean"}},
		{name: "invalid integer flag", args: []string{"--agent", "action", "confirm", "preview-id", "--choice", "not-an-integer"}},
		{name: "missing positional argument", args: []string{"--agent", "handoff"}},
		{name: "too many positional arguments", args: []string{"--agent", "handoff", "generic", "extra"}},
		{name: "JSON and Markdown conflict", args: []string{"--json", "--md", "version"}},
		{name: "agent and Markdown conflict", args: []string{"--agent", "--md", "version"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, talento := newTestRoot(t)
			var stdout, stderr bytes.Buffer
			talento.Stdout, talento.Stderr = &stdout, &stderr
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs(test.args)
			err := root.Execute()
			if err == nil {
				t.Fatal("expected usage error")
			}
			if exit := clioutput.ExitCode(err); exit != 1 {
				t.Fatalf("exit = %d, want 1: %v", exit, err)
			}
			if err := talento.Output().Error(err); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				OK    bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
					Hint string `json:"hint"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &payload); err != nil {
				t.Fatalf("structured error is not JSON: %v\n%s", err, stderr.String())
			}
			if payload.OK || payload.Error.Code != "usage" || !strings.Contains(payload.Error.Hint, "--help") {
				t.Fatalf("structured error = %#v", payload)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestRunEErrorsRetainTheirStructuredClasses(t *testing.T) {
	tests := []struct {
		name     string
		error    error
		wantCode string
		wantExit int
	}{
		{name: "authentication", error: clioutput.Auth("authentication fixture"), wantCode: "auth_required", wantExit: 3},
		{name: "API", error: clioutput.API("API fixture", nil), wantCode: "api_error", wantExit: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, talento := newTestRoot(t)
			command := &cobra.Command{Use: "fixture", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return test.error }}
			root.AddCommand(command)
			var stdout, stderr bytes.Buffer
			talento.Stdout, talento.Stderr = &stdout, &stderr
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs([]string{"--agent", "fixture"})
			err := root.Execute()
			if err == nil {
				t.Fatal("expected fixture error")
			}
			if exit := clioutput.ExitCode(err); exit != test.wantExit {
				t.Fatalf("exit = %d, want %d: %v", exit, test.wantExit, err)
			}
			if err := talento.Output().Error(err); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &payload); err != nil {
				t.Fatalf("structured error is not JSON: %v\n%s", err, stderr.String())
			}
			if payload.Error.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", payload.Error.Code, test.wantCode)
			}
		})
	}
}

func TestProjectTrustCommandsHandleNestedPathsStalenessAndMalformedUntrust(t *testing.T) {
	root, talento := newTestRoot(t)
	if _, err := talento.Config.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(t.TempDir(), "project")
	marker := filepath.Join(projectDir, ".talento")
	deep := filepath.Join(projectDir, "src", "nested")
	if err := os.MkdirAll(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(marker, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"profile":"acme"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	talento.Stdout, talento.Stderr = &stdout, &stderr
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"profile", "trust-project", deep})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	cfg, err := talento.Config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProjectTrust) != 1 || !strings.Contains(stdout.String(), "TRUSTED") {
		t.Fatalf("trust=%#v output=%s", cfg.ProjectTrust, stdout.String())
	}

	if err := os.WriteFile(configPath, []byte("{\n  \"profile\": \"acme\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	root.SetArgs([]string{"profile", "project-status", deep})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "STALE") {
		t.Fatalf("status output = %s", stdout.String())
	}

	// Untrust locates but does not parse the file, so a malicious or broken
	// edit cannot pin an obsolete global authorization record in place.
	if err := os.WriteFile(configPath, []byte(`{"profile":"acme","token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	root.SetArgs([]string{"profile", "untrust-project", deep})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	cfg, err = talento.Config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProjectTrust) != 0 || strings.Contains(stdout.String(), "secret") {
		t.Fatalf("untrust=%#v output=%s", cfg.ProjectTrust, stdout.String())
	}

	stdout.Reset()
	root.SetArgs([]string{"profile", "untrust-project", deep})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "already untrusted") {
		t.Fatalf("idempotent output = %s", stdout.String())
	}
}
