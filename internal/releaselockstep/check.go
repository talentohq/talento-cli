package releaselockstep

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/talentohq/talento-cli/internal/releaseversion"
)

type Options struct {
	Root    string
	Tag     string
	Commit  string
	Date    string
	Dist    string
	Indexes string
	Binary  string

	skipPlatformBinaryMetadata bool
}

type binaryEnvelope struct {
	OK   bool `json:"ok"`
	Data struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
		Source  string `json:"source"`
	} `json:"data"`
}

var platforms = []struct {
	OS, Arch, Extension string
}{
	{"darwin", "amd64", "tar.gz"},
	{"darwin", "arm64", "tar.gz"},
	{"linux", "amd64", "tar.gz"},
	{"linux", "arm64", "tar.gz"},
	{"windows", "amd64", "zip"},
	{"windows", "arm64", "zip"},
}

func Check(options Options) error {
	version, err := releaseversion.Normalize(options.Tag)
	if err != nil {
		return fmt.Errorf("release tag: %w", err)
	}
	tag, _ := releaseversion.Tag(version)
	if options.Tag != tag {
		return fmt.Errorf("release tag %q must be the canonical tag %q", options.Tag, tag)
	}
	if options.Commit == "" || options.Date == "" {
		return fmt.Errorf("expected commit and date are required")
	}
	if options.Root == "" {
		options.Root = "."
	}
	if err := checkNix(options.Root, version); err != nil {
		return err
	}
	if err := checkArtifactFileVersions(options.Dist, version); err != nil {
		return err
	}
	if err := checkPluginArchives(options.Dist, version); err != nil {
		return err
	}
	if err := checkPlatformArchives(options.Dist, version, options.Commit, options.Date, options.skipPlatformBinaryMetadata); err != nil {
		return err
	}
	if err := checkPackageIndexes(options.Indexes, version); err != nil {
		return err
	}
	if options.Binary == "" {
		binary, cleanup, err := extractHostBinary(options.Dist, version)
		if err != nil {
			return err
		}
		defer cleanup()
		options.Binary = binary
	}
	if err := checkBinary(options.Binary, version, options.Commit, options.Date); err != nil {
		return err
	}
	return nil
}

func checkArtifactFileVersions(dist, version string) error {
	entries, err := os.ReadDir(dist)
	if err != nil {
		return fmt.Errorf("read release artifacts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		candidate := ""
		switch {
		case strings.HasPrefix(name, "talento_"):
			rest := strings.TrimPrefix(name, "talento_")
			candidate, _, _ = strings.Cut(rest, "_")
		case strings.HasPrefix(name, "talento-") && strings.Contains(name, "_"):
			withoutExtension := strings.TrimSuffix(name, ".zip")
			index := strings.LastIndexByte(withoutExtension, '_')
			if index >= 0 {
				candidate = withoutExtension[index+1:]
			}
		}
		if candidate == "" {
			continue
		}
		normalized, normalizeErr := releaseversion.Normalize(candidate)
		if normalizeErr == nil && normalized != version {
			return fmt.Errorf("artifact %s carries version %s, expected %s", name, normalized, version)
		}
	}
	return nil
}

func checkNix(root, version string) error {
	versionPath := filepath.Join(root, "nix", "version.nix")
	data, err := os.ReadFile(versionPath)
	if err != nil {
		return fmt.Errorf("read Nix version stamp: %w", err)
	}
	if strings.TrimSpace(lastNonCommentLine(string(data))) != fmt.Sprintf("%q", version) {
		return fmt.Errorf("Nix version stamp does not equal %s", version)
	}
	packageData, err := os.ReadFile(filepath.Join(root, "nix", "package.nix"))
	if err != nil {
		return fmt.Errorf("read Nix package: %w", err)
	}
	for _, field := range []string{"Version=${version}", "Commit=${commit}", "Date=${date}", "Source=nix", "MetadataFingerprint=talento-release-metadata:${version}:${commit}:${date}:nix"} {
		if !strings.Contains(string(packageData), "internal/buildinfo."+field) {
			return fmt.Errorf("Nix package does not inject buildinfo.%s", field)
		}
	}
	flakeData, err := os.ReadFile(filepath.Join(root, "flake.nix"))
	if err != nil {
		return fmt.Errorf("read flake: %w", err)
	}
	for _, field := range []string{"version = import ./nix/version.nix;", "inherit version commit date;"} {
		if !strings.Contains(string(flakeData), field) {
			return fmt.Errorf("flake does not derive complete build metadata: missing %q", field)
		}
	}
	return nil
}

func lastNonCommentLine(value string) string {
	var result string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			result = line
		}
	}
	return result
}

func checkPluginArchives(dist, version string) error {
	tests := []struct {
		archive, manifest string
	}{
		{"talento-codex-plugin_" + version + ".zip", "talento-codex-plugin/.codex-plugin/plugin.json"},
		{"talento-claude-code-plugin_" + version + ".zip", "talento-claude-code-plugin/.claude-plugin/plugin.json"},
	}
	for _, test := range tests {
		archivePath := filepath.Join(dist, test.archive)
		data, err := readArchiveFile(archivePath, test.manifest)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", test.archive, err)
		}
		if err := checkManifestVersion(data, version); err != nil {
			return fmt.Errorf("%s:%s: %w", test.archive, test.manifest, err)
		}
	}
	return nil
}

func checkPlatformArchives(dist, version, commit, date string, skipBinaryMetadata bool) error {
	for _, platform := range platforms {
		name := fmt.Sprintf("talento_%s_%s_%s.%s", version, platform.OS, platform.Arch, platform.Extension)
		archivePath := filepath.Join(dist, name)
		for _, manifest := range []string{
			"plugins/talento/.codex-plugin/plugin.json",
			"plugins/claude-code/.claude-plugin/plugin.json",
		} {
			data, err := readArchiveFile(archivePath, manifest)
			if err != nil {
				return fmt.Errorf("inspect %s: %w", name, err)
			}
			if err := checkManifestVersion(data, version); err != nil {
				return fmt.Errorf("%s:%s: %w", name, manifest, err)
			}
		}
		if !skipBinaryMetadata {
			binaryName := "talento"
			if platform.OS == "windows" {
				binaryName = "talento.exe"
			}
			binary, err := readArchiveFile(archivePath, binaryName)
			if err != nil {
				return fmt.Errorf("inspect %s: %w", name, err)
			}
			if err := checkEmbeddedBuildFlags(binary, version, commit, date); err != nil {
				return fmt.Errorf("%s:%s: %w", name, binaryName, err)
			}
		}
	}
	return nil
}

func checkEmbeddedBuildFlags(binary []byte, version, commit, date string) error {
	expected := "talento-release-metadata:" + version + ":" + commit + ":" + date + ":release"
	if !bytes.Contains(binary, []byte(expected)) {
		return fmt.Errorf("embedded release fingerprint does not equal %s", expected)
	}
	return nil
}

func checkManifestVersion(data []byte, version string) error {
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("invalid manifest JSON: %w", err)
	}
	if manifest.Version != version {
		return fmt.Errorf("manifest version is %q, expected %q", manifest.Version, version)
	}
	return nil
}

func checkPackageIndexes(indexes, version string) error {
	cask, err := os.ReadFile(filepath.Join(indexes, "Casks", "talento.rb"))
	if err != nil {
		return fmt.Errorf("read Homebrew cask: %w", err)
	}
	if !strings.Contains(string(cask), fmt.Sprintf("version %q", version)) {
		return fmt.Errorf("Homebrew cask version does not equal %s", version)
	}
	expectedBase := "releases/download/v" + version + "/"
	if !strings.Contains(string(cask), expectedBase) {
		return fmt.Errorf("Homebrew cask release URL does not use tag v%s", version)
	}
	scoopData, err := os.ReadFile(filepath.Join(indexes, "talento.json"))
	if err != nil {
		return fmt.Errorf("read Scoop manifest: %w", err)
	}
	var scoop struct {
		Version      string `json:"version"`
		Architecture map[string]struct {
			URL string `json:"url"`
		} `json:"architecture"`
	}
	if err := json.Unmarshal(scoopData, &scoop); err != nil {
		return fmt.Errorf("parse Scoop manifest: %w", err)
	}
	if scoop.Version != version {
		return fmt.Errorf("Scoop manifest version is %q, expected %q", scoop.Version, version)
	}
	for _, architecture := range []string{"64bit", "arm64"} {
		entry, ok := scoop.Architecture[architecture]
		if !ok {
			return fmt.Errorf("Scoop manifest has no %s package", architecture)
		}
		if !strings.Contains(entry.URL, expectedBase) {
			return fmt.Errorf("Scoop %s URL does not use tag v%s", architecture, version)
		}
	}
	return nil
}

func checkBinary(binary, version, commit, date string) error {
	output, err := exec.Command(binary, "--json", "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("execute release binary: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var envelope binaryEnvelope
	if err := json.Unmarshal(output, &envelope); err != nil {
		return fmt.Errorf("parse release binary version output: %w", err)
	}
	if !envelope.OK {
		return fmt.Errorf("release binary version command did not succeed")
	}
	expected := map[string]string{"version": version, "commit": commit, "date": date, "source": "release"}
	actual := map[string]string{"version": envelope.Data.Version, "commit": envelope.Data.Commit, "date": envelope.Data.Date, "source": envelope.Data.Source}
	for _, key := range []string{"version", "commit", "date", "source"} {
		if actual[key] != expected[key] {
			return fmt.Errorf("release binary %s is %q, expected %q", key, actual[key], expected[key])
		}
	}
	return nil
}

func extractHostBinary(dist, version string) (string, func(), error) {
	osName := runtime.GOOS
	extension := "tar.gz"
	binaryName := "talento"
	if osName == "windows" {
		extension = "zip"
		binaryName = "talento.exe"
	}
	archivePath := filepath.Join(dist, fmt.Sprintf("talento_%s_%s_%s.%s", version, osName, runtime.GOARCH, extension))
	data, err := readArchiveFile(archivePath, binaryName)
	if err != nil {
		return "", func() {}, fmt.Errorf("extract host release binary: %w", err)
	}
	directory, err := os.MkdirTemp("", "talento-release-lockstep-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	target := filepath.Join(directory, binaryName)
	if err := os.WriteFile(target, data, 0o755); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return target, cleanup, nil
}

func readArchiveFile(archivePath, target string) ([]byte, error) {
	if strings.HasSuffix(archivePath, ".zip") {
		return readZipFile(archivePath, target)
	}
	return readTarGzipFile(archivePath, target)
}

func readZipFile(archivePath, target string) ([]byte, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	for _, file := range archive.File {
		if cleanArchiveName(file.Name) != target {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	}
	return nil, fmt.Errorf("archive entry %s is missing", target)
}

func readTarGzipFile(archivePath, target string) ([]byte, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()
	archive := tar.NewReader(gzipReader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if cleanArchiveName(header.Name) == target {
			return io.ReadAll(archive)
		}
	}
	return nil, fmt.Errorf("archive entry %s is missing", target)
}

func cleanArchiveName(value string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(value)), "./")
}
