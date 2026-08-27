package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/talentohq/talento-cli/internal/releaselockstep"
)

func main() {
	options := releaselockstep.Options{}
	flag.StringVar(&options.Root, "root", ".", "repository root")
	flag.StringVar(&options.Tag, "tag", "", "canonical release tag (vVERSION)")
	flag.StringVar(&options.Commit, "commit", "", "expected embedded commit")
	flag.StringVar(&options.Date, "date", "", "expected embedded build date")
	flag.StringVar(&options.Dist, "dist", "dist", "release artifact directory")
	flag.StringVar(&options.Indexes, "indexes", "package-indexes", "generated package-manager metadata directory")
	flag.StringVar(&options.Binary, "binary", "", "release binary to inspect (defaults to extracting the host archive)")
	flag.Parse()
	if err := releaselockstep.Check(options); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "release-lockstep:", err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintf(os.Stdout, "release metadata is in lockstep for %s\n", options.Tag)
}
