package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateReleaseCompletions(t *testing.T) {
	t.Setenv("TALENTO_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	t.Setenv("TALENTO_HOME", filepath.Join(t.TempDir(), "home"))
	output := t.TempDir()
	if err := generate(output); err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{
		"talento.bash": "__start_talento",
		"_talento":     "#compdef talento",
		"talento.fish": "complete -c talento",
		"talento.ps1":  "Register-ArgumentCompleter",
	}
	for name, marker := range wants {
		data, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), marker) {
			t.Fatalf("%s does not contain %q", name, marker)
		}
	}
}
