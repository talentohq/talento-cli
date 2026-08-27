package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/talentohq/talento-cli/internal/buildinfo"
	"github.com/talentohq/talento-cli/internal/config"
)

type Manager struct {
	Assets  fs.FS
	Config  *config.Store
	Home    string
	Now     func() time.Time
	Runtime AdapterEnvironment
}

type InstallOptions struct {
	Agents     []string
	Scope      string
	ProjectDir string
	Force      bool
}

type Result struct {
	Installed []string `json:"installed,omitempty"`
	Updated   []string `json:"updated,omitempty"`
	Unchanged []string `json:"unchanged,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Backups   []string `json:"backups,omitempty"`
}

func (r Result) HumanText() string {
	lines := make([]string, 0)
	for _, path := range r.Installed {
		lines = append(lines, "Installed: "+path)
	}
	for _, path := range r.Updated {
		lines = append(lines, "Updated: "+path)
	}
	for _, path := range r.Unchanged {
		lines = append(lines, "Unchanged: "+path)
	}
	for _, path := range r.Removed {
		lines = append(lines, "Removed: "+path)
	}
	for _, path := range r.Backups {
		lines = append(lines, "Backup: "+path)
	}
	if len(lines) == 0 {
		return "No managed files changed."
	}
	return strings.Join(lines, "\n")
}

type targetFile struct {
	Path  string
	Data  []byte
	Kind  string
	Agent string
	Scope string
}

type originalFile struct {
	Path   string
	Data   []byte
	Mode   fs.FileMode
	Exists bool
}

func NewManager(assets fs.FS, cfg *config.Store, home string) *Manager {
	return &Manager{Assets: assets, Config: cfg, Home: home, Now: time.Now, Runtime: DefaultAdapterEnvironment(home)}
}

func (m *Manager) Install(options InstallOptions) (Result, error) {
	targets, err := m.targets(options, false)
	if err != nil {
		return Result{}, err
	}
	cfg, err := m.Config.Load()
	if err != nil {
		return Result{}, err
	}
	result := Result{}
	originals := make([]originalFile, 0, len(targets))
	toWrite := make([]targetFile, 0, len(targets))
	for _, target := range targets {
		original, err := readOriginal(target.Path)
		if err != nil {
			return Result{}, err
		}
		targetDigest := digest(target.Data)
		if original.Exists && digest(original.Data) == targetDigest {
			result.Unchanged = append(result.Unchanged, target.Path)
			continue
		}
		managed, wasManaged := cfg.ManagedFiles[target.Path]
		if original.Exists && (!wasManaged || digest(original.Data) != managed.Digest) && !options.Force {
			return Result{}, fmt.Errorf("refusing to overwrite modified or unmanaged file %s; rerun with --force to create a backup", target.Path)
		}
		if original.Exists && options.Force {
			backup, err := backupFile(target.Path, original.Data, original.Mode, m.Now())
			if err != nil {
				return Result{}, err
			}
			result.Backups = append(result.Backups, backup)
		}
		originals = append(originals, original)
		toWrite = append(toWrite, target)
		if original.Exists {
			result.Updated = append(result.Updated, target.Path)
		} else {
			result.Installed = append(result.Installed, target.Path)
		}
	}
	for index, target := range toWrite {
		if err := atomicWrite(target.Path, target.Data, 0o644); err != nil {
			rollback(originals[:index])
			return Result{}, err
		}
	}
	if err := m.Config.Update(func(file *config.File) error {
		for _, target := range targets {
			method := MethodManagedFile
			if adapter, ok := AdapterByID(target.Agent); ok {
				method = adapter.Capabilities().InstallMethod
			}
			file.ManagedFiles[target.Path] = config.ManagedFile{
				Path: target.Path, Digest: digest(target.Data), Kind: target.Kind, Agent: target.Agent,
				Method: method, Scope: target.Scope, Version: buildinfo.Version, UpdatedAt: m.Now().UTC(),
			}
		}
		return nil
	}); err != nil {
		rollback(originals)
		return Result{}, err
	}
	sortResult(&result)
	return result, nil
}

func (m *Manager) Remove(options InstallOptions) (Result, error) {
	targets, err := m.targets(options, true)
	if err != nil {
		return Result{}, err
	}
	cfg, err := m.Config.Load()
	if err != nil {
		return Result{}, err
	}
	targets = m.removalTargets(targets, options, cfg)
	result := Result{}
	originals := make(map[string]originalFile)
	removed := make([]string, 0)
	removePaths := make([]string, 0)
	for _, target := range targets {
		managed, ok := cfg.ManagedFiles[target.Path]
		if !ok {
			continue
		}
		original, err := readOriginal(target.Path)
		if err != nil {
			return Result{}, err
		}
		if !original.Exists {
			removePaths = append(removePaths, target.Path)
			continue
		}
		if digest(original.Data) != managed.Digest && !options.Force {
			return Result{}, fmt.Errorf("refusing to remove modified managed file %s; rerun with --force to create a backup", target.Path)
		}
		if digest(original.Data) != managed.Digest && options.Force {
			backup, err := backupFile(target.Path, original.Data, original.Mode, m.Now())
			if err != nil {
				return Result{}, err
			}
			result.Backups = append(result.Backups, backup)
		}
		originals[target.Path] = original
		removePaths = append(removePaths, target.Path)
	}
	for _, path := range removePaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			rollbackPaths(removed, originals)
			return Result{}, err
		}
		if _, ok := originals[path]; ok {
			removed = append(removed, path)
		}
		result.Removed = append(result.Removed, path)
	}
	if err := m.Config.Update(func(file *config.File) error {
		for _, path := range removePaths {
			delete(file.ManagedFiles, path)
		}
		return nil
	}); err != nil {
		rollbackPaths(removed, originals)
		return Result{}, err
	}
	sortResult(&result)
	return result, nil
}

func (m *Manager) removalTargets(targets []targetFile, options InstallOptions, cfg config.File) []targetFile {
	requestedAgentInstalled := false
	for _, target := range targets {
		if target.Kind == "shared-skill" {
			continue
		}
		if _, ok := cfg.ManagedFiles[target.Path]; ok {
			requestedAgentInstalled = true
			break
		}
	}

	keepSharedSkill := !requestedAgentInstalled || m.hasRemainingSharedSkillDependent(options, cfg)
	if !keepSharedSkill {
		return targets
	}

	filtered := make([]targetFile, 0, len(targets))
	for _, target := range targets {
		if target.Kind != "shared-skill" {
			filtered = append(filtered, target)
		}
	}
	return filtered
}

func (m *Manager) hasRemainingSharedSkillDependent(options InstallOptions, cfg config.File) bool {
	selected := make(map[string]bool, len(options.Agents))
	for _, id := range options.Agents {
		selected[id] = true
	}

	base := m.Home
	if options.Scope == "project" {
		base = options.ProjectDir
	}
	if canonical, err := canonicalRoot(base); err == nil {
		base = canonical
	}
	for _, agent := range SupportedAgents {
		if !agent.DependsOnSharedSkill || selected[agent.ID] {
			continue
		}
		relative := agent.UserPath
		if options.Scope == "project" {
			relative = agent.ProjectPath
		}
		path := filepath.Join(base, filepath.FromSlash(relative))
		managed, ok := cfg.ManagedFiles[path]
		if ok && managed.Kind == "agent-wrapper" && managed.Agent == agent.ID {
			return true
		}
	}
	return false
}

type Integrity struct {
	Path             string `json:"path"`
	Status           string `json:"status"`
	Agent            string `json:"agent,omitempty"`
	Method           string `json:"method,omitempty"`
	Scope            string `json:"scope,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	Expected         string `json:"expected_digest,omitempty"`
	Actual           string `json:"actual_digest,omitempty"`
}

func (m *Manager) Integrity() ([]Integrity, error) {
	cfg, err := m.Config.Load()
	if err != nil {
		return nil, err
	}
	result := make([]Integrity, 0, len(cfg.ManagedFiles))
	for _, managed := range cfg.ManagedFiles {
		method := managed.Method
		if method == "" {
			method = MethodManagedFile
		}
		item := Integrity{Path: managed.Path, Agent: managed.Agent, Method: method, Scope: managed.Scope, InstalledVersion: managed.Version, Expected: managed.Digest, Status: "ok"}
		data, err := os.ReadFile(managed.Path)
		if os.IsNotExist(err) {
			item.Status = "missing"
		} else if err != nil {
			item.Status = "unreadable"
		} else {
			item.Actual = digest(data)
			if item.Actual != item.Expected {
				item.Status = "modified"
			}
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (m *Manager) targets(options InstallOptions, removing bool) ([]targetFile, error) {
	if len(options.Agents) == 0 {
		return nil, fmt.Errorf("at least one supported agent is required")
	}
	if options.Scope == "" {
		options.Scope = "user"
	}
	if options.Scope != "user" && options.Scope != "project" {
		return nil, fmt.Errorf("scope must be user or project")
	}
	base := m.Home
	if options.Scope == "project" {
		base = options.ProjectDir
		if base == "" {
			return nil, fmt.Errorf("project directory is required for project scope")
		}
	}
	base, err := canonicalRoot(base)
	if err != nil {
		return nil, err
	}
	if options.Scope == "project" {
		options.ProjectDir = base
	}
	canonical, err := m.canonicalFiles()
	if err != nil {
		return nil, err
	}
	targets := skillTargets(filepath.Join(base, ".agents/skills/talento"), canonical, "shared-skill", "", options.Scope)
	seenAgents := make(map[string]bool)
	operation := adapterOperation{Manager: m, Options: options}
	for _, id := range options.Agents {
		if seenAgents[id] {
			continue
		}
		seenAgents[id] = true
		adapter, ok := AdapterByID(id)
		if !ok {
			return nil, fmt.Errorf("unsupported agent %q", id)
		}
		var planned []targetFile
		if removing {
			planned, err = adapter.Remove(context.Background(), operation)
		} else {
			planned, err = adapter.Install(context.Background(), operation)
		}
		if err != nil {
			return nil, err
		}
		targets = append(targets, planned...)
	}
	unique := make(map[string]targetFile)
	for _, target := range targets {
		unique[target.Path] = target
	}
	targets = targets[:0]
	for _, target := range unique {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	return targets, nil
}

func (m *Manager) agentTargets(agent Agent, options InstallOptions) ([]targetFile, error) {
	base := m.Home
	if options.Scope == "project" {
		base = options.ProjectDir
	}
	base, err := canonicalRoot(base)
	if err != nil {
		return nil, err
	}
	canonical, err := m.canonicalFiles()
	if err != nil {
		return nil, err
	}
	relative := agent.UserPath
	if options.Scope == "project" {
		relative = agent.ProjectPath
	}
	target := filepath.Join(base, filepath.FromSlash(relative))
	if agent.SingleFile {
		return []targetFile{{Path: target, Data: []byte(WrapperFor(agent)), Kind: "agent-wrapper", Agent: agent.ID, Scope: options.Scope}}, nil
	}
	targets := skillTargets(target, canonical, "agent-skill", agent.ID, options.Scope)
	if agent.ID == "codex" || agent.ID == "claude-code" {
		manifest, err := integrationManifest(agent.ID, options.Scope)
		if err != nil {
			return nil, err
		}
		targets = append(targets, targetFile{
			Path: filepath.Join(target, ".talento-integration.json"), Data: manifest,
			Kind: "integration-manifest", Agent: agent.ID, Scope: options.Scope,
		})
	}
	return targets, nil
}

func canonicalRoot(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve integration root: %w", err)
	}
	if evaluated, err := filepath.EvalSymlinks(absolute); err == nil {
		return evaluated, nil
	}
	return filepath.Clean(absolute), nil
}

func integrationManifest(agent, scope string) ([]byte, error) {
	manifest := struct {
		SchemaVersion int    `json:"schema_version"`
		Name          string `json:"name"`
		Agent         string `json:"agent"`
		Method        string `json:"method"`
		Scope         string `json:"scope"`
		Version       string `json:"version"`
	}{
		SchemaVersion: 1, Name: "talento", Agent: agent, Method: MethodManagedFile,
		Scope: scope, Version: buildinfo.Version,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (m *Manager) canonicalFiles() (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := fs.WalkDir(m.Assets, "skills/talento", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(m.Assets, path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel("skills/talento", path)
		files[filepath.ToSlash(relative)] = data
		return nil
	})
	return files, err
}

func skillTargets(root string, files map[string][]byte, kind, agent, scope string) []targetFile {
	targets := make([]targetFile, 0, len(files))
	for relative, data := range files {
		targets = append(targets, targetFile{Path: filepath.Join(root, filepath.FromSlash(relative)), Data: data, Kind: kind, Agent: agent, Scope: scope})
	}
	return targets
}

func WrapperFor(_ Agent) string {
	return "---\ndescription: Use TalentoHQ through the local talento CLI\nalwaysApply: false\n---\n\n# TalentoHQ\n\nWhen a request involves TalentoHQ, inspect `talento --agent --help` and `talento commands --available --agent`. Use the local CLI and its selected OAuth profile; do not create a duplicate MCP grant. Follow the returned state: committed, preview, submitted_for_approval, returned, or error. Never claim a preview committed or bypass server-authoritative tenant, role, module, permission, or visibility limits. Answer in the user's language.\n\nLoad the canonical skill from `~/.agents/skills/talento/SKILL.md` for user setup or `.agents/skills/talento/SKILL.md` for project setup.\n"
}

func readOriginal(path string) (originalFile, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return originalFile{Path: path}, nil
	}
	if err != nil {
		return originalFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return originalFile{}, err
	}
	return originalFile{Path: path, Data: data, Mode: info.Mode(), Exists: true}, nil
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".talento-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Rename(tmpPath, path)
	}
	rollback := path + ".talento-rollback"
	_ = os.Remove(rollback)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, rollback); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Rename(rollback, path)
		return err
	}
	_ = os.Remove(rollback)
	return nil
}

func backupFile(path string, data []byte, mode fs.FileMode, now time.Time) (string, error) {
	backup := fmt.Sprintf("%s.bak.%s", path, now.UTC().Format("20060102T150405Z"))
	if err := atomicWrite(backup, data, mode.Perm()); err != nil {
		return "", err
	}
	return backup, nil
}

func rollback(originals []originalFile) {
	for index := len(originals) - 1; index >= 0; index-- {
		original := originals[index]
		if original.Exists {
			_ = atomicWrite(original.Path, original.Data, original.Mode.Perm())
		} else {
			_ = os.Remove(original.Path)
		}
	}
}

func rollbackPaths(paths []string, originals map[string]originalFile) {
	for index := len(paths) - 1; index >= 0; index-- {
		original := originals[paths[index]]
		_ = atomicWrite(original.Path, original.Data, original.Mode.Perm())
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sortResult(result *Result) {
	sort.Strings(result.Installed)
	sort.Strings(result.Updated)
	sort.Strings(result.Unchanged)
	sort.Strings(result.Removed)
	sort.Strings(result.Backups)
}
