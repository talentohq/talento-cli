package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/talentohq/talento-cli/internal/releaseversion"
)

func main() {
	version := flag.String("version", "", "release version (an optional v prefix is normalized away)")
	checksumsPath := flag.String("checksums", "", "checksums.txt path")
	output := flag.String("output", "package-indexes", "output directory")
	flag.Parse()
	if *version == "" || *checksumsPath == "" {
		fatal("--version and --checksums are required")
	}
	normalizedVersion, err := releaseversion.Normalize(*version)
	if err != nil {
		fatal("invalid --version: %v", err)
	}
	*version = normalizedVersion
	checksums, err := readChecksums(*checksumsPath)
	if err != nil {
		fatal("read checksums: %v", err)
	}
	darwinAMD64 := archiveName(*version, "darwin", "amd64", "tar.gz")
	darwinARM64 := archiveName(*version, "darwin", "arm64", "tar.gz")
	windowsAMD64 := archiveName(*version, "windows", "amd64", "zip")
	windowsARM64 := archiveName(*version, "windows", "arm64", "zip")
	for _, name := range []string{darwinAMD64, darwinARM64, windowsAMD64, windowsARM64} {
		if checksums[name] == "" {
			fatal("checksum missing for %s", name)
		}
	}
	base := "https://github.com/talentohq/talento-cli/releases/download/v" + *version + "/"
	cask := fmt.Sprintf(`cask "talento" do
  version %q
  arch arm: "arm64", intel: "amd64"
  sha256 arm: %q, intel: %q

  url %q + "talento_#{version}_darwin_#{arch}.tar.gz"
  name "Talento CLI"
  desc "Native command-line client for TalentoHQ"
  homepage "https://talentohq.com"

  binary "talento"
end
`, *version, checksums[darwinARM64], checksums[darwinAMD64], base)
	scoop := map[string]any{
		"version":     *version,
		"description": "Native command-line client for TalentoHQ",
		"homepage":    "https://talentohq.com",
		"license":     "MIT",
		"architecture": map[string]any{
			"64bit": map[string]any{"url": base + windowsAMD64, "hash": checksums[windowsAMD64]},
			"arm64": map[string]any{"url": base + windowsARM64, "hash": checksums[windowsARM64]},
		},
		"bin":      "talento.exe",
		"checkver": map[string]any{"github": "https://github.com/talentohq/talento-cli"},
		"autoupdate": map[string]any{"architecture": map[string]any{
			"64bit": map[string]any{"url": "https://github.com/talentohq/talento-cli/releases/download/v$version/talento_$version_windows_amd64.zip"},
			"arm64": map[string]any{"url": "https://github.com/talentohq/talento-cli/releases/download/v$version/talento_$version_windows_arm64.zip"},
		}},
	}
	encoded, err := json.MarshalIndent(scoop, "", "  ")
	if err != nil {
		fatal("encode Scoop manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(*output, "Casks"), 0o755); err != nil {
		fatal("create output: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*output, "Casks", "talento.rb"), []byte(cask), 0o644); err != nil {
		fatal("write Homebrew cask: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*output, "talento.json"), append(encoded, '\n'), 0o644); err != nil {
		fatal("write Scoop manifest: %v", err)
	}
}

func readChecksums(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			result[strings.TrimPrefix(fields[len(fields)-1], "*")] = fields[0]
		}
	}
	return result, scanner.Err()
}

func archiveName(version, goos, goarch, extension string) string {
	return fmt.Sprintf("talento_%s_%s_%s.%s", version, goos, goarch, extension)
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "packageindexes: "+format+"\n", args...)
	os.Exit(1)
}
