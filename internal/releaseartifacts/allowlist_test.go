package releaseartifacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitArtifactAllowlist(t *testing.T) {
	allowlist, err := Load("../../release/artifact-allowlist.json")
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		"talento_0.1.0_darwin_amd64.tar.gz", "talento_0.1.0_darwin_arm64.tar.gz",
		"talento_0.1.0_linux_amd64.tar.gz", "talento_0.1.0_linux_arm64.tar.gz",
		"talento_0.1.0_linux_amd64.deb", "talento-codex-plugin_0.1.0.zip",
		"checksums.txt", "checksums.txt.sig", "checksums.txt.sigstore.json", "install.sh",
	}
	for _, name := range allowed {
		if !allowlist.Allows(name) {
			t.Errorf("expected %s to be allowlisted", name)
		}
	}
	for _, name := range []string{"config.json", "credentials.txt", "talento_0.1.0_linux_386.tar.gz", "private-repo.zip", "talento_0.1.0_windows_amd64.zip", "install.ps1"} {
		if allowlist.Allows(name) {
			t.Errorf("expected %s to be rejected", name)
		}
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, allowed[0]), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := allowlist.ValidateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "secret.env"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := allowlist.ValidateDirectory(directory); err == nil {
		t.Fatal("expected unknown artifact rejection")
	}
}

func TestCopyAllowedRejectsNonBasenamesBeforeCopying(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "safe.zip"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../escape", `..\\escape`, "/absolute", ".."} {
		t.Run(name, func(t *testing.T) {
			if err := CopyAllowed(source, destination, []string{"safe.zip", name}); err == nil {
				t.Fatalf("expected unsafe artifact name %q to be rejected", name)
			}
			if _, err := os.Stat(filepath.Join(destination, "safe.zip")); !os.IsNotExist(err) {
				t.Fatalf("copy began before validating all names: %v", err)
			}
		})
	}
}
