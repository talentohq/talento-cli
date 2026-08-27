package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiscoverProjectProfileUsesNearestAncestorAndExactBytes(t *testing.T) {
	root := t.TempDir()
	writeProjectConfig(t, root, `{"profile":"outer"}\n`)
	nestedProject := filepath.Join(root, "work", "nested")
	writeProjectConfig(t, nestedProject, "{\n  \"profile\": \"inner\"\n}\n")
	deep := filepath.Join(nestedProject, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	project, err := DiscoverProjectProfile(deep)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(nestedProject)
	if err != nil {
		t.Fatal(err)
	}
	if project.ProjectDir != canonical || project.Profile != "inner" {
		t.Fatalf("project = %#v", project)
	}
	if len(project.Digest) != 64 {
		t.Fatalf("digest = %q", project.Digest)
	}

	writeProjectConfig(t, nestedProject, `{"profile":"inner"}`)
	changed, err := DiscoverProjectProfile(deep)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest == project.Digest {
		t.Fatal("exact-byte edit did not change digest")
	}
}

func TestDiscoverProjectProfileCanonicalizesDirectoryAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks normally requires elevated Windows privileges")
	}
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	writeProjectConfig(t, projectDir, `{"profile":"acme"}`)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(projectDir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	fromReal, err := DiscoverProjectProfile(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	fromAlias, err := DiscoverProjectProfile(alias)
	if err != nil {
		t.Fatal(err)
	}
	if fromAlias.ConfigPath != fromReal.ConfigPath || fromAlias.ProjectDir != fromReal.ProjectDir {
		t.Fatalf("alias=%#v real=%#v", fromAlias, fromReal)
	}
}

func TestDiscoverProjectProfileRejectsSymlinkedManagedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks normally requires elevated Windows privileges")
	}
	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		writeProjectConfig(t, target, `{"profile":"acme"}`)
		project := filepath.Join(root, "project")
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(target, ".talento"), filepath.Join(project, ".talento")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := DiscoverProjectProfile(project); err == nil || !strings.Contains(err.Error(), "symlinked project config directory") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("file", func(t *testing.T) {
		root := t.TempDir()
		marker := filepath.Join(root, ".talento")
		if err := os.MkdirAll(marker, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "actual.json")
		if err := os.WriteFile(target, []byte(`{"profile":"acme"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(marker, "config.json")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := DiscoverProjectProfile(root); err == nil || !strings.Contains(err.Error(), "symlinked project config file") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestProjectConfigSchemaIsStrictAndBounded(t *testing.T) {
	tests := []struct {
		name, content, want string
	}{
		{name: "non object", content: `[]`, want: "top-level value must be an object"},
		{name: "missing", content: `{}`, want: "profile is required"},
		{name: "empty", content: `{"profile":""}`, want: "invalid profile name"},
		{name: "wrong type", content: `{"profile":12}`, want: "profile must be a string"},
		{name: "duplicate", content: `{"profile":"a","profile":"b"}`, want: "duplicate field"},
		{name: "trailing", content: `{"profile":"a"} {}`, want: "unexpected trailing JSON value"},
		{name: "endpoint", content: `{"profile":"a","endpoint":"https://evil.example"}`, want: "unknown field \"endpoint\""},
		{name: "token", content: `{"profile":"a","token":"secret"}`, want: "unknown field \"token\""},
		{name: "environment", content: `{"profile":"a","environment":{"TOKEN":"secret"}}`, want: "unknown field \"environment\""},
		{name: "command", content: `{"profile":"a","command":"run-me"}`, want: "unknown field \"command\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectConfig(t, root, test.content)
			_, err := DiscoverProjectProfile(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	root := t.TempDir()
	writeProjectConfig(t, root, `{"profile":"a","padding":"`+strings.Repeat("x", int(ProjectConfigMaxBytes))+`"}`)
	if _, err := DiscoverProjectProfile(root); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestStableProjectReadRejectsSameInodeConcurrentWrite(t *testing.T) {
	root := t.TempDir()
	writeProjectConfig(t, root, `{"profile":"alpha"}`)
	path := filepath.Join(root, ".talento", "config.json")
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readStableProjectFileWithHook(path, before, func() error {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if _, err := file.WriteAt([]byte(`{"profile":"bravo"}`), 0); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		// Force a portable, deterministic metadata transition even on filesystems
		// whose natural modification-time resolution is coarse.
		changed := before.ModTime().Add(2 * time.Hour)
		return os.Chtimes(path, changed, changed)
	})
	if err == nil || !strings.Contains(err.Error(), "changed while reading") {
		t.Fatalf("error = %v", err)
	}
	after, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() {
		t.Fatalf("regression did not exercise same-inode, same-size mutation")
	}
}

func TestProjectTrustRequiresExactPathDigestAndProfile(t *testing.T) {
	root := t.TempDir()
	writeProjectConfig(t, root, `{"profile":"acme"}`)
	project, err := DiscoverProjectProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	if err := store.TrustProject(project); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := ProjectTrustStatus(project, cfg.ProjectTrust); got != ProjectTrusted {
		t.Fatalf("status = %s", got)
	}

	writeProjectConfig(t, root, "{\n\"profile\":\"acme\"\n}")
	edited, err := DiscoverProjectProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProjectTrustStatus(edited, cfg.ProjectTrust); got != ProjectStale {
		t.Fatalf("edited status = %s", got)
	}

	removed, err := store.UntrustProject(project.ConfigPath)
	if err != nil || !removed {
		t.Fatalf("removed=%t err=%v", removed, err)
	}
	removed, err = store.UntrustProject(project.ConfigPath)
	if err != nil || removed {
		t.Fatalf("idempotent removal=%t err=%v", removed, err)
	}
}

func TestProjectTrustCannotCreateAnUnknownGlobalProfile(t *testing.T) {
	root := t.TempDir()
	writeProjectConfig(t, root, `{"profile":"missing"}`)
	project, err := DiscoverProjectProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err := store.TrustProject(project); err == nil || !strings.Contains(err.Error(), `profile "missing"`) {
		t.Fatalf("error = %v", err)
	}
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 0 || len(cfg.ProjectTrust) != 0 {
		t.Fatalf("trust created data: %#v", cfg)
	}
}

func TestProjectTrustUpdatesAreAtomicAndConcurrent(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	projects := make([]ProjectProfile, 8)
	for index := range projects {
		name := fmt.Sprintf("company-%d", index)
		if _, err := store.CreateProfile(name); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(t.TempDir(), name)
		writeProjectConfig(t, root, fmt.Sprintf(`{"profile":%q}`, name))
		project, err := DiscoverProjectProfile(root)
		if err != nil {
			t.Fatal(err)
		}
		projects[index] = project
	}

	var wait sync.WaitGroup
	errors := make(chan error, len(projects))
	for _, project := range projects {
		project := project
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- store.TrustProject(project)
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProjectTrust) != len(projects) {
		t.Fatalf("trust records = %d, want %d", len(cfg.ProjectTrust), len(projects))
	}
}

func TestDiscoverProjectProfileNotFound(t *testing.T) {
	_, err := DiscoverProjectProfile(t.TempDir())
	if !errors.Is(err, ErrProjectConfigNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func writeProjectConfig(t *testing.T, projectDir, content string) {
	t.Helper()
	directory := filepath.Join(projectDir, ".talento")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
