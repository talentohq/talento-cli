package schema

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestReviewedSnapshotCoverage(t *testing.T) {
	snapshotData, err := os.ReadFile("../../schemas/gateway.json")
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile("../../coverage/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ParseSnapshot(snapshotData)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCoverage(snapshot, snapshotData, manifest); err != nil {
		t.Fatal(err)
	}
	if got, want := len(snapshot.Tools), 151; got != want {
		t.Fatalf("tools = %d, want %d", got, want)
	}
	if got, want := len(snapshot.Resources), 17; got != want {
		t.Fatalf("resources = %d, want %d", got, want)
	}
}

func TestCoverageRejectsStaleSnapshot(t *testing.T) {
	snapshotData, _ := os.ReadFile("../../schemas/gateway.json")
	manifestData, _ := os.ReadFile("../../coverage/manifest.json")
	snapshot, _ := ParseSnapshot(snapshotData)
	manifest, _ := ParseManifest(manifestData)
	manifest.SnapshotDigest = "stale"
	if err := ValidateCoverage(snapshot, snapshotData, manifest); err == nil {
		t.Fatal("expected stale manifest error")
	}
}

func TestPropertyPrimaryTypeAllowsNullableValues(t *testing.T) {
	property := Property{Type: []any{"null", "integer"}}
	if got := property.PrimaryType(); got != "integer" {
		t.Fatalf("PrimaryType = %q", got)
	}
}

func TestCompleteInputSchemaIsPreservedCompiledAndValidated(t *testing.T) {
	data := []byte(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "properties":{
    "mode":{"type":"string","enum":["safe","fast"]},
    "progress":{"type":"integer","minimum":0,"maximum":100},
    "nested":{"type":"object","properties":{"code":{"type":"string","minLength":2}},"required":["code"],"additionalProperties":false},
    "tags":{"type":"array","items":{"type":"string","enum":["a","b"]},"minItems":1,"uniqueItems":true},
    "nullable":{"type":["string","null"]},
    "choice":{"anyOf":[{"type":"string"},{"type":"integer"}]},
    "exclusive":{"oneOf":[{"type":"number"},{"type":"integer"}]}
  },
  "required":["mode","nested"],
  "additionalProperties":false
}`)
	var inputSchema JSONSchema
	if err := json.Unmarshal(data, &inputSchema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inputSchema.raw), `"minimum":0`) || !strings.Contains(string(inputSchema.Properties["nested"].raw), `"additionalProperties":false`) {
		t.Fatalf("complete schema was not preserved: %s", inputSchema.raw)
	}
	roundTrip, err := json.Marshal(inputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, keyword := range []string{`"minimum":0`, `"minLength":2`, `"uniqueItems":true`, `"anyOf"`, `"oneOf"`} {
		if !strings.Contains(string(roundTrip), keyword) {
			t.Fatalf("round-tripped schema lacks %s: %s", keyword, roundTrip)
		}
	}
	if err := inputSchema.CompileInputSchema(); err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{
		"mode": "safe", "progress": int64(25), "nested": map[string]any{"code": "ok"},
		"tags": []any{"a"}, "nullable": nil, "choice": "text", "exclusive": 1.5,
	}
	if err := inputSchema.ValidateInput(valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	tests := []struct {
		name       string
		mutate     func(map[string]any)
		path       string
		constraint string
		secret     string
	}{
		{name: "enum", mutate: func(v map[string]any) { v["mode"] = "do-not-leak" }, path: "$.mode", constraint: "enum", secret: "do-not-leak"},
		{name: "numeric bound", mutate: func(v map[string]any) { v["progress"] = -1 }, path: "$.progress", constraint: "minimum"},
		{name: "nested required", mutate: func(v map[string]any) { v["nested"] = map[string]any{} }, path: "$.nested", constraint: "required"},
		{name: "nested additional property", mutate: func(v map[string]any) { v["nested"] = map[string]any{"code": "ok", "secret_field": "do-not-leak"} }, path: "$.nested", constraint: "additionalProperties", secret: "do-not-leak"},
		{name: "array item", mutate: func(v map[string]any) { v["tags"] = []any{"nope"} }, path: "$.tags[0]", constraint: "enum", secret: "nope"},
		{name: "unique array", mutate: func(v map[string]any) { v["tags"] = []any{"a", "a"} }, path: "$.tags", constraint: "uniqueItems"},
		{name: "any of", mutate: func(v map[string]any) { v["choice"] = true }, path: "$.choice", constraint: "anyOf"},
		{name: "one of", mutate: func(v map[string]any) { v["exclusive"] = 1 }, path: "$.exclusive", constraint: "oneOf"},
		{name: "root additional property", mutate: func(v map[string]any) { v["unknown"] = true }, path: "$", constraint: "additionalProperties"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneJSONMap(t, valid)
			test.mutate(candidate)
			err := inputSchema.ValidateInput(candidate)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %T %v, want ValidationError", err, err)
			}
			if validationErr.Path != test.path || validationErr.Constraint != test.constraint {
				t.Fatalf("validation error = %#v, want path %q constraint %q (raw: %v)", validationErr, test.path, test.constraint, err)
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("diagnostic leaked input value %q: %v", test.secret, err)
			}
		})
	}
}

func TestValidationDiagnosticsUseDeterministicPropertyOrder(t *testing.T) {
	var inputSchema JSONSchema
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"z":{"type":"integer"},"a":{"type":"integer"}},"additionalProperties":false}`), &inputSchema); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		err := inputSchema.ValidateInput(map[string]any{"z": "wrong", "a": "wrong"})
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) || validationErr.Path != "$.a" {
			t.Fatalf("diagnostic = %#v (%v), want first sorted path $.a", validationErr, err)
		}
	}
}

func TestIntegerAndNumberRemainDistinct(t *testing.T) {
	var inputSchema JSONSchema
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"integer":{"type":"integer"},"number":{"type":"number"}},"additionalProperties":false}`), &inputSchema); err != nil {
		t.Fatal(err)
	}
	if err := inputSchema.ValidateInput(map[string]any{"integer": 2, "number": 2.5}); err != nil {
		t.Fatal(err)
	}
	err := inputSchema.ValidateInput(map[string]any{"integer": 2.5, "number": 2})
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Path != "$.integer" || validationErr.Constraint != "type" {
		t.Fatalf("integer diagnostic = %#v (%v)", validationErr, err)
	}
}

func cloneJSONMap(t *testing.T, source map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
