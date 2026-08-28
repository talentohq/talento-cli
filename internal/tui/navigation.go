package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/schema"
	"github.com/yosida95/uritemplate/v3"
)

type entryKind uint8

const (
	toolEntry entryKind = iota
	resourceEntry
	templateEntry
)

type entry struct {
	id, title, description, group string
	kind                          entryKind
	tool                          app.SessionTool
	uri                           string
}

func (e entry) label() string {
	if e.kind != toolEntry {
		return e.title
	}
	if e.tool.Reviewed && e.tool.Domain != "" && e.tool.Command != "" {
		return e.tool.Domain + " " + e.tool.Command
	}
	return e.title
}

func (e entry) badge() string {
	if e.kind != toolEntry || e.tool.ReadOnly {
		return "READ"
	}
	if e.tool.Destructive {
		return "HIGH IMPACT"
	}
	return "WRITE"
}

type group struct {
	name    string
	entries []entry
}

var groupOrder = []string{"People & HR", "Work", "Sales", "Finance", "Content & reports", "Advanced / Live schema", "Resources"}

func domainGroup(domain string) string {
	switch domain {
	case "people", "time", "absences", "expenses", "schedules", "documents", "goals", "skills", "evaluations", "surveys", "trainings", "recruitment", "onboarding":
		return "People & HR"
	case "appointments", "tasks", "todos":
		return "Work"
	case "crm", "customers", "contacts", "leads", "opportunities":
		return "Sales"
	case "invoices", "purchases", "providers", "items":
		return "Finance"
	case "views", "reports":
		return "Content & reports"
	default:
		return "Advanced / Live schema"
	}
}

func catalogueEntries(catalogue *app.Catalogue) ([]entry, []group) {
	if catalogue == nil {
		return nil, nil
	}
	var entries []entry
	seen := make(map[string]bool)
	for _, tool := range catalogue.Tools {
		if tool.Name == "confirm_action" || tool.Name == "" || seen["tool:"+tool.Name] {
			continue
		}
		seen["tool:"+tool.Name] = true
		title := tool.Title
		if title == "" {
			title = strings.ReplaceAll(tool.Name, "_", " ")
		}
		area := "Advanced / Live schema"
		if tool.Reviewed {
			area = domainGroup(tool.Domain)
		}
		entries = append(entries, entry{id: "tool:" + tool.Name, title: title, description: tool.Description, group: area, kind: toolEntry, tool: tool})
	}
	for _, resource := range catalogue.Resources {
		if resource == nil || resource.URI == "" || seen["resource:"+resource.URI] {
			continue
		}
		seen["resource:"+resource.URI] = true
		title := resource.Name
		if title == "" {
			title = resource.URI
		}
		entries = append(entries, entry{id: "resource:" + resource.URI, title: title, description: resource.Description, group: "Resources", kind: resourceEntry, uri: resource.URI})
	}
	for _, resource := range catalogue.ResourceTemplates {
		if resource == nil || resource.URITemplate == "" || seen["template:"+resource.URITemplate] {
			continue
		}
		seen["template:"+resource.URITemplate] = true
		title := resource.Name
		if title == "" {
			title = resource.URITemplate
		}
		entries = append(entries, entry{id: "template:" + resource.URITemplate, title: title, description: resource.Description, group: "Resources", kind: templateEntry, uri: resource.URITemplate})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].label() < entries[j].label() })
	var groups []group
	for _, name := range groupOrder {
		g := group{name: name}
		for _, e := range entries {
			if e.group == name {
				g.entries = append(g.entries, e)
			}
		}
		if len(g.entries) > 0 {
			groups = append(groups, g)
		}
	}
	return entries, groups
}

var shortcutTools = []string{
	"get_employee_current_activity", "list_clock_ins", "list_absences", "list_expenses",
	"list_personal_todos", "list_employee_tasks", "list_employees", "list_schedules",
	"list_appointments", "list_trainings", "list_crm_action_queue", "list_customers",
	"list_opportunities", "list_invoices",
}

func shortcuts(entries []entry) []entry {
	var result []entry
	for _, name := range shortcutTools {
		for _, e := range entries {
			if e.kind == toolEntry && e.tool.Name == name && e.tool.Reviewed && e.tool.ReadOnly && e.tool.SchemaError == "" {
				result = append(result, e)
				break
			}
		}
		if len(result) == 9 {
			break
		}
	}
	return result
}

func searchEntries(entries []entry, query string, recent []string) []entry {
	type match struct {
		entry entry
		score int
	}
	query = strings.ToLower(strings.TrimSpace(query))
	var matches []match
	for _, e := range entries {
		haystack := strings.ToLower(e.label() + " " + e.tool.Name + " " + e.title + " " + e.tool.Domain + " " + e.description)
		score, ok := fuzzyScore(haystack, query)
		if !ok {
			continue
		}
		for i, id := range recent {
			if id == e.id {
				score -= 100 - i
				break
			}
		}
		for i, name := range shortcutTools {
			if e.kind == toolEntry && e.tool.Name == name {
				score -= 30 - i
				break
			}
		}
		matches = append(matches, match{e, score})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score < matches[j].score })
	result := make([]entry, len(matches))
	for i, match := range matches {
		result[i] = match.entry
	}
	return result
}

// A subsequence match tolerates spaces and abbreviated command paths without
// treating server-provided text as a regexp or executable search expression.
func fuzzyScore(value, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	if index := strings.Index(value, query); index >= 0 {
		return index, true
	}
	wanted := []rune(strings.ReplaceAll(query, " ", ""))
	position, score := 0, 100
	for i, r := range value {
		if position < len(wanted) && r == wanted[position] {
			position++
			score += i
		}
	}
	return score, position == len(wanted)
}

func resourceSchema(e entry) (schema.JSONSchema, error) {
	s := schema.JSONSchema{Type: "object", Properties: map[string]schema.Property{}, AdditionalProperties: false}
	if e.kind != templateEntry {
		return s, nil
	}
	template, err := uritemplate.New(e.uri)
	if err != nil {
		return s, fmt.Errorf("unsupported resource template: %w", err)
	}
	for _, name := range template.Varnames() {
		s.Properties[name] = schema.Property{Type: "string", Description: "Resource template parameter (sent to Talento, never opened locally)."}
		s.Required = append(s.Required, name)
	}
	return s, nil
}

func resourceURI(e entry, arguments map[string]any) (string, error) {
	if e.kind == resourceEntry {
		return e.uri, nil
	}
	template, err := uritemplate.New(e.uri)
	if err != nil {
		return "", err
	}
	values := uritemplate.Values{}
	for _, name := range template.Varnames() {
		value, ok := arguments[name].(string)
		if !ok || value == "" {
			return "", fmt.Errorf("resource parameter %q is required", name)
		}
		values[name] = uritemplate.String(value)
	}
	return template.Expand(values)
}

func prettyJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "Unable to display JSON: " + err.Error()
	}
	// An exact JSON review must escape, not delete, hostile Unicode. Escaping
	// every non-ASCII rune also preserves combining sequences that the prose
	// sanitizer intentionally limits. Decoding this text recovers the payload.
	var escaped strings.Builder
	for _, r := range string(data) {
		switch {
		case r < 0x7f:
			escaped.WriteRune(r)
		case r <= 0xffff:
			fmt.Fprintf(&escaped, `\u%04x`, r)
		default:
			high, low := utf16.EncodeRune(r)
			fmt.Fprintf(&escaped, `\u%04x\u%04x`, high, low)
		}
	}
	return escaped.String()
}
