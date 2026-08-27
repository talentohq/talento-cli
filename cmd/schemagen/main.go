package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/talentohq/talento-cli/internal/schema"
)

func main() {
	checkOnly := flag.Bool("check", false, "verify the committed manifest without rewriting it")
	flag.Parse()
	snapshotPath := filepath.Join("schemas", "gateway.json")
	manifestPath := filepath.Join("coverage", "manifest.json")
	data, err := os.ReadFile(snapshotPath)
	check(err)
	snapshot, err := schema.ParseSnapshot(data)
	check(err)
	manifest := schema.BuildManifest(snapshot, data)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	check(err)
	encoded = append(encoded, '\n')
	if *checkOnly {
		current, err := os.ReadFile(manifestPath)
		check(err)
		if !bytes.Equal(current, encoded) {
			check(fmt.Errorf("%s is stale; run go run ./cmd/schemagen", manifestPath))
		}
		check(schema.ValidateCoverage(snapshot, data, manifest))
		fmt.Printf("verified %s with %d tools and %d resources\n", manifestPath, len(manifest.Tools), len(manifest.Resources))
		return
	}
	// #nosec G703 -- manifestPath is the fixed repository path coverage/manifest.json.
	check(os.WriteFile(manifestPath, encoded, 0o644))
	check(schema.ValidateCoverage(snapshot, data, manifest))
	fmt.Printf("wrote %s with %d tools and %d resources\n", manifestPath, len(manifest.Tools), len(manifest.Resources))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
