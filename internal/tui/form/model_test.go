package form

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	talentocli "github.com/talentohq/talento-cli"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/schema"
	"github.com/talentohq/talento-cli/internal/terminal"
)

var styleSequences = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

func assertSafeView(t *testing.T, view string) {
	t.Helper()
	plain := styleSequences.ReplaceAllString(view, "")
	if terminal.Sanitize(plain) != plain {
		t.Fatalf("unsafe terminal view: %q", view)
	}
}

func inputSchema(t *testing.T, source string) schema.JSONSchema {
	t.Helper()
	var input schema.JSONSchema
	if err := json.Unmarshal([]byte(source), &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func press(m Model, key string) Model {
	message := tea.KeyPressMsg{Code: rune(0)}
	switch key {
	case "tab":
		message.Code = tea.KeyTab
	case "shift+tab":
		message.Code, message.Mod = tea.KeyTab, tea.ModShift
	case "enter":
		message.Code = tea.KeyEnter
	case "esc":
		message.Code = tea.KeyEscape
	case "left":
		message.Code = tea.KeyLeft
	case "right":
		message.Code = tea.KeyRight
	case "backspace":
		message.Code = tea.KeyBackspace
	case "f4":
		message.Code = tea.KeyF4
	default:
		if strings.HasPrefix(key, "ctrl+") {
			message.Code, message.Mod = []rune(strings.TrimPrefix(key, "ctrl+"))[0], tea.ModCtrl
		} else {
			message.Text, message.Code = key, []rune(key)[0]
		}
	}
	m, _ = m.Update(message)
	return m
}

func paste(m Model, text string) Model {
	m, _ = m.Update(tea.PasteMsg{Content: text})
	return m
}

func focus(m Model, name string) Model {
	for index, f := range m.fields {
		if f.name == name {
			m.focus, m.item = index, 0
			m.setFocus()
			return m
		}
	}
	panic("unknown field: " + name)
}

func assertArguments(t *testing.T, m Model, expected string) {
	t.Helper()
	arguments, err := m.Arguments()
	if err != nil {
		t.Fatal(err)
	}
	want, err := decodeJSON(expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.NormalizeInputNumbers(want.(map[string]any)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("arguments = %#v, want %#v", arguments, want)
	}
}

func TestRequiredFirstAndDefaultsAreSuggestions(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"a":{"type":"string","default":"suggested"},"z":{"type":"integer"},"b":{"type":"boolean"}},"required":["z","b"]}`), false)
	var names []string
	for _, f := range m.fields {
		names = append(names, f.name)
		if f.state != omitted {
			t.Fatalf("new field %s was included", f.name)
		}
	}
	if strings.Join(names, ",") != "b,z,a" || m.Dirty() {
		t.Fatalf("order=%v dirty=%v", names, m.Dirty())
	}
	m = focus(m, "a")
	if !strings.Contains(m.View(), "Suggested default (not applied)") {
		t.Fatal("default suggestion missing")
	}
	if _, err := m.Arguments(); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing required error = %v", err)
	}
}

func TestPresenceDistinguishesAllEmptyValues(t *testing.T) {
	input := inputSchema(t, `{"type":"object","properties":{"text":{"type":"string"},"nullable":{"type":["string","null"]},"enabled":{"type":["boolean","null"]},"count":{"type":"integer"},"items":{"type":"array","items":{"type":"string"}}}}`)
	m := New(input, false)
	assertArguments(t, m, `{}`)
	m = press(focus(m, "text"), "ctrl+o")
	m = press(focus(m, "nullable"), "ctrl+o")
	m = press(m, "ctrl+o")
	m = press(focus(m, "enabled"), "ctrl+o")
	m = paste(focus(m, "count"), "0")
	m = press(focus(m, "items"), "ctrl+o")
	assertArguments(t, m, `{"text":"","nullable":null,"enabled":false,"count":0,"items":[]}`)
	m = press(focus(m, "enabled"), "ctrl+o")
	assertArguments(t, m, `{"text":"","nullable":null,"enabled":null,"count":0,"items":[]}`)
	m = press(m, "ctrl+o")
	assertArguments(t, m, `{"text":"","nullable":null,"count":0,"items":[]}`)
	m = press(focus(m, "text"), "ctrl+o")
	assertArguments(t, m, `{"nullable":null,"count":0,"items":[]}`)
	if !m.Dirty() {
		t.Fatal("presence edits are not marked dirty")
	}
}

func TestSelectorsAndTypingDoNotTriggerSubmission(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"enabled":{"type":"boolean"},"mode":{"type":"string","enum":["first","second"]},"text":{"type":"string"}}}`), false)
	m = press(focus(m, "enabled"), "enter")
	assertArguments(t, m, `{"enabled":false}`)
	m = press(m, "right")
	assertArguments(t, m, `{"enabled":true}`)
	m = press(focus(m, "mode"), "left")
	assertArguments(t, m, `{"enabled":true,"mode":"first"}`)
	m = press(m, "right")
	assertArguments(t, m, `{"enabled":true,"mode":"second"}`)
	m = press(m, "right")
	assertArguments(t, m, `{"enabled":true,"mode":"first"}`)
	m = focus(m, "text")
	m = press(press(m, "/"), "?")
	assertArguments(t, m, `{"enabled":true,"mode":"first","text":"/?"}`)
	for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: 's', Mod: tea.ModCtrl}, {Code: tea.KeyEscape}} {
		var command tea.Cmd
		m, command = m.Update(msg)
		if command != nil {
			t.Fatal("form emitted a command for reserved or submit-like key")
		}
	}
}

func TestNumericEnumsKeepExactSelectedValues(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"small":{"type":"integer","enum":[1,2]},"large":{"type":"integer","enum":[9007199254740993,9007199254740995]}}}`), false)
	m = press(focus(m, "small"), "enter")
	assertArguments(t, m, `{"small":1}`)
	m = press(m, "right")
	assertArguments(t, m, `{"small":2}`)
	m = press(focus(m, "large"), "enter")
	got, err := m.arguments()
	if err != nil || got["large"] != json.Number("9007199254740993") {
		t.Fatalf("large enum rounded before validation: %#v, %v", got, err)
	}
	m = press(m, "right")
	got, err = m.arguments()
	if err != nil || got["large"] != json.Number("9007199254740995") {
		t.Fatalf("large enum rounded before validation: %#v, %v", got, err)
	}
}

func TestActualSpaceKeySelectsBooleanAndEnumButTypesInText(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"enabled":{"type":"boolean"},"mode":{"type":"string","enum":["first","second"]},"text":{"type":"string"}}}`), false)
	space := tea.KeyPressMsg{Code: ' ', Text: " "}
	if space.String() != "space" {
		t.Fatalf("unexpected Bubble Tea space representation: %q", space.String())
	}
	m = focus(m, "enabled")
	m, _ = m.Update(space)
	assertArguments(t, m, `{"enabled":false}`)
	m, _ = m.Update(space)
	assertArguments(t, m, `{"enabled":true}`)
	m = focus(m, "mode")
	m, _ = m.Update(space)
	assertArguments(t, m, `{"enabled":true,"mode":"first"}`)
	m, _ = m.Update(space)
	assertArguments(t, m, `{"enabled":true,"mode":"second"}`)
	m = focus(m, "text")
	m, _ = m.Update(space)
	assertArguments(t, m, `{"enabled":true,"mode":"second","text":" "}`)
}

func TestScalarArraysAddRemoveNavigateAndNullItems(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"items":{"type":"array","items":{"type":["integer","null"]}},"next":{"type":"string"}}}`), false)
	m = press(m, "ctrl+n")
	m = paste(m, "0")
	m = press(m, "ctrl+n")
	m = paste(m, "2")
	assertArguments(t, m, `{"items":[0,2]}`)
	m = press(m, "shift+tab")
	if m.focus != 0 || m.item != 0 {
		t.Fatalf("backward focus = %d,%d", m.focus, m.item)
	}
	m = press(m, "ctrl+l")
	assertArguments(t, m, `{"items":[null,2]}`)
	m = press(m, "ctrl+l")
	m = press(m, "tab")
	m = press(m, "tab")
	if m.focus != 1 || m.item != 0 {
		t.Fatalf("next-field focus = %d,%d", m.focus, m.item)
	}
	m = press(m, "shift+tab")
	if m.focus != 0 || m.item != 1 {
		t.Fatalf("previous-field focus = %d,%d", m.focus, m.item)
	}
	m = press(m, "ctrl+d")
	m = press(m, "ctrl+d")
	assertArguments(t, m, `{"items":[]}`)
	m = press(m, "ctrl+d")
	assertArguments(t, m, `{"items":[]}`)
}

func TestArrayEnumsAndBooleans(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"booleans":{"type":"array","items":{"type":"boolean"}},"enums":{"type":"array","items":{"type":"string","enum":["a","b"]}},"strings":{"type":"array","items":{"type":"string"}}}}`), false)
	m = press(m, "ctrl+n")
	m = press(m, "enter")
	m = press(focus(m, "enums"), "ctrl+n")
	m = press(m, "right")
	m = press(focus(m, "strings"), "ctrl+n")
	assertArguments(t, m, `{"booleans":[true],"enums":["b"],"strings":[""]}`)
}

func TestJSONConversionRetainsExtrasAndOmittedDrafts(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"text":{"type":"string"}},"additionalProperties":true}`), false)
	if err := m.SetArguments(map[string]any{"extra": map[string]any{"nested": []any{false, nil, ""}}}); err != nil {
		t.Fatal(err)
	}
	m = paste(m, "draft")
	m = press(m, "ctrl+o")
	m = press(m, "ctrl+j")
	if !m.JSONMode() {
		t.Fatal("JSON mode not entered")
	}
	assertArguments(t, m, `{"extra":{"nested":[false,null,""]}}`)
	m = press(m, "ctrl+j")
	if m.JSONMode() || !strings.Contains(m.View(), "extra properties retained") {
		t.Fatalf("extra-field notice or form missing: %s", m.View())
	}
	m = press(m, "ctrl+o")
	assertArguments(t, m, `{"text":"draft","extra":{"nested":[false,null,""]}}`)
}

func TestInvalidDraftCannotBlocklesslyChangeMode(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"count":{"type":"integer"}}}`), false)
	m = paste(m, "private-invalid-number")
	m = press(m, "ctrl+j")
	if m.JSONMode() || m.fields[0].scalar.input.Value() != "private-invalid-number" {
		t.Fatal("invalid scalar draft lost during conversion")
	}
	if !strings.Contains(m.message, "mode unchanged") || strings.Contains(m.message, "private") {
		t.Fatalf("unexpected diagnostic: %s", m.message)
	}
	if err := m.SetArguments(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	m = press(m, "ctrl+j")
	m.json.SetValue(`{"count":`)
	m = press(m, "ctrl+j")
	if !m.JSONMode() || m.json.Value() != `{"count":` {
		t.Fatal("invalid JSON draft lost during conversion")
	}
}

func TestRawLiveSchemasAndComplexRootStayJSON(t *testing.T) {
	for _, source := range []string{
		`{"type":"object","oneOf":[{"required":["a"]},{"required":["b"]}],"properties":{"a":{"type":"string"},"b":{"type":"string"}}}`,
		`{"type":"object","allOf":[{"required":["a"]}],"properties":{"a":{"type":"string"}}}`,
		`{"type":"object","if":{"required":["a"]},"then":{"required":["b"]}}`,
		`{"type":"object","patternProperties":{"^x":{"type":"string"}}}`,
		`{}`,
	} {
		m := New(inputSchema(t, source), false)
		if !m.JSONMode() {
			t.Fatalf("complex root has typed form: %s", source)
		}
		m = press(m, "ctrl+j")
		if !m.JSONMode() {
			t.Fatal("complex root escaped JSON-only mode")
		}
	}
	m := New(inputSchema(t, `{"type":"object","properties":{"name":{"type":"integer"}},"additionalProperties":false}`), true)
	if err := m.SetArguments(map[string]any{"name": "previous-draft", "extra": false}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.json.Value(), "previous-draft") || m.Dirty() {
		t.Fatal("live schema discarded or dirtied draft")
	}
	m = press(m, "ctrl+j")
	if !m.JSONMode() || !strings.Contains(m.message, "exact JSON") {
		t.Fatal("live schema was allowed into typed mode")
	}
	if _, err := m.Arguments(); err == nil || strings.Contains(err.Error(), "previous-draft") {
		t.Fatalf("invalid live draft error = %v", err)
	}
}

func TestComplexAndUntypedPropertiesUseJSONEditors(t *testing.T) {
	input := inputSchema(t, `{"type":"object","properties":{"object":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]},"objects":{"type":"array","items":{"type":"object"}},"untyped":{},"choice":{"anyOf":[{"type":"integer"},{"type":"string"}]},"nullable":{"type":["number","string","null"]}}}`)
	m := New(input, false)
	for _, f := range m.fields {
		if f.scalar.kind != jsonEditor {
			t.Fatalf("%s has kind %d", f.name, f.scalar.kind)
		}
	}
	for name, text := range map[string]string{"object": `{"value":1}`, "objects": `[{"name":"x"}]`, "untyped": "false", "choice": "42", "nullable": `"string"`} {
		m = paste(focus(m, name), text)
	}
	assertArguments(t, m, `{"object":{"value":1},"objects":[{"name":"x"}],"untyped":false,"choice":42,"nullable":"string"}`)
}

func TestFullSchemaValidationAndValueFreeErrors(t *testing.T) {
	input := inputSchema(t, `{"type":"object","properties":{"count":{"type":"integer","minimum":1,"maximum":3},"name":{"type":"string","minLength":3,"pattern":"^[a-z]+$"},"tags":{"type":"array","items":{"type":"string"},"minItems":1,"uniqueItems":true},"nested":{"type":"object","properties":{"flag":{"type":"boolean"}},"required":["flag"],"additionalProperties":false}},"additionalProperties":false}`)
	for name, arguments := range map[string]map[string]any{
		"minimum":    {"count": 0},
		"maximum":    {"count": 4},
		"integer":    {"count": 1.5},
		"pattern":    {"name": "SECRET-PERSON"},
		"minItems":   {"tags": []any{}},
		"unique":     {"tags": []any{"SECRET-PERSON", "SECRET-PERSON"}},
		"nested":     {"nested": map[string]any{"flag": "SECRET-PERSON"}},
		"additional": {"other": "SECRET-PERSON"},
	} {
		t.Run(name, func(t *testing.T) {
			m := New(input, false)
			if err := m.SetArguments(arguments); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Arguments(); err == nil || strings.Contains(err.Error(), "SECRET-PERSON") {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestMalformedJSONObjectsAreRejectedWithoutEcho(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object"}`), true)
	for _, value := range []string{"", "null", "[]", "42", `{"private":"SECRET"`, `{} {}`, `{"a":1,"a":2}`, `{"nested":{"a":1,"a":2}}`} {
		m.json.SetValue(value)
		if _, err := m.Arguments(); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("JSON %q error = %v", value, err)
		}
	}
}

func TestSetArgumentsCopiesInvalidValuesAndRetainsPrecision(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"count":{"type":"integer"},"name":{"type":"string"},"items":{"type":"array","items":{"type":"integer"}}}}`), false)
	arguments := map[string]any{"count": json.Number("9007199254740993"), "name": "name", "items": []any{1}}
	if err := m.SetArguments(arguments); err != nil {
		t.Fatal(err)
	}
	arguments["name"] = "changed"
	arguments["items"].([]any)[0] = 3
	assertArguments(t, m, `{"count":9007199254740993,"name":"name","items":[1]}`)
	result, err := m.Arguments()
	if err != nil {
		t.Fatal(err)
	}
	result["items"].([]any)[0] = json.Number("8")
	assertArguments(t, m, `{"count":9007199254740993,"name":"name","items":[1]}`)
	if err := m.SetArguments(map[string]any{"count": math.Inf(1)}); err == nil {
		t.Fatal("non-JSON values accepted")
	}
	assertArguments(t, m, `{"count":9007199254740993,"name":"name","items":[1]}`)
	if err := m.SetArguments(map[string]any{"name": false, "items": "not-array"}); err != nil {
		t.Fatal(err)
	}
	got, err := m.arguments()
	if err != nil || got["name"] != false || got["items"] != "not-array" {
		t.Fatalf("invalid values lost: %v, %v", got, err)
	}
	if err := m.SetArguments(nil); err != nil || m.Dirty() {
		t.Fatalf("reset failed: %v", err)
	}
	assertArguments(t, m, `{}`)
}

func TestTerminalControlsPreservedAsEscapesButNeverExecuted(t *testing.T) {
	values := []string{"hello\nworld", "\x1b]52;c;secret\a", "\x1b[31mred", "name\u202ereversed", "\u200bhidden", "a" + strings.Repeat("\u0301", 12), "emoji 👩‍💻 🇪🇸"}
	for _, value := range values {
		m := New(inputSchema(t, `{"type":"object","properties":{"text":{"type":"string"}}}`), false)
		if err := m.SetArguments(map[string]any{"text": value}); err != nil {
			t.Fatal(err)
		}
		got, err := m.Arguments()
		if err != nil || got["text"] != value {
			t.Fatalf("value lost: %#v, %v", got, err)
		}
		view := ansi.Strip(m.View())
		assertSafeView(t, m.View())
		if terminal.Sanitize(view) != view {
			t.Fatalf("unsafe view: %q", view)
		}
		m = press(m, "ctrl+j")
		view = ansi.Strip(m.View())
		assertSafeView(t, m.View())
		if terminal.Sanitize(view) != view {
			t.Fatalf("unsafe JSON view: %q", view)
		}
		m = press(m, "ctrl+j")
		got, err = m.Arguments()
		if err != nil || got["text"] != value {
			t.Fatalf("value lost after toggling: %#v, %v", got, err)
		}
	}
}

func TestUnsafePastesAreAtomicAndDoNotDirtyForm(t *testing.T) {
	for _, content := range []string{"\x1b[31msecret", "a\x00b", "bidi\u202e", "line\nbreak", "tab\tvalue"} {
		m := New(inputSchema(t, `{"type":"object","properties":{"text":{"type":"string"}}}`), false)
		m = paste(m, content)
		assertArguments(t, m, `{}`)
		if m.Dirty() || !strings.Contains(m.message, "Nothing was inserted") || strings.Contains(m.message, "secret") {
			t.Fatalf("unsafe paste mutated or leaked: dirty=%v message=%s", m.Dirty(), m.message)
		}
		m = press(m, "safe")
		assertArguments(t, m, `{"text":"safe"}`)
	}
	m := New(inputSchema(t, `{"type":"object"}`), true)
	m.json.SetValue("")
	m = paste(m, "{\n  \"ok\": true\n}")
	assertArguments(t, m, `{"ok":true}`)
	m = paste(m, "\x1b[31msecret")
	assertArguments(t, m, `{"ok":true}`)
}

func TestCombiningControlsCannotBypassContainmentAcrossChunks(t *testing.T) {
	input := inputSchema(t, `{"type":"object","properties":{"text":{"type":"string"}}}`)
	for _, raw := range []bool{false, true} {
		m := New(input, raw)
		if raw {
			m.json.SetValue(`{"text":"a`)
			m.json.CursorEnd()
		} else {
			m = paste(m, "a")
		}
		for range 8 {
			m = paste(m, "\u0301")
		}
		before := m.View()
		m = paste(m, "\u0301")
		if !strings.Contains(m.message, "Edit was not applied") {
			t.Fatalf("split combining paste was accepted: %s", m.message)
		}
		assertSafeView(t, before)
		assertSafeView(t, m.View())
	}
}

func TestNumbersPreservePrecisionAndRejectUnsupportedRange(t *testing.T) {
	input := inputSchema(t, `{"type":"object","properties":{"number":{"type":"number"}}}`)
	for _, value := range []string{"9007199254740993.0", "18446744073709551615", "0.1", "1e3", "-5.25"} {
		m := New(input, true)
		m.json.SetValue(`{"number":` + value + `}`)
		assertArguments(t, m, `{"number":`+value+`}`)
	}
	for _, value := range []string{"18446744073709551616", "0.10000000000000000001", "1e99999999"} {
		m := New(input, true)
		m.json.SetValue(`{"number":` + value + `}`)
		if _, err := m.Arguments(); err == nil || strings.Contains(err.Error(), value) {
			t.Fatalf("unsupported number error = %v", err)
		}
	}
}

func TestInputLimitsRejectAtomicallyAndPreserveLoadedDraft(t *testing.T) {
	input := inputSchema(t, `{"type":"object","properties":{"text":{"type":"string"}}}`)
	for _, raw := range []bool{false, true} {
		m := New(input, raw)
		if err := m.SetArguments(map[string]any{"text": "original"}); err != nil {
			t.Fatal(err)
		}
		m = paste(m, strings.Repeat("x", MaxInputBytes+1))
		assertArguments(t, m, `{"text":"original"}`)
		if m.Dirty() || !strings.Contains(m.message, "8 MiB") {
			t.Fatalf("oversize paste dirty=%v message=%s", m.Dirty(), m.message)
		}
		if err := m.SetArguments(map[string]any{"text": strings.Repeat("x", MaxInputBytes)}); err == nil {
			t.Fatal("oversize arguments accepted")
		}
		assertArguments(t, m, `{"text":"original"}`)
	}
	m := New(input, true)
	m = paste(m, strings.Repeat("\n", maxJSONLines))
	assertArguments(t, m, `{}`)
	if !strings.Contains(m.message, "10,000-line") || m.Dirty() {
		t.Fatal("line ceiling silently truncated draft")
	}
	m.json.SetValue(strings.Repeat("\n", maxJSONLines-1))
	before := m.json.Value()
	m = press(m, "enter")
	if m.json.Value() != before {
		t.Fatal("newline bypassed line ceiling")
	}
}

func TestGeneratedLargeJSONCompactsInsteadOfTruncating(t *testing.T) {
	values := make([]any, maxJSONLines+1)
	for index := range values {
		values[index] = index
	}
	m := New(inputSchema(t, `{"type":"object"}`), true)
	if err := m.SetArguments(map[string]any{"items": values}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.json.Value(), "\n") {
		t.Fatal("oversized pretty JSON was not compacted")
	}
	got, err := m.Arguments()
	if err != nil || len(got["items"].([]any)) != len(values) {
		t.Fatalf("large JSON truncated: %v", err)
	}
}

func TestClipboardShortcutsDisabledAndF4Fallback(t *testing.T) {
	m := New(inputSchema(t, `{"type":"object","properties":{"text":{"type":"string"},"items":{"type":"array","items":{"type":"string"}}}}`), false)
	if m.json.KeyMap.Paste.Enabled() {
		t.Fatal("textarea clipboard shortcut enabled")
	}
	for _, f := range m.fields {
		if f.scalar.input.KeyMap.Paste.Enabled() {
			t.Fatal("textinput clipboard shortcut enabled")
		}
	}
	m = press(focus(m, "items"), "ctrl+n")
	if m.fields[m.focus].items[0].input.KeyMap.Paste.Enabled() {
		t.Fatal("array-item clipboard shortcut enabled")
	}
	m = press(m, "f4")
	if !m.JSONMode() {
		t.Fatal("F4 did not enter JSON mode")
	}
	m = press(m, "f4")
	if m.JSONMode() {
		t.Fatal("F4 did not restore form")
	}
}

func TestNamesDescriptionsAndDefaultsAreContained(t *testing.T) {
	name := "name\x1b[2J\u202e"
	m := New(schema.JSONSchema{Type: "object", Properties: map[string]schema.Property{name: {Type: "string", Description: "\x1b]52;c;c2VjcmV0\aDescription\u202e", Default: "\x1b[31mdefault"}}}, false)
	view := ansi.Strip(m.View())
	assertSafeView(t, m.View())
	if terminal.Sanitize(view) != view || !strings.Contains(view, "Description") {
		t.Fatalf("unsafe metadata view: %q", view)
	}
}

func TestResizeFocusScrollingAndDirtyState(t *testing.T) {
	properties := map[string]schema.Property{}
	for index := range 40 {
		properties[fmt.Sprintf("field_%02d_界", index)] = schema.Property{Type: "string", Description: strings.Repeat("long description ", 8)}
	}
	m := New(schema.JSONSchema{Type: "object", Properties: properties}, false)
	for _, width := range []int{80, 32, 12, 1, 0} {
		for _, height := range []int{24, 8, 2, 1, 0} {
			m.SetSize(width, height)
			for range 42 {
				m = press(m, "tab")
				view := m.View()
				if len(strings.Split(view, "\n")) > max(1, height) {
					t.Fatalf("height overflow (%d,%d): %q", width, height, view)
				}
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > max(1, width) {
						t.Fatalf("width overflow (%d,%d): %q", width, height, line)
					}
				}
			}
		}
	}
	if m.Dirty() {
		t.Fatal("navigation and resize dirtied draft")
	}
	m.SetSize(80, 24)
	m = focus(m, "field_39_界")
	if !strings.Contains(m.View(), "field_39_界") {
		t.Fatalf("focused field was scrolled out: %s", m.View())
	}
	m = paste(m, "👩‍💻界")
	if !m.Dirty() {
		t.Fatal("typing did not dirty form")
	}
}

func TestJSONResizeAndEmptyForms(t *testing.T) {
	for _, raw := range []bool{false, true} {
		m := New(schema.JSONSchema{Type: "object"}, raw)
		for _, size := range []tea.WindowSizeMsg{{Width: 1, Height: 1}, {Width: 80, Height: 24}, {Width: 10, Height: 3}} {
			m, _ = m.Update(size)
			for _, key := range []string{"tab", "shift+tab", "enter", "ctrl+o", "ctrl+n", "ctrl+d"} {
				m = press(m, key)
			}
			view := m.View()
			if len(strings.Split(view, "\n")) > size.Height {
				t.Fatalf("empty form height overflow: %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > size.Width {
					t.Fatalf("empty form width overflow: %q", line)
				}
			}
		}
	}
}

func TestNoColorFormHasNoColorSequences(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := New(inputSchema(t, `{"type":"object","properties":{"name":{"type":"string"}}}`), false)
	m = paste(m, "example")
	for _, view := range []string{m.View(), press(m, "ctrl+j").View()} {
		// Cursor reverse video is not a color. No editor style sets foreground
		// or background colors, so it also works before theme detection.
		if strings.Contains(view, "38;") || strings.Contains(view, "48;") || strings.Contains(view, "[37m") || strings.Contains(view, "[40m") {
			t.Fatalf("color sequence in NO_COLOR form: %q", view)
		}
	}
}

func TestEveryGatewayPropertyShapeRoundTrips(t *testing.T) {
	data, err := talentocli.Content.ReadFile("schemas/gateway.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := schema.ParseSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range snapshot.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			m := New(tool.InputSchema, false)
			values := map[string]any{}
			for name, property := range tool.InputSchema.Properties {
				data, _ := json.Marshal(property)
				var node map[string]any
				if err := json.Unmarshal(data, &node); err != nil {
					t.Fatal(err)
				}
				values[name] = sampleValue(node)
			}
			if err := m.SetArguments(values); err != nil {
				t.Fatal(err)
			}
			before, err := cloneValue(values)
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				got, err := m.arguments()
				if err != nil || !reflect.DeepEqual(got, before) {
					t.Fatalf("schema shape round-trip lost input: got=%#v want=%#v err=%v", got, before, err)
				}
				m = press(m, "ctrl+j")
			}
			if m.Dirty() {
				t.Fatal("mode switching dirtied loaded arguments")
			}
			// Compare with the sole authoritative validator rather than inventing
			// business-valid example inputs for each tool.
			_, gotError := m.Arguments()
			if err := app.NormalizeInputNumbers(before.(map[string]any)); err != nil {
				t.Fatal(err)
			}
			wantError := tool.InputSchema.ValidateInput(before)
			if (gotError == nil) != (wantError == nil) {
				t.Fatalf("validation diverged: form=%v schema=%v", gotError, wantError)
			}
		})
	}
}

func sampleValue(node map[string]any) any {
	if options, ok := node["enum"].([]any); ok && len(options) > 0 {
		return options[0]
	}
	typeName := node["type"]
	if types, ok := typeName.([]any); ok {
		typeName = types[0]
	}
	switch typeName {
	case "string":
		if node["format"] == "date" {
			return "2026-08-27"
		}
		return "Example"
	case "integer", "number":
		if minimum, exists := node["minimum"]; exists {
			return minimum
		}
		return 1
	case "boolean":
		return false
	case "array":
		if item, ok := node["items"].(map[string]any); ok {
			return []any{sampleValue(item)}
		}
		return []any{}
	case "object":
		object := map[string]any{}
		if properties, ok := node["properties"].(map[string]any); ok {
			for name, property := range properties {
				if property, ok := property.(map[string]any); ok {
					object[name] = sampleValue(property)
				}
			}
		}
		return object
	}
	return nil
}
