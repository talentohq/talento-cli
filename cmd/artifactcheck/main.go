package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/talentohq/talento-cli/internal/releaseartifacts"
)

func main() {
	directory := flag.String("dir", "dist", "release artifact directory")
	allowlistPath := flag.String("allowlist", "release/artifact-allowlist.json", "artifact allowlist")
	copyTo := flag.String("copy-to", "", "copy only allowlisted artifacts to this directory")
	flag.Parse()
	allowlist, err := releaseartifacts.Load(*allowlistPath)
	check(err)
	names, err := allowlist.ValidateDirectory(*directory)
	check(err)
	if *copyTo != "" {
		check(releaseartifacts.CopyAllowed(*directory, *copyTo, names))
	}
	for _, name := range names {
		fmt.Println(name)
	}
}

func check(err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
