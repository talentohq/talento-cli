package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProfileStoreIsAtomicAndConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	store := NewStore(path)
	var wait sync.WaitGroup
	errors := make(chan error, 12)
	for index := 0; index < 12; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.CreateProfile(fmt.Sprintf("company-%02d", index))
			errors <- err
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
	if len(cfg.Profiles) != 12 || cfg.DefaultProfile == "" {
		t.Fatalf("config = %#v", cfg)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions = %o", info.Mode().Perm())
	}
}

func TestProfileNamesAreValidatedAndDefaultCannotDangle(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("bad name"); err == nil {
		t.Fatal("expected invalid profile name error")
	}
	if err := store.SetDefault("missing"); err == nil {
		t.Fatal("expected missing profile error")
	}
}

func TestProfileSnapshotRestoresExactMetadata(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	profile, err := store.CreateProfile("acme")
	if err != nil {
		t.Fatal(err)
	}
	profile.ClientID = "old-client"
	profile.RedirectURI = "http://127.0.0.1:1234/callback"
	profile.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertProfile(profile); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.SnapshotProfile("acme")
	if err != nil {
		t.Fatal(err)
	}
	storedBefore, err := store.Profile("acme")
	if err != nil {
		t.Fatal(err)
	}
	replacement := storedBefore
	replacement.ClientID = "replacement-client"
	replacement.RedirectURI = "http://127.0.0.1:5678/callback"
	if err := store.UpsertProfile(replacement); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreProfile(snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Profile("acme")
	if err != nil {
		t.Fatal(err)
	}
	if restored != storedBefore {
		t.Fatalf("restored profile = %#v, want %#v", restored, storedBefore)
	}

	missing, err := store.SnapshotProfile("new-profile")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProfile(Profile{Name: "new-profile"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreProfile(missing); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Profile("new-profile"); err == nil {
		t.Fatalf("new profile was not removed: %v", err)
	}
}

func TestLegacySchemaOneConfigLoadsWithoutProjectTrust(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := File{SchemaVersion: 1, Profiles: map[string]Profile{}, UpdatedAt: time.Now()}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectTrust == nil || len(cfg.ProjectTrust) != 0 {
		t.Fatalf("project trust = %#v", cfg.ProjectTrust)
	}
}

func TestProjectTrustRecordsRejectUnknownAndInvalidMetadataOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	project := filepath.Join(t.TempDir(), "project")
	configPath := filepath.Join(project, ".talento", "config.json")
	content := fmt.Sprintf(`{"schema_version":1,"profiles":{},"project_trust":{%q:{"project_dir":%q,"config_path":%q,"profile":"acme","sha256":%q,"updated_at":"2026-01-01T00:00:00Z","token":"secret"}}}`,
		configPath, project, configPath, strings.Repeat("0", 64))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).Load(); err == nil || !strings.Contains(err.Error(), "unknown field \"token\"") {
		t.Fatalf("error = %v", err)
	}
}

func TestProjectTrustRecordsAreValidatedBeforeUpdate(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	err := store.Update(func(cfg *File) error {
		cfg.ProjectTrust["relative"] = ProjectTrust{
			ProjectDir: "relative", ConfigPath: "relative", Profile: "acme", Digest: strings.Repeat("0", 64),
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "canonical project path") {
		t.Fatalf("error = %v", err)
	}
	cfg, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(cfg.ProjectTrust) != 0 {
		t.Fatalf("invalid trust persisted: %#v", cfg.ProjectTrust)
	}
}
