package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSignedChecksums(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	archive := []byte("archive")
	sum := sha256.Sum256(archive)
	manifest := []byte(hex.EncodeToString(sum[:]) + "  talento_1.2.3_linux_amd64.tar.gz\n")
	signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest)))
	encodedKey := base64.StdEncoding.EncodeToString(publicKey)
	if err := verifySignature(manifest, signature, encodedKey); err != nil {
		t.Fatal(err)
	}
	if got, err := checksumFor(manifest, "talento_1.2.3_linux_amd64.tar.gz"); err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("checksum = %q, err = %v", got, err)
	}
	// Always change the signed bytes: a random signature can already start
	// with 'x', which made this rejection check intermittently test no change.
	if signature[0] == 'x' {
		signature[0] = 'y'
	} else {
		signature[0] = 'x'
	}
	if err := verifySignature(manifest, signature, encodedKey); err == nil {
		t.Fatal("expected signature rejection")
	}
}

func TestTransactionalExecutableReplacementAndPreflightRollback(t *testing.T) {
	current := buildVersionBinary(t, "1.0.0", "current")
	candidate := buildVersionBinary(t, "1.2.3", "candidate")
	candidateData, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(context.Background(), current, candidateData, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := validateBinary(context.Background(), current, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current + ".rollback"); !os.IsNotExist(err) {
		t.Fatalf("rollback file was not cleaned up: %v", err)
	}

	original := buildVersionBinary(t, "1.0.0", "original")
	if err := replaceExecutable(context.Background(), original, candidateData, "9.9.9"); err == nil {
		t.Fatal("expected candidate version rejection")
	}
	if err := validateBinary(context.Background(), original, "1.0.0"); err != nil {
		t.Fatalf("original executable changed after failed preflight: %v", err)
	}
}

func buildVersionBinary(t *testing.T, version, name string) string {
	t.Helper()
	directory := t.TempDir()
	source := fmt.Sprintf("package main\nimport (\"fmt\"; \"os\")\nfunc main(){ if len(os.Args) == 3 && os.Args[1] == \"--agent\" && os.Args[2] == \"version\" { fmt.Print(`{\"version\":%q}`); return }; os.Exit(2) }\n", version)
	if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, name)
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", executable, filepath.Join(directory, "main.go"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build helper executable: %v\n%s", err, output)
	}
	return executable
}

func TestArtifactNamesAndPackageManagerDelegation(t *testing.T) {
	if got := artifactName("1.2.3", "windows", "arm64"); got != "talento_1.2.3_windows_arm64.zip" {
		t.Fatalf("artifact = %q", got)
	}
	if got := PackageManagerCommand("/opt/homebrew/Cellar/talento/1.2.3/bin/talento"); len(got) == 0 || got[0] != "brew" {
		t.Fatalf("delegation = %#v", got)
	}
	if compareVersions("1.2.4", "1.2.3") <= 0 || compareVersions("1.2.3", "1.2.3") != 0 {
		t.Fatal("version comparison failed")
	}
}
