package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

const (
	maxDiagnosticValues = 8
	maxDiagnosticText   = 96
	maxDiagnosticPath   = 512
)

var (
	compiledSchemas sync.Map
	validationPath  = regexp.MustCompile(`validating ([^:]+):`)
	quotedName      = regexp.MustCompile(`"([^"\\]*(?:\\.[^"\\]*)*)"`)
)

// ValidationError is a value-free, user-facing description of a JSON Schema
// failure. It intentionally never includes the rejected instance value.
type ValidationError struct {
	Path       string   `json:"path"`
	Constraint string   `json:"constraint"`
	Expected   string   `json:"expected,omitempty"`
	Properties []string `json:"properties,omitempty"`
}

func (e *ValidationError) Error() string {
	path := e.Path
	if path == "" {
		path = "$"
	}
	switch e.Constraint {
	case "required":
		return fmt.Sprintf("input %s is missing required properties: %s", path, strings.Join(e.Properties, ", "))
	case "additionalProperties":
		if len(e.Properties) > 0 {
			return fmt.Sprintf("input %s contains unsupported properties: %s", path, strings.Join(e.Properties, ", "))
		}
		return fmt.Sprintf("input %s contains an unsupported property", path)
	case "type":
		return fmt.Sprintf("input %s must be %s", path, e.Expected)
	case "enum":
		return fmt.Sprintf("input %s must be one of %s", path, e.Expected)
	case "minimum":
		return fmt.Sprintf("input %s must be at least %s", path, e.Expected)
	case "maximum":
		return fmt.Sprintf("input %s must be at most %s", path, e.Expected)
	case "exclusiveMinimum":
		return fmt.Sprintf("input %s must be greater than %s", path, e.Expected)
	case "exclusiveMaximum":
		return fmt.Sprintf("input %s must be less than %s", path, e.Expected)
	case "minLength":
		return fmt.Sprintf("input %s must contain at least %s characters", path, e.Expected)
	case "maxLength":
		return fmt.Sprintf("input %s must contain at most %s characters", path, e.Expected)
	case "minItems":
		return fmt.Sprintf("input %s must contain at least %s items", path, e.Expected)
	case "maxItems":
		return fmt.Sprintf("input %s must contain at most %s items", path, e.Expected)
	case "minProperties":
		return fmt.Sprintf("input %s must contain at least %s properties", path, e.Expected)
	case "maxProperties":
		return fmt.Sprintf("input %s must contain at most %s properties", path, e.Expected)
	case "pattern":
		return fmt.Sprintf("input %s must match the required pattern", path)
	case "oneOf":
		return fmt.Sprintf("input %s must match exactly one allowed shape", path)
	case "anyOf":
		return fmt.Sprintf("input %s must match at least one allowed shape", path)
	case "allOf":
		return fmt.Sprintf("input %s must satisfy every required shape", path)
	case "uniqueItems":
		return fmt.Sprintf("input %s must not contain duplicate items", path)
	case "multipleOf":
		return fmt.Sprintf("input %s must be a multiple of %s", path, e.Expected)
	default:
		return fmt.Sprintf("input %s does not satisfy its JSON Schema (%s)", path, e.Constraint)
	}
}

// CompileInputSchema verifies that the complete, original schema can be loaded
// and resolved by the standards-compliant Draft 7 / Draft 2020-12 validator.
func (s JSONSchema) CompileInputSchema() error {
	_, _, err := s.resolved()
	return err
}

// ValidateInput validates a JSON value against the complete original schema.
// Business and authorization rules remain exclusively server-authoritative.
func (s JSONSchema) ValidateInput(instance any) error {
	resolved, data, err := s.resolved()
	if err != nil {
		return fmt.Errorf("compile input schema: %w", err)
	}
	if err := resolved.Validate(instance); err != nil {
		if diagnostic := deterministicValidationError(data, instance); diagnostic != nil {
			return diagnostic
		}
		return describeValidationError(data, err)
	}
	return nil
}

// deterministicValidationError uses the same standards validator on nested
// schemas in stable property/index order. This is diagnostic traversal only:
// the complete root schema above remains the sole validity decision.
func deterministicValidationError(schemaData []byte, instance any) *ValidationError {
	var root map[string]any
	if err := json.Unmarshal(schemaData, &root); err != nil {
		return nil
	}
	return diagnoseNode(root, instance, "$", root)
}

func diagnoseNode(node map[string]any, instance any, path string, root map[string]any) *ValidationError {
	err := validateDiagnosticNode(node, instance, root)
	if err == nil {
		return nil
	}
	// A failed combinator describes the shape at this path as a whole. Picking
	// an arbitrary failing branch would be less stable and less actionable.
	for _, combinator := range []string{"oneOf", "anyOf", "allOf"} {
		if _, ok := node[combinator]; ok && strings.Contains(err.Error(), combinator+":") {
			return issueForNode(path, node, combinator, err.Error())
		}
	}
	if object, ok := instance.(map[string]any); ok {
		if properties, ok := node["properties"].(map[string]any); ok {
			names := make([]string, 0, len(properties))
			for name := range properties {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				value, present := object[name]
				child, validSchema := properties[name].(map[string]any)
				if !present || !validSchema {
					continue
				}
				if issue := diagnoseNode(child, value, appendPropertyPath(path, name), root); issue != nil {
					return issue
				}
			}
		}
	}
	if array, ok := instance.([]any); ok {
		if items, ok := node["items"].(map[string]any); ok {
			for index, item := range array {
				if issue := diagnoseNode(items, item, fmt.Sprintf("%s[%d]", path, index), root); issue != nil {
					return issue
				}
			}
		}
	}
	constraint := validationConstraint(err.Error())
	return issueForNode(path, node, constraint, err.Error())
}

func validateDiagnosticNode(node map[string]any, instance any, root map[string]any) error {
	standalone := make(map[string]any, len(node)+3)
	for key, value := range node {
		standalone[key] = value
	}
	for _, shared := range []string{"$schema", "$defs", "definitions"} {
		if _, exists := standalone[shared]; exists {
			continue
		}
		if value, ok := root[shared]; ok {
			standalone[shared] = value
		}
	}
	data, err := json.Marshal(standalone)
	if err != nil {
		return err
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return err
	}
	return resolved.Validate(instance)
}

func issueForNode(path string, node map[string]any, constraint, message string) *ValidationError {
	issue := &ValidationError{
		Path:       truncateDiagnostic(path, maxDiagnosticPath),
		Constraint: constraint,
		Expected:   expectedConstraint(node, constraint),
	}
	if constraint == "required" || constraint == "additionalProperties" {
		issue.Properties = diagnosticProperties(message)
	}
	return issue
}

func appendPropertyPath(path, name string) string {
	if simplePropertyName(name) {
		return path + "." + name
	}
	encoded, _ := json.Marshal(name)
	return path + "[" + string(encoded) + "]"
}

func (s JSONSchema) resolved() (*jsonschema.Resolved, []byte, error) {
	data, err := s.schemaData()
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(data)
	key := hex.EncodeToString(digest[:])
	if cached, ok := compiledSchemas.Load(key); ok {
		return cached.(*jsonschema.Resolved), data, nil
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil, fmt.Errorf("decode JSON Schema: %w", err)
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve JSON Schema: %w", err)
	}
	actual, _ := compiledSchemas.LoadOrStore(key, resolved)
	return actual.(*jsonschema.Resolved), data, nil
}

func (s JSONSchema) schemaData() ([]byte, error) {
	if len(s.raw) > 0 {
		return append([]byte(nil), s.raw...), nil
	}
	return json.Marshal(s)
}

func describeValidationError(schemaData []byte, validationErr error) *ValidationError {
	message := validationErr.Error()
	schemaPath := deepestSchemaPath(message)
	var document any
	_ = json.Unmarshal(schemaData, &document)
	node := schemaNode(document, schemaPath)
	constraint := validationConstraint(message)
	issue := &ValidationError{
		Path:       truncateDiagnostic(instancePath(schemaPath), maxDiagnosticPath),
		Constraint: constraint,
		Expected:   expectedConstraint(node, constraint),
	}
	if constraint == "required" || constraint == "additionalProperties" {
		issue.Properties = diagnosticProperties(message)
	}
	return issue
}

func deepestSchemaPath(message string) string {
	matches := validationPath.FindAllStringSubmatch(message, -1)
	for index := len(matches) - 1; index >= 0; index-- {
		if strings.HasPrefix(matches[index][1], "/") {
			return matches[index][1]
		}
	}
	return ""
}

func validationConstraint(message string) string {
	if strings.Contains(message, "unexpected additional properties") {
		return "additionalProperties"
	}
	constraints := []string{
		// Combinators wrap their branch failures. Report the public shape
		// contract instead of whichever branch happened to be checked last.
		"oneOf", "anyOf", "allOf",
		"required", "type", "enum", "const", "minimum", "maximum",
		"exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minLength",
		"maxLength", "pattern", "minItems", "maxItems", "uniqueItems",
		"minProperties", "maxProperties",
		"contains", "minContains", "maxContains", "not",
	}
	for _, constraint := range constraints {
		if strings.Contains(message, constraint+":") {
			return constraint
		}
	}
	return "validation"
}

func schemaNode(document any, pointer string) any {
	current := document
	if pointer == "" {
		return current
	}
	for _, encoded := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			current = node[token]
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return nil
			}
			current = node[index]
		default:
			return nil
		}
	}
	return current
}

func instancePath(pointer string) string {
	path := "$"
	tokens := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for index := 0; index < len(tokens); index++ {
		token := strings.ReplaceAll(strings.ReplaceAll(tokens[index], "~1", "/"), "~0", "~")
		switch token {
		case "properties":
			if index+1 < len(tokens) {
				index++
				name := strings.ReplaceAll(strings.ReplaceAll(tokens[index], "~1", "/"), "~0", "~")
				if simplePropertyName(name) {
					path += "." + name
				} else {
					encoded, _ := json.Marshal(name)
					path += "[" + string(encoded) + "]"
				}
			}
		case "items":
			path += "[]"
		case "prefixItems":
			if index+1 < len(tokens) {
				index++
				path += "[" + tokens[index] + "]"
			}
		case "additionalProperties", "patternProperties":
			path += ".*"
		}
	}
	return path
}

func simplePropertyName(name string) bool {
	if name == "" {
		return false
	}
	for index, current := range name {
		if !(current == '_' || current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' || index > 0 && current >= '0' && current <= '9') {
			return false
		}
	}
	return true
}

func expectedConstraint(node any, constraint string) string {
	object, _ := node.(map[string]any)
	value := object[constraint]
	if constraint == "type" {
		return formatTypes(value)
	}
	if constraint == "enum" {
		values, _ := value.([]any)
		formatted := make([]string, 0, min(len(values), maxDiagnosticValues))
		for _, item := range values {
			if len(formatted) == maxDiagnosticValues {
				break
			}
			encoded, _ := json.Marshal(item)
			formatted = append(formatted, truncateDiagnostic(string(encoded), maxDiagnosticText))
		}
		if len(values) > maxDiagnosticValues {
			formatted = append(formatted, "...")
		}
		return strings.Join(formatted, ", ")
	}
	switch number := value.(type) {
	case float64:
		return strconv.FormatFloat(number, 'g', -1, 64)
	case string:
		return number
	default:
		return fmt.Sprint(number)
	}
}

func formatTypes(value any) string {
	var types []string
	switch value := value.(type) {
	case string:
		types = []string{value}
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				types = append(types, text)
			}
		}
	}
	if len(types) == 0 {
		return "the expected type"
	}
	for index, value := range types {
		switch value {
		case "null":
			types[index] = value
		case "integer", "object", "array":
			types[index] = "an " + value
		default:
			types[index] = "a " + value
		}
	}
	return strings.Join(types, " or ")
}

func diagnosticProperties(message string) []string {
	matches := quotedName.FindAllStringSubmatch(message, maxDiagnosticValues+1)
	properties := make([]string, 0, min(len(matches), maxDiagnosticValues))
	for _, match := range matches {
		if len(properties) == maxDiagnosticValues {
			break
		}
		name, err := strconv.Unquote(`"` + match[1] + `"`)
		if err == nil {
			properties = append(properties, truncateDiagnostic(name, maxDiagnosticText))
		}
	}
	sort.Strings(properties)
	return properties
}

func truncateDiagnostic(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-3]) + "..."
}
