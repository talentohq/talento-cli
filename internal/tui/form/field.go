package form

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"

	"charm.land/bubbles/v2/textinput"
	"github.com/talentohq/talento-cli/internal/schema"
	"github.com/talentohq/talento-cli/internal/terminal"
)

type presence uint8

const (
	omitted presence = iota
	included
	null
)

type editorKind uint8

const (
	textEditor editorKind = iota
	booleanEditor
	enumEditor
	jsonEditor
	arrayEditor
)

type scalar struct {
	property schema.Property
	kind     editorKind
	input    textinput.Model
	choice   int
	boolean  bool
	isNull   bool
}

type field struct {
	name     string
	property schema.Property
	required bool
	state    presence
	kind     editorKind
	scalar   scalar
	items    []scalar
}

func newScalar(property schema.Property) scalar {
	// The schema's convenience fields decode enum numbers as float64. Recover
	// exact enum values from its retained JSON before selecting any value.
	if data, err := json.Marshal(property); err == nil {
		if value, err := decodeJSON(string(data)); err == nil {
			if node, ok := value.(map[string]any); ok {
				if choices, ok := node["enum"].([]any); ok {
					property.Enum = choices
				}
			}
		}
	}
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 0
	// Terminal paste arrives as tea.PasteMsg and is checked before reaching an
	// editor. Disable the separate clipboard command, whose private reply would
	// otherwise bypass that containment boundary.
	input.KeyMap.Paste.SetEnabled(false)
	input.SetStyles(textinput.Styles{})
	kind := kindFor(property)
	if kind == arrayEditor {
		kind = jsonEditor
	}
	if kind == jsonEditor {
		input.Placeholder = "JSON value"
	} else {
		input.Placeholder = "Type to include; Ctrl+O includes an empty value"
	}
	return scalar{property: property, kind: kind, input: input}
}

func kindFor(property schema.Property) editorKind {
	data, _ := json.Marshal(property)
	var node map[string]any
	_ = json.Unmarshal(data, &node)
	for _, key := range []string{"$ref", "$dynamicRef", "oneOf", "anyOf", "allOf", "if", "then", "else", "not"} {
		if _, exists := node[key]; exists {
			return jsonEditor
		}
	}
	if len(property.Enum) > 0 {
		for _, option := range property.Enum {
			switch option.(type) {
			case nil, string, bool, float64, float32, int, int64, json.Number:
			default:
				return jsonEditor
			}
		}
		return enumEditor
	}
	switch scalarType(property) {
	case "string", "integer", "number":
		return textEditor
	case "boolean":
		return booleanEditor
	case "array":
		item, ok := property.ItemProperty()
		if ok && kindFor(item) != arrayEditor && kindFor(item) != jsonEditor {
			return arrayEditor
		}
	}
	return jsonEditor
}

// scalarType deliberately does not apply Property.PrimaryType's string
// fallback: an untyped property must retain its full JSON value space.
func scalarType(property schema.Property) string {
	var types []string
	switch value := property.Type.(type) {
	case string:
		types = []string{value}
	case []string:
		types = value
	case []any:
		for _, value := range value {
			if name, ok := value.(string); ok {
				types = append(types, name)
			}
		}
	}
	result := ""
	for _, name := range types {
		if name == "null" {
			continue
		}
		if result != "" && result != name {
			return ""
		}
		result = name
	}
	return result
}

func nullable(property schema.Property) bool {
	data, _ := json.Marshal(property)
	var node map[string]any
	_ = json.Unmarshal(data, &node)
	if node["type"] == "null" {
		return true
	}
	if types, ok := node["type"].([]any); ok {
		for _, value := range types {
			if value == "null" {
				return true
			}
		}
	}
	// An untyped schema may permit null, as may an enum or combinator. The
	// authoritative full-schema validator decides whether it is actually valid.
	return node["type"] == nil
}

func (s scalar) value(path string) (any, error) {
	if s.isNull {
		return nil, nil
	}
	switch s.kind {
	case booleanEditor:
		return s.boolean, nil
	case enumEditor:
		return cloneValue(s.property.Enum[s.choice])
	case jsonEditor:
		value, err := decodeJSON(s.input.Value())
		if err != nil {
			return nil, fmt.Errorf("input %s must contain one valid JSON value", safePath(path))
		}
		return value, nil
	default:
		if scalarType(s.property) == "string" {
			return s.input.Value(), nil
		}
		value, err := decodeJSON(s.input.Value())
		if _, ok := value.(json.Number); err != nil || !ok {
			return nil, fmt.Errorf("input %s must be a JSON number", safePath(path))
		}
		return value, nil
	}
}

func (s *scalar) setValue(value any) {
	if value == nil && nullable(s.property) {
		s.isNull = true
		return
	}
	s.isNull = false
	switch s.kind {
	case booleanEditor:
		if value, ok := value.(bool); ok {
			s.boolean = value
			return
		}
	case enumEditor:
		encoded, _ := json.Marshal(value)
		for index, option := range s.property.Enum {
			candidate, _ := json.Marshal(option)
			if string(encoded) == string(candidate) {
				s.choice = index
				return
			}
		}
	case textEditor:
		if scalarType(s.property) == "string" {
			if value, ok := value.(string); ok && safeText(value, false) {
				s.input.SetValue(value)
				return
			}
		} else if number, ok := value.(json.Number); ok {
			s.input.SetValue(string(number))
			return
		}
	}
	// Keep values which do not fit a typed control verbatim in a JSON editor.
	// This includes invalid drafts, multiline strings and terminal controls.
	s.kind = jsonEditor
	s.input.Placeholder = "JSON value"
	s.input.SetValue(encodeJSON(value, false))
}

func (f *field) setValue(value any) {
	f.state = included
	if value == nil {
		f.state = null
		return
	}
	f.kind = kindFor(f.property)
	f.scalar = newScalar(f.property)
	if f.kind == arrayEditor {
		if values, ok := value.([]any); ok {
			property, _ := f.property.ItemProperty()
			f.items = make([]scalar, len(values))
			for index, value := range values {
				f.items[index] = newScalar(property)
				f.items[index].setValue(value)
			}
			return
		}
		f.kind = jsonEditor
		f.scalar.kind = jsonEditor
	}
	f.scalar.setValue(value)
}

func (f field) value() (any, error) {
	if f.state == null {
		return nil, nil
	}
	if f.kind == arrayEditor {
		values := make([]any, 0, len(f.items))
		for index, editor := range f.items {
			value, err := editor.value(fmt.Sprintf("%s[%d]", f.name, index))
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}
	return f.scalar.value(f.name)
}

func decodeJSON(text string) (any, error) {
	if len(text) > MaxInputBytes {
		return nil, fmt.Errorf("arguments exceed the 8 MiB input limit")
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	value, err := readJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON value")
	}
	return value, nil
}

// Reject duplicate object keys instead of silently keeping the last value.
// This protects exact JSON editing from losing data during a mode conversion.
func readJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("expected an object key")
			}
			if _, exists := object[name]; exists {
				return nil, fmt.Errorf("duplicate object key")
			}
			object[name], err = readJSONValue(decoder)
			if err != nil {
				return nil, err
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("invalid JSON object")
		}
		return object, nil
	case '[':
		values := []any{}
		for decoder.More() {
			value, err := readJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("invalid JSON array")
		}
		return values, nil
	default:
		return nil, fmt.Errorf("invalid JSON delimiter")
	}
}

func cloneValue(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("arguments must contain JSON values")
	}
	return decodeJSON(string(data))
}

func editorJSON(value any) (string, error) {
	text := encodeJSON(value, true)
	if len(text) > MaxInputBytes || strings.Count(text, "\n") >= maxJSONLines {
		text = encodeJSON(value, false)
	}
	if len(text) > MaxInputBytes {
		return "", fmt.Errorf("escaped arguments exceed the 8 MiB input limit")
	}
	return text, nil
}

// JSON escapes retain exact data while making terminal-control and bidi runes
// visible. Unlike removing those runes, this does not change argument values.
func encodeJSON(value any, indent bool) string {
	var data []byte
	if indent {
		data, _ = json.MarshalIndent(value, "", "  ")
	} else {
		data, _ = json.Marshal(value)
	}
	var result strings.Builder
	for _, current := range string(data) {
		if current >= 0x7f || terminal.UnsafeRune(current) {
			if current <= 0xffff {
				fmt.Fprintf(&result, "\\u%04x", current)
			} else {
				hi, lo := utf16.EncodeRune(current)
				fmt.Fprintf(&result, "\\u%04x\\u%04x", hi, lo)
			}
		} else {
			result.WriteRune(current)
		}
	}
	return result.String()
}

func safeText(value string, multiline bool) bool {
	if terminal.Sanitize(value) != value || strings.ContainsRune(value, '\t') {
		return false
	}
	return multiline || !strings.ContainsRune(value, '\n')
}

func safePath(path string) string {
	return terminal.SanitizeLine(path)
}
