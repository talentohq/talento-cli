package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	baseprofile "github.com/basecamp/cli/profile"
)

const ProjectConfigMaxBytes int64 = 64 << 10

var ErrProjectConfigNotFound = errors.New("no .talento/config.json found")

type ProjectProfile struct {
	ProjectDir string `json:"project_dir"`
	ConfigPath string `json:"config_path"`
	Profile    string `json:"profile"`
	Digest     string `json:"sha256"`
}

type ProjectLocation struct {
	ProjectDir string `json:"project_dir"`
	ConfigPath string `json:"config_path"`
}

// ProjectTrust is non-secret metadata in the owner-only global config. A
// record authorizes exactly one canonical path and one exact project file.
type ProjectTrust struct {
	ProjectDir string    `json:"project_dir"`
	ConfigPath string    `json:"config_path"`
	Profile    string    `json:"profile"`
	Digest     string    `json:"sha256"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (record *ProjectTrust) UnmarshalJSON(data []byte) error {
	type plain ProjectTrust
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	*record = ProjectTrust(decoded)
	return nil
}

type ProjectTrustState string

const (
	ProjectTrusted   ProjectTrustState = "trusted"
	ProjectUntrusted ProjectTrustState = "untrusted"
	ProjectStale     ProjectTrustState = "stale"
)

// DiscoverProjectProfile resolves start to a canonical directory, walks toward
// the filesystem root, and parses the nearest project selector. The .talento
// directory and config file must both be real filesystem entries, not symlinks.
func DiscoverProjectProfile(start string) (ProjectProfile, error) {
	location, err := LocateProjectConfig(start)
	if err != nil {
		return ProjectProfile{}, err
	}
	markerPath := filepath.Dir(location.ConfigPath)
	markerBefore, err := os.Lstat(markerPath)
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("reinspect project config directory %s: %w", markerPath, err)
	}
	if markerBefore.Mode()&os.ModeSymlink != 0 || !markerBefore.IsDir() {
		return ProjectProfile{}, fmt.Errorf("project config directory %s changed while reading", markerPath)
	}
	configInfo, err := os.Lstat(location.ConfigPath)
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("inspect project config %s: %w", location.ConfigPath, err)
	}
	data, err := readStableProjectFile(location.ConfigPath, configInfo)
	if err != nil {
		return ProjectProfile{}, err
	}
	markerAfter, err := os.Lstat(markerPath)
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("reinspect project config directory %s: %w", markerPath, err)
	}
	if markerAfter.Mode()&os.ModeSymlink != 0 || !markerAfter.IsDir() || !os.SameFile(markerBefore, markerAfter) {
		return ProjectProfile{}, fmt.Errorf("project config directory %s changed while reading", markerPath)
	}
	profile, err := parseProjectProfile(data)
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("parse project config %s: %w", location.ConfigPath, err)
	}
	digest := sha256.Sum256(data)
	return ProjectProfile{
		ProjectDir: location.ProjectDir, ConfigPath: location.ConfigPath,
		Profile: profile, Digest: hex.EncodeToString(digest[:]),
	}, nil
}

// LocateProjectConfig performs the same canonical, nearest-ancestor and
// no-symlink checks as discovery without parsing the file. This lets a user
// remove a stale trust record even after the project file becomes malformed.
func LocateProjectConfig(start string) (ProjectLocation, error) {
	directory, err := CanonicalDirectory(start)
	if err != nil {
		return ProjectLocation{}, err
	}
	for {
		marker := filepath.Join(directory, ".talento")
		markerInfo, markerErr := os.Lstat(marker)
		switch {
		case markerErr == nil:
			if markerInfo.Mode()&os.ModeSymlink != 0 {
				return ProjectLocation{}, fmt.Errorf("refusing symlinked project config directory %s", marker)
			}
			if !markerInfo.IsDir() {
				return ProjectLocation{}, fmt.Errorf("project config directory %s is not a directory", marker)
			}
			configPath := filepath.Join(marker, "config.json")
			configInfo, configErr := os.Lstat(configPath)
			switch {
			case configErr == nil:
				if configInfo.Mode()&os.ModeSymlink != 0 {
					return ProjectLocation{}, fmt.Errorf("refusing symlinked project config file %s", configPath)
				}
				if !configInfo.Mode().IsRegular() {
					return ProjectLocation{}, fmt.Errorf("project config %s is not a regular file", configPath)
				}
				return ProjectLocation{ProjectDir: directory, ConfigPath: filepath.Clean(configPath)}, nil
			case errors.Is(configErr, os.ErrNotExist):
				// A marker directory without a config does not shadow an ancestor.
			default:
				return ProjectLocation{}, fmt.Errorf("inspect project config %s: %w", configPath, configErr)
			}
		case errors.Is(markerErr, os.ErrNotExist):
		default:
			return ProjectLocation{}, fmt.Errorf("inspect project config directory %s: %w", marker, markerErr)
		}

		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return ProjectLocation{}, ErrProjectConfigNotFound
}

func CanonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("project path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve project path %s: %w", filepath.Clean(absolute), err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect project path %s: %w", filepath.Clean(resolved), err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path %s is not a directory", filepath.Clean(resolved))
	}
	return filepath.Clean(resolved), nil
}

func ProjectTrustStatus(project ProjectProfile, records map[string]ProjectTrust) ProjectTrustState {
	record, ok := records[project.ConfigPath]
	if !ok {
		return ProjectUntrusted
	}
	if record.ConfigPath == project.ConfigPath && record.ProjectDir == project.ProjectDir &&
		record.Profile == project.Profile && record.Digest == project.Digest {
		return ProjectTrusted
	}
	return ProjectStale
}

func parseProjectProfile(data []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return "", fmt.Errorf("top-level value must be an object")
	}
	seen := make(map[string]bool)
	profile := ""
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", err
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", fmt.Errorf("object key must be a string")
		}
		if seen[key] {
			return "", fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = true
		if key != "profile" {
			return "", fmt.Errorf("unknown field %q; only profile is allowed", key)
		}
		if err := decoder.Decode(&profile); err != nil {
			return "", fmt.Errorf("profile must be a string: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("unexpected trailing JSON value")
		}
		return "", err
	}
	if !seen["profile"] {
		return "", fmt.Errorf("profile is required")
	}
	if err := baseprofile.ValidateName(profile); err != nil {
		return "", err
	}
	return profile, nil
}

func readStableProjectFile(path string, before os.FileInfo) ([]byte, error) {
	return readStableProjectFileWithHook(path, before, nil)
}

// readStableProjectFileWithHook keeps the production read path testable at the
// exact boundary between consuming bytes and rechecking the open descriptor.
// Production callers always pass a nil hook.
func readStableProjectFileWithHook(path string, before os.FileInfo, afterRead func() error) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open project config %s: %w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect open project config %s: %w", path, err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("project config %s changed while opening", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, ProjectConfigMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read project config %s: %w", path, err)
	}
	if int64(len(data)) > ProjectConfigMaxBytes {
		return nil, fmt.Errorf("project config %s exceeds %d bytes", path, ProjectConfigMaxBytes)
	}
	if afterRead != nil {
		if err := afterRead(); err != nil {
			return nil, err
		}
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("reinspect open project config %s: %w", path, err)
	}
	if !stableFileMetadata(opened, openedAfter) {
		return nil, fmt.Errorf("project config %s changed while reading", path)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("reinspect project config %s: %w", path, err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!stableFileMetadata(openedAfter, after) {
		return nil, fmt.Errorf("project config %s changed while reading", path)
	}
	return data, nil
}

func stableFileMetadata(before, after os.FileInfo) bool {
	return before.Mode().IsRegular() && after.Mode().IsRegular() &&
		os.SameFile(before, after) && before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime())
}
