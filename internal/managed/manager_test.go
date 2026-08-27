package managed

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/talentohq/talento-cli/internal/config"
)

func TestManagedInstallUpdateBackupAndRemove(t *testing.T) {
	home := t.TempDir()
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	assets := fstest.MapFS{
		"skills/talento/SKILL.md":               &fstest.MapFile{Data: []byte("canonical-v1\n"), Mode: 0o644},
		"skills/talento/references/employee.md": &fstest.MapFile{Data: []byte("employee\n"), Mode: 0o644},
	}
	manager := NewManager(assets, store, home)
	manager.Now = func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) }
	options := InstallOptions{Agents: []string{"codex"}, Scope: "user"}
	first, err := manager.Install(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Installed) != 5 {
		t.Fatalf("installed = %#v", first.Installed)
	}
	second, err := manager.Install(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Unchanged) != 5 || len(second.Updated) != 0 {
		t.Fatalf("idempotent result = %#v", second)
	}
	managedPath := filepath.Join(home, ".codex", "skills", "talento", "SKILL.md")
	if err := os.WriteFile(managedPath, []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(options); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected modified-file refusal, got %v", err)
	}
	forced := options
	forced.Force = true
	result, err := manager.Install(forced)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Backups) != 1 {
		t.Fatalf("backups = %#v", result.Backups)
	}
	backupData, err := os.ReadFile(result.Backups[0])
	if err != nil || string(backupData) != "user edit\n" {
		t.Fatalf("backup = %q, err = %v", backupData, err)
	}
	removed, err := manager.Remove(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Removed) != 5 {
		t.Fatalf("removed = %#v", removed.Removed)
	}
	if _, err := os.Stat(managedPath); !os.IsNotExist(err) {
		t.Fatalf("managed file still exists: %v", err)
	}
	cfg, err := store.Load()
	if err != nil || len(cfg.ManagedFiles) != 0 {
		t.Fatalf("managed config = %#v, err = %v", cfg.ManagedFiles, err)
	}
}

func TestSingleFileWrappersPointAtCanonicalSkill(t *testing.T) {
	for _, id := range []string{"cursor", "windsurf"} {
		agent, _ := AgentByID(id)
		if !agent.DependsOnSharedSkill {
			t.Fatalf("%s wrapper dependency is not declared", id)
		}
		wrapper := WrapperFor(agent)
		if !strings.Contains(wrapper, ".agents/skills/talento/SKILL.md") || strings.Contains(wrapper, "all writes") {
			t.Fatalf("invalid %s wrapper: %s", id, wrapper)
		}
	}
}

func TestProjectScopeAdapterInstallAndRemove(t *testing.T) {
	manager, store, home := newLifecycleManager(t)
	project := t.TempDir()
	options := InstallOptions{Agents: []string{"claude-code"}, Scope: "project", ProjectDir: project}
	result, err := manager.Install(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) != 5 {
		t.Fatalf("installed = %#v", result.Installed)
	}
	manifestPath := filepath.Join(project, ".claude", "skills", "talento", ".talento-integration.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil || !strings.Contains(string(manifest), `"scope": "project"`) {
		t.Fatalf("manifest = %q, err = %v", manifest, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "talento", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("project install wrote into user home: %v", err)
	}
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for path, managed := range cfg.ManagedFiles {
		if !filepath.IsAbs(path) || managed.Scope != "project" || managed.Method != MethodManagedFile {
			t.Fatalf("managed record = %#v", managed)
		}
	}
	removed, err := manager.Remove(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Removed) != 5 {
		t.Fatalf("removed = %#v", removed.Removed)
	}
}

func TestRemoveKeepsSharedSkillForRemainingDependentAdapter(t *testing.T) {
	manager, store, home := newLifecycleManager(t)
	if _, err := manager.Install(InstallOptions{Agents: []string{"cursor", "codex"}, Scope: "user"}); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Remove(InstallOptions{Agents: []string{"codex"}, Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 3 {
		t.Fatalf("removed = %#v", result.Removed)
	}
	assertFileExists(t, filepath.Join(home, ".agents", "skills", "talento", "SKILL.md"))
	assertFileExists(t, filepath.Join(home, ".cursor", "rules", "talento.mdc"))

	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ManagedFiles) != 3 {
		t.Fatalf("managed files = %#v", cfg.ManagedFiles)
	}
}

func TestRemoveNeverInstalledAdapterLeavesSharedSkillAlone(t *testing.T) {
	manager, store, home := newLifecycleManager(t)
	if _, err := manager.Install(InstallOptions{Agents: []string{"cursor"}, Scope: "user"}); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Remove(InstallOptions{Agents: []string{"codex"}, Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("removed = %#v", result.Removed)
	}
	assertFileExists(t, filepath.Join(home, ".agents", "skills", "talento", "SKILL.md"))

	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ManagedFiles) != 3 {
		t.Fatalf("managed files = %#v", cfg.ManagedFiles)
	}
}

func TestRemoveDeletesSharedSkillWithLastDependentAdapter(t *testing.T) {
	manager, store, home := newLifecycleManager(t)
	if _, err := manager.Install(InstallOptions{Agents: []string{"cursor", "windsurf"}, Scope: "user"}); err != nil {
		t.Fatal(err)
	}

	first, err := manager.Remove(InstallOptions{Agents: []string{"cursor"}, Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Removed) != 1 {
		t.Fatalf("first removal = %#v", first.Removed)
	}
	assertFileExists(t, filepath.Join(home, ".agents", "skills", "talento", "SKILL.md"))

	last, err := manager.Remove(InstallOptions{Agents: []string{"windsurf"}, Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Removed) != 3 {
		t.Fatalf("last removal = %#v", last.Removed)
	}
	assertFileMissing(t, filepath.Join(home, ".agents", "skills", "talento", "SKILL.md"))

	cfg, err := store.Load()
	if err != nil || len(cfg.ManagedFiles) != 0 {
		t.Fatalf("managed files = %#v, err = %v", cfg.ManagedFiles, err)
	}
}

func TestRemoveLastDependentDoesNotRemoveStandaloneAdapter(t *testing.T) {
	manager, store, home := newLifecycleManager(t)
	if _, err := manager.Install(InstallOptions{Agents: []string{"cursor", "codex"}, Scope: "user"}); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Remove(InstallOptions{Agents: []string{"cursor"}, Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 3 {
		t.Fatalf("removed = %#v", result.Removed)
	}
	assertFileMissing(t, filepath.Join(home, ".agents", "skills", "talento", "SKILL.md"))
	assertFileExists(t, filepath.Join(home, ".codex", "skills", "talento", "SKILL.md"))

	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ManagedFiles) != 3 {
		t.Fatalf("managed files = %#v", cfg.ManagedFiles)
	}
}

func newLifecycleManager(t *testing.T) (*Manager, *config.Store, string) {
	t.Helper()
	home := t.TempDir()
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	assets := fstest.MapFS{
		"skills/talento/SKILL.md":               &fstest.MapFile{Data: []byte("canonical-v1\n"), Mode: 0o644},
		"skills/talento/references/employee.md": &fstest.MapFile{Data: []byte("employee\n"), Mode: 0o644},
	}
	return NewManager(assets, store, home), store, home
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing: %v", path, err)
	}
}

func TestAssetsImplementFS(t *testing.T) {
	var _ fs.FS = fstest.MapFS{}
}
