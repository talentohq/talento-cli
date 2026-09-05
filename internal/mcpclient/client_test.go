package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolOutcomeStatesAndContentPreservation(t *testing.T) {
	tests := []struct {
		name, tool string
		text       string
		error      bool
		state      ToolState
		previewID  string
	}{
		{name: "read", tool: "list_employees", text: "Employee Ana", state: StateReturned},
		{name: "commit", tool: "create_task", text: "ACTION COMPLETED\nSaved Ana", state: StateCommitted},
		{name: "preview", tool: "create_invoice", text: "=== PREVIEW — NOT YET EXECUTED ===\npreview_id: \"pv_123\"", state: StatePreview, previewID: "pv_123"},
		{name: "approval", tool: "example", text: "Request submitted for approval", state: StateSubmitted},
		{name: "error", tool: "create_training", text: "Permission denied", error: true, state: StateError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: test.text}}, StructuredContent: map[string]any{"kept": true}, IsError: test.error}
			outcome := NewToolOutcome(test.tool, result)
			if outcome.State != test.state || outcome.PreviewID != test.previewID {
				t.Fatalf("outcome = %#v", outcome)
			}
			if outcome.Result != result || outcome.HumanText() != test.text {
				t.Fatal("original MCP result/content was not preserved")
			}
		})
	}
}

func TestToolOutcomeRecognizesPendingApprovalResults(t *testing.T) {
	tests := []struct {
		name, tool, text string
	}{
		{
			name: "absence",
			tool: "create_absence",
			text: "ACTION COMPLETED — Absence created.\nHoliday | 25 Aug 2026 - 25 Aug 2026 | 1 days | Pending",
		},
		{
			name: "expense",
			tool: "create_expense",
			text: "ACTION COMPLETED — Expense created successfully.\nTrain | Travel | 25 Aug 2026 | EUR 25.00 | Pending",
		},
		{
			name: "reschedule",
			tool: "create_reschedule",
			text: "ACTION COMPLETED — Reschedule created.\nAna\n  Dates: 25 Aug 2026\n  Status: Pending",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: test.text}}}
			if outcome := NewToolOutcome(test.tool, result); outcome.State != StateSubmitted {
				t.Fatalf("state = %q, want %q", outcome.State, StateSubmitted)
			}
		})
	}
}

func TestToolOutcomeDoesNotTreatUnrelatedPendingStatusAsApproval(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ACTION COMPLETED\nStatus: Pending"}}}
	if outcome := NewToolOutcome("create_task", result); outcome.State != StateCommitted {
		t.Fatalf("state = %q, want %q", outcome.State, StateCommitted)
	}
}

func TestToolOutcomePreservesImmediateAbsenceApproval(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ACTION COMPLETED\nHoliday | 25 Aug 2026 | Approved"}}}
	if outcome := NewToolOutcome("create_absence", result); outcome.State != StateCommitted {
		t.Fatalf("state = %q, want %q", outcome.State, StateCommitted)
	}
}

func TestToolOutcomeRecognizesTextOnlyTrainingWrites(t *testing.T) {
	tests := []struct {
		name, tool, text string
		state            ToolState
	}{
		{
			name:  "localized create",
			tool:  "create_training",
			text:  `{"message":"Formación creada","authoring_status":"draft"}`,
			state: StateCommitted,
		},
		{
			name:  "localized lifecycle write",
			tool:  "publish_training",
			text:  `{"message":"Formación publicada","authoring_status":"published"}`,
			state: StateCommitted,
		},
		{
			name:  "localized archive",
			tool:  "archive_training",
			text:  `{"message":"Formación archivada","authoring_status":"archived"}`,
			state: StateCommitted,
		},
		{
			name:  "return to draft",
			tool:  "return_training_to_draft",
			text:  `{"message":"Formación devuelta a borrador","authoring_status":"draft"}`,
			state: StateCommitted,
		},
		{
			name:  "review submission",
			tool:  "submit_training_for_review",
			text:  `{"message":"Enviada para revisión","authoring_status":"in_review"}`,
			state: StateSubmitted,
		},
		{
			name:  "read-only training result",
			tool:  "get_training",
			text:  `{"name":"Seguridad","authoring_status":"published"}`,
			state: StateReturned,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: test.text}}}
			outcome := NewToolOutcome(test.tool, result)
			if outcome.State != test.state {
				t.Fatalf("state = %q, want %q", outcome.State, test.state)
			}
			if outcome.Result != result || outcome.HumanText() != test.text {
				t.Fatal("original MCP result/content was not preserved")
			}
		})
	}
}

func TestStructuredToolStateOverridesTextFallbacks(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  ToolState
	}{
		{name: "returned", state: "returned", want: StateReturned},
		{name: "preview", state: "pending_confirmation", want: StatePreview},
		{name: "submitted", state: "pending_approval", want: StateSubmitted},
		{name: "committed", state: "persisted", want: StateCommitted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: `{"message":"resultado"}`}},
				StructuredContent: map[string]any{"state": test.state},
			}
			if outcome := NewToolOutcome("create_training", result); outcome.State != test.want {
				t.Fatalf("state = %q, want %q", outcome.State, test.want)
			}
		})
	}
}

func TestStreamableHTTPClientListsCallsAndReads(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo input"}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		Value string `json:"value"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: input.Value}}, StructuredContent: map[string]any{"value": input.Value}}, nil, nil
	})
	server.AddResource(&mcp.Resource{Name: "guide", URI: "talento://guide", MIMEType: "text/plain"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, MIMEType: "text/plain", Text: "guide text"}}}, nil
	})
	var bearer string
	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		bearer = request.Header.Get("Authorization")
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client, err := ConnectTo(context.Background(), httpServer.URL, "secret-token", httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %#v, err = %v", tools, err)
	}
	outcome, err := client.CallTool(context.Background(), "echo", map[string]any{"value": "preserved"})
	if err != nil || outcome.HumanText() != "preserved" || outcome.Result.StructuredContent == nil {
		t.Fatalf("outcome = %#v, err = %v", outcome, err)
	}
	resources, err := client.ListResources(context.Background())
	if err != nil || len(resources) != 1 {
		t.Fatalf("resources = %#v, err = %v", resources, err)
	}
	resource, err := client.ReadResource(context.Background(), "talento://guide")
	if err != nil || resource.HumanText() != "guide text" {
		t.Fatalf("resource = %#v, err = %v", resource, err)
	}
	if bearer != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", bearer)
	}
}
