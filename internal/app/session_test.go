package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	baseoutput "github.com/basecamp/cli/output"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/talentohq/talento-cli/internal/auth"
	"github.com/talentohq/talento-cli/internal/config"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	"github.com/talentohq/talento-cli/internal/schema"
)

type fakeSessionClient struct {
	tools         []*mcp.Tool
	resources     []*mcp.Resource
	templates     []*mcp.ResourceTemplate
	toolsErr      error
	resourcesErr  error
	templatesErr  error
	callErr       error
	readErr       error
	result        *mcpclient.ToolOutcome
	calls         []string
	arguments     []map[string]any
	reads         []string
	listCalls     int
	closed        int
	listHook      func()
	callHook      func()
	resourcesHook func()
}

func (f *fakeSessionClient) ListTools(context.Context) ([]*mcp.Tool, error) {
	f.listCalls++
	if f.listHook != nil {
		f.listHook()
	}
	return f.tools, f.toolsErr
}
func (f *fakeSessionClient) CallTool(_ context.Context, name string, arguments map[string]any) (*mcpclient.ToolOutcome, error) {
	f.calls = append(f.calls, name)
	f.arguments = append(f.arguments, arguments)
	if f.callHook != nil {
		f.callHook()
	}
	return f.result, f.callErr
}
func (f *fakeSessionClient) ListResources(context.Context) ([]*mcp.Resource, error) {
	if f.resourcesHook != nil {
		f.resourcesHook()
	}
	return f.resources, f.resourcesErr
}
func (f *fakeSessionClient) ListResourceTemplates(context.Context) ([]*mcp.ResourceTemplate, error) {
	return f.templates, f.templatesErr
}
func (f *fakeSessionClient) ReadResource(_ context.Context, uri string) (*mcpclient.ResourceOutcome, error) {
	f.reads = append(f.reads, uri)
	if f.readErr != nil {
		return nil, f.readErr
	}
	return &mcpclient.ResourceOutcome{URI: uri, Result: &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, Text: "live data"}}}}, nil
}
func (f *fakeSessionClient) Close() error { f.closed++; return nil }

func sessionTool(name, raw string, readOnly bool) *mcp.Tool {
	destructive, openWorld := !readOnly, false
	return &mcp.Tool{
		Name: name, Description: "live description", InputSchema: json.RawMessage(raw),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, OpenWorldHint: &openWorld},
	}
}

func reviewedSession(t *testing.T, client *fakeSessionClient) *liveSession {
	t.Helper()
	snapshot := schema.Snapshot{}
	for _, live := range client.tools {
		if live == nil {
			continue
		}
		data, err := json.Marshal(live.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var input schema.JSONSchema
		if err := json.Unmarshal(data, &input); err != nil {
			t.Fatal(err)
		}
		reviewed := schema.Tool{Name: live.Name, Description: "reviewed description", InputSchema: input}
		if live.Annotations != nil {
			reviewed.Annotations.ReadOnlyHint = live.Annotations.ReadOnlyHint
			reviewed.Annotations.IdempotentHint = live.Annotations.IdempotentHint
			if live.Annotations.DestructiveHint != nil {
				reviewed.Annotations.DestructiveHint = *live.Annotations.DestructiveHint
			}
			if live.Annotations.OpenWorldHint != nil {
				reviewed.Annotations.OpenWorldHint = *live.Annotations.OpenWorldHint
			}
		}
		snapshot.Tools = append(snapshot.Tools, reviewed)
	}
	return newSession("acme", snapshot, schema.BuildManifest(snapshot, nil), client)
}

func firstSessionTool(t *testing.T, session *liveSession) SessionTool {
	t.Helper()
	catalogue, err := session.Catalogue(context.Background())
	if err != nil || len(catalogue.Tools) != 1 {
		t.Fatalf("catalogue=%#v err=%v", catalogue, err)
	}
	return catalogue.Tools[0]
}

func TestSessionCatalogueUsesLiveAvailabilityAndReviewedShapes(t *testing.T) {
	known := sessionTool("list_employees", `{"type":"object","properties":{"page":{"type":"integer"}}}`, true)
	client := &fakeSessionClient{tools: []*mcp.Tool{known, sessionTool("confirm_action", `{"type":"object"}`, false)}}
	session := reviewedSession(t, client)
	tool := firstSessionTool(t, session)
	if !tool.Reviewed || !tool.ReadOnly || tool.Destructive || tool.Domain != "people" || tool.Command != "list" || tool.Description != "reviewed description" || tool.SchemaError != "" {
		t.Fatalf("reviewed tool = %#v", tool)
	}
	if session.Profile() != "acme" {
		t.Fatal("profile changed")
	}
	// Different JSON key ordering is not schema drift.
	known.InputSchema = json.RawMessage(`{"properties":{"page":{"type":"integer"}},"type":"object"}`)
	if reordered := firstSessionTool(t, session); !reordered.Reviewed || reordered.SchemaRevision != tool.SchemaRevision {
		t.Fatalf("key ordering changed revision: %#v", reordered)
	}
	known.InputSchema = json.RawMessage(`{"type":"object","properties":{"page":{"type":"integer","minimum":1}}}`)
	drifted := firstSessionTool(t, session)
	if drifted.Reviewed || drifted.ReadOnly || drifted.Domain != "advanced" || drifted.SchemaError != "" || drifted.SchemaRevision == tool.SchemaRevision {
		t.Fatalf("drifted tool = %#v", drifted)
	}
	client.tools = []*mcp.Tool{sessionTool("new_tool", `{"type":"object"}`, true)}
	if unknown := firstSessionTool(t, session); unknown.Reviewed || unknown.ReadOnly || unknown.Domain != "advanced" {
		t.Fatalf("unknown tool = %#v", unknown)
	}
	client.tools = nil
	if catalogue, err := session.Catalogue(context.Background()); err != nil || len(catalogue.Tools) != 0 {
		t.Fatalf("snapshot invented availability: %#v %v", catalogue, err)
	}
}

func TestSessionSafetyAnnotationsCannotOnlyDowngradeReviewedRisk(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*mcp.Tool)
	}{
		{"missing", func(tool *mcp.Tool) { tool.Annotations = nil }},
		{"read-to-write", func(tool *mcp.Tool) { tool.Annotations.ReadOnlyHint = false }},
		{"destructive conflict", func(tool *mcp.Tool) { *tool.Annotations.DestructiveHint = true }},
		{"missing destructive", func(tool *mcp.Tool) { tool.Annotations.DestructiveHint = nil }},
		{"missing open-world", func(tool *mcp.Tool) { tool.Annotations.OpenWorldHint = nil }},
		{"idempotence conflict", func(tool *mcp.Tool) { tool.Annotations.IdempotentHint = true }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			live := sessionTool("list_employees", `{"type":"object"}`, true)
			session := reviewedSession(t, &fakeSessionClient{tools: []*mcp.Tool{live}})
			before := firstSessionTool(t, session)
			mutate.fn(live)
			after := firstSessionTool(t, session)
			if after.ReadOnly || after.SchemaRevision == before.SchemaRevision {
				t.Fatalf("risk downgraded or revision unchanged: %#v", after)
			}
		})
	}
	live := sessionTool("delete_training", `{"type":"object"}`, false)
	session := reviewedSession(t, &fakeSessionClient{tools: []*mcp.Tool{live}})
	*live.Annotations.DestructiveHint = false
	live.Annotations.ReadOnlyHint = true
	if tool := firstSessionTool(t, session); !tool.Destructive || tool.ReadOnly {
		t.Fatalf("known destructive write was downgraded: %#v", tool)
	}
}

func TestSessionInvokeRechecksAvailabilityShapeAndFullValidation(t *testing.T) {
	live := sessionTool("create_task", `{"type":"object","properties":{"title":{"type":"string","minLength":2},"state":{"enum":["open","done"]}},"required":["title"],"additionalProperties":false}`, false)
	client := &fakeSessionClient{tools: []*mcp.Tool{live}, result: outcome(live.Name, mcpclient.StateCommitted, "")}
	session := reviewedSession(t, client)
	tool := firstSessionTool(t, session)
	for _, arguments := range []map[string]any{{}, {"title": "a"}, {"title": "ok", "secret": "not-for-errors"}, {"title": "ok", "state": "not-for-errors"}} {
		_, err := session.Invoke(context.Background(), Invocation{Tool: live.Name, Arguments: arguments, SchemaRevision: tool.SchemaRevision})
		if err == nil || strings.Contains(err.Error(), "not-for-errors") || len(client.calls) != 0 {
			t.Fatalf("validation error=%v calls=%v", err, client.calls)
		}
	}
	live.InputSchema = json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`)
	_, err := session.Invoke(context.Background(), Invocation{Tool: live.Name, Arguments: map[string]any{"title": "ok"}, SchemaRevision: tool.SchemaRevision})
	var changed *SchemaChangedError
	if !errors.As(err, &changed) || changed.Tool.Reviewed || len(client.calls) != 0 {
		t.Fatalf("schema drift err=%v calls=%v", err, client.calls)
	}
	if _, err := session.Invoke(context.Background(), Invocation{Tool: live.Name, Arguments: map[string]any{"title": "ok"}, SchemaRevision: changed.Tool.SchemaRevision}); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("calls=%v", client.calls)
	}
	client.tools = nil
	if _, err := session.Invoke(context.Background(), Invocation{Tool: live.Name, SchemaRevision: changed.Tool.SchemaRevision}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unavailable err=%v", err)
	}
	if _, err := session.Invoke(context.Background(), Invocation{Tool: "confirm_action"}); !errors.Is(err, ErrInvalidPreview) {
		t.Fatalf("raw confirmation err=%v", err)
	}
}

func TestSessionInputCopiesPreserveOmissionFalseZeroNullAndLargeIntegers(t *testing.T) {
	live := sessionTool("create_task", `{"type":"object","properties":{"enabled":{"type":"boolean"},"id":{"type":"integer"},"zero":{"type":"integer"},"empty":{"type":"string"},"null":{"type":["null","string"]},"list":{"type":"array","items":{"type":"integer"}},"object":{"type":"object"},"omitted":{"type":"string","default":"do not apply"}}}`, false)
	client := &fakeSessionClient{tools: []*mcp.Tool{live}, result: outcome(live.Name, mcpclient.StateCommitted, "")}
	session := reviewedSession(t, client)
	tool := firstSessionTool(t, session)
	arguments := map[string]any{"enabled": false, "zero": 0, "id": int64(9007199254740993), "empty": "", "null": nil, "list": []any{3}, "object": map[string]any{"inside": 4}}
	client.callHook = func() { arguments["enabled"] = true; arguments["object"].(map[string]any)["inside"] = 99 }
	if _, err := session.Invoke(context.Background(), Invocation{Tool: live.Name, Arguments: arguments, SchemaRevision: tool.SchemaRevision}); err != nil {
		t.Fatal(err)
	}
	got := client.arguments[0]
	if got["enabled"] != false || got["zero"] != int64(0) || got["id"] != int64(9007199254740993) || got["empty"] != "" || got["null"] != nil || got["object"].(map[string]any)["inside"] != int64(4) {
		t.Fatalf("arguments changed: %#v", got)
	}
	if _, exists := got["omitted"]; exists {
		t.Fatal("applied omitted default")
	}
	if _, exists := got["null"]; !exists {
		t.Fatal("explicit null was omitted")
	}
}

func TestSessionInvalidLiveSchemasRemainInspectableButCannotExecute(t *testing.T) {
	for _, raw := range []string{`null`, `true`, `{"type":"invalid-type"}`, `{"type":"object","$ref":"#/$defs/missing"}`} {
		t.Run(raw, func(t *testing.T) {
			client := &fakeSessionClient{tools: []*mcp.Tool{sessionTool("new_tool", raw, false)}}
			session := newSession("acme", schema.Snapshot{}, schema.Manifest{}, client)
			tool := firstSessionTool(t, session)
			if tool.SchemaError == "" || len(tool.RawSchema) == 0 || tool.Reviewed {
				t.Fatalf("invalid schema not inspectable: %#v", tool)
			}
			if _, err := session.Invoke(context.Background(), Invocation{Tool: tool.Name, SchemaRevision: tool.SchemaRevision}); err == nil || len(client.calls) != 0 {
				t.Fatalf("invalid schema executed: err=%v calls=%v", err, client.calls)
			}
		})
	}
}

func previewSession(t *testing.T, previewID string) (*liveSession, *fakeSessionClient, *ToolExecution) {
	t.Helper()
	live := sessionTool("create_task", `{"type":"object"}`, false)
	client := &fakeSessionClient{
		tools:  []*mcp.Tool{live, sessionTool("confirm_action", `{"type":"object","properties":{"preview_id":{"type":"string"}},"required":["preview_id"],"additionalProperties":false}`, false)},
		result: outcome(live.Name, mcpclient.StatePreview, previewID),
	}
	session := reviewedSession(t, client)
	tool := firstSessionTool(t, session)
	execution, err := session.Invoke(context.Background(), Invocation{Tool: live.Name, Arguments: map[string]any{"name": "original"}, SchemaRevision: tool.SchemaRevision})
	if err != nil {
		t.Fatal(err)
	}
	return session, client, execution
}

func TestSessionPreviewIsExactSingleUseAndIndependentOfMutableResult(t *testing.T) {
	session, client, execution := previewSession(t, "exact:preview-ID_01")
	if !execution.PreviewHandle.Valid() || execution.Confirmation != nil || len(client.calls) != 1 {
		t.Fatalf("preview auto-confirmed: %#v", execution)
	}
	original := session.previews[execution.PreviewHandle.id]
	if original.argumentDigest != sha256.Sum256([]byte(`{"name":"original"}`)) {
		t.Fatal("argument provenance was not bound")
	}
	execution.Preview.PreviewID = "edited-id"
	execution.Preview.Result.Content[0].(*mcp.TextContent).Text = "edited text"
	client.result = outcome("confirm_action", mcpclient.StateCommitted, "")
	confirmed, err := session.Confirm(context.Background(), execution.PreviewHandle)
	if err != nil || confirmed.Result.State != mcpclient.StateCommitted || confirmed.Confirmation == nil {
		t.Fatalf("confirmation=%#v err=%v", confirmed, err)
	}
	if got := client.arguments[1]["preview_id"]; got != "exact:preview-ID_01" || confirmed.Preview.PreviewID != got || confirmed.Preview.HumanText() != "preview" {
		t.Fatalf("preview provenance changed: %#v %#v", client.arguments, confirmed.Preview)
	}
	if _, err := session.Confirm(context.Background(), execution.PreviewHandle); !errors.Is(err, ErrInvalidPreview) || len(client.calls) != 2 {
		t.Fatalf("double confirmation err=%v calls=%v", err, client.calls)
	}
}

func TestSessionPreviewInvalidationAndIsolation(t *testing.T) {
	tests := []struct {
		name string
		act  func(*liveSession, *fakeSessionClient, *ToolExecution) error
	}{
		{"missing ID", func(session *liveSession, _ *fakeSessionClient, _ *ToolExecution) error {
			_, err := session.Confirm(context.Background(), PreviewHandle{})
			return err
		}},
		{"edited arguments", func(session *liveSession, _ *fakeSessionClient, e *ToolExecution) error {
			session.InvalidatePreviews()
			_, err := session.Confirm(context.Background(), e.PreviewHandle)
			return err
		}},
		{"other profile", func(_ *liveSession, _ *fakeSessionClient, e *ToolExecution) error {
			other := newSession("other", schema.Snapshot{}, schema.Manifest{}, &fakeSessionClient{})
			_, err := other.Confirm(context.Background(), e.PreviewHandle)
			return err
		}},
		{"same profile new session", func(_ *liveSession, _ *fakeSessionClient, e *ToolExecution) error {
			other := newSession("acme", schema.Snapshot{}, schema.Manifest{}, &fakeSessionClient{})
			_, err := other.Confirm(context.Background(), e.PreviewHandle)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, client, e := previewSession(t, "preview-1")
			if err := test.act(session, client, e); !errors.Is(err, ErrInvalidPreview) || len(client.calls) != 1 {
				t.Fatalf("err=%v calls=%v", err, client.calls)
			}
		})
	}
	session, client, e := previewSession(t, "")
	if e.PreviewHandle.Valid() || e.Result.State != mcpclient.StatePreview || len(client.calls) != 1 {
		t.Fatalf("missing ID got handle: %#v", e)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil || client.closed != 1 {
		t.Fatalf("close repeated=%d err=%v", client.closed, err)
	}
	if _, err := session.Confirm(context.Background(), e.PreviewHandle); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed err=%v", err)
	}
}

func TestSessionConcurrentConfirmationDispatchesOnlyOnce(t *testing.T) {
	session, client, e := previewSession(t, "preview-1")
	client.result = outcome("confirm_action", mcpclient.StateSubmitted, "")
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := session.Confirm(context.Background(), e.PreviewHandle); errorsSeen <- err })
	}
	wg.Wait()
	close(errorsSeen)
	var rejected int
	for err := range errorsSeen {
		if errors.Is(err, ErrInvalidPreview) {
			rejected++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if rejected != 1 || len(client.calls) != 2 {
		t.Fatalf("rejected=%d calls=%v", rejected, client.calls)
	}
}

func TestSessionConfirmationRefreshRejectsDriftAndConsumesHandle(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*liveSession, *fakeSessionClient)
	}{
		{"origin removed", func(_ *liveSession, f *fakeSessionClient) { f.tools = f.tools[1:] }},
		{"confirmation removed", func(_ *liveSession, f *fakeSessionClient) { f.tools = f.tools[:1] }},
		{"origin schema changed", func(_ *liveSession, f *fakeSessionClient) {
			f.tools[0].InputSchema = json.RawMessage(`{"type":"object","required":["name"]}`)
		}},
		{"confirmation schema changed", func(_ *liveSession, f *fakeSessionClient) {
			f.tools[1].InputSchema = json.RawMessage(`{"type":"object","required":["extra"]}`)
		}},
		{"list failed", func(_ *liveSession, f *fakeSessionClient) { f.toolsErr = errors.New("offline") }},
		{"auth changes during list", func(s *liveSession, f *fakeSessionClient) { f.listHook = s.InvalidatePreviews }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			session, client, e := previewSession(t, "preview-1")
			mutate.fn(session, client)
			if _, err := session.Confirm(context.Background(), e.PreviewHandle); err == nil || len(client.calls) != 1 {
				t.Fatalf("unsafe confirmation err=%v calls=%v", err, client.calls)
			}
			if _, err := session.Confirm(context.Background(), e.PreviewHandle); !errors.Is(err, ErrInvalidPreview) {
				t.Fatalf("failed preflight did not consume handle: %v", err)
			}
		})
	}
}

func TestSessionWriteOutcomesPreserveServerStatesAndUnknownTransportOutcomes(t *testing.T) {
	for _, state := range []mcpclient.ToolState{mcpclient.StateCommitted, mcpclient.StateSubmitted, mcpclient.StateReturned, mcpclient.StateError} {
		t.Run(string(state), func(t *testing.T) {
			live := sessionTool("create_task", `{"type":"object"}`, false)
			client := &fakeSessionClient{tools: []*mcp.Tool{live}, result: outcome(live.Name, state, "")}
			session := reviewedSession(t, client)
			tool := firstSessionTool(t, session)
			e, err := session.Invoke(context.Background(), Invocation{Tool: live.Name, SchemaRevision: tool.SchemaRevision})
			if e == nil || e.Result.State != state || len(client.calls) != 1 || (err != nil) != (state == mcpclient.StateError) {
				t.Fatalf("execution=%#v err=%v calls=%v", e, err, client.calls)
			}
			var unknown *OutcomeUnknownError
			if errors.As(err, &unknown) {
				t.Fatal("explicit server rejection classified as unknown")
			}
		})
	}
	for _, readOnly := range []bool{true, false} {
		client := &fakeSessionClient{tools: []*mcp.Tool{sessionTool("example", `{"type":"object"}`, readOnly)}, callErr: errors.New("lost response")}
		session := reviewedSession(t, client)
		tool := firstSessionTool(t, session)
		_, err := session.Invoke(context.Background(), Invocation{Tool: tool.Name, SchemaRevision: tool.SchemaRevision})
		var unknown *OutcomeUnknownError
		if errors.As(err, &unknown) == readOnly || len(client.calls) != 1 {
			t.Fatalf("readOnly=%v err=%v calls=%v", readOnly, err, client.calls)
		}
		client.callErr = fmt.Errorf("%w: local auth rejected", mcpclient.ErrNotDispatched)
		if _, err = session.Invoke(context.Background(), Invocation{Tool: tool.Name, SchemaRevision: tool.SchemaRevision}); errors.As(err, &unknown) {
			t.Fatalf("pre-dispatch error classified unknown: %v", err)
		}
	}
	session, client, e := previewSession(t, "preview-1")
	client.callErr = context.DeadlineExceeded
	confirmed, err := session.Confirm(context.Background(), e.PreviewHandle)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, context.DeadlineExceeded) || confirmed.Preview == nil || len(client.calls) != 2 {
		t.Fatalf("unknown confirmation=%#v err=%v calls=%v", confirmed, err, client.calls)
	}
	if _, err := session.Confirm(context.Background(), e.PreviewHandle); !errors.Is(err, ErrInvalidPreview) || len(client.calls) != 2 {
		t.Fatalf("unknown confirmation replayed: %v %v", err, client.calls)
	}
}

func TestSessionResourcesRequireLiveAdvertisementIncludingReviewedLegacyTemplates(t *testing.T) {
	client := &fakeSessionClient{
		resources:    []*mcp.Resource{{Name: "guide", URI: "talento://guide"}, {Name: "employees", URI: "employees:///{employee_id}"}, {Name: "unknown", URI: "unknown:///{id}"}},
		templatesErr: &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "unsupported"},
	}
	snapshot := schema.Snapshot{Resources: []schema.Resource{
		{Name: "employees", URI: "employees:///{employee_id}", Description: "Reviewed employee resource"},
		{Name: "unavailable", URI: "unavailable:///{id}"},
	}}
	session := newSession("acme", snapshot, schema.BuildManifest(snapshot, nil), client)
	catalogue, err := session.Catalogue(context.Background())
	if err != nil || len(catalogue.Resources) != 2 || len(catalogue.ResourceTemplates) != 1 || len(catalogue.Warnings) != 2 {
		t.Fatalf("catalogue=%#v err=%v", catalogue, err)
	}
	if catalogue.ResourceTemplates[0].URITemplate != "employees:///{employee_id}" || catalogue.ResourceTemplates[0].Description != "Reviewed employee resource" {
		t.Fatalf("legacy template=%#v", catalogue.ResourceTemplates[0])
	}
	for _, uri := range []string{"talento://guide", "employees:///42"} {
		if result, err := session.ReadResource(context.Background(), uri); err != nil || result.HumanText() != "live data" {
			t.Fatalf("read %s result=%#v err=%v", uri, result, err)
		}
	}
	for _, uri := range []string{"unavailable:///42", "unknown:///42", "employees:///42/secret", "https://arbitrary.example"} {
		if _, err := session.ReadResource(context.Background(), uri); err == nil {
			t.Fatalf("unadvertised URI %s was read", uri)
		}
	}
	client.resources = nil
	if _, err := session.ReadResource(context.Background(), "employees:///42"); err == nil || len(client.reads) != 2 {
		t.Fatalf("removed resource remained available: err=%v reads=%v", err, client.reads)
	}
	client.templatesErr = nil
	client.templates = []*mcp.ResourceTemplate{{Name: "live", URITemplate: "live:///{id}"}}
	if _, err := session.ReadResource(context.Background(), "live:///42"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCatalogueFailuresNeverFallBackToSnapshot(t *testing.T) {
	client := &fakeSessionClient{tools: []*mcp.Tool{sessionTool("list_employees", `{"type":"object"}`, true)}}
	session := reviewedSession(t, client)
	client.toolsErr = errors.New("offline")
	if _, err := session.Catalogue(context.Background()); err == nil {
		t.Fatal("tool failure was hidden")
	}
	client.toolsErr = nil
	client.templatesErr = errors.New("unauthorized")
	if _, err := session.Catalogue(context.Background()); err == nil {
		t.Fatal("resource auth failure was hidden")
	}
	client.templatesErr = &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound}
	client.resourcesErr = &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound}
	if catalogue, err := session.Catalogue(context.Background()); err != nil || len(catalogue.Warnings) != 2 || len(catalogue.Resources) != 0 || len(catalogue.ResourceTemplates) != 0 {
		t.Fatalf("unsupported resources=%#v err=%v", catalogue, err)
	}
	client.resourcesHook = session.InvalidatePreviews
	if _, err := session.Catalogue(context.Background()); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("mixed authentication catalogue err=%v", err)
	}
	client.resourcesHook = nil
	client.tools = []*mcp.Tool{nil}
	if _, err := session.Catalogue(context.Background()); err == nil {
		t.Fatal("invalid live tool accepted")
	}
}

type fakeAccessTokens struct {
	token    string
	err      error
	profiles []string
	hook     func()
}

func (f *fakeAccessTokens) AccessToken(_ context.Context, profile string) (string, error) {
	f.profiles = append(f.profiles, profile)
	if f.hook != nil {
		f.hook()
	}
	return f.token, f.err
}

func TestSessionTokenRenewalPreservesSessionButGrantReplacementInvalidates(t *testing.T) {
	session, client, e := previewSession(t, "preview-1")
	tokens := &fakeAccessTokens{token: "token-1"}
	grant := "registered-client-1"
	provider := session.tokenProvider(tokens, func() (string, error) { return grant, nil })
	if _, err := provider(context.Background()); err != nil {
		t.Fatal(err)
	}
	version, _ := session.generation()
	tokens.token = "renewed-access-token"
	if _, err := provider(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current, _ := session.generation(); current != version || len(session.previews) != 1 {
		t.Fatal("ordinary token renewal invalidated the session")
	}
	grant = "reauthenticated-client-2"
	if _, err := provider(context.Background()); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("replaced grant did not stop token acquisition: %v", err)
	}
	if _, err := session.Confirm(context.Background(), e.PreviewHandle); !errors.Is(err, ErrSessionChanged) || len(client.calls) != 1 {
		t.Fatalf("grant replacement retained old preview: %v %v", err, client.calls)
	}
	if !reflect.DeepEqual(tokens.profiles, []string{"acme", "acme"}) {
		t.Fatalf("token provider changed profiles: %v", tokens.profiles)
	}
	listCalls := client.listCalls
	for range 2 {
		if _, err := session.Catalogue(context.Background()); !errors.Is(err, ErrSessionChanged) {
			t.Fatalf("latched catalogue err=%v", err)
		}
		if _, err := session.Invoke(context.Background(), Invocation{Tool: "create_task"}); !errors.Is(err, ErrSessionChanged) {
			t.Fatalf("latched invocation err=%v", err)
		}
		if _, err := provider(context.Background()); !errors.Is(err, ErrSessionChanged) {
			t.Fatalf("latched token provider err=%v", err)
		}
	}
	if client.listCalls != listCalls || len(client.calls) != 1 {
		t.Fatalf("old session contacted new company: list=%d calls=%v", client.listCalls, client.calls)
	}
	session.InvalidatePreviews()
	if _, err := session.generation(); !errors.Is(err, ErrSessionChanged) {
		t.Fatal("form invalidation reset the authentication latch")
	}
	reconnected := newSession("acme", session.snapshot, session.manifest, client)
	provider = reconnected.tokenProvider(tokens, func() (string, error) { return grant, nil })
	if _, err := provider(context.Background()); err != nil {
		t.Fatalf("new explicitly opened session cannot use replacement grant: %v", err)
	}
	tokens.err = os.ErrNotExist
	if _, err := provider(context.Background()); err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("missing credential error=%v", err)
	}
}

func TestSessionCancelledAndClosedOperationsDoNotDispatch(t *testing.T) {
	client := &fakeSessionClient{tools: []*mcp.Tool{sessionTool("create_task", `{"type":"object"}`, false)}}
	session := reviewedSession(t, client)
	tool := firstSessionTool(t, session)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.Invoke(ctx, Invocation{Tool: tool.Name, SchemaRevision: tool.SchemaRevision}); !errors.Is(err, context.Canceled) || len(client.calls) != 0 {
		t.Fatalf("cancelled invoke err=%v calls=%v", err, client.calls)
	}
	client.listHook = session.InvalidatePreviews
	if _, err := session.Invoke(context.Background(), Invocation{Tool: tool.Name, SchemaRevision: tool.SchemaRevision}); !errors.Is(err, ErrSessionChanged) || len(client.calls) != 0 {
		t.Fatalf("changed grant invoke err=%v calls=%v", err, client.calls)
	}
	_ = session.Close()
	if _, err := session.Catalogue(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatal(err)
	}
	if _, err := session.ReadResource(context.Background(), "talento://guide"); !errors.Is(err, ErrSessionClosed) {
		t.Fatal(err)
	}
	if _, err := session.Invoke(context.Background(), Invocation{}); !errors.Is(err, ErrSessionClosed) {
		t.Fatal(err)
	}
}

func TestOpenSessionRequiresExplicitConfiguredProfileBeforeAuth(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("default"); err != nil {
		t.Fatal(err)
	}
	application := &App{Config: store, Global: &GlobalOptions{Profile: "default"}}
	for _, profile := range []string{"", "missing"} {
		if _, err := application.OpenSession(context.Background(), profile, false); err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("explicit profile %q error=%v", profile, err)
		}
	}
}

func TestSessionAuthenticationFailuresRequireExplicitSignInWithoutReplay(t *testing.T) {
	for _, rejected := range []error{os.ErrNotExist, fmt.Errorf("refresh rejected: %w", auth.ErrReauthenticationRequired)} {
		if err := sessionAuthenticationError("acme", rejected); baseoutput.AsError(err).Code != baseoutput.CodeAuth {
			t.Fatalf("rejected grant error is not actionable auth: %v", err)
		}
	}
	for _, transient := range []error{context.Canceled, context.DeadlineExceeded, errors.New("connection refused"), errors.New("token endpoint HTTP 503")} {
		if err := sessionAuthenticationError("acme", transient); baseoutput.AsError(err).Code == baseoutput.CodeAuth {
			t.Fatalf("transient auth request failure suggests new login: %v", err)
		}
	}
	for _, operation := range []string{"catalogue", "invoke", "confirm", "resource-list", "resource-read"} {
		t.Run(operation, func(t *testing.T) {
			session, client, e := previewSession(t, "preview-1")
			var err error
			switch operation {
			case "catalogue":
				client.toolsErr = mcpclient.ErrUnauthorized
				_, err = session.Catalogue(context.Background())
			case "invoke":
				tool := firstSessionTool(t, session)
				client.callErr = mcpclient.ErrUnauthorized
				_, err = session.Invoke(context.Background(), Invocation{Tool: tool.Name, SchemaRevision: tool.SchemaRevision})
			case "confirm":
				client.callErr = mcpclient.ErrUnauthorized
				_, err = session.Confirm(context.Background(), e.PreviewHandle)
			case "resource-list":
				client.resourcesErr = mcpclient.ErrUnauthorized
				_, err = session.ReadResource(context.Background(), "talento://guide")
			case "resource-read":
				client.resources = []*mcp.Resource{{Name: "guide", URI: "talento://guide"}}
				client.readErr = mcpclient.ErrUnauthorized
				_, err = session.ReadResource(context.Background(), "talento://guide")
			}
			var unknown *OutcomeUnknownError
			if baseoutput.AsError(err).Code != baseoutput.CodeAuth || errors.As(err, &unknown) || len(session.previews) != 0 {
				t.Fatalf("401 err=%v pending=%d", err, len(session.previews))
			}
			if len(client.calls) > 2 {
				t.Fatalf("401 replayed a write: %v", client.calls)
			}
		})
	}
}

func TestNormalizeInputNumbersPreservesExactValuesOrRejects(t *testing.T) {
	for _, test := range []struct {
		value string
		want  any
	}{
		{"9007199254740993", int64(9007199254740993)},
		{"9007199254740993.0", int64(9007199254740993)},
		{"9.007199254740993e15", int64(9007199254740993)},
		{"18446744073709551615", uint64(18446744073709551615)},
		{"18446744073709551615.0", uint64(18446744073709551615)},
		{"-42", int64(-42)},
		{"1e3", int64(1000)},
		{"0.1", 0.1},
		{"-0.125", -0.125},
	} {
		t.Run(test.value, func(t *testing.T) {
			object := map[string]any{"number": json.Number(test.value)}
			if err := NormalizeInputNumbers(object); err != nil || object["number"] != test.want {
				t.Fatalf("value=%#v want=%#v err=%v", object["number"], test.want, err)
			}
		})
	}
	for _, value := range []string{"18446744073709551616", "18446744073709551616.0", "0.10000000000000000001", "1e999999999999", "1e-1000000", "1e-400"} {
		if err := NormalizeInputNumbers(map[string]any{"number": json.Number(value)}); err == nil || strings.Contains(err.Error(), value) {
			t.Fatalf("unsupported number %s err=%v", value, err)
		}
	}
}
