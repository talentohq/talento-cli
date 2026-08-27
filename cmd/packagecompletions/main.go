package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	talentocli "github.com/talentohq/talento-cli"
	"github.com/talentohq/talento-cli/internal/commands"
)

type completionTarget struct {
	Shell string
	File  string
}

var completionTargets = []completionTarget{
	{Shell: "bash", File: "talento.bash"},
	{Shell: "zsh", File: "_talento"},
	{Shell: "fish", File: "talento.fish"},
	{Shell: "powershell", File: "talento.ps1"},
}

func main() {
	output := flag.String("output", "release-extra/completions", "output directory")
	flag.Parse()
	if err := generate(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(output string) error {
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create completion directory: %w", err)
	}
	snapshot, err := fs.ReadFile(talentocli.Content, "schemas/gateway.json")
	if err != nil {
		return err
	}
	manifest, err := fs.ReadFile(talentocli.Content, "coverage/manifest.json")
	if err != nil {
		return err
	}
	for _, target := range completionTargets {
		root, talento, err := commands.NewRoot(snapshot, manifest, talentocli.Content)
		if err != nil {
			return err
		}
		var stdout, stderr bytes.Buffer
		talento.Stdout, talento.Stderr = &stdout, &stderr
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{"completion", target.Shell})
		if err := root.Execute(); err != nil {
			return fmt.Errorf("generate %s completion: %w: %s", target.Shell, err, stderr.String())
		}
		if stdout.Len() == 0 {
			return fmt.Errorf("generate %s completion: empty output", target.Shell)
		}
		if err := os.WriteFile(filepath.Join(output, target.File), stdout.Bytes(), 0o644); err != nil {
			return fmt.Errorf("write %s completion: %w", target.Shell, err)
		}
	}
	return nil
}
