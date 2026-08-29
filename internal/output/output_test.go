package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type humanValue struct {
	Value string `json:"value"`
}

func (v humanValue) HumanText() string { return "Human: " + v.Value }

type distinctMarkdownValue struct{ humanValue }

func (v distinctMarkdownValue) MarkdownText() string { return "Markdown: " + v.Value }

func TestStableJSONEnvelope(t *testing.T) {
	var stdout bytes.Buffer
	writer := New(Options{JSON: true, Writer: &stdout})
	if err := writer.Success(humanValue{Value: "ok"}, "Done.", nil, map[string]any{"state": "returned"}); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["ok"] != true || response["summary"] != "Done." {
		t.Fatalf("unexpected envelope: %#v", response)
	}
}

func TestAgentSuccessIsDataOnly(t *testing.T) {
	var stdout bytes.Buffer
	writer := New(Options{Agent: true, Writer: &stdout})
	if err := writer.Success(humanValue{Value: "ok"}, "ignored", nil, nil); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil || value["value"] != "ok" || value["ok"] != nil {
		t.Fatalf("agent output = %s, err = %v", stdout.String(), err)
	}
}

func TestAgentErrorIncludesRichData(t *testing.T) {
	var stderr bytes.Buffer
	writer := New(Options{Agent: true, ErrWriter: &stderr})
	err := fmt.Errorf("wrapped: %w", WithData(API("denied", errors.New("cause")), map[string]any{"state": "error"}))
	if writeErr := writer.Error(err); writeErr != nil {
		t.Fatal(writeErr)
	}
	if !strings.Contains(stderr.String(), `"state": "error"`) || !strings.Contains(stderr.String(), `"ok": false`) {
		t.Fatalf("structured error = %s", stderr.String())
	}
}

func TestBuiltInJQAndMarkdown(t *testing.T) {
	var jq bytes.Buffer
	writer := New(Options{JQ: ".data.value", Writer: &jq})
	if err := writer.Success(humanValue{Value: "filtered"}, "Done.", nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(jq.String()) != `"filtered"` {
		t.Fatalf("jq output = %s", jq.String())
	}
	var markdown bytes.Buffer
	writer = New(Options{Markdown: true, Writer: &markdown})
	if err := writer.Success(humanValue{Value: "shown"}, "Result ready.", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown.String(), "## Result ready") || !strings.Contains(markdown.String(), "Human: shown") {
		t.Fatalf("markdown output = %s", markdown.String())
	}
}

func TestMarkdownCanPreserveASeparateMachineFacingRepresentation(t *testing.T) {
	var markdown bytes.Buffer
	writer := New(Options{Markdown: true, Writer: &markdown})
	value := distinctMarkdownValue{humanValue{Value: "shown"}}
	if err := writer.Success(value, "Result ready.", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown.String(), "Markdown: shown") || strings.Contains(markdown.String(), "Human: shown") {
		t.Fatalf("markdown output = %s", markdown.String())
	}
}

func TestTerminalFacingOutputContainsUntrustedText(t *testing.T) {
	dangerous := "Café \x1b[31mred\x1b[0m \x1b]0;title\x07 invoice\u202efdp.exe pay\u200bpal \u009b31mend"
	want := "Human: Café red  invoicefdp.exe paypal end\n"

	for _, markdown := range []bool{false, true} {
		var stdout bytes.Buffer
		writer := New(Options{Markdown: markdown, Writer: &stdout})
		if err := writer.Success(humanValue{Value: dangerous}, "Safe summary.", nil, nil); err != nil {
			t.Fatal(err)
		}
		if markdown {
			if !strings.HasSuffix(stdout.String(), want) {
				t.Fatalf("Markdown output = %q, want safe body suffix %q", stdout.String(), want)
			}
		} else if stdout.String() != want {
			t.Fatalf("human output = %q, want %q", stdout.String(), want)
		}
		assertNoRawTerminalControls(t, stdout.String())
	}
}

func TestHumanErrorsContainMessageHintAndRenderedData(t *testing.T) {
	var stderr bytes.Buffer
	writer := New(Options{ErrWriter: &stderr})
	err := Usage("bad \x1b[2Jlabel\u202e\nforged", "try \x1b]0;title\x07again\u200b\tagain")
	if writeErr := writer.Error(err); writeErr != nil {
		t.Fatal(writeErr)
	}
	if got, want := stderr.String(), "Error: bad label forged\nHint: try again again\n"; got != want {
		t.Fatalf("error output = %q, want %q", got, want)
	}
	assertNoRawTerminalControls(t, stderr.String())

	stderr.Reset()
	err = WithRenderedData(API("rejected", nil), humanValue{Value: "bad \x1b[31mdata\u202e"})
	if writeErr := writer.Error(err); writeErr != nil {
		t.Fatal(writeErr)
	}
	if got, want := stderr.String(), "Human: bad data\n"; got != want {
		t.Fatalf("rendered error output = %q, want %q", got, want)
	}
}

func TestStructuredOutputLosslesslyEscapesUnsafeRunes(t *testing.T) {
	dangerous := "Café\x7f\u0085\u009b\u202e\u200b\u200d\U000e0061☕"
	tests := []struct {
		name    string
		options Options
		value   func(map[string]any) any
	}{
		{name: "json envelope", options: Options{JSON: true}, value: func(value map[string]any) any { return value["data"] }},
		{name: "agent data", options: Options{Agent: true}, value: func(value map[string]any) any { return value }},
		{name: "jq result", options: Options{JQ: ".data"}, value: func(value map[string]any) any { return value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			test.options.Writer = &stdout
			if err := New(test.options).Success(map[string]any{"value": dangerous}, "Done.", nil, nil); err != nil {
				t.Fatal(err)
			}
			assertNoRawTerminalControls(t, stdout.String())
			for _, escaped := range []string{`\u007f`, `\u0085`, `\u009b`, `\u202e`, `\u200b`, `\u200d`, `\udb40\udc61`} {
				if !strings.Contains(stdout.String(), escaped) {
					t.Errorf("output %q does not contain %s", stdout.String(), escaped)
				}
			}
			var decoded map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			data, ok := test.value(decoded).(map[string]any)
			if !ok || data["value"] != dangerous {
				t.Fatalf("decoded data = %#v, want exact value %q", data, dangerous)
			}
		})
	}
}

func TestStructuredErrorAndMarkdownJSONRemainLossless(t *testing.T) {
	dangerous := "bad\u009b\u202e\u200b\U000e0061"
	var stderr bytes.Buffer
	writer := New(Options{JSON: true, ErrWriter: &stderr})
	if err := writer.Error(WithData(API(dangerous, nil), map[string]any{"value": dangerous})); err != nil {
		t.Fatal(err)
	}
	assertNoRawTerminalControls(t, stderr.String())
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Message != dangerous || envelope.Data["value"] != dangerous {
		t.Fatalf("decoded error = %#v, want exact value %q", envelope, dangerous)
	}

	var markdown bytes.Buffer
	if err := New(Options{Markdown: true, Writer: &markdown}).Success(map[string]any{"value": dangerous}, "Result.", nil, nil); err != nil {
		t.Fatal(err)
	}
	assertNoRawTerminalControls(t, markdown.String())
	encoded := strings.TrimSuffix(strings.TrimPrefix(markdown.String(), "## Result\n\n```json\n"), "\n```\n")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("Markdown JSON is invalid: %v\n%s", err, markdown.String())
	}
	if decoded["value"] != dangerous {
		t.Fatalf("decoded Markdown value = %q, want %q", decoded["value"], dangerous)
	}
}

func assertNoRawTerminalControls(t *testing.T, value string) {
	t.Helper()
	for _, unsafe := range []rune{'\x1b', '\x7f', '\u0085', '\u009b', '\u202e', '\u200b', '\u200d', '\U000e0061'} {
		if strings.ContainsRune(value, unsafe) {
			t.Errorf("output %q contains raw unsafe rune U+%04X", value, unsafe)
		}
	}
}
