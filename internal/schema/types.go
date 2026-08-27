package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type Snapshot struct {
	SnapshotVersion int        `json:"snapshot_version"`
	GeneratedAt     string     `json:"generated_at"`
	Source          string     `json:"source"`
	Endpoint        string     `json:"endpoint"`
	Tools           []Tool     `json:"tools"`
	Resources       []Resource `json:"resources"`
}

type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema JSONSchema  `json:"inputSchema"`
	Annotations Annotations `json:"annotations"`
}

type Annotations struct {
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
	ReadOnlyHint    bool `json:"readOnlyHint"`
}

type JSONSchema struct {
	Schema               string              `json:"$schema,omitempty"`
	Type                 string              `json:"type,omitempty"`
	Description          string              `json:"description,omitempty"`
	Properties           map[string]Property `json:"properties,omitempty"`
	Required             []string            `json:"required,omitempty"`
	AdditionalProperties any                 `json:"additionalProperties,omitempty"`
	raw                  json.RawMessage
}

type Property struct {
	Type        any             `json:"type,omitempty"`
	Description string          `json:"description,omitempty"`
	Format      string          `json:"format,omitempty"`
	Enum        []any           `json:"enum,omitempty"`
	Items       json.RawMessage `json:"items,omitempty"`
	Default     any             `json:"default,omitempty"`
	OneOf       []any           `json:"oneOf,omitempty"`
	AnyOf       []any           `json:"anyOf,omitempty"`
	raw         json.RawMessage
}

func (s *JSONSchema) UnmarshalJSON(data []byte) error {
	type plain JSONSchema
	if err := json.Unmarshal(data, (*plain)(s)); err != nil {
		return err
	}
	s.raw = append(s.raw[:0], data...)
	return nil
}

func (s JSONSchema) MarshalJSON() ([]byte, error) {
	if len(s.raw) > 0 {
		return append([]byte(nil), s.raw...), nil
	}
	type plain JSONSchema
	return json.Marshal(plain(s))
}

func (p *Property) UnmarshalJSON(data []byte) error {
	type plain Property
	if err := json.Unmarshal(data, (*plain)(p)); err != nil {
		return err
	}
	p.raw = append(p.raw[:0], data...)
	return nil
}

func (p Property) MarshalJSON() ([]byte, error) {
	if len(p.raw) > 0 {
		return append([]byte(nil), p.raw...), nil
	}
	type plain Property
	return json.Marshal(plain(p))
}

func (p Property) PrimaryType() string {
	switch value := p.Type.(type) {
	case string:
		return value
	case []any:
		for _, item := range value {
			if item != "null" {
				if text, ok := item.(string); ok {
					return text
				}
			}
		}
	}
	return "string"
}

// ItemProperty returns the item schema used by a homogeneous array property.
// Boolean and tuple item schemas deliberately return false: they remain fully
// supported by JSON validation, but do not have an unambiguous scalar CLI flag.
func (p Property) ItemProperty() (Property, bool) {
	if len(p.Items) == 0 || p.Items[0] != '{' {
		return Property{}, false
	}
	var item Property
	if err := json.Unmarshal(p.Items, &item); err != nil {
		return Property{}, false
	}
	return item, true
}

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Manifest struct {
	ManifestVersion int               `json:"manifest_version"`
	SnapshotVersion int               `json:"snapshot_version"`
	SnapshotDigest  string            `json:"snapshot_sha256"`
	Endpoint        string            `json:"endpoint"`
	Tools           []ToolMapping     `json:"tools"`
	Resources       []ResourceMapping `json:"resources"`
}

type ToolMapping struct {
	Tool     string `json:"tool"`
	Domain   string `json:"domain"`
	Command  string `json:"command"`
	ReadOnly bool   `json:"read_only"`
}

func (m ToolMapping) Path() string { return m.Domain + " " + m.Command }

type ResourceMapping struct {
	Resource    string `json:"resource"`
	URITemplate string `json:"uri_template"`
	Command     string `json:"command"`
}

func ParseSnapshot(data []byte) (Snapshot, error) {
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("parse gateway schema snapshot: %w", err)
	}
	if snapshot.SnapshotVersion < 1 || snapshot.Endpoint == "" {
		return Snapshot{}, fmt.Errorf("invalid gateway schema snapshot")
	}
	seen := make(map[string]bool, len(snapshot.Tools))
	for _, tool := range snapshot.Tools {
		if tool.Name == "" || seen[tool.Name] {
			return Snapshot{}, fmt.Errorf("invalid or duplicate tool name %q", tool.Name)
		}
		if err := tool.InputSchema.CompileInputSchema(); err != nil {
			return Snapshot{}, fmt.Errorf("invalid input schema for tool %q: %w", tool.Name, err)
		}
		seen[tool.Name] = true
	}
	return snapshot, nil
}

func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse coverage manifest: %w", err)
	}
	return manifest, nil
}

func SnapshotDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ValidateCoverage(snapshot Snapshot, snapshotData []byte, manifest Manifest) error {
	if manifest.ManifestVersion != 1 {
		return fmt.Errorf("unsupported coverage manifest version %d", manifest.ManifestVersion)
	}
	if manifest.SnapshotVersion != snapshot.SnapshotVersion {
		return fmt.Errorf("coverage snapshot version %d does not match schema snapshot version %d", manifest.SnapshotVersion, snapshot.SnapshotVersion)
	}
	if manifest.SnapshotDigest != SnapshotDigest(snapshotData) {
		return fmt.Errorf("coverage manifest is stale: snapshot digest changed")
	}
	mapped := make(map[string]bool, len(manifest.Tools))
	paths := make(map[string]string, len(manifest.Tools))
	for _, item := range manifest.Tools {
		if mapped[item.Tool] {
			return fmt.Errorf("tool %q is mapped more than once", item.Tool)
		}
		if previous, exists := paths[item.Path()]; exists {
			return fmt.Errorf("command %q maps both %q and %q", item.Path(), previous, item.Tool)
		}
		mapped[item.Tool] = true
		paths[item.Path()] = item.Tool
	}
	for _, tool := range snapshot.Tools {
		if !mapped[tool.Name] {
			return fmt.Errorf("tool %q has no coverage decision", tool.Name)
		}
	}
	if len(mapped) != len(snapshot.Tools) {
		return fmt.Errorf("coverage manifest contains tools not present in the snapshot")
	}
	resources := make(map[string]bool, len(manifest.Resources))
	for _, item := range manifest.Resources {
		resources[item.Resource] = true
	}
	for _, resource := range snapshot.Resources {
		if !resources[resource.Name] {
			return fmt.Errorf("resource %q has no coverage decision", resource.Name)
		}
	}
	if len(resources) != len(snapshot.Resources) {
		return fmt.Errorf("coverage manifest contains resources not present in the snapshot")
	}
	return nil
}

func ToolByName(snapshot Snapshot, name string) (Tool, bool) {
	for _, tool := range snapshot.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

func SortedProperties(tool Tool) []string {
	names := make([]string, 0, len(tool.InputSchema.Properties))
	for name := range tool.InputSchema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func IsRequired(tool Tool, property string) bool {
	for _, required := range tool.InputSchema.Required {
		if required == property {
			return true
		}
	}
	return false
}
