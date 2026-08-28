package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/talentohq/talento-cli/internal/buildinfo"
	"github.com/talentohq/talento-cli/internal/config"
)

type Client struct {
	session *mcp.ClientSession
}

// TokenProvider is called for each HTTP request, allowing the CLI-owned OAuth
// service to refresh expiring credentials without reconnecting or replaying a
// tool call. Calls to a provider are serialized by this package.
type TokenProvider func(context.Context) (string, error)

// ErrNotDispatched distinguishes a failed local authorization/preflight check
// from a transport error after a write may have reached the gateway.
var ErrNotDispatched = errors.New("MCP request was not dispatched")

// ErrUnauthorized means the gateway explicitly rejected authentication. It is
// not an unknown write outcome and never triggers an automatic login or replay.
var ErrUnauthorized = errors.New("MCP authorization was rejected")

type dispatchGuardKey struct{}

// WithDispatchGuard checks session-local state after token refresh and directly
// before dispatch. A rejected guard never sends the HTTP request. It is used to
// prevent a preview authorized before reauthentication from being confirmed.
func WithDispatchGuard(ctx context.Context, guard func() error) context.Context {
	return context.WithValue(ctx, dispatchGuardKey{}, guard)
}

func Connect(ctx context.Context, accessToken string) (*Client, error) {
	return ConnectTo(ctx, config.Endpoint, accessToken, nil)
}

func ConnectTo(ctx context.Context, endpoint, accessToken string, base *http.Client) (*Client, error) {
	return connectTo(ctx, endpoint, accessToken, nil, base)
}

func ConnectWithTokenProvider(ctx context.Context, provider TokenProvider) (*Client, error) {
	return ConnectToWithTokenProvider(ctx, config.Endpoint, provider, nil)
}

func ConnectToWithTokenProvider(ctx context.Context, endpoint string, provider TokenProvider, base *http.Client) (*Client, error) {
	if provider == nil {
		return nil, errors.New("MCP token provider is required")
	}
	return connectTo(ctx, endpoint, "", &serializedTokenSource{provider: provider}, base)
}

func connectTo(ctx context.Context, endpoint, token string, source *serializedTokenSource, base *http.Client) (*Client, error) {
	if base == nil {
		base = &http.Client{Timeout: 45 * time.Second}
	}
	transportBase := base.Transport
	if transportBase == nil {
		transportBase = http.DefaultTransport
	}
	httpClient := &http.Client{
		Timeout: 45 * time.Second,
		// A 307/308 redirect would otherwise replay a POST, possibly including a
		// write. The gateway is fixed; redirects are never part of this protocol.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: bearerTransport{
			token: token, source: source, base: transportBase,
		},
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           httpClient,
		MaxRetries:           2,
		DisableStandaloneSSE: true,
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "talento", Version: buildinfo.Version},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("initialize MCP session: %w", err)
	}
	return &Client{session: session}, nil
}

func (c *Client) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	return c.session.Close()
}

func (c *Client) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	cursor := ""
	seen := make(map[string]bool)
	for {
		result, err := c.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			return tools, nil
		}
		if seen[result.NextCursor] {
			return nil, errors.New("list MCP tools: repeated pagination cursor")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
}

func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolOutcome, error) {
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("call MCP tool %q: %w", name, err)
	}
	return NewToolOutcome(name, result), nil
}

func (c *Client) ListResources(ctx context.Context) ([]*mcp.Resource, error) {
	var resources []*mcp.Resource
	cursor := ""
	seen := make(map[string]bool)
	for {
		result, err := c.session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list MCP resources: %w", err)
		}
		resources = append(resources, result.Resources...)
		if result.NextCursor == "" {
			return resources, nil
		}
		if seen[result.NextCursor] {
			return nil, errors.New("list MCP resources: repeated pagination cursor")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
}

func (c *Client) ListResourceTemplates(ctx context.Context) ([]*mcp.ResourceTemplate, error) {
	var templates []*mcp.ResourceTemplate
	cursor := ""
	seen := make(map[string]bool)
	for {
		result, err := c.session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list MCP resource templates: %w", err)
		}
		templates = append(templates, result.ResourceTemplates...)
		if result.NextCursor == "" {
			return templates, nil
		}
		if seen[result.NextCursor] {
			return nil, errors.New("list MCP resource templates: repeated pagination cursor")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
}

func (c *Client) ReadResource(ctx context.Context, uri string) (*ResourceOutcome, error) {
	result, err := c.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("read MCP resource %q: %w", uri, err)
	}
	return &ResourceOutcome{URI: uri, Result: result}, nil
}

type bearerTransport struct {
	token  string
	source *serializedTokenSource
	base   http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	token := t.token
	if t.source != nil {
		var err error
		token, err = t.source.accessToken(request.Context())
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotDispatched, err)
		}
	}
	if err := request.Context().Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotDispatched, err)
	}
	if guard, ok := request.Context().Value(dispatchGuardKey{}).(func() error); ok {
		if err := guard(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotDispatched, err)
		}
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+token)
	clone.Header.Set("User-Agent", "talento/"+buildinfo.Version)
	response, err := t.base.RoundTrip(clone)
	if err == nil && response.StatusCode == http.StatusUnauthorized {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: HTTP 401", ErrUnauthorized)
	}
	return response, err
}

type serializedTokenSource struct {
	mu       sync.Mutex
	provider TokenProvider
}

func (s *serializedTokenSource) accessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	token, err := s.provider(ctx)
	if err == nil && token == "" {
		err = errors.New("MCP token provider returned an empty access token")
	}
	return token, err
}

type ToolState string

const (
	StateReturned  ToolState = "returned"
	StateCommitted ToolState = "committed"
	StatePreview   ToolState = "preview"
	StateSubmitted ToolState = "submitted_for_approval"
	StateError     ToolState = "error"
)

type ToolOutcome struct {
	Tool      string              `json:"tool"`
	State     ToolState           `json:"state"`
	PreviewID string              `json:"preview_id,omitempty"`
	Result    *mcp.CallToolResult `json:"result"`
}

func NewToolOutcome(name string, result *mcp.CallToolResult) *ToolOutcome {
	text := strings.ToLower(contentText(result.Content))
	state := StateReturned
	previewID := structuredPreviewID(result.StructuredContent)
	structuredState := structuredToolState(result.StructuredContent)
	textOnlyState := successfulStateForTextOnlyTool(name)
	switch {
	case result.IsError:
		state = StateError
	case structuredState != "":
		state = structuredState
	case previewID != "" || strings.Contains(text, "=== preview — not yet executed ===") || strings.Contains(text, "=== preview - not yet executed ==="):
		state = StatePreview
	case looksSubmitted(text) || hasPendingApprovalResult(name, text):
		state = StateSubmitted
	case strings.Contains(text, "action completed"):
		state = StateCommitted
	case textOnlyState != "":
		state = textOnlyState
	}
	if previewID == "" {
		previewID = extractPreviewID(contentText(result.Content))
	}
	return &ToolOutcome{Tool: name, State: state, PreviewID: previewID, Result: result}
}

func (o *ToolOutcome) HumanText() string {
	if o == nil || o.Result == nil {
		return ""
	}
	return contentText(o.Result.Content)
}

func (o *ToolOutcome) IsError() bool   { return o != nil && o.State == StateError }
func (o *ToolOutcome) IsPreview() bool { return o != nil && o.State == StatePreview }

type ResourceOutcome struct {
	URI    string                  `json:"uri"`
	Result *mcp.ReadResourceResult `json:"result"`
}

func (o *ResourceOutcome) HumanText() string {
	if o == nil || o.Result == nil {
		return ""
	}
	parts := make([]string, 0, len(o.Result.Contents))
	for _, content := range o.Result.Contents {
		if content.Text != "" {
			parts = append(parts, content.Text)
			continue
		}
		data, err := json.MarshalIndent(content, "", "  ")
		if err == nil {
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n\n")
}

func contentText(contents []mcp.Content) string {
	parts := make([]string, 0, len(contents))
	for _, content := range contents {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func extractPreviewID(text string) string {
	match := previewIDPattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

var previewIDPattern = regexp.MustCompile(`(?i)preview[_ -]?id["']?\s*[:=]\s*["']?([a-z0-9._:-]+)`)

func structuredPreviewID(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"preview_id", "previewId"} {
		if id, ok := object[key].(string); ok {
			return id
		}
	}
	return ""
}

func structuredToolState(value any) ToolState {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"state", "status"} {
		state, ok := object[key].(string)
		if !ok {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(state))
		switch normalized {
		case "returned", "read_only":
			return StateReturned
		case "preview", "pending_confirmation":
			return StatePreview
		case "committed", "completed", "persisted":
			return StateCommitted
		case "submitted_for_approval", "pending_approval", "submitted":
			return StateSubmitted
		case "error", "failed", "denied":
			return StateError
		}
	}
	return ""
}

// Some server tools currently return a successful, text-only result without the
// generic ACTION COMPLETED marker. Keep this mapping deliberately narrow: an
// explicit structured state, an MCP error, and a preview always take precedence.
var textOnlySuccessfulToolStates = map[string]ToolState{
	"create_training":             StateCommitted,
	"update_training":             StateCommitted,
	"delete_training":             StateCommitted,
	"create_topic":                StateCommitted,
	"update_topic":                StateCommitted,
	"delete_topic":                StateCommitted,
	"create_lesson":               StateCommitted,
	"update_lesson":               StateCommitted,
	"delete_lesson":               StateCommitted,
	"create_segment":              StateCommitted,
	"update_segment":              StateCommitted,
	"delete_segment":              StateCommitted,
	"submit_training_for_review":  StateSubmitted,
	"request_training_changes":    StateCommitted,
	"publish_training":            StateCommitted,
	"archive_training":            StateCommitted,
	"duplicate_training_as_draft": StateCommitted,
}

func successfulStateForTextOnlyTool(name string) ToolState {
	return textOnlySuccessfulToolStates[name]
}

// Absences can either remain pending or be approved immediately by a manager.
// Absence and expense presenters expose the approval enum as a bare
// pipe-delimited value, so scope this fallback to approval-backed tools rather
// than treating every domain object's "Pending" status as an approval request.
var approvalStatusTools = map[string]bool{
	"create_absence": true,
	"create_expense": true,
	"update_absence": true,
}

var pendingResultPattern = regexp.MustCompile(`(?m)(?:^|[|:])\s*pending\s*(?:$|\|)`)

func hasPendingApprovalResult(name, text string) bool {
	// Reschedule creation and updates always leave the request pending. Its
	// presenter localizes both the label and value, so tool semantics are the
	// reliable fallback until the server supplies an explicit structured state.
	if name == "create_reschedule" || name == "update_reschedule" {
		return true
	}
	return approvalStatusTools[name] && pendingResultPattern.MatchString(text)
}

func looksSubmitted(text string) bool {
	markers := []string{
		"submitted for approval",
		"submitted for review",
		"pending approval",
		"awaiting approval",
		"request will need manager approval",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
