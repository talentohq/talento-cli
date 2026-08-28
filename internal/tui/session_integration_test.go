package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/auth"
	"github.com/talentohq/talento-cli/internal/config"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	"github.com/talentohq/talento-cli/internal/schema"
)

type memoryTransport struct{ handler http.Handler }

func (transport memoryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Never fall through to the network. This deliberately exercises the real
	// session and SDK against only an in-process handler, with synthetic tokens.
	if request.URL.String() != config.Endpoint || request.Header.Get("Authorization") != "Bearer tui-test-token" {
		return nil, fmt.Errorf("unexpected test request")
	}
	response := httptest.NewRecorder()
	transport.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

func TestRealSessionPreviewRequiresSecondApprovalAndUsesExactHandle(t *testing.T) {
	// This test must remain serial: it temporarily replaces the default HTTP
	// transport, which OpenSession wraps. Cleanup restores it after sessions close.
	t.Setenv("TALENTO_NO_KEYRING", "1")
	paths := config.Paths{ConfigDir: t.TempDir()}
	paths.ConfigFile = filepath.Join(paths.ConfigDir, "config.json")
	paths.CredentialDir = filepath.Join(paths.ConfigDir, "credentials")
	store := config.NewStore(paths.ConfigFile)
	if _, err := store.CreateProfile("alpha"); err != nil {
		t.Fatal(err)
	}
	credentials, err := auth.NewCredentialStore(paths, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.Save("alpha", auth.Credentials{
		AccessToken: "tui-test-token", TokenType: "Bearer", Scope: config.Scope,
		ExpiresAt: time.Now().Add(time.Hour), ClientID: "tui-test-client",
		Issuer: "https://test.invalid", Resource: config.Endpoint,
	}); err != nil {
		t.Fatal(err)
	}
	input := schema.JSONSchema{Type: "object", Properties: map[string]schema.Property{}, AdditionalProperties: false}
	server := mcp.NewServer(&mcp.Implementation{Name: "tui-memory-test", Version: "1"}, nil)
	var writes, confirmations atomic.Int32
	const previewID = "pv_Exact:MiXeD-123"
	const previewText = "EXACT SERVER PREVIEW\nOne employee will be created."
	server.AddTool(&mcp.Tool{Name: "create_employee", InputSchema: input}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		writes.Add(1)
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: previewText}},
			StructuredContent: map[string]any{"state": "preview", "preview_id": previewID},
		}, nil
	})
	server.AddTool(&mcp.Tool{Name: "confirm_action", InputSchema: schema.JSONSchema{
		Type: "object", Properties: map[string]schema.Property{"preview_id": {Type: "string"}}, Required: []string{"preview_id"},
	}}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments map[string]any
		if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
			return nil, err
		}
		if len(arguments) != 1 || arguments["preview_id"] != previewID {
			return nil, fmt.Errorf("confirmation did not use the exact returned ID")
		}
		confirmations.Add(1)
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "Confirmed exactly once"}},
			StructuredContent: map[string]any{"state": "committed"},
		}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	originalTransport := http.DefaultTransport
	http.DefaultTransport = memoryTransport{handler: handler}
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	application := &app.App{
		Paths: paths, Config: store,
		Snapshot: schema.Snapshot{Tools: []schema.Tool{{Name: "create_employee", InputSchema: input}}},
		Manifest: schema.Manifest{Tools: []schema.ToolMapping{{Tool: "create_employee", Domain: "people", Command: "create"}}},
	}
	registry := &sessionRegistry{sessions: make(map[uint64]app.Session)}
	t.Cleanup(registry.closeAll)
	m := newModel(context.Background(), Options{
		Profile: "alpha", OpenSession: func(ctx context.Context, profile string) (app.Session, error) {
			return application.OpenSession(ctx, profile, true)
		},
	}, registry)
	complete(t, m, m.connect("alpha"))
	if m.session == nil || len(m.entries) != 1 {
		t.Fatalf("cannot connect in-memory session: %s; entries=%v", m.status, m.entries)
	}
	m.openEntry(m.entries[0])
	press(m, "ctrl+s")
	if writes.Load() != 0 || m.page != pageReview {
		t.Fatal("write skipped local argument review")
	}
	press(m, "tab")
	complete(t, m, press(m, "enter"))
	if writes.Load() != 1 || confirmations.Load() != 0 || m.result == nil || !m.result.PreviewHandle.Valid() {
		t.Fatalf("real session did not issue a bound preview: %s", m.status)
	}
	if m.viewport.GetContent() != previewText {
		t.Fatal("server preview was not displayed exactly")
	}
	if !strings.Contains(m.View().Content, "Confirm preview") {
		t.Fatal("issued preview did not expose a separate confirmation control")
	}
	press(m, "tab")
	confirmation := press(m, "enter")
	if confirmation == nil || !m.writing || m.result.PreviewHandle.Valid() {
		t.Fatal("confirmation did not consume its UI handle before dispatch")
	}
	if duplicate := press(m, "enter"); duplicate != nil {
		t.Fatal("second Enter queued another confirmation")
	}
	complete(t, m, confirmation)
	if confirmations.Load() != 1 || m.result.Result.State != mcpclient.StateCommitted || m.result.Preview == nil {
		t.Fatalf("exact confirmation failed: %s", m.status)
	}
	press(m, "enter")
	if confirmations.Load() != 1 {
		t.Fatal("completed confirmation replayed")
	}
}
