package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	talentocli "github.com/talentohq/talento-cli"
	"github.com/talentohq/talento-cli/internal/commands"
	"github.com/talentohq/talento-cli/internal/surface"
)

const surfaceDir = ".surface"

type indexFile struct {
	SchemaVersion  int `json:"schema_version"`
	CurrentVersion int `json:"current_version"`
}

func main() {
	checkOnly := flag.Bool("check", false, "verify the current public CLI surface without rewriting files")
	showDiff := flag.Bool("diff", false, "print changes between the checked-in surface and the current command tree")
	next := flag.Bool("next", false, "append a reviewed surface version and advance the index")
	initialize := flag.Bool("init", false, "create version 1 when no surface history exists")
	flag.Parse()
	modes := 0
	for _, selected := range []bool{*checkOnly, *showDiff, *next, *initialize} {
		if selected {
			modes++
		}
	}
	if modes != 1 {
		check(fmt.Errorf("choose exactly one of -check, -diff, -next, or -init"))
	}

	live := liveSurface()
	if *initialize {
		initializeHistory(live)
		return
	}
	index, history, policy := loadHistory()
	validateHistory(index, history, policy, *next)
	current := history[index.CurrentVersion-1]

	if *showDiff {
		data, err := json.MarshalIndent(surface.Diff(current, live), "", "  ")
		check(err)
		_, err = os.Stdout.Write(append(data, '\n'))
		check(err)
		return
	}

	liveData, err := surface.Encode(live)
	check(err)
	currentPath := versionPath(index.CurrentVersion)
	currentData, err := os.ReadFile(currentPath)
	check(err)
	if *checkOnly {
		if !bytes.Equal(currentData, liveData) {
			check(fmt.Errorf("public CLI surface differs from %s; run `go run ./cmd/surfacegen -diff`, then append a reviewed version with -next", currentPath))
		}
		fmt.Printf("verified public CLI surface version %d (%d commands)\n", index.CurrentVersion, len(live.Commands))
		return
	}

	if bytes.Equal(currentData, liveData) {
		check(fmt.Errorf("public CLI surface version %d is already current", index.CurrentVersion))
	}
	nextVersion := index.CurrentVersion + 1
	check(surface.ValidateTransition(index.CurrentVersion, nextVersion, current, live, policy.Changes))
	nextPath := versionPath(nextVersion)
	if _, err := os.Stat(nextPath); err == nil {
		check(fmt.Errorf("refusing to overwrite existing %s", nextPath))
	} else if !os.IsNotExist(err) {
		check(err)
	}
	check(os.WriteFile(nextPath, liveData, 0o644))
	index.CurrentVersion = nextVersion
	writeIndex(index)
	fmt.Printf("appended public CLI surface version %d; review %s and %s\n", nextVersion, nextPath, filepath.Join(surfaceDir, "breaking.json"))
}

func liveSurface() surface.Snapshot {
	snapshot, err := fs.ReadFile(talentocli.Content, "schemas/gateway.json")
	check(err)
	manifest, err := fs.ReadFile(talentocli.Content, "coverage/manifest.json")
	check(err)
	root, _, err := commands.NewRoot(snapshot, manifest, talentocli.Content)
	check(err)
	return surface.Build(root)
}

func initializeHistory(snapshot surface.Snapshot) {
	indexPath := filepath.Join(surfaceDir, "index.json")
	if _, err := os.Stat(indexPath); err == nil {
		check(fmt.Errorf("refusing to replace existing surface history at %s", indexPath))
	} else if !os.IsNotExist(err) {
		check(err)
	}
	check(os.MkdirAll(filepath.Join(surfaceDir, "versions"), 0o755))
	data, err := surface.Encode(snapshot)
	check(err)
	check(os.WriteFile(versionPath(1), data, 0o644))
	policy := surface.BreakPolicy{SchemaVersion: surface.SchemaVersion, Changes: []surface.BreakApproval{}}
	policyData, err := json.MarshalIndent(policy, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(surfaceDir, "breaking.json"), append(policyData, '\n'), 0o644))
	writeIndex(indexFile{SchemaVersion: surface.SchemaVersion, CurrentVersion: 1})
	fmt.Printf("initialized public CLI surface version 1 with %d commands\n", len(snapshot.Commands))
}

func loadHistory() (indexFile, []surface.Snapshot, surface.BreakPolicy) {
	indexData, err := os.ReadFile(filepath.Join(surfaceDir, "index.json"))
	check(err)
	var index indexFile
	check(json.Unmarshal(indexData, &index))
	if index.SchemaVersion != surface.SchemaVersion || index.CurrentVersion < 1 {
		check(fmt.Errorf("invalid surface index"))
	}
	policyData, err := os.ReadFile(filepath.Join(surfaceDir, "breaking.json"))
	check(err)
	policy, err := surface.ParseBreakPolicy(policyData)
	check(err)
	history := make([]surface.Snapshot, 0, index.CurrentVersion)
	for version := 1; version <= index.CurrentVersion; version++ {
		path := versionPath(version)
		data, err := os.ReadFile(path)
		check(err)
		snapshot, err := surface.Parse(data)
		check(err)
		canonical, err := surface.Encode(snapshot)
		check(err)
		if !bytes.Equal(data, canonical) {
			check(fmt.Errorf("%s is not canonical deterministic JSON", path))
		}
		history = append(history, snapshot)
	}
	return index, history, policy
}

func validateHistory(index indexFile, history []surface.Snapshot, policy surface.BreakPolicy, allowNext bool) {
	maximumVersion := index.CurrentVersion
	if allowNext {
		maximumVersion++
	}
	for _, approval := range policy.Changes {
		if approval.FromVersion < 1 || approval.ToVersion != approval.FromVersion+1 || approval.ToVersion > maximumVersion {
			check(fmt.Errorf("breaking approval %q references an invalid transition %d -> %d", approval.ChangeID, approval.FromVersion, approval.ToVersion))
		}
	}
	for version := 1; version < index.CurrentVersion; version++ {
		check(surface.ValidateTransition(version, version+1, history[version-1], history[version], policy.Changes))
	}
}

func writeIndex(index indexFile) {
	data, err := json.MarshalIndent(index, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(surfaceDir, "index.json"), append(data, '\n'), 0o644))
}

func versionPath(version int) string {
	return filepath.Join(surfaceDir, "versions", fmt.Sprintf("%04d.json", version))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
