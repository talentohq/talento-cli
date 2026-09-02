package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStampPluginManifest(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"plugin/plugin.json": []byte(`{"name":"talento","version":"0.1.0"}`)}
	if err := stampPluginManifest(files, "plugin/plugin.json", "1.2.3-rc.1"); err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(files["plugin/plugin.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.2.3-rc.1" {
		t.Fatalf("version = %q, want 1.2.3-rc.1", manifest.Version)
	}
}

func TestStampPluginManifestFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string][]byte
	}{
		{name: "missing", files: map[string][]byte{}},
		{name: "invalid JSON", files: map[string][]byte{"plugin.json": []byte(`{`)}},
		{name: "missing version", files: map[string][]byte{"plugin.json": []byte(`{"name":"talento"}`)}},
		{name: "non-string version", files: map[string][]byte{"plugin.json": []byte(`{"version":1}`)}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := stampPluginManifest(test.files, "plugin.json", "1.2.3"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestWriteZipCarriesStampedManifest(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "plugin.zip")
	files := map[string][]byte{"plugin/plugin.json": []byte(`{"version":"0.1.0"}`)}
	if err := stampPluginManifest(files, "plugin/plugin.json", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := writeZip(target, files); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(target)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	reader, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "2.0.0" {
		t.Fatalf("archive version = %q, want 2.0.0", manifest.Version)
	}
	if info, err := os.Stat(target); err != nil || info.Size() == 0 {
		t.Fatalf("archive was not written: info=%v err=%v", info, err)
	}
}

func TestStageInstallersStampsWindowsPublisher(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "../.."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	output := t.TempDir()
	publisher := "CN=TalentoHQ, O=TalentoHQ"
	if err := stageInstallers(output, publisher); err != nil {
		t.Fatal(err)
	}
	powershell, err := os.ReadFile(filepath.Join(output, "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(powershell), windowsPublisherPlaceholder) || !strings.Contains(string(powershell), publisher) {
		t.Fatal("staged PowerShell installer does not contain the expected publisher")
	}
	if info, err := os.Stat(filepath.Join(output, "install.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("staged shell installer mode = %v, err = %v", info, err)
	}
}

func TestStageInstallersRejectsUnsafePublisher(t *testing.T) {
	for _, publisher := range []string{"CN=TalentoHQ\nforged", "CN=TalentoHQ\x00forged", "CN=TalentóHQ"} {
		if err := stageInstallers(t.TempDir(), publisher); err == nil {
			t.Fatalf("publisher %q unexpectedly passed", publisher)
		}
	}
}

func TestStageInstallersKeepsPlaceholderWithoutPublisher(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "../.."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	output := t.TempDir()
	if err := stageInstallers(output, ""); err != nil {
		t.Fatal(err)
	}
	powershell, err := os.ReadFile(filepath.Join(output, "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(powershell), windowsPublisherPlaceholder) {
		t.Fatal("staged PowerShell installer dropped the publisher placeholder")
	}
}
