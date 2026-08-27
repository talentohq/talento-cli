package managed

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/talentohq/talento-cli/internal/buildinfo"
	"github.com/talentohq/talento-cli/internal/config"
)

func TestManagedDiagnosisHealthyStaleModifiedAndMissing(t *testing.T) {
	home := t.TempDir()
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	manager := NewManager(testAssets(), store, home)
	manager.Runtime = availableRuntime(home, "codex")
	previousVersion := buildinfo.Version
	buildinfo.Version = "1.2.3"
	t.Cleanup(func() { buildinfo.Version = previousVersion })

	if _, err := manager.Install(InstallOptions{Agents: []string{"codex"}, Scope: "user"}); err != nil {
		t.Fatal(err)
	}
	diagnosis := diagnosisFor(t, manager, "codex")
	if !diagnosis.Installed || diagnosis.Status != "healthy" || diagnosis.Method != MethodManagedFile || diagnosis.InstalledVersion != "1.2.3" {
		t.Fatalf("healthy diagnosis = %#v", diagnosis)
	}
	manifestPath := filepath.Join(home, ".codex", "skills", "talento", ".talento-integration.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil || manifest["version"] != "1.2.3" || manifest["method"] != MethodManagedFile {
		t.Fatalf("manifest = %#v, err = %v", manifest, err)
	}
	manager.Runtime = missingRuntime(home)
	diagnosis = diagnosisFor(t, manager, "codex")
	if diagnosis.Status != "runtime-missing" || !strings.Contains(diagnosis.RepairCommands[0], "install or start Codex") {
		t.Fatalf("runtime-missing diagnosis = %#v", diagnosis)
	}
	manager.Runtime = availableRuntime(home, "codex")

	buildinfo.Version = "1.2.4"
	diagnosis = diagnosisFor(t, manager, "codex")
	if diagnosis.Status != "stale" || diagnosis.ExpectedVersion != "1.2.4" || !strings.Contains(diagnosis.RepairCommands[0], "skill update") {
		t.Fatalf("stale diagnosis = %#v", diagnosis)
	}

	skillPath := filepath.Join(home, ".codex", "skills", "talento", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diagnosis = diagnosisFor(t, manager, "codex")
	if diagnosis.Status != "modified" || !strings.Contains(diagnosis.RepairCommands[0], "--force") {
		t.Fatalf("modified diagnosis = %#v", diagnosis)
	}
	if err := os.Remove(skillPath); err != nil {
		t.Fatal(err)
	}
	diagnosis = diagnosisFor(t, manager, "codex")
	if diagnosis.Status != "missing" {
		t.Fatalf("missing diagnosis = %#v", diagnosis)
	}
}

func TestManagedDiagnosisLoadsLegacyRecordsWithoutNewAdapterFields(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "skills", "talento", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("legacy managed skill\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err := store.Update(func(file *config.File) error {
		file.ManagedFiles[path] = config.ManagedFile{
			Path: path, Digest: digest(data), Kind: "agent-skill", Scope: "user",
			Version: buildinfo.Version, UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(testAssets(), store, home)
	manager.Runtime = availableRuntime(home, "codex")
	diagnosis := diagnosisFor(t, manager, "codex")
	if !diagnosis.Installed || diagnosis.Status != "healthy" || diagnosis.Method != MethodManagedFile || len(diagnosis.Files) != 1 {
		t.Fatalf("legacy diagnosis = %#v", diagnosis)
	}
}

func TestDiagnoseReportsExecutableVersionProblemsWithoutLaunchingSessions(t *testing.T) {
	home := t.TempDir()
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	manager := NewManager(testAssets(), store, home)
	if _, err := manager.Install(InstallOptions{Agents: []string{"codex"}, Scope: "user"}); err != nil {
		t.Fatal(err)
	}
	manager.Runtime = AdapterEnvironment{
		Home: home, LookPath: func(name string) (string, error) { return "/safe/" + name, nil },
		Runner: runnerFunc(func(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
			if len(args) != 1 || args[0] != "--version" {
				t.Fatalf("unsafe probe args = %v", args)
			}
			return []byte("\x1b[2J"), nil
		}),
		Timeout: time.Second,
	}
	diagnosis := diagnosisFor(t, manager, "codex")
	if diagnosis.Status != "version-unavailable" || diagnosis.ExecutableVersion.Status != "malformed" {
		t.Fatalf("diagnosis = %#v", diagnosis)
	}
}

func diagnosisFor(t *testing.T, manager *Manager, id string) Diagnosis {
	t.Helper()
	diagnoses, err := manager.Diagnose(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnosis := range diagnoses {
		if diagnosis.Agent.ID == id {
			return diagnosis
		}
	}
	t.Fatalf("diagnosis %q missing", id)
	return Diagnosis{}
}

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"skills/talento/SKILL.md":                        &fstest.MapFile{Data: []byte("skill\n"), Mode: 0o644},
		"skills/talento/references/employee.md":          &fstest.MapFile{Data: []byte("employee\n"), Mode: 0o644},
		"plugins/talento/.codex-plugin/plugin.json":      &fstest.MapFile{Data: []byte(`{"name":"talento","version":"0.1.0"}`), Mode: 0o644},
		"plugins/claude-code/.claude-plugin/plugin.json": &fstest.MapFile{Data: []byte(`{"name":"talento","version":"0.1.0"}`), Mode: 0o644},
	}
}

func missingRuntime(home string) AdapterEnvironment {
	return AdapterEnvironment{
		Home: home,
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		Runner: runnerFunc(func(context.Context, string, []string, []string) ([]byte, error) {
			return nil, exec.ErrNotFound
		}),
		Timeout: time.Second,
	}
}

func availableRuntime(home, executable string) AdapterEnvironment {
	return AdapterEnvironment{
		Home: home,
		LookPath: func(name string) (string, error) {
			if name == executable {
				return "/safe/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		Runner: runnerFunc(func(_ context.Context, path string, args []string, _ []string) ([]byte, error) {
			return []byte(filepath.Base(path) + " 1.0.0\n"), nil
		}),
		Timeout: time.Second,
	}
}
