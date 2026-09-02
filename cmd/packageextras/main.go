package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	talentocli "github.com/talentohq/talento-cli"
	"github.com/talentohq/talento-cli/internal/managed"
	"github.com/talentohq/talento-cli/internal/releaseversion"
)

func main() {
	version := flag.String("version", "", "release version (an optional v prefix is normalized away)")
	output := flag.String("output", "dist", "output directory")
	windowsPublisher := flag.String("windows-publisher", "", "expected stable Windows Authenticode certificate subject")
	flag.Parse()
	normalizedVersion, err := releaseversion.Normalize(*version)
	if err != nil {
		fatal("invalid --version: %v", err)
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		fatal("create output directory: %v", err)
	}

	canonical, err := tree("skills/talento", "talento")
	if err != nil {
		fatal("load canonical skill: %v", err)
	}
	codex := mustTree("plugins/talento", "talento-codex-plugin")
	if err := stampPluginManifest(codex, "talento-codex-plugin/.codex-plugin/plugin.json", normalizedVersion); err != nil {
		fatal("stamp Codex plugin: %v", err)
	}
	claude := mustTree("plugins/claude-code", "talento-claude-code-plugin")
	if err := stampPluginManifest(claude, "talento-claude-code-plugin/.claude-plugin/plugin.json", normalizedVersion); err != nil {
		fatal("stamp Claude Code plugin: %v", err)
	}
	packages := map[string]map[string][]byte{
		"talento-skill":              canonical,
		"talento-codex-plugin":       codex,
		"talento-claude-code-plugin": claude,
		"talento-gemini-wrapper":     canonical,
		"talento-grok-wrapper":       canonical,
		"talento-copilot-wrapper":    canonical,
		"talento-opencode-wrapper":   canonical,
		"talento-cursor-wrapper":     {"talento.mdc": []byte(wrapper("cursor"))},
		"talento-windsurf-wrapper":   {"talento.md": []byte(wrapper("windsurf"))},
	}
	for name, files := range packages {
		target := filepath.Join(*output, fmt.Sprintf("%s_%s.zip", name, normalizedVersion))
		if err := writeZip(target, files); err != nil {
			fatal("write %s: %v", target, err)
		}
	}
	stagedPlugins := make(map[string][]byte)
	for name, data := range codex {
		stagedPlugins[strings.Replace(name, "talento-codex-plugin/", "talento/", 1)] = data
	}
	for name, data := range claude {
		stagedPlugins[strings.Replace(name, "talento-claude-code-plugin/", "claude-code/", 1)] = data
	}
	if err := writeTree(filepath.Join(*output, "plugins"), stagedPlugins); err != nil {
		fatal("write staged plugins: %v", err)
	}
	if err := stageInstallers(filepath.Join(*output, "installers"), *windowsPublisher); err != nil {
		fatal("stage installers: %v", err)
	}
}

const windowsPublisherPlaceholder = "__TALENTO_WINDOWS_AUTHENTICODE_PUBLISHER__"

func stageInstallers(output, windowsPublisher string) error {
	shell, err := os.ReadFile("install.sh")
	if err != nil {
		return fmt.Errorf("read install.sh: %w", err)
	}
	powershell, err := os.ReadFile("install.ps1")
	if err != nil {
		return fmt.Errorf("read install.ps1: %w", err)
	}
	if bytes.Count(powershell, []byte(windowsPublisherPlaceholder)) != 1 {
		return fmt.Errorf("install.ps1 must contain the Windows publisher placeholder exactly once")
	}
	if windowsPublisher != "" {
		if strings.ContainsAny(windowsPublisher, "\r\n") {
			return fmt.Errorf("Windows publisher must be a single-line certificate subject")
		}
		for _, current := range windowsPublisher {
			if current < 0x20 || current > 0x7e {
				return fmt.Errorf("Windows publisher must contain printable ASCII only for Windows PowerShell 5.1")
			}
		}
		escapedPublisher := strings.ReplaceAll(windowsPublisher, "'", "''")
		powershell = bytes.ReplaceAll(powershell, []byte(windowsPublisherPlaceholder), []byte(escapedPublisher))
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	// #nosec G703 -- output is an explicit trusted release-build directory and the filename is fixed.
	if err := os.WriteFile(filepath.Join(output, "install.sh"), shell, 0o755); err != nil {
		return err
	}
	// #nosec G703 -- output is an explicit trusted release-build directory and the filename is fixed.
	return os.WriteFile(filepath.Join(output, "install.ps1"), powershell, 0o644)
}

func tree(root, destination string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := fs.WalkDir(talentocli.Content, root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(talentocli.Content, filePath)
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(filePath, root+"/")
		files[path.Join(destination, relative)] = data
		return nil
	})
	return files, err
}

func mustTree(root, destination string) map[string][]byte {
	files, err := tree(root, destination)
	if err != nil {
		fatal("load %s: %v", root, err)
	}
	return files
}

func wrapper(id string) string {
	agent, ok := managed.AgentByID(id)
	if !ok {
		fatal("unknown agent %s", id)
	}
	return managed.WrapperFor(agent)
}

func stampPluginManifest(files map[string][]byte, manifestPath, version string) error {
	data, ok := files[manifestPath]
	if !ok {
		return fmt.Errorf("manifest %s is missing", manifestPath)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	if _, ok := manifest["version"].(string); !ok {
		return fmt.Errorf("manifest %s has no string version", manifestPath)
	}
	manifest["version"] = version
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", manifestPath, err)
	}
	files[manifestPath] = append(encoded, '\n')
	return nil
}

func writeTree(root string, files map[string][]byte) error {
	for name, data := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeZip(target string, files map[string][]byte) error {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: path.Clean(name), Method: zip.Deflate}
		header.SetMode(0o644)
		header.SetModTime(time.Unix(0, 0).UTC())
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := writer.Write(files[name]); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return os.WriteFile(target, buffer.Bytes(), 0o644)
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "packageextras: "+format+"\n", args...)
	os.Exit(1)
}
