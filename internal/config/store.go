package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	baseprofile "github.com/basecamp/cli/profile"
)

const schemaVersion = 1

type Profile struct {
	Name              string    `json:"name"`
	Endpoint          string    `json:"endpoint"`
	ClientID          string    `json:"client_id,omitempty"`
	RedirectURI       string    `json:"redirect_uri,omitempty"`
	RegistrationScope string    `json:"registration_scope,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ProfileSnapshot struct {
	Name           string
	Profile        Profile
	Exists         bool
	DefaultProfile string
}

type ManagedFile struct {
	Path           string    `json:"path"`
	Digest         string    `json:"digest"`
	Kind           string    `json:"kind"`
	Agent          string    `json:"agent,omitempty"`
	Method         string    `json:"method,omitempty"`
	RegistrationID string    `json:"registration_id,omitempty"`
	Scope          string    `json:"scope"`
	Version        string    `json:"version"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type File struct {
	SchemaVersion  int                     `json:"schema_version"`
	DefaultProfile string                  `json:"default_profile,omitempty"`
	Profiles       map[string]Profile      `json:"profiles"`
	ManagedFiles   map[string]ManagedFile  `json:"managed_files,omitempty"`
	ProjectTrust   map[string]ProjectTrust `json:"project_trust,omitempty"`
	UpdatedAt      time.Time               `json:"updated_at"`
}

type Store struct {
	path string
	now  func() time.Time
}

func NewStore(path string) *Store {
	return &Store{path: path, now: time.Now}
}

func emptyFile() File {
	return File{
		SchemaVersion: schemaVersion,
		Profiles:      make(map[string]Profile),
		ManagedFiles:  make(map[string]ManagedFile),
		ProjectTrust:  make(map[string]ProjectTrust),
	}
}

func (s *Store) Path() string { return s.path }

func (s *Store) Load() (File, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyFile(), nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read config: %w", err)
	}

	var cfg File
	if err := json.Unmarshal(data, &cfg); err != nil {
		return File{}, fmt.Errorf("parse config %s: %w", s.path, err)
	}
	if cfg.SchemaVersion != schemaVersion {
		return File{}, fmt.Errorf("unsupported config schema %d (expected %d)", cfg.SchemaVersion, schemaVersion)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]Profile)
	}
	if cfg.ManagedFiles == nil {
		cfg.ManagedFiles = make(map[string]ManagedFile)
	}
	if cfg.ProjectTrust == nil {
		cfg.ProjectTrust = make(map[string]ProjectTrust)
	}
	if err := validateFile(cfg); err != nil {
		return File{}, fmt.Errorf("validate config %s: %w", s.path, err)
	}
	return cfg, nil
}

func (s *Store) Update(fn func(*File) error) error {
	release, err := s.acquireLock(5 * time.Second)
	if err != nil {
		return err
	}
	defer release()

	cfg, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	if err := validateFile(cfg); err != nil {
		return err
	}
	cfg.UpdatedAt = s.now().UTC()
	return s.saveAtomic(cfg)
}

func (s *Store) CreateProfile(name string) (Profile, error) {
	if err := baseprofile.ValidateName(name); err != nil {
		return Profile{}, err
	}
	now := s.now().UTC()
	p := Profile{
		Name:              name,
		Endpoint:          Endpoint,
		RegistrationScope: Scope,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	err := s.Update(func(cfg *File) error {
		if _, exists := cfg.Profiles[name]; exists {
			return fmt.Errorf("profile %q already exists", name)
		}
		cfg.Profiles[name] = p
		if len(cfg.Profiles) == 1 {
			cfg.DefaultProfile = name
		}
		return nil
	})
	return p, err
}

func (s *Store) UpsertProfile(p Profile) error {
	if err := baseprofile.ValidateName(p.Name); err != nil {
		return err
	}
	return s.Update(func(cfg *File) error {
		existing, ok := cfg.Profiles[p.Name]
		if ok && p.CreatedAt.IsZero() {
			p.CreatedAt = existing.CreatedAt
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = s.now().UTC()
		}
		p.Endpoint = Endpoint
		p.RegistrationScope = Scope
		p.UpdatedAt = s.now().UTC()
		cfg.Profiles[p.Name] = p
		if cfg.DefaultProfile == "" {
			cfg.DefaultProfile = p.Name
		}
		return nil
	})
}

func (s *Store) DeleteProfile(name string) error {
	return s.Update(func(cfg *File) error {
		if _, ok := cfg.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		delete(cfg.Profiles, name)
		if cfg.DefaultProfile == name {
			cfg.DefaultProfile = ""
		}
		return nil
	})
}

func (s *Store) SetDefault(name string) error {
	return s.Update(func(cfg *File) error {
		if _, ok := cfg.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		cfg.DefaultProfile = name
		return nil
	})
}

func (s *Store) Profile(name string) (Profile, error) {
	cfg, err := s.Load()
	if err != nil {
		return Profile{}, err
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("profile %q not found", name)
	}
	return p, nil
}

func (s *Store) SnapshotProfile(name string) (ProfileSnapshot, error) {
	cfg, err := s.Load()
	if err != nil {
		return ProfileSnapshot{}, err
	}
	profile, exists := cfg.Profiles[name]
	return ProfileSnapshot{
		Name: name, Profile: profile, Exists: exists, DefaultProfile: cfg.DefaultProfile,
	}, nil
}

func (s *Store) RestoreProfile(snapshot ProfileSnapshot) error {
	return s.Update(func(cfg *File) error {
		if snapshot.Exists {
			cfg.Profiles[snapshot.Name] = snapshot.Profile
		} else {
			delete(cfg.Profiles, snapshot.Name)
		}
		if snapshot.DefaultProfile == "" && cfg.DefaultProfile == snapshot.Name {
			cfg.DefaultProfile = ""
		}
		return nil
	})
}

func (s *Store) ProfileNames() ([]string, string, error) {
	cfg, err := s.Load()
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, cfg.DefaultProfile, nil
}

func (s *Store) TrustProject(project ProjectProfile) error {
	if err := validateProjectProfile(project); err != nil {
		return err
	}
	return s.Update(func(cfg *File) error {
		if _, ok := cfg.Profiles[project.Profile]; !ok {
			return fmt.Errorf("project config selects profile %q, which is not configured globally", project.Profile)
		}
		cfg.ProjectTrust[project.ConfigPath] = ProjectTrust{
			ProjectDir: project.ProjectDir, ConfigPath: project.ConfigPath,
			Profile: project.Profile, Digest: project.Digest, UpdatedAt: s.now().UTC(),
		}
		return nil
	})
}

func (s *Store) UntrustProject(configPath string) (bool, error) {
	removed := false
	err := s.Update(func(cfg *File) error {
		if _, ok := cfg.ProjectTrust[configPath]; ok {
			delete(cfg.ProjectTrust, configPath)
			removed = true
		}
		return nil
	})
	return removed, err
}

func validateFile(cfg File) error {
	if cfg.SchemaVersion != schemaVersion {
		return fmt.Errorf("config schema must be %d", schemaVersion)
	}
	if cfg.DefaultProfile != "" {
		if _, ok := cfg.Profiles[cfg.DefaultProfile]; !ok {
			return fmt.Errorf("default profile %q does not exist", cfg.DefaultProfile)
		}
	}
	for name, p := range cfg.Profiles {
		if err := baseprofile.ValidateName(name); err != nil {
			return err
		}
		if p.Name != name {
			return fmt.Errorf("profile key %q does not match profile name %q", name, p.Name)
		}
		if p.Endpoint != Endpoint {
			return fmt.Errorf("profile %q has unsupported endpoint %q", name, p.Endpoint)
		}
	}
	for path, record := range cfg.ProjectTrust {
		if path != record.ConfigPath {
			return fmt.Errorf("project trust key %q does not match config path %q", path, record.ConfigPath)
		}
		if err := validateProjectTrust(record); err != nil {
			return err
		}
	}
	return nil
}

func validateProjectProfile(project ProjectProfile) error {
	return validateProjectTrust(ProjectTrust{
		ProjectDir: project.ProjectDir, ConfigPath: project.ConfigPath,
		Profile: project.Profile, Digest: project.Digest,
	})
}

func validateProjectTrust(record ProjectTrust) error {
	if record.ProjectDir == "" || !filepath.IsAbs(record.ProjectDir) || filepath.Clean(record.ProjectDir) != record.ProjectDir {
		return fmt.Errorf("project trust has invalid canonical project path %q", record.ProjectDir)
	}
	if record.ConfigPath == "" || !filepath.IsAbs(record.ConfigPath) || filepath.Clean(record.ConfigPath) != record.ConfigPath {
		return fmt.Errorf("project trust has invalid canonical config path %q", record.ConfigPath)
	}
	expected := filepath.Join(record.ProjectDir, ".talento", "config.json")
	if record.ConfigPath != expected {
		return fmt.Errorf("project trust config path %q is outside project %q", record.ConfigPath, record.ProjectDir)
	}
	if err := baseprofile.ValidateName(record.Profile); err != nil {
		return fmt.Errorf("project trust profile: %w", err)
	}
	digest, err := hex.DecodeString(record.Digest)
	if err != nil || len(digest) != sha256.Size || record.Digest != strings.ToLower(record.Digest) {
		return fmt.Errorf("project trust for %s has invalid SHA-256 digest", record.ConfigPath)
	}
	return nil
}

func (s *Store) saveAtomic(cfg File) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if runtime.GOOS != "windows" {
		if err := os.Rename(tmpPath, s.path); err != nil {
			return fmt.Errorf("replace config: %w", err)
		}
		return nil
	}

	rollback := s.path + ".rollback"
	_ = os.Remove(rollback)
	if _, err := os.Stat(s.path); err == nil {
		if err := os.Rename(s.path, rollback); err != nil {
			return fmt.Errorf("prepare config rollback: %w", err)
		}
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Rename(rollback, s.path)
		return fmt.Errorf("replace config: %w", err)
	}
	_ = os.Remove(rollback)
	return nil
}

func (s *Store) acquireLock(timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, err
	}
	lockPath := s.path + ".lock"
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire config lock: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for config lock")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
