package surface

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBreakingCommandAndFlagChangesFailWithoutExactApproval(t *testing.T) {
	baseline := fixtureSnapshot()
	tests := map[string]func(*cobra.Command){
		"deletion": func(root *cobra.Command) { root.RemoveCommand(find(root, "list")) },
		"rename":   func(root *cobra.Command) { find(root, "list").Use = "find <query>" },
		"type": func(root *cobra.Command) {
			command := find(root, "list")
			command.ResetFlags()
			command.Flags().String("limit", "10", "limit")
		},
		"default": func(root *cobra.Command) { find(root, "list").Flag("limit").DefValue = "20" },
		"alias":   func(root *cobra.Command) { find(root, "list").Aliases = nil },
		"args":    func(root *cobra.Command) { find(root, "list").Args = cobra.ExactArgs(2) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := fixtureRoot()
			mutate(root)
			current := Build(root)
			if err := ValidateTransition(1, 2, baseline, current, nil); err == nil || !strings.Contains(err.Error(), "unapproved") {
				t.Fatalf("ValidateTransition() error = %v", err)
			}
		})
	}
}

func TestAdditiveCommandsAndFlagsNeedNoBreakApproval(t *testing.T) {
	baseline := fixtureSnapshot()
	root := fixtureRoot()
	root.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, Run: func(*cobra.Command, []string) {}})
	find(root, "list").Flags().Bool("verbose", false, "verbose")
	if err := ValidateTransition(1, 2, baseline, Build(root), nil); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalIsNarrowAndCannotMaskAnotherChange(t *testing.T) {
	baseline := fixtureSnapshot()
	root := fixtureRoot()
	find(root, "list").Aliases = nil
	find(root, "list").Args = cobra.ExactArgs(2)
	current := Build(root)
	var first Change
	for _, change := range Diff(baseline, current) {
		if change.Breaking {
			first = change
			break
		}
	}
	approval := BreakApproval{FromVersion: 1, ToVersion: 2, ChangeID: first.ID, BeforeSHA256: first.BeforeSHA256, AfterSHA256: first.AfterSHA256, Reason: "reviewed"}
	if err := ValidateTransition(1, 2, baseline, current, []BreakApproval{approval}); err == nil || !strings.Contains(err.Error(), "unapproved") {
		t.Fatalf("one approval masked another change: %v", err)
	}
	approval.AfterSHA256 = strings.Repeat("0", 64)
	if err := ValidateTransition(1, 2, baseline, current, []BreakApproval{approval}); err == nil || !strings.Contains(err.Error(), "fingerprints") {
		t.Fatalf("stale fingerprint accepted: %v", err)
	}
}

func TestExactApprovalPermitsOnlyItsBreakingChange(t *testing.T) {
	baseline := fixtureSnapshot()
	root := fixtureRoot()
	find(root, "list").Flag("limit").DefValue = "20"
	current := Build(root)
	changes := Diff(baseline, current)
	if len(changes) != 1 || !changes[0].Breaking {
		t.Fatalf("changes = %#v", changes)
	}
	approval := BreakApproval{
		FromVersion: 1, ToVersion: 2, ChangeID: changes[0].ID,
		BeforeSHA256: changes[0].BeforeSHA256, AfterSHA256: changes[0].AfterSHA256,
		Reason: "reviewed default change",
	}
	if err := ValidateTransition(1, 2, baseline, current, []BreakApproval{approval}); err != nil {
		t.Fatal(err)
	}
	extra := approval
	extra.ChangeID = "flag:example list:local:other"
	if err := ValidateTransition(1, 2, baseline, current, []BreakApproval{approval, extra}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unused approval accepted: %v", err)
	}
}

func TestBuildCapturesVisibleScopesAndRequiredMarkersDeterministically(t *testing.T) {
	root := fixtureRoot()
	if err := find(root, "list").MarkFlagRequired("limit"); err != nil {
		t.Fatal(err)
	}
	first, err := Encode(Build(root))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(Build(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("surface encoding is nondeterministic")
	}
	text := string(first)
	for _, want := range []string{`"scope": "local"`, `"scope": "inherited"`, `"required": true`, `"type": "int"`, `"argument_contract": "exact:1"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("surface lacks %s:\n%s", want, text)
		}
	}
}

func TestParseRejectsUnknownFieldsAndDuplicatePaths(t *testing.T) {
	if _, err := Parse([]byte(`{"schema_version":1,"commands":[],"surprise":true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	duplicate := Snapshot{SchemaVersion: SchemaVersion, Commands: []Command{
		{Path: "example", Name: "example", Kind: "command", Use: "example", ArgumentContract: "none"},
		{Path: "example", Name: "example", Kind: "command", Use: "example", ArgumentContract: "none"},
	}}
	data, err := Encode(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate path error = %v", err)
	}
}

func TestPreservedArgumentContractSurvivesBehaviorWrapper(t *testing.T) {
	root := fixtureRoot()
	command := find(root, "list")
	baseline := Build(root)
	PreserveArgumentContract(command)
	original := command.Args
	command.Args = func(cmd *cobra.Command, args []string) error { return original(cmd, args) }
	if changes := Diff(baseline, Build(root)); len(changes) != 0 {
		t.Fatalf("behavior-only wrapper drifted surface: %#v", changes)
	}
}

func fixtureRoot() *cobra.Command {
	root := &cobra.Command{Use: "example", Args: cobra.NoArgs}
	root.PersistentFlags().String("profile", "", "profile")
	list := &cobra.Command{Use: "list <query>", Aliases: []string{"ls"}, Args: cobra.ExactArgs(1), Run: func(*cobra.Command, []string) {}}
	list.Flags().Int("limit", 10, "limit")
	root.AddCommand(list)
	return root
}

func fixtureSnapshot() Snapshot { return Build(fixtureRoot()) }

func find(root *cobra.Command, name string) *cobra.Command {
	for _, command := range root.Commands() {
		if command.Name() == name {
			return command
		}
	}
	panic("command not found: " + name)
}
