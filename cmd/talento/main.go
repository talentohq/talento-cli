package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"

	talentocli "github.com/talentohq/talento-cli"
	"github.com/talentohq/talento-cli/internal/commands"
	clioutput "github.com/talentohq/talento-cli/internal/output"
)

func main() {
	os.Exit(run())
}

func run() int {
	snapshot, err := fs.ReadFile(talentocli.Content, "schemas/gateway.json")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error: load embedded gateway schema:", err)
		return 1
	}
	manifest, err := fs.ReadFile(talentocli.Content, "coverage/manifest.json")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error: load embedded coverage manifest:", err)
		return 1
	}
	root, talento, err := commands.NewRoot(snapshot, manifest, talentocli.Content)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error: initialize talento:", err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		_ = talento.Output().Error(err)
		return clioutput.ExitCode(err)
	}
	return 0
}
