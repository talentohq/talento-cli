// Package surface captures and compares the stable public Cobra API.
package surface

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const SchemaVersion = 1

const argumentContractAnnotation = "talento.surface/argument-contract"

// Snapshot is a deterministic description of every visible command and flag.
type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	Commands      []Command `json:"commands"`
}

type Command struct {
	Path                string   `json:"path"`
	Name                string   `json:"name"`
	Kind                string   `json:"kind"`
	Use                 string   `json:"use"`
	ArgumentPattern     string   `json:"argument_pattern,omitempty"`
	ArgumentContract    string   `json:"argument_contract"`
	Aliases             []string `json:"aliases,omitempty"`
	ArgumentAliases     []string `json:"argument_aliases,omitempty"`
	ValidArguments      []string `json:"valid_arguments,omitempty"`
	GroupID             string   `json:"group_id,omitempty"`
	Runnable            bool     `json:"runnable"`
	Deprecated          string   `json:"deprecated,omitempty"`
	DisableFlagParsing  bool     `json:"disable_flag_parsing,omitempty"`
	TraverseChildren    bool     `json:"traverse_children,omitempty"`
	UnknownFlagsAllowed bool     `json:"unknown_flags_allowed,omitempty"`
	Flags               []Flag   `json:"flags,omitempty"`
}

type Flag struct {
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	Shorthand  string `json:"shorthand,omitempty"`
	Type       string `json:"type"`
	Default    string `json:"default"`
	NoOptValue string `json:"no_option_value,omitempty"`
	Required   bool   `json:"required"`
	Deprecated string `json:"deprecated,omitempty"`
	Hidden     bool   `json:"hidden"`
}

type BreakApproval struct {
	FromVersion  int    `json:"from_version"`
	ToVersion    int    `json:"to_version"`
	ChangeID     string `json:"change_id"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
	Reason       string `json:"reason"`
}

type BreakPolicy struct {
	SchemaVersion int             `json:"schema_version"`
	Changes       []BreakApproval `json:"changes"`
}

type Change struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Breaking     bool   `json:"breaking"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
}

// Build initializes Cobra's public help command and help flags before walking
// the tree. Hidden implementation commands are intentionally outside the
// public contract; hiding a previously visible command is therefore detected
// as a removal.
func Build(root *cobra.Command) Snapshot {
	root.InitDefaultHelpCmd()
	commands := make([]Command, 0)
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Hidden {
			return
		}
		command.InitDefaultHelpFlag()
		entry := Command{
			Path:                command.CommandPath(),
			Name:                command.Name(),
			Kind:                commandKind(command),
			Use:                 command.Use,
			ArgumentPattern:     argumentPattern(command.Use),
			ArgumentContract:    argumentContract(command),
			Aliases:             sortedCopy(command.Aliases),
			ArgumentAliases:     sortedCopy(command.ArgAliases),
			ValidArguments:      sortedCopy(command.ValidArgs),
			GroupID:             command.GroupID,
			Runnable:            command.Runnable(),
			Deprecated:          command.Deprecated,
			DisableFlagParsing:  command.DisableFlagParsing,
			TraverseChildren:    command.TraverseChildren,
			UnknownFlagsAllowed: command.FParseErrWhitelist.UnknownFlags,
		}
		appendFlags := func(flags *pflag.FlagSet, scope string) {
			flags.VisitAll(func(flag *pflag.Flag) {
				_, required := flag.Annotations[cobra.BashCompOneRequiredFlag]
				entry.Flags = append(entry.Flags, Flag{
					Name: flag.Name, Scope: scope, Shorthand: flag.Shorthand,
					Type: flag.Value.Type(), Default: flag.DefValue, NoOptValue: flag.NoOptDefVal,
					Required: required, Deprecated: flag.Deprecated, Hidden: flag.Hidden,
				})
			})
		}
		appendFlags(command.LocalNonPersistentFlags(), "local")
		appendFlags(command.PersistentFlags(), "persistent")
		appendFlags(command.InheritedFlags(), "inherited")
		sort.Slice(entry.Flags, func(i, j int) bool {
			if entry.Flags[i].Scope != entry.Flags[j].Scope {
				return entry.Flags[i].Scope < entry.Flags[j].Scope
			}
			return entry.Flags[i].Name < entry.Flags[j].Name
		})
		commands = append(commands, entry)
		children := append([]*cobra.Command(nil), command.Commands()...)
		sort.Slice(children, func(i, j int) bool { return children[i].CommandPath() < children[j].CommandPath() })
		for _, child := range children {
			walk(child)
		}
	}
	walk(root)
	return normalize(Snapshot{SchemaVersion: SchemaVersion, Commands: commands})
}

func Encode(snapshot Snapshot) ([]byte, error) {
	snapshot = normalize(snapshot)
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func Parse(data []byte) (Snapshot, error) {
	var snapshot Snapshot
	if err := decodeStrict(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.SchemaVersion != SchemaVersion {
		return Snapshot{}, fmt.Errorf("unsupported surface schema version %d", snapshot.SchemaVersion)
	}
	snapshot = normalize(snapshot)
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func ParseBreakPolicy(data []byte) (BreakPolicy, error) {
	var policy BreakPolicy
	if err := decodeStrict(data, &policy); err != nil {
		return BreakPolicy{}, err
	}
	if policy.SchemaVersion != SchemaVersion {
		return BreakPolicy{}, fmt.Errorf("unsupported breaking-policy schema version %d", policy.SchemaVersion)
	}
	return policy, nil
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func normalize(snapshot Snapshot) Snapshot {
	for index := range snapshot.Commands {
		command := &snapshot.Commands[index]
		command.Aliases = sortedCopy(command.Aliases)
		command.ArgumentAliases = sortedCopy(command.ArgumentAliases)
		command.ValidArguments = sortedCopy(command.ValidArguments)
		sort.Slice(command.Flags, func(i, j int) bool {
			if command.Flags[i].Scope != command.Flags[j].Scope {
				return command.Flags[i].Scope < command.Flags[j].Scope
			}
			return command.Flags[i].Name < command.Flags[j].Name
		})
	}
	sort.Slice(snapshot.Commands, func(i, j int) bool { return snapshot.Commands[i].Path < snapshot.Commands[j].Path })
	return snapshot
}

func validateSnapshot(snapshot Snapshot) error {
	paths := make(map[string]bool, len(snapshot.Commands))
	for _, command := range snapshot.Commands {
		if strings.TrimSpace(command.Path) == "" {
			return fmt.Errorf("surface command has an empty path")
		}
		if paths[command.Path] {
			return fmt.Errorf("duplicate surface command path %q", command.Path)
		}
		paths[command.Path] = true
		flags := make(map[string]bool, len(command.Flags))
		for _, flag := range command.Flags {
			key := flag.Scope + ":" + flag.Name
			if flags[key] {
				return fmt.Errorf("duplicate surface flag %q on %s", key, command.Path)
			}
			flags[key] = true
		}
	}
	return nil
}

// Diff reports every change. Additions are non-breaking. Removals and changes
// to an incumbent command, alias, argument contract, or flag require an exact
// approval record.
func Diff(before, after Snapshot) []Change {
	changes := make([]Change, 0)
	oldCommands := commandMap(before.Commands)
	newCommands := commandMap(after.Commands)
	paths := unionKeys(oldCommands, newCommands)
	for _, path := range paths {
		oldCommand, hadOld := oldCommands[path]
		newCommand, hasNew := newCommands[path]
		switch {
		case !hadOld:
			changes = append(changes, makeChange("command:"+path, "command-added", false, nil, newCommand))
		case !hasNew:
			changes = append(changes, makeChange("command:"+path, "command-removed", true, oldCommand, nil))
		default:
			changes = append(changes, diffCommand(oldCommand, newCommand)...)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	return changes
}

func ValidateTransition(fromVersion, toVersion int, before, after Snapshot, approvals []BreakApproval) error {
	if toVersion != fromVersion+1 {
		return fmt.Errorf("surface versions must be adjacent: %d -> %d", fromVersion, toVersion)
	}
	required := make(map[string]Change)
	for _, change := range Diff(before, after) {
		if change.Breaking {
			required[change.ID] = change
		}
	}
	seen := make(map[string]bool)
	for _, approval := range approvals {
		if approval.FromVersion != fromVersion || approval.ToVersion != toVersion {
			continue
		}
		if strings.TrimSpace(approval.Reason) == "" {
			return fmt.Errorf("breaking approval %q needs a reason", approval.ChangeID)
		}
		if seen[approval.ChangeID] {
			return fmt.Errorf("duplicate breaking approval %q for %d -> %d", approval.ChangeID, fromVersion, toVersion)
		}
		seen[approval.ChangeID] = true
		change, ok := required[approval.ChangeID]
		if !ok {
			return fmt.Errorf("breaking approval %q does not match a breaking change for %d -> %d", approval.ChangeID, fromVersion, toVersion)
		}
		if approval.BeforeSHA256 != change.BeforeSHA256 || approval.AfterSHA256 != change.AfterSHA256 {
			return fmt.Errorf("breaking approval %q fingerprints do not match the exact change", approval.ChangeID)
		}
	}
	missing := make([]string, 0)
	for id := range required {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("unapproved breaking surface changes: %s", strings.Join(missing, ", "))
	}
	return nil
}

func diffCommand(before, after Command) []Change {
	changes := make([]Change, 0)
	oldFlags, newFlags := flagMap(before.Flags), flagMap(after.Flags)
	for _, key := range unionKeys(oldFlags, newFlags) {
		oldFlag, hadOld := oldFlags[key]
		newFlag, hasNew := newFlags[key]
		id := "flag:" + before.Path + ":" + key
		switch {
		case !hadOld:
			changes = append(changes, makeChange(id, "flag-added", false, nil, newFlag))
		case !hasNew:
			changes = append(changes, makeChange(id, "flag-removed", true, oldFlag, nil))
		case !reflect.DeepEqual(oldFlag, newFlag):
			changes = append(changes, makeChange(id, "flag-changed", true, oldFlag, newFlag))
		}
	}
	prefix := "command:" + before.Path + ":"
	changes = append(changes, diffSet(prefix+"alias", "alias", before.Aliases, after.Aliases)...)
	changes = append(changes, diffSet(prefix+"argument-alias", "argument-alias", before.ArgumentAliases, after.ArgumentAliases)...)
	changes = append(changes, diffSet(prefix+"valid-argument", "valid-argument", before.ValidArguments, after.ValidArguments)...)
	changes = append(changes, fieldChange(prefix+"name", before.Name, after.Name, true)...)
	changes = append(changes, fieldChange(prefix+"kind", before.Kind, after.Kind, true)...)
	changes = append(changes, fieldChange(prefix+"use", before.Use, after.Use, true)...)
	changes = append(changes, fieldChange(prefix+"argument-pattern", before.ArgumentPattern, after.ArgumentPattern, true)...)
	changes = append(changes, fieldChange(prefix+"argument-contract", before.ArgumentContract, after.ArgumentContract, true)...)
	changes = append(changes, fieldChange(prefix+"group-id", before.GroupID, after.GroupID, false)...)
	changes = append(changes, fieldChange(prefix+"runnable", before.Runnable, after.Runnable, before.Runnable && !after.Runnable)...)
	changes = append(changes, fieldChange(prefix+"deprecated", before.Deprecated, after.Deprecated, true)...)
	changes = append(changes, fieldChange(prefix+"disable-flag-parsing", before.DisableFlagParsing, after.DisableFlagParsing, true)...)
	changes = append(changes, fieldChange(prefix+"traverse-children", before.TraverseChildren, after.TraverseChildren, true)...)
	changes = append(changes, fieldChange(prefix+"unknown-flags-allowed", before.UnknownFlagsAllowed, after.UnknownFlagsAllowed, true)...)
	return changes
}

func fieldChange(id string, before, after any, breaking bool) []Change {
	if reflect.DeepEqual(before, after) {
		return nil
	}
	return []Change{makeChange(id, "property-changed", breaking, before, after)}
}

func diffSet(prefix, kind string, before, after []string) []Change {
	oldValues := make(map[string]bool, len(before))
	newValues := make(map[string]bool, len(after))
	for _, value := range before {
		oldValues[value] = true
	}
	for _, value := range after {
		newValues[value] = true
	}
	changes := make([]Change, 0)
	for _, value := range unionKeys(oldValues, newValues) {
		if oldValues[value] == newValues[value] {
			continue
		}
		removed := oldValues[value]
		changeKind := kind + "-added"
		var oldValue, newValue any
		if removed {
			changeKind = kind + "-removed"
			oldValue = value
		} else {
			newValue = value
		}
		changes = append(changes, makeChange(prefix+":"+value, changeKind, removed, oldValue, newValue))
	}
	return changes
}

func makeChange(id, kind string, breaking bool, before, after any) Change {
	return Change{ID: id, Kind: kind, Breaking: breaking, BeforeSHA256: digest(before), AfterSHA256: digest(after)}
}

func digest(value any) string {
	if value == nil {
		return "absent"
	}
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func commandMap(commands []Command) map[string]Command {
	result := make(map[string]Command, len(commands))
	for _, command := range commands {
		result[command.Path] = command
	}
	return result
}

func flagMap(flags []Flag) map[string]Flag {
	result := make(map[string]Flag, len(flags))
	for _, flag := range flags {
		result[flag.Scope+":"+flag.Name] = flag
	}
	return result
}

func unionKeys[T any](left, right map[string]T) []string {
	seen := make(map[string]bool, len(left)+len(right))
	for key := range left {
		seen[key] = true
	}
	for key := range right {
		seen[key] = true
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedCopy(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copyOfValues := append([]string(nil), values...)
	sort.Strings(copyOfValues)
	return copyOfValues
}

func commandKind(command *cobra.Command) string {
	if command.IsAdditionalHelpTopicCommand() {
		return "help-topic"
	}
	if command.Name() == "help" && command.Parent() != nil {
		return "help"
	}
	return "command"
}

func argumentPattern(use string) string {
	parts := strings.SplitN(strings.TrimSpace(use), " ", 2)
	if len(parts) == 1 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func argumentContract(command *cobra.Command) string {
	if value := command.Annotations[argumentContractAnnotation]; value != "" {
		return value
	}
	if command.Args == nil {
		return "arbitrary"
	}
	name := runtime.FuncForPC(reflect.ValueOf(command.Args).Pointer()).Name()
	patternCount := len(strings.Fields(argumentPattern(command.Use)))
	switch {
	case strings.HasSuffix(name, ".NoArgs"):
		return "none"
	case strings.Contains(name, ".ExactArgs.func"):
		return probeArgumentContract(command, "exact", patternCount)
	case strings.Contains(name, ".MaximumNArgs.func"):
		return probeArgumentContract(command, "maximum", patternCount)
	case strings.Contains(name, ".MinimumNArgs.func"):
		return probeArgumentContract(command, "minimum", patternCount)
	case strings.Contains(name, ".RangeArgs.func"):
		return probeArgumentContract(command, "range", patternCount)
	case strings.HasSuffix(name, ".ArbitraryArgs"):
		return "arbitrary"
	case strings.HasSuffix(name, ".OnlyValidArgs"):
		return "valid-arguments-only"
	default:
		return "custom:" + name
	}
}

// PreserveArgumentContract records the current positional contract before a
// wrapper adds cross-cutting behavior such as structured error classification.
// The wrapper must not change which argument counts are accepted.
func PreserveArgumentContract(command *cobra.Command) {
	if command.Args == nil || command.Annotations[argumentContractAnnotation] != "" {
		return
	}
	if command.Annotations == nil {
		command.Annotations = make(map[string]string)
	}
	command.Annotations[argumentContractAnnotation] = argumentContract(command)
}

func probeArgumentContract(command *cobra.Command, kind string, fallback int) string {
	const probeLimit = 64
	accepted := make([]int, 0)
	for count := 0; count <= probeLimit; count++ {
		arguments := make([]string, count)
		for index := range arguments {
			arguments[index] = "surface-argument"
		}
		if command.Args(command, arguments) == nil {
			accepted = append(accepted, count)
		}
	}
	switch kind {
	case "exact":
		if len(accepted) == 1 {
			return fmt.Sprintf("exact:%d", accepted[0])
		}
	case "maximum":
		if len(accepted) > 0 && accepted[0] == 0 && accepted[len(accepted)-1] < probeLimit {
			return fmt.Sprintf("maximum:%d", accepted[len(accepted)-1])
		}
	case "minimum":
		if len(accepted) > 0 && accepted[len(accepted)-1] == probeLimit {
			return fmt.Sprintf("minimum:%d", accepted[0])
		}
	case "range":
		if len(accepted) > 0 && accepted[len(accepted)-1] < probeLimit {
			return fmt.Sprintf("range:%d:%d", accepted[0], accepted[len(accepted)-1])
		}
	}
	return fmt.Sprintf("%s:unresolved:%d", kind, fallback)
}
