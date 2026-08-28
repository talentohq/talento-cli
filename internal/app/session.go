package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/talentohq/talento-cli/internal/auth"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/schema"
	"github.com/yosida95/uritemplate/v3"
)

// Session is a long-lived, explicit-profile connection. It never prompts,
// auto-confirms, or retries an invocation. The caller owns presentation and
// deliberate review; the gateway remains the authorization authority.
type Session interface {
	Profile() string
	Catalogue(context.Context) (*Catalogue, error)
	Invoke(context.Context, Invocation) (*ToolExecution, error)
	Confirm(context.Context, PreviewHandle) (*ToolExecution, error)
	ReadResource(context.Context, string) (*mcpclient.ResourceOutcome, error)
	InvalidatePreviews()
	Close() error
}

type Invocation struct {
	Tool           string
	Arguments      map[string]any
	SchemaRevision string
}

type Catalogue struct {
	Tools             []SessionTool
	Resources         []*mcp.Resource
	ResourceTemplates []*mcp.ResourceTemplate
	Warnings          []string
}

// SessionTool combines live availability with reviewed local metadata. An
// unreviewed or uncompileable schema is retained for inspection, never silently
// replaced with the embedded schema. ReadOnly is deliberately conservative.
type SessionTool struct {
	Name, Title, Description, Domain, Command, SchemaRevision string
	InputSchema                                               schema.JSONSchema
	RawSchema                                                 json.RawMessage
	Reviewed, ReadOnly, Destructive                           bool
	SchemaError                                               string
}

// PreviewHandle carries no user-editable preview ID. Valid reports whether a
// handle was issued; Confirm also checks whether it remains valid and unused.
type PreviewHandle struct {
	owner *liveSession
	id    uint64
}

func (h PreviewHandle) Valid() bool { return h.owner != nil && h.id != 0 }

var (
	ErrInvalidPreview = errors.New("preview is missing, expired, invalidated, or already used; obtain and review a new preview")
	ErrSessionClosed  = errors.New("session is closed")
	ErrSessionChanged = errors.New("session authentication changed; refresh and review before continuing")
)

type SchemaChangedError struct{ Tool SessionTool }

func (e *SchemaChangedError) Error() string {
	return fmt.Sprintf("tool %q changed; review the current schema and arguments before submitting again", e.Tool.Name)
}

// OutcomeUnknownError never means that a write failed or was cancelled. The
// gateway may have executed it before the connection was lost. Do not replay.
type OutcomeUnknownError struct {
	Tool  string
	Cause error
}

func (e *OutcomeUnknownError) Error() string {
	return fmt.Sprintf("outcome unknown for %q: the gateway may have executed the action; inspect its current state before taking another action", e.Tool)
}
func (e *OutcomeUnknownError) Unwrap() error { return e.Cause }

type sessionClient interface {
	toolClient
	ListResources(context.Context) ([]*mcp.Resource, error)
	ListResourceTemplates(context.Context) ([]*mcp.ResourceTemplate, error)
	ReadResource(context.Context, string) (*mcpclient.ResourceOutcome, error)
	Close() error
}

type pendingPreview struct {
	id             string
	origin         SessionTool
	argumentDigest [sha256.Size]byte
	preview        *mcpclient.ToolOutcome
	generation     uint64
}

type liveSession struct {
	profile     string
	client      sessionClient
	snapshot    schema.Snapshot
	manifest    schema.Manifest
	opMu        sync.Mutex
	mu          sync.Mutex
	closed      bool
	authChanged bool
	version     uint64
	nextID      uint64
	previews    map[uint64]pendingPreview
}

// OpenSession does not resolve or persist a default profile. The command must
// resolve precedence/project trust before calling this explicit-profile API.
func (a *App) OpenSession(ctx context.Context, profile string, allowFileCredentials bool) (Session, error) {
	cfg, err := a.Config.Load()
	if err != nil {
		return nil, err
	}
	if _, ok := cfg.Profiles[profile]; !ok || profile == "" {
		return nil, clioutput.Usage(fmt.Sprintf("profile %q is not configured", profile), "Create a profile or select an existing profile.")
	}
	authService, err := a.AuthService(allowFileCredentials)
	if err != nil {
		return nil, err
	}
	session := newSession(profile, a.Snapshot, a.Manifest, nil)
	grantIdentity := func() (string, error) {
		credentials, err := authService.Credentials.Load(profile)
		if err != nil {
			return "", err
		}
		// Login registers a new OAuth client; ordinary token renewal retains
		// this grant identity. Do not invalidate a session merely on expiry.
		identity, _ := json.Marshal([]string{credentials.ClientID, credentials.Issuer, credentials.Resource, credentials.Scope})
		return string(identity), nil
	}
	client, err := mcpclient.ConnectWithTokenProvider(ctx, session.tokenProvider(authService, grantIdentity))
	if err != nil {
		return nil, session.sessionError(err)
	}
	session.client = client
	return session, nil
}

type accessTokenService interface {
	AccessToken(context.Context, string) (string, error)
}

func (s *liveSession) tokenProvider(authService accessTokenService, grantIdentity func() (string, error)) mcpclient.TokenProvider {
	// ConnectWithTokenProvider serializes this closure, including AccessToken's
	// credential read/refresh/store operation, across all session HTTP requests.
	var previousGrant string
	return func(ctx context.Context) (string, error) {
		s.mu.Lock()
		changed := s.authChanged
		s.mu.Unlock()
		if changed {
			return "", ErrSessionChanged
		}
		before, err := grantIdentity()
		if err != nil {
			s.InvalidatePreviews()
			return "", sessionAuthenticationError(s.profile, err)
		}
		if previousGrant != "" && previousGrant != before {
			s.invalidateAuthentication()
			return "", ErrSessionChanged
		}
		token, err := authService.AccessToken(ctx, s.profile)
		if err != nil {
			s.InvalidatePreviews()
			return "", sessionAuthenticationError(s.profile, err)
		}
		after, err := grantIdentity()
		if err != nil {
			s.InvalidatePreviews()
			return "", sessionAuthenticationError(s.profile, err)
		}
		if before != after {
			s.invalidateAuthentication()
			return "", ErrSessionChanged
		}
		previousGrant = after
		return token, nil
	}
}

func sessionAuthenticationError(profile string, err error) error {
	if auth.IsMissingCredentials(err) || errors.Is(err, auth.ErrReauthenticationRequired) {
		return clioutput.Auth(fmt.Sprintf("profile %q is not authenticated", profile))
	}
	return fmt.Errorf("authenticate profile %q: %w", profile, err)
}

func (s *liveSession) sessionError(err error) error {
	if errors.Is(err, mcpclient.ErrUnauthorized) {
		s.InvalidatePreviews()
		return clioutput.Auth(fmt.Sprintf("profile %q authorization was rejected; sign in again", s.profile))
	}
	return err
}

func newSession(profile string, snapshot schema.Snapshot, manifest schema.Manifest, client sessionClient) *liveSession {
	return &liveSession{profile: profile, client: client, snapshot: snapshot, manifest: manifest, previews: make(map[uint64]pendingPreview)}
}

func (s *liveSession) Profile() string { return s.profile }

func (s *liveSession) InvalidatePreviews() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	s.previews = make(map[uint64]pendingPreview)
}

// A different OAuth grant may represent another company under the same local
// profile name. Unlike an ordinary form edit, this poisons the connection until
// the caller explicitly opens a new session and clears all previous UI state.
func (s *liveSession) invalidateAuthentication() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authChanged = true
	s.version++
	s.previews = nil
}

func (s *liveSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.version++
	s.previews = nil
	s.mu.Unlock()
	return s.client.Close()
}

func (s *liveSession) generation() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrSessionClosed
	}
	if s.authChanged {
		return 0, ErrSessionChanged
	}
	return s.version, nil
}

func (s *liveSession) checkGeneration(version uint64) error {
	current, err := s.generation()
	if err != nil {
		return err
	}
	if current != version {
		return ErrSessionChanged
	}
	return nil
}

func (s *liveSession) Catalogue(ctx context.Context) (*Catalogue, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	version, err := s.generation()
	if err != nil {
		return nil, err
	}
	tools, err := s.liveTools(ctx)
	if err != nil {
		return nil, err
	}
	catalogue := &Catalogue{Tools: tools}
	resources, templates, warnings, err := s.liveResources(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkGeneration(version); err != nil {
		return nil, err
	}
	catalogue.Resources, catalogue.ResourceTemplates, catalogue.Warnings = resources, templates, warnings
	return catalogue, nil
}

func (s *liveSession) liveTools(ctx context.Context) ([]SessionTool, error) {
	tools, err := s.client.ListTools(ctx)
	if err != nil {
		return nil, s.sessionError(err)
	}
	result := make([]SessionTool, 0, len(tools))
	seen := make(map[string]bool)
	for _, tool := range tools {
		if tool == nil || tool.Name == "" || seen[tool.Name] {
			return nil, errors.New("gateway returned an invalid or duplicate tool name")
		}
		seen[tool.Name] = true
		// Confirmation is only reachable through an exact, session-bound handle.
		if tool.Name != "confirm_action" {
			result = append(result, s.describeTool(tool))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *liveSession) describeTool(live *mcp.Tool) SessionTool {
	tool := SessionTool{Name: live.Name, Title: live.Title, Description: live.Description, Domain: "advanced", Destructive: true}
	if tool.Title == "" && live.Annotations != nil {
		tool.Title = live.Annotations.Title
	}
	if tool.Title == "" {
		tool.Title = strings.ReplaceAll(live.Name, "_", " ")
	}
	raw, err := canonicalJSON(live.InputSchema)
	if err != nil {
		tool.SchemaError = "the gateway input schema is not valid JSON"
	} else {
		tool.RawSchema = raw
		if err := json.Unmarshal(raw, &tool.InputSchema); err != nil || bytes.Equal(raw, []byte("null")) {
			tool.SchemaError = "the gateway input schema is not a supported JSON Schema object"
		} else if tool.InputSchema.Type != "" && tool.InputSchema.Type != "object" {
			tool.SchemaError = "the gateway tool input schema must describe a JSON object"
		} else if err := tool.InputSchema.CompileInputSchema(); err != nil {
			tool.SchemaError = fmt.Sprintf("cannot compile the gateway input schema: %v", err)
		}
	}
	reviewed, known := schema.ToolByName(s.snapshot, live.Name)
	if known {
		reviewedJSON, err := canonicalJSON(reviewed.InputSchema)
		tool.Reviewed = err == nil && tool.SchemaError == "" && bytes.Equal(raw, reviewedJSON)
		// Missing or drifted annotations cannot silently turn a reviewed write
		// into an unreviewed read. New tools default to writes as well.
		tool.ReadOnly = tool.Reviewed && reviewed.Annotations.ReadOnlyHint && consistentSafetyHints(reviewed.Annotations, live.Annotations)
		if tool.Reviewed {
			tool.Description = reviewed.Description
			for _, mapping := range s.manifest.Tools {
				if mapping.Tool == tool.Name {
					tool.Domain, tool.Command = mapping.Domain, mapping.Command
					tool.Title = strings.TrimSpace(mapping.Domain + " " + mapping.Command)
					break
				}
			}
		}
	}
	if tool.ReadOnly {
		tool.Destructive = false
	} else if known && reviewed.Annotations.DestructiveHint {
		tool.Destructive = true
	} else if live.Annotations != nil && live.Annotations.DestructiveHint != nil {
		tool.Destructive = *live.Annotations.DestructiveHint
	}
	// Include safety metadata so a read-to-write change requires another review
	// even when the JSON input shape is identical.
	revision, _ := canonicalJSON(struct {
		Schema      json.RawMessage
		Annotations *mcp.ToolAnnotations
		ReadOnly    bool
	}{raw, live.Annotations, tool.ReadOnly})
	digest := sha256.Sum256(revision)
	tool.SchemaRevision = hex.EncodeToString(digest[:])
	return tool
}

func consistentSafetyHints(reviewed schema.Annotations, live *mcp.ToolAnnotations) bool {
	return live != nil && live.DestructiveHint != nil && live.OpenWorldHint != nil &&
		live.ReadOnlyHint == reviewed.ReadOnlyHint && live.IdempotentHint == reviewed.IdempotentHint &&
		*live.DestructiveHint == reviewed.DestructiveHint && *live.OpenWorldHint == reviewed.OpenWorldHint
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}

func (s *liveSession) Invoke(ctx context.Context, invocation Invocation) (*ToolExecution, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	version, err := s.generation()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if invocation.Tool == "confirm_action" {
		return nil, ErrInvalidPreview
	}
	tools, err := s.liveTools(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkGeneration(version); err != nil {
		return nil, err
	}
	var selected *SessionTool
	for i := range tools {
		if tools[i].Name == invocation.Tool {
			selected = &tools[i]
			break
		}
	}
	if selected == nil {
		return nil, unavailableTool(invocation.Tool, s.profile)
	}
	if selected.SchemaRevision != invocation.SchemaRevision || invocation.SchemaRevision == "" {
		return nil, &SchemaChangedError{Tool: *selected}
	}
	if selected.SchemaError != "" {
		return nil, clioutput.Usage(selected.SchemaError, "Inspect the live schema; this tool cannot be invoked through the TUI until its schema is supported.")
	}
	arguments := invocation.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	// Clone before validation and dispatch so subsequent form edits cannot
	// change the argument object authorized by the caller.
	argumentJSON, err := canonicalJSON(arguments)
	if err != nil {
		return nil, clioutput.Usage("arguments must be a JSON object", "Review the input values.")
	}
	decoder := json.NewDecoder(bytes.NewReader(argumentJSON))
	decoder.UseNumber()
	arguments = nil
	if err := decoder.Decode(&arguments); err != nil {
		return nil, err
	}
	if err := NormalizeInputNumbers(arguments); err != nil {
		return nil, clioutput.Usage(err.Error(), "Use a supported JSON number without losing precision.")
	}
	if err := selected.InputSchema.ValidateInput(arguments); err != nil {
		return nil, err
	}
	guarded := mcpclient.WithDispatchGuard(ctx, func() error { return s.checkGeneration(version) })
	execution, err := callToolExecution(guarded, s.client, s.profile, invocation.Tool, arguments)
	if err != nil {
		if errors.Is(err, mcpclient.ErrUnauthorized) {
			return nil, s.sessionError(err)
		}
		if execution == nil && !selected.ReadOnly && !errors.Is(err, mcpclient.ErrNotDispatched) {
			return nil, &OutcomeUnknownError{Tool: invocation.Tool, Cause: err}
		}
		return execution, err
	}
	if execution.Preview != nil && execution.Preview.PreviewID != "" {
		previewJSON, err := json.Marshal(execution.Preview)
		if err != nil {
			return execution, fmt.Errorf("preserve exact preview: %w", err)
		}
		var original mcpclient.ToolOutcome
		if err := json.Unmarshal(previewJSON, &original); err != nil {
			return execution, fmt.Errorf("preserve exact preview: %w", err)
		}
		s.mu.Lock()
		if !s.closed && s.version == version {
			s.nextID++
			// The exact ID is stored separately from the rendered result, which
			// callers may inspect or copy without changing confirmation input.
			s.previews[s.nextID] = pendingPreview{id: original.PreviewID, origin: *selected, argumentDigest: sha256.Sum256(argumentJSON), preview: &original, generation: version}
			execution.PreviewHandle = PreviewHandle{owner: s, id: s.nextID}
		}
		s.mu.Unlock()
	}
	return execution, nil
}

// NormalizeInputNumbers prepares a decoded JSON object for schema validation.
// The validator recognizes int/uint/float JSON numbers, but currently treats a
// json.Number as a string during its type check. Preserve large integer inputs
// rather than converting every number through float64 during the defensive copy.
func NormalizeInputNumbers(object map[string]any) error {
	for name, value := range object {
		normalized, err := normalizeNumberValue(value)
		if err != nil {
			return err
		}
		object[name] = normalized
	}
	return nil
}

func normalizeNumberValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		text := value.String()
		if !strings.ContainsAny(text, ".eE") {
			if integer, err := value.Int64(); err == nil {
				return integer, nil
			}
			if integer, err := strconv.ParseUint(value.String(), 10, 64); err == nil {
				return integer, nil
			}
			return nil, errors.New("JSON integer is outside the supported 64-bit range")
		}
		// Bound arbitrary-precision work independently of the editor's input
		// bound; enormous exponents must not allocate enormous big integers.
		if len(text) > 1024 {
			return nil, errors.New("JSON number is outside the supported range")
		}
		if exponentAt := strings.IndexAny(text, "eE"); exponentAt >= 0 {
			exponent, err := strconv.Atoi(text[exponentAt+1:])
			if err != nil || exponent > 400 || exponent < -400 {
				return nil, errors.New("JSON number is outside the supported range")
			}
		}
		decimal, err := value.Float64()
		if err != nil {
			return nil, errors.New("JSON number is outside the supported range")
		}
		exact, ok := new(big.Rat).SetString(text)
		if !ok {
			return nil, errors.New("JSON number is not supported")
		}
		if exact.IsInt() {
			if exact.Num().IsInt64() {
				return exact.Num().Int64(), nil
			}
			if exact.Num().IsUint64() {
				return exact.Num().Uint64(), nil
			}
			return nil, errors.New("JSON integer is outside the supported 64-bit range")
		}
		encoded, _ := json.Marshal(decimal)
		roundTripped, ok := new(big.Rat).SetString(string(encoded))
		if !ok || exact.Cmp(roundTripped) != 0 {
			return nil, errors.New("JSON number would lose precision")
		}
		return decimal, nil
	case map[string]any:
		return value, NormalizeInputNumbers(value)
	case []any:
		for index, item := range value {
			normalized, err := normalizeNumberValue(item)
			if err != nil {
				return nil, err
			}
			value[index] = normalized
		}
	}
	return value, nil
}

func (s *liveSession) Confirm(ctx context.Context, handle PreviewHandle) (*ToolExecution, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrSessionClosed
	}
	if s.authChanged {
		s.mu.Unlock()
		return nil, ErrSessionChanged
	}
	pending, ok := s.previews[handle.id]
	if handle.owner != s || !ok || pending.id == "" || pending.generation != s.version {
		s.mu.Unlock()
		return nil, ErrInvalidPreview
	}
	// Consume before any network activity, including capability refresh. A
	// second click or uncertain response can never replay this confirmation.
	delete(s.previews, handle.id)
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tools, err := s.client.ListTools(ctx)
	if err != nil {
		return nil, s.sessionError(err)
	}
	var origin, confirmation *mcp.Tool
	for _, tool := range tools {
		if tool != nil && tool.Name == pending.origin.Name {
			origin = tool
		}
		if tool != nil && tool.Name == "confirm_action" {
			confirmation = tool
		}
	}
	if origin == nil {
		return nil, unavailableTool(pending.origin.Name, s.profile)
	}
	if confirmation == nil {
		return nil, unavailableTool("confirm_action", s.profile)
	}
	confirmSchema := s.describeTool(confirmation)
	if confirmSchema.SchemaError != "" {
		return nil, fmt.Errorf("confirmation schema is not executable: %s", confirmSchema.SchemaError)
	}
	if err := confirmSchema.InputSchema.ValidateInput(map[string]any{"preview_id": pending.id}); err != nil {
		return nil, fmt.Errorf("confirmation schema changed; obtain and review a new preview: %w", err)
	}
	current := s.describeTool(origin)
	if current.SchemaRevision != pending.origin.SchemaRevision {
		return nil, &SchemaChangedError{Tool: current}
	}
	if err := s.checkGeneration(pending.generation); err != nil {
		return nil, err
	}
	guarded := mcpclient.WithDispatchGuard(ctx, func() error { return s.checkGeneration(pending.generation) })
	execution := &ToolExecution{Profile: s.profile, Preview: pending.preview, Result: pending.preview}
	confirmed, err := confirmToolExecution(guarded, s.client, execution, pending.id)
	if errors.Is(err, mcpclient.ErrUnauthorized) {
		return execution, s.sessionError(err)
	}
	if err != nil && execution.Confirmation == nil && !errors.Is(err, mcpclient.ErrNotDispatched) {
		return execution, &OutcomeUnknownError{Tool: "confirm_action", Cause: err}
	}
	return confirmed, err
}

func (s *liveSession) liveResources(ctx context.Context) ([]*mcp.Resource, []*mcp.ResourceTemplate, []string, error) {
	var warnings []string
	resources, err := s.client.ListResources(ctx)
	if err != nil {
		if !unsupportedMethod(err) {
			return nil, nil, nil, s.sessionError(err)
		}
		resources = nil
		warnings = append(warnings, "The gateway does not support resources/list; no concrete resources are advertised.")
	}
	templates, err := s.client.ListResourceTemplates(ctx)
	if err != nil {
		if !unsupportedMethod(err) {
			return nil, nil, nil, s.sessionError(err)
		}
		templates = nil
		warnings = append(warnings, "The gateway does not support resources/templates/list; resource-template discovery is incomplete.")
	}
	resources = append([]*mcp.Resource(nil), resources...)
	templates = append([]*mcp.ResourceTemplate(nil), templates...)
	// Snapshot descriptions enrich only exact, live-advertised identifiers.
	concrete := make([]*mcp.Resource, 0, len(resources))
	legacyTemplates := false
	for _, resource := range resources {
		if resource == nil || resource.URI == "" {
			return nil, nil, nil, errors.New("gateway returned an invalid resource")
		}
		copy := *resource
		for _, embedded := range s.snapshot.Resources {
			if copy.URI == embedded.URI && copy.Description == "" {
				copy.Description = embedded.Description
			}
		}
		if s.reviewedLegacyTemplate(copy.URI) {
			legacyTemplates = true
			exists := false
			for _, template := range templates {
				if template != nil && template.URITemplate == copy.URI {
					exists = true
					break
				}
			}
			if !exists {
				templates = append(templates, &mcp.ResourceTemplate{
					Name: copy.Name, Title: copy.Title, Description: copy.Description,
					MIMEType: copy.MIMEType, URITemplate: copy.URI, Annotations: copy.Annotations,
				})
			}
		} else {
			concrete = append(concrete, &copy)
		}
	}
	resources = concrete
	if legacyTemplates {
		warnings = append(warnings, "The gateway advertises reviewed URI templates through resources/list; these live legacy entries are shown as resource templates.")
	}
	for i, template := range templates {
		if template == nil || template.URITemplate == "" {
			return nil, nil, nil, errors.New("gateway returned an invalid resource template")
		}
		copy := *template
		for _, embedded := range s.snapshot.Resources {
			if copy.URITemplate == embedded.URI && copy.Description == "" {
				copy.Description = embedded.Description
			}
		}
		templates[i] = &copy
	}
	return resources, templates, warnings, nil
}

func (s *liveSession) reviewedLegacyTemplate(uri string) bool {
	parsed, err := uritemplate.New(uri)
	if err != nil || len(parsed.Varnames()) == 0 {
		return false
	}
	for _, embedded := range s.snapshot.Resources {
		if embedded.URI != uri {
			continue
		}
		for _, mapping := range s.manifest.Resources {
			if mapping.Resource == embedded.Name && mapping.URITemplate == uri {
				return true
			}
		}
	}
	return false
}

func unsupportedMethod(err error) bool {
	var rpcError *jsonrpc.Error
	return errors.As(err, &rpcError) && rpcError.Code == jsonrpc.CodeMethodNotFound
}

func (s *liveSession) ReadResource(ctx context.Context, uri string) (*mcpclient.ResourceOutcome, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	version, err := s.generation()
	if err != nil {
		return nil, err
	}
	resources, templates, _, err := s.liveResources(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkGeneration(version); err != nil {
		return nil, err
	}
	available := false
	for _, resource := range resources {
		available = available || resource.URI == uri
	}
	for _, resource := range templates {
		template, err := uritemplate.New(resource.URITemplate)
		if err == nil && template.Match(uri) != nil {
			available = true
		}
	}
	if !available {
		return nil, clioutput.Forbidden(fmt.Sprintf("resource %q is not advertised for profile %q", uri, s.profile))
	}
	guarded := mcpclient.WithDispatchGuard(ctx, func() error { return s.checkGeneration(version) })
	outcome, err := s.client.ReadResource(guarded, uri)
	return outcome, s.sessionError(err)
}
