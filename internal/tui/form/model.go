// Package form contains a presentation-only JSON Schema argument editor. It
// never authenticates, calls a tool, submits a write, or applies schema defaults.
package form

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/schema"
	"github.com/talentohq/talento-cli/internal/terminal"
)

// MaxInputBytes matches the existing CLI's bounded JSON input. Oversized edits
// are rejected atomically, never truncated by the underlying widgets.
const MaxInputBytes = 8 << 20

// The upstream textarea has a hard logical-line ceiling. Check it ourselves so
// its internal insertion truncation can never silently alter a JSON draft.
const maxJSONLines = 10000

// Model is an argument draft. SetArguments starts a fresh, clean draft;
// Arguments returns a detached object only after complete schema validation.
type Model struct {
	input   schema.JSONSchema
	fields  []field
	extra   map[string]any
	focus   int
	item    int
	raw     bool
	rawOnly bool
	json    textarea.Model
	width   int
	height  int
	dirty   bool
	message string
}

// New creates a form without filling defaults or inventing required values.
// rawJSON locks live/unreviewed schemas to exact JSON-object input.
func New(input schema.JSONSchema, rawJSON bool) Model {
	editor := textarea.New()
	editor.CharLimit = 0
	editor.MaxHeight = 0
	editor.MaxWidth = 0
	editor.ShowLineNumbers = false
	editor.Prompt = ""
	editor.SetStyles(textarea.Styles{})
	editor.KeyMap.Paste.SetEnabled(false)
	editor.SetValue("{}")
	m := Model{input: input, json: editor, rawOnly: rawJSON || complexRoot(input), extra: map[string]any{}}
	m.raw = m.rawOnly
	names := make([]string, 0, len(input.Properties))
	required := map[string]bool{}
	for _, name := range input.Required {
		required[name] = true
	}
	for name := range input.Properties {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		property := input.Properties[name]
		m.fields = append(m.fields, field{name: name, property: property, required: required[name], kind: kindFor(property), scalar: newScalar(property)})
	}
	m.SetSize(80, 18)
	m.setFocus()
	return m
}

func complexRoot(input schema.JSONSchema) bool {
	data, _ := json.Marshal(input)
	var node map[string]any
	_ = json.Unmarshal(data, &node)
	for _, key := range []string{"$ref", "$dynamicRef", "oneOf", "anyOf", "allOf", "if", "then", "else", "not", "patternProperties", "dependentSchemas"} {
		if _, exists := node[key]; exists {
			return true
		}
	}
	return input.Type != "object"
}

// SetSize changes the available viewport without modifying the draft.
func (m *Model) SetSize(width, height int) {
	m.width, m.height = max(1, width), max(1, height)
	m.json.SetWidth(m.width)
	m.json.SetHeight(max(1, m.height-4))
	for index := range m.fields {
		m.fields[index].scalar.input.SetWidth(max(1, m.width-4))
		for item := range m.fields[index].items {
			m.fields[index].items[item].input.SetWidth(max(1, m.width-9))
		}
	}
}

// SetArguments accepts even a schema-invalid draft (e.g. after schema drift),
// but rejects non-JSON values without changing the existing form. It retains
// unknown properties; only Arguments performs full input validation.
func (m *Model) SetArguments(arguments map[string]any) error {
	if arguments == nil {
		arguments = map[string]any{}
	}
	value, err := cloneValue(arguments)
	if err != nil {
		return err
	}
	text, err := editorJSON(value)
	if err != nil {
		return err
	}
	next := New(m.input, m.rawOnly)
	next.raw = m.raw
	next.SetSize(m.width, m.height)
	next.loadArguments(value.(map[string]any))
	next.json.SetValue(text)
	next.setFocus()
	*m = next
	return nil
}

func (m *Model) loadArguments(arguments map[string]any) {
	m.extra = make(map[string]any, len(arguments))
	for key, value := range arguments {
		m.extra[key] = value
	}
	for index := range m.fields {
		f := &m.fields[index]
		value, exists := arguments[f.name]
		if !exists {
			// Preserve an omitted field's hidden draft during JSON conversion.
			f.state = omitted
			continue
		}
		f.setValue(value)
		delete(m.extra, f.name)
	}
	m.SetSize(m.width, m.height)
}

// Arguments validates the complete original schema, including constraints that
// cannot be represented by a typed control. Diagnostics never contain values.
func (m Model) Arguments() (map[string]any, error) {
	arguments, err := m.arguments()
	if err != nil {
		return nil, err
	}
	if err := app.NormalizeInputNumbers(arguments); err != nil {
		return nil, err
	}
	if err := m.input.ValidateInput(arguments); err != nil {
		return nil, err
	}
	return arguments, nil
}

func (m Model) arguments() (map[string]any, error) {
	if m.raw {
		value, err := decodeJSON(m.json.Value())
		arguments, ok := value.(map[string]any)
		if err != nil || !ok {
			return nil, fmt.Errorf("arguments must contain one valid JSON object with unique keys")
		}
		return arguments, nil
	}
	arguments := make(map[string]any, len(m.fields)+len(m.extra))
	for key, value := range m.extra {
		arguments[key] = value
	}
	for _, f := range m.fields {
		if f.state == omitted {
			continue
		}
		value, err := f.value()
		if err != nil {
			return nil, err
		}
		arguments[f.name] = value
	}
	copy, err := cloneValue(arguments)
	if err != nil {
		return nil, err
	}
	return copy.(map[string]any), nil
}

// Dirty includes invalid and currently omitted edits, so navigating away never
// silently loses an unfinished draft. Only SetArguments resets the baseline.
func (m Model) Dirty() bool { return m.dirty }

// JSONMode reports whether the full-object JSON editor is active.
func (m Model) JSONMode() bool { return m.raw }

// Update only edits draft state. Enter never emits an invocation/submission.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(size.Width, size.Height)
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+j", "f4":
			m.toggleJSON()
			return m, nil
		case "ctrl+s", "esc":
			return m, nil // Reserved for the containing screen.
		}
	}
	if m.raw {
		if !m.acceptInput(msg, true) {
			return m, nil
		}
		previous := m.json.Value()
		var command tea.Cmd
		m.json, command = m.json.Update(msg)
		if m.json.Value() != previous {
			if !safeText(m.json.Value(), true) {
				m.json.SetValue(previous)
				m.message = "Combined input contains unsafe controls; use escaped JSON. Edit was not applied."
				return m, nil
			}
			m.dirty, m.message = true, ""
		}
		return m, command
	}
	if len(m.fields) == 0 {
		return m, nil
	}
	f := &m.fields[m.focus]
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "tab", "shift+tab":
			delta := 1
			if key.String() == "shift+tab" {
				delta = -1
			}
			m.moveFocus(delta)
			return m, nil
		case "ctrl+o":
			switch f.state {
			case omitted:
				f.state = included
			case included:
				if nullable(f.property) {
					f.state = null
				} else {
					f.state = omitted
				}
			case null:
				f.state = omitted
			}
			m.dirty, m.message = true, ""
			m.item = 0
			m.setFocus()
			return m, nil
		case "ctrl+n":
			if f.kind == arrayEditor {
				if m.draftBytes()+64 > MaxInputBytes {
					m.message = "Arguments exceed the 8 MiB input limit; no item added."
					return m, nil
				}
				property, _ := f.property.ItemProperty()
				f.items = append(f.items, newScalar(property))
				f.state, m.item = included, len(f.items)-1
				m.dirty, m.message = true, ""
				m.SetSize(m.width, m.height)
				m.setFocus()
			}
			return m, nil
		case "ctrl+d":
			if f.kind == arrayEditor && f.state == included && len(f.items) > 0 {
				f.items = append(f.items[:m.item], f.items[m.item+1:]...)
				m.item = max(0, min(m.item, len(f.items)-1))
				m.dirty, m.message = true, ""
				m.setFocus()
			}
			return m, nil
		}
	}
	editor := m.activeEditor()
	if editor == nil {
		if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" {
			m.moveFocus(1)
		}
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+l":
			if f.kind == arrayEditor && nullable(editor.property) {
				editor.isNull = !editor.isNull
				m.dirty, m.message = true, ""
			}
			return m, nil
		case "enter", "left", "right", "space":
			if editor.kind == booleanEditor || editor.kind == enumEditor {
				if editor.kind == booleanEditor {
					if f.state == included && !editor.isNull {
						editor.boolean = !editor.boolean
					}
				} else {
					delta := 1
					if key.String() == "left" {
						delta = -1
					}
					if f.state == included && !editor.isNull {
						editor.choice = (editor.choice + len(editor.property.Enum) + delta) % len(editor.property.Enum)
					}
				}
				f.state, editor.isNull = included, false
				m.dirty, m.message = true, ""
				return m, nil
			}
			if key.String() == "enter" {
				m.moveFocus(1)
				return m, nil
			}
		}
	}
	if editor.kind == booleanEditor || editor.kind == enumEditor || !m.acceptInput(msg, false) {
		return m, nil
	}
	previous, position := editor.input.Value(), editor.input.Position()
	var command tea.Cmd
	editor.input, command = editor.input.Update(msg)
	if editor.input.Value() != previous {
		if !safeText(editor.input.Value(), false) {
			editor.input.SetValue(previous)
			editor.input.SetCursor(position)
			m.message = "Combined input contains unsafe controls; use escaped JSON. Edit was not applied."
			return m, nil
		}
		f.state, editor.isNull = included, false
		m.dirty, m.message = true, ""
	}
	return m, command
}

func (m *Model) acceptInput(msg tea.Msg, multiline bool) bool {
	var content string
	switch msg := msg.(type) {
	case tea.PasteMsg:
		content = msg.Content
	case tea.KeyPressMsg:
		content = msg.Text
		if multiline && (msg.String() == "enter" || msg.String() == "ctrl+m") {
			content = "\n"
		}
	default:
		return true
	}
	if len(content) > MaxInputBytes || m.draftBytes()+len(content) > MaxInputBytes {
		m.message = "Arguments exceed the 8 MiB input limit. Nothing was inserted."
		return false
	}
	if multiline && strings.Count(m.json.Value(), "\n")+strings.Count(content, "\n") >= maxJSONLines {
		m.message = "JSON exceeds the 10,000-line editor limit. Nothing was inserted."
		return false
	}
	if !safeText(content, multiline) {
		m.message = "Input contains controls or line breaks; use escaped JSON (F4/Ctrl+J). Nothing was inserted."
		return false
	}
	return true
}

func (m Model) draftBytes() int {
	if m.raw {
		return len(m.json.Value())
	}
	total := 0
	for _, f := range m.fields {
		total += len(f.name) + len(f.scalar.input.Value()) + 8
		for _, item := range f.items {
			total += len(item.input.Value()) + 64
		}
	}
	for name, value := range m.extra {
		total += len(name) + len(encodeJSON(value, false)) + 8
	}
	return total
}

func (m *Model) toggleJSON() {
	if m.rawOnly {
		m.message = "This schema uses exact JSON-object input."
		return
	}
	arguments, err := m.arguments()
	if err != nil {
		m.message = err.Error() + "; mode unchanged."
		return
	}
	if m.raw {
		m.loadArguments(arguments)
		m.raw = false
	} else {
		text, err := editorJSON(arguments)
		if err != nil {
			m.message = err.Error() + "; mode unchanged."
			return
		}
		m.json.SetValue(text)
		m.raw = true
	}
	m.message = ""
	m.setFocus()
}

func (m *Model) activeEditor() *scalar {
	if len(m.fields) == 0 {
		return nil
	}
	f := &m.fields[m.focus]
	if f.kind == arrayEditor {
		if f.state != included || len(f.items) == 0 {
			return nil
		}
		return &f.items[m.item]
	}
	return &f.scalar
}

func (m *Model) setFocus() {
	m.json.Blur()
	for index := range m.fields {
		m.fields[index].scalar.input.Blur()
		for item := range m.fields[index].items {
			m.fields[index].items[item].input.Blur()
		}
	}
	if m.raw {
		m.json.Focus()
	} else if editor := m.activeEditor(); editor != nil {
		editor.input.Focus()
	}
}

func (m *Model) moveFocus(delta int) {
	f := m.fields[m.focus]
	if f.kind == arrayEditor && f.state == included {
		if next := m.item + delta; next >= 0 && next < len(f.items) {
			m.item = next
			m.setFocus()
			return
		}
	}
	m.focus = (m.focus + len(m.fields) + delta) % len(m.fields)
	m.item = 0
	if delta < 0 {
		f := m.fields[m.focus]
		if f.kind == arrayEditor && f.state == included {
			m.item = max(0, len(f.items)-1)
		}
	}
	m.setFocus()
}

// View bounds both columns and rows using terminal display width, not bytes.
// Names/descriptions are sanitized before presentation. Editor contents are
// either safe literal text or escaped JSON and cannot inject terminal controls.
func (m Model) View() string {
	title := "Arguments"
	help := "Tab/Shift+Tab: fields · Ctrl+O: omit/value/null · F4/Ctrl+J: JSON · Ctrl+S: review · Esc: back"
	var body []string
	focusLine := 0
	if m.raw {
		title = "Arguments — exact JSON object"
		help = "F4/Ctrl+J: form · Ctrl+S: review · Esc: back"
		if m.rawOnly {
			help = "Ctrl+S: review · Esc: back"
		}
		body = strings.Split(m.json.View(), "\n")
	} else if len(m.fields) == 0 {
		body = []string{"No named arguments. F4/Ctrl+J edits the exact JSON object."}
	} else {
		title = fmt.Sprintf("Arguments — field %d/%d", m.focus+1, len(m.fields))
		for index, f := range m.fields {
			prefix := "  "
			if index == m.focus {
				prefix = "> "
			}
			required := ""
			if f.required {
				required = " *"
			}
			state := []string{"omitted", "included", "null"}[f.state]
			body = append(body, prefix+terminal.SanitizeLine(f.name)+required+" ["+state+"]")
			if index == m.focus {
				focusLine = len(body)
			}
			if f.kind == arrayEditor {
				if f.state != included || len(f.items) == 0 {
					body = append(body, "  []  Ctrl+N adds an item; Ctrl+O includes an empty array")
				} else {
					for item, editor := range f.items {
						marker := " "
						if index == m.focus && item == m.item {
							marker, focusLine = ">", len(body)
						}
						body = append(body, fmt.Sprintf("  %s[%d] %s", marker, item+1, scalarView(editor)))
					}
				}
				if index == m.focus {
					help = "Tab: next · Ctrl+N/D: add/remove item · Ctrl+L: item null · Ctrl+O: presence · F4/Ctrl+J: JSON · Ctrl+S: review · Esc: back"
				}
			} else {
				body = append(body, "  "+scalarView(f.scalar))
			}
			if index == m.focus {
				description := terminal.SanitizeLine(f.property.Description)
				if description != "" {
					lines := strings.Split(ansi.Hardwrap(description, max(1, m.width-2), true), "\n")
					for _, line := range lines[:min(2, len(lines))] {
						body = append(body, "  "+line)
					}
				}
				if f.property.Default != nil {
					body = append(body, "  Suggested default (not applied): "+encodeJSON(f.property.Default, false))
				}
			}
			body = append(body, "")
		}
		if len(m.extra) > 0 {
			title += fmt.Sprintf("; %d extra properties retained in JSON", len(m.extra))
		}
	}
	footer := strings.Split(ansi.Hardwrap(help, m.width, true), "\n")
	footer = footer[:min(len(footer), max(1, m.height/3))]
	if m.message != "" {
		footer = append([]string{terminal.SanitizeLine(m.message)}, footer...)
	}
	available := max(1, m.height-len(footer)-1)
	start := max(0, min(focusLine-available/2, len(body)-available))
	end := min(len(body), start+available)
	lines := append([]string{title}, body[start:end]...)
	lines = append(lines, footer...)
	if len(lines) > m.height {
		// On exceptionally small viewports prioritize the editor over chrome.
		first := max(0, min(focusLine, len(body)-1))
		lines = body[first:min(len(body), first+m.height)]
	}
	for index := range lines {
		lines[index] = ansi.Truncate(lines[index], m.width, "")
	}
	return strings.Join(lines, "\n")
}

func scalarView(editor scalar) string {
	if editor.isNull {
		return "null (Ctrl+L changes a nullable array item)"
	}
	switch editor.kind {
	case booleanEditor:
		return fmt.Sprintf("‹ %t ›  Enter/←/→ selects", editor.boolean)
	case enumEditor:
		return "‹ " + encodeJSON(editor.property.Enum[editor.choice], false) + " ›  Enter/←/→ selects"
	case jsonEditor:
		return "JSON " + editor.input.View()
	default:
		return editor.input.View()
	}
}
