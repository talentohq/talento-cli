package releaselockstep

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckAcceptsMatchingReleaseSurfaces(t *testing.T) {
	if runtimeGOOS() == "windows" {
		t.Skip("fixture binary is a POSIX shell script")
	}
	t.Parallel()
	fixture := newFixture(t, "1.2.3")
	if err := Check(fixture.options); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRejectsEmbeddedPluginMismatch(t *testing.T) {
	if runtimeGOOS() == "windows" {
		t.Skip("fixture binary is a POSIX shell script")
	}
	t.Parallel()
	fixture := newFixture(t, "1.2.3")
	name := filepath.Join(fixture.options.Dist, "talento_1.2.3_linux_amd64.tar.gz")
	writeTar(t, name, archiveFiles("9.9.9"))
	err := Check(fixture.options)
	if err == nil || !strings.Contains(err.Error(), `manifest version is "9.9.9", expected "1.2.3"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckRejectsBinaryProvenanceMismatch(t *testing.T) {
	if runtimeGOOS() == "windows" {
		t.Skip("fixture binary is a POSIX shell script")
	}
	t.Parallel()
	fixture := newFixture(t, "1.2.3")
	writeFakeBinary(t, fixture.options.Binary, "1.2.3", "wrong", "1720000000")
	err := Check(fixture.options)
	if err == nil || !strings.Contains(err.Error(), `release binary commit is "wrong"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckEmbeddedBuildFlags(t *testing.T) {
	t.Parallel()
	binary := append([]byte("prefix\x00"), buildFlags("1.2.3", "abc1234", "1720000000")...)
	if err := checkEmbeddedBuildFlags(binary, "1.2.3", "abc1234", "1720000000"); err != nil {
		t.Fatal(err)
	}
	if err := checkEmbeddedBuildFlags(binary, "1.2.4", "abc1234", "1720000000"); err == nil {
		t.Fatal("mismatched embedded version unexpectedly passed")
	}
}

func TestCheckRejectsScoopManifest(t *testing.T) {
	if runtimeGOOS() == "windows" {
		t.Skip("fixture binary is a POSIX shell script")
	}
	t.Parallel()
	fixture := newFixture(t, "1.2.3")
	writeFile(t, filepath.Join(fixture.options.Indexes, "talento.json"), `{"version":"1.2.3"}`)
	err := Check(fixture.options)
	if err == nil || !strings.Contains(err.Error(), "Scoop manifest is present") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckArtifactFileVersionsRejectsAnotherRelease(t *testing.T) {
	t.Parallel()
	dist := t.TempDir()
	writeFile(t, filepath.Join(dist, "talento-codex-plugin_1.2.2.zip"), "fixture")
	err := checkArtifactFileVersions(dist, "1.2.3")
	if err == nil || !strings.Contains(err.Error(), "carries version 1.2.2, expected 1.2.3") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type fixture struct {
	options Options
}

func newFixture(t *testing.T, version string) fixture {
	t.Helper()
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	indexes := filepath.Join(root, "indexes")
	if err := os.MkdirAll(filepath.Join(root, "nix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(indexes, "Casks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "nix", "version.nix"), `"`+version+`"`)
	writeFile(t, filepath.Join(root, "nix", "package.nix"), strings.Join([]string{
		"internal/buildinfo.Version=${version}",
		"internal/buildinfo.Commit=${commit}",
		"internal/buildinfo.Date=${date}",
		"internal/buildinfo.Source=nix",
		"internal/buildinfo.MetadataFingerprint=talento-release-metadata:${version}:${commit}:${date}:nix",
	}, "\n"))
	writeFile(t, filepath.Join(root, "flake.nix"), "version = import ./nix/version.nix;\ninherit version commit date;")

	writeZip(t, filepath.Join(dist, "talento-codex-plugin_"+version+".zip"), map[string][]byte{
		"talento-codex-plugin/.codex-plugin/plugin.json": manifest(version),
	})
	writeZip(t, filepath.Join(dist, "talento-claude-code-plugin_"+version+".zip"), map[string][]byte{
		"talento-claude-code-plugin/.claude-plugin/plugin.json": manifest(version),
	})
	for _, platform := range platforms {
		name := filepath.Join(dist, "talento_"+version+"_"+platform.OS+"_"+platform.Arch+"."+platform.Extension)
		if platform.Extension == "zip" {
			writeZip(t, name, archiveFiles(version))
		} else {
			writeTar(t, name, archiveFiles(version))
		}
	}
	writeFile(t, filepath.Join(indexes, "Casks", "talento.rb"), `version "`+version+`"
url "https://github.com/talentohq/talento-cli/releases/download/v`+version+`/talento.tar.gz"`)
	binary := filepath.Join(root, "talento")
	writeFakeBinary(t, binary, version, "abc1234", "1720000000")
	return fixture{options: Options{
		Root: root, Tag: "v" + version, Commit: "abc1234", Date: "1720000000",
		Dist: dist, Indexes: indexes, Binary: binary, skipPlatformBinaryMetadata: true,
	}}
}

func archiveFiles(version string) map[string][]byte {
	return map[string][]byte{
		"plugins/talento/.codex-plugin/plugin.json":      manifest(version),
		"plugins/claude-code/.claude-plugin/plugin.json": manifest(version),
		"talento":     buildFlags(version, "abc1234", "1720000000"),
		"talento.exe": buildFlags(version, "abc1234", "1720000000"),
	}
}

func buildFlags(version, commit, date string) []byte {
	return []byte("talento-release-metadata:" + version + ":" + commit + ":" + date + ":release")
}

func manifest(version string) []byte {
	return []byte(`{"name":"talento","version":"` + version + `"}`)
}

func writeFakeBinary(t *testing.T, target, version, commit, date string) {
	t.Helper()
	writeFile(t, target, "#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"data\":{\"version\":\""+version+"\",\"commit\":\""+commit+"\",\"date\":\""+date+"\",\"source\":\"release\"}}'\n")
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, target, value string) {
	t.Helper()
	if err := os.WriteFile(target, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, target string, files map[string][]byte) {
	t.Helper()
	file, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, data := range files {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTar(t *testing.T, target string, files map[string][]byte) {
	t.Helper()
	file, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for name, data := range files {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

var runtimeGOOS = func() string { return runtime.GOOS }
