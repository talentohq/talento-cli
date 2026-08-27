package releaseartifacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Allowlist struct {
	Version  int      `json:"version"`
	Patterns []string `json:"patterns"`
	compiled []*regexp.Regexp
}

func Load(path string) (*Allowlist, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var allowlist Allowlist
	if err := json.Unmarshal(data, &allowlist); err != nil {
		return nil, err
	}
	if allowlist.Version != 1 || len(allowlist.Patterns) == 0 {
		return nil, fmt.Errorf("unsupported or empty artifact allowlist")
	}
	for _, pattern := range allowlist.Patterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile %q: %w", pattern, err)
		}
		allowlist.compiled = append(allowlist.compiled, compiled)
	}
	return &allowlist, nil
}

func (a *Allowlist) Allows(name string) bool {
	name = filepath.Base(name)
	for _, pattern := range a.compiled {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}

func (a *Allowlist) ValidateDirectory(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.Name() == "artifacts.json" || entry.Name() == "metadata.json" || entry.Name() == "config.yaml" {
			continue
		}
		if !a.Allows(entry.Name()) {
			return nil, fmt.Errorf("release artifact %q is not allowlisted", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func CopyAllowed(source, destination string, names []string) error {
	for _, name := range names {
		if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
			return fmt.Errorf("release artifact %q is not a safe base name", name)
		}
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		// #nosec G703 -- every name passed the base-name and separator checks above before any copy began.
		if err := os.WriteFile(filepath.Join(destination, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
