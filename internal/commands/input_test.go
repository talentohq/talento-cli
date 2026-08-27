package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	talentocli "github.com/talentohq/talento-cli"
	"github.com/talentohq/talento-cli/internal/app"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/schema"
)

const exhaustiveInputSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "properties":{
    "name":{"type":"string"},
    "mode":{"type":"string","enum":["safe","fast"]},
    "count":{"type":"integer","minimum":1},
    "ratio":{"type":"number"},
    "enabled":{"type":"boolean"},
    "tags":{"type":"array","items":{"type":"string"}},
    "states":{"type":"array","items":{"type":"string","enum":["open","closed"]}},
    "ids":{"type":"array","items":{"type":"integer"}},
    "records":{"type":"array","items":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}},
    "metadata":{"type":"object","properties":{"owner":{"type":"string"}},"required":["owner"],"additionalProperties":false},
    "nullable":{"type":["string","null"]},
    "choice":{"anyOf":[{"type":"string"},{"type":"integer"}]},
    "exclusive":{"oneOf":[{"type":"number"},{"type":"integer"}]}
  },
  "required":["name","mode"],
  "additionalProperties":false
}`

func TestSchemaInputMergesSourcesFlagsAndCompleteValidation(t *testing.T) {
	tool := inputTestTool(t, exhaustiveInputSchema)
	tests := []struct {
		name  string
		args  []string
		stdin string
		want  map[string]any
	}{
		{
			name: "raw input with explicit precedence",
			args: []string{"--input", `{"name":"raw","mode":"safe","count":2,"nullable":null}`, "--name", "flag", "--count", "3", "--ratio", "2", "--enabled=false"},
			want: map[string]any{"name": "flag", "mode": "safe", "count": int64(3), "ratio": float64(2), "enabled": false, "nullable": nil},
		},
		{
			name: "repeatable strings preserve commas",
			args: []string{"--name", "Ana", "--mode", "safe", "--tags-item", "one,two", "--tags-item", "three"},
			want: map[string]any{"name": "Ana", "mode": "safe", "tags": []any{"one,two", "three"}},
		},
		{
			name: "repeatable integers",
			args: []string{"--name", "Ana", "--mode", "safe", "--ids-item", "7", "--ids-item", "8"},
			want: map[string]any{"name": "Ana", "mode": "safe", "ids": []any{int64(7), int64(8)}},
		},
		{
			name: "empty array escape hatch",
			args: []string{"--name", "Ana", "--mode", "safe", "--tags", "[]"},
			want: map[string]any{"name": "Ana", "mode": "safe", "tags": []any{}},
		},
		{
			name: "complex JSON flags",
			args: []string{"--name", "Ana", "--mode", "safe", "--records", `[{"id":4}]`, "--metadata", `{"owner":"Jorge"}`},
			want: map[string]any{"name": "Ana", "mode": "safe", "records": []any{map[string]any{"id": float64(4)}}, "metadata": map[string]any{"owner": "Jorge"}},
		},
		{
			name:  "stdin",
			args:  []string{"--input-file", "-", "--mode", "fast"},
			stdin: `{"name":"stdin","mode":"safe"}`,
			want:  map[string]any{"name": "stdin", "mode": "fast"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, input := inputTestCommand(t, tool, test.args)
			command.SetIn(strings.NewReader(test.stdin))
			arguments, err := input.arguments(command, tool)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(arguments, test.want) {
				t.Fatalf("arguments = %#v, want %#v", arguments, test.want)
			}
		})
	}
}

func TestSchemaInputReadsFileAndRejectsInvalidSources(t *testing.T) {
	tool := inputTestTool(t, exhaustiveInputSchema)
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(`{"name":"file","mode":"safe"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	command, input := inputTestCommand(t, tool, []string{"--input-file", path})
	arguments, err := input.arguments(command, tool)
	if err != nil || arguments["name"] != "file" {
		t.Fatalf("file arguments = %#v, err = %v", arguments, err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "mutually exclusive sources", args: []string{"--input", `{}`, "--input-file", path}, want: "mutually exclusive"},
		{name: "malformed", args: []string{"--input", `{"name":`}, want: "cannot parse --input as JSON"},
		{name: "array top level", args: []string{"--input", `[]`}, want: "must be a JSON object"},
		{name: "null top level", args: []string{"--input", `null`}, want: "must be a JSON object"},
		{name: "trailing value", args: []string{"--input", `{} {}`}, want: "exactly one JSON value"},
		{name: "array flag plus item flag", args: []string{"--input", `{"name":"Ana","mode":"safe"}`, "--tags", `[]`, "--tags-item", "x"}, want: "mutually exclusive"},
		{name: "invalid integer", args: []string{"--name", "Ana", "--mode", "safe", "--count", "1.5"}, want: "must be an integer"},
		{name: "invalid complex array", args: []string{"--name", "Ana", "--mode", "safe", "--records", `{}`}, want: "must be a JSON array"},
		{name: "unknown property", args: []string{"--input", `{"name":"Ana","mode":"safe","secret":"must-not-leak"}`}, want: "unsupported properties: secret"},
		{name: "enum", args: []string{"--name", "Ana", "--mode", "must-not-leak"}, want: `must be one of "safe", "fast"`},
		{name: "array item enum", args: []string{"--name", "Ana", "--mode", "safe", "--states-item", "must-not-leak"}, want: `input $.states[0] must be one of "open", "closed"`},
		{name: "nested path", args: []string{"--name", "Ana", "--mode", "safe", "--metadata", `{}`}, want: "input $.metadata is missing required properties: owner"},
		{name: "numeric minimum", args: []string{"--name", "Ana", "--mode", "safe", "--count", "0"}, want: "input $.count must be at least 1"},
		{name: "integer rejects number", args: []string{"--input", `{"name":"Ana","mode":"safe","count":1.5}`}, want: "input $.count must be an integer"},
		{name: "any of", args: []string{"--input", `{"name":"Ana","mode":"safe","choice":true}`}, want: "input $.choice must match at least one allowed shape"},
		{name: "one of", args: []string{"--input", `{"name":"Ana","mode":"safe","exclusive":1}`}, want: "input $.exclusive must match exactly one allowed shape"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, input := inputTestCommand(t, tool, test.args)
			_, err := input.arguments(command, tool)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("error leaked rejected input: %v", err)
			}
			if clioutput.ExitCode(err) != 1 {
				t.Fatalf("exit code = %d, want usage 1 (%v)", clioutput.ExitCode(err), err)
			}
		})
	}
}

func TestEnumFlagCompletionForScalarAndArrayItems(t *testing.T) {
	tool := inputTestTool(t, exhaustiveInputSchema)
	command, _ := inputTestCommand(t, tool, nil)
	tests := []struct {
		flag string
		want []string
	}{
		{flag: "mode", want: []string{"safe", "fast"}},
		{flag: "states-item", want: []string{"open", "closed"}},
	}
	for _, test := range tests {
		completion, ok := command.GetFlagCompletionFunc(test.flag)
		if !ok {
			t.Fatalf("no completion registered for --%s", test.flag)
		}
		values, directive := completion(command, nil, "")
		if !reflect.DeepEqual(values, test.want) || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
			t.Fatalf("--%s completion = %#v, %v", test.flag, values, directive)
		}
	}
}

func TestRequiredInputsCanComeFromJSONAndAreOnlyDocumented(t *testing.T) {
	tool := inputTestTool(t, exhaustiveInputSchema)
	command, input := inputTestCommand(t, tool, []string{"--input", `{"name":"Ana","mode":"safe"}`})
	if _, required := command.Flags().Lookup("name").Annotations[cobra.BashCompOneRequiredFlag]; required {
		t.Fatal("schema-required flag was marked Cobra-required and would reject --input")
	}
	if !strings.Contains(command.Flags().Lookup("name").Usage, "required; may be supplied by --input") {
		t.Fatalf("required help = %q", command.Flags().Lookup("name").Usage)
	}
	if _, err := input.arguments(command, tool); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidInputNeverReachesToolExecution(t *testing.T) {
	tool := inputTestTool(t, exhaustiveInputSchema)
	command, input := inputTestCommand(t, tool, []string{"--name", "Ana", "--mode", "invalid-secret"})
	calls := 0
	_, err := executeValidatedTool(context.Background(), command, input, tool, func(context.Context, string, map[string]any) (*app.ToolExecution, error) {
		calls++
		return &app.ToolExecution{}, nil
	})
	if err == nil || calls != 0 {
		t.Fatalf("error = %v, execution calls = %d; invalid input reached execution seam", err, calls)
	}
}

func TestRootPreflightValidatesAndCachesStdinBeforeAuthentication(t *testing.T) {
	tool := inputTestTool(t, exhaustiveInputSchema)
	command, input := inputTestCommand(t, tool, []string{"--input-file", "-"})
	command.Annotations = map[string]string{schemaToolAnnotation: tool.Name}
	command.SetIn(strings.NewReader(`{"name":"Ana","mode":"safe"}`))
	talento := &app.App{Snapshot: schema.Snapshot{Tools: []schema.Tool{tool}}}
	if err := preflightSchemaArguments(command, nil, talento); err != nil {
		t.Fatal(err)
	}
	// A second read would see EOF; validatedArguments must use the preflight
	// result cached before the authentication/network gate.
	command.SetIn(strings.NewReader(""))
	arguments, err := validatedArguments(command, input, tool)
	if err != nil || arguments["name"] != "Ana" {
		t.Fatalf("cached arguments = %#v, err = %v", arguments, err)
	}
}

func TestValidationErrorsUseSafeHumanAndStructuredUsageOutput(t *testing.T) {
	tool := inputTestTool(t, exhaustiveInputSchema)
	command, input := inputTestCommand(t, tool, []string{"--name", "Ana", "--mode", "secret\x1b[2Jvalue"})
	_, err := input.arguments(command, tool)
	if err == nil {
		t.Fatal("expected validation error")
	}

	var human bytes.Buffer
	if writeErr := clioutput.New(clioutput.Options{ErrWriter: &human}).Error(err); writeErr != nil {
		t.Fatal(writeErr)
	}
	if strings.Contains(human.String(), "secret") || strings.Contains(human.String(), "\x1b") || !strings.Contains(human.String(), "Error: input $.mode") {
		t.Fatalf("human error = %q", human.String())
	}

	var structured bytes.Buffer
	if writeErr := clioutput.New(clioutput.Options{JSON: true, ErrWriter: &structured}).Error(err); writeErr != nil {
		t.Fatal(writeErr)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Data schema.ValidationError `json:"data"`
	}
	if decodeErr := json.Unmarshal(structured.Bytes(), &envelope); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if envelope.Error.Code != "usage" || envelope.Data.Path != "$.mode" || envelope.Data.Constraint != "enum" {
		t.Fatalf("structured error = %s", structured.String())
	}
	if strings.Contains(structured.String(), "secret") {
		t.Fatalf("structured error leaked input: %s", structured.String())
	}
}

func TestReviewedSnapshotFlagShapesAreFullyRecognized(t *testing.T) {
	data, err := fs.ReadFile(talentocli.Content, "schemas/gateway.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := schema.ParseSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	properties := 0
	scalarArrays := 0
	for _, tool := range snapshot.Tools {
		for name, property := range tool.InputSchema.Properties {
			properties++
			switch property.PrimaryType() {
			case "string", "integer", "number", "boolean", "object":
			case "array":
				item, ok := property.ItemProperty()
				if !ok {
					t.Fatalf("%s.%s has an unrecognized array item schema", tool.Name, name)
				}
				switch item.PrimaryType() {
				case "string", "integer", "number", "boolean":
					scalarArrays++
				case "object":
				default:
					t.Fatalf("%s.%s has unrecognized array item type %q", tool.Name, name, item.PrimaryType())
				}
			default:
				t.Fatalf("%s.%s has unrecognized type %q", tool.Name, name, property.PrimaryType())
			}
		}
	}
	if len(snapshot.Tools) != 151 || properties != 826 || scalarArrays != 19 {
		t.Fatalf("audited tools=%d properties=%d scalar arrays=%d", len(snapshot.Tools), properties, scalarArrays)
	}
}

func inputTestTool(t *testing.T, inputSchema string) schema.Tool {
	t.Helper()
	var tool schema.Tool
	data := []byte(`{"name":"test_tool","inputSchema":` + inputSchema + `}`)
	if err := json.Unmarshal(data, &tool); err != nil {
		t.Fatal(err)
	}
	if err := tool.InputSchema.CompileInputSchema(); err != nil {
		t.Fatal(err)
	}
	return tool
}

func inputTestCommand(t *testing.T, tool schema.Tool, args []string) (*cobra.Command, *schemaInput) {
	t.Helper()
	command := &cobra.Command{Use: "test"}
	input := addSchemaFlags(command, tool)
	if err := command.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return command, input
}
