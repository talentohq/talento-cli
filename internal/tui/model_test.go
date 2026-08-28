package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/schema"
)

type fakeSession struct {
	name          string
	catalogue     *app.Catalogue
	catalogueErr  error
	execution     *app.ToolExecution
	invokeErr     error
	resource      *mcpclient.ResourceOutcome
	resourceErr   error
	invocations   []app.Invocation
	readURIs      []string
	confirmations int
	catalogues    int
	invalidations int
	closed        int
}

func (s *fakeSession) Profile() string { return s.name }
func (s *fakeSession) Catalogue(context.Context) (*app.Catalogue, error) {
	s.catalogues++
	return s.catalogue, s.catalogueErr
}
func (s *fakeSession) Invoke(_ context.Context, invocation app.Invocation) (*app.ToolExecution, error) {
	s.invocations = append(s.invocations, invocation)
	return s.execution, s.invokeErr
}
func (s *fakeSession) Confirm(context.Context, app.PreviewHandle) (*app.ToolExecution, error) {
	s.confirmations++
	return s.execution, s.invokeErr
}
func (s *fakeSession) ReadResource(_ context.Context, uri string) (*mcpclient.ResourceOutcome, error) {
	s.readURIs = append(s.readURIs, uri)
	return s.resource, s.resourceErr
}
func (s *fakeSession) InvalidatePreviews() { s.invalidations++ }
func (s *fakeSession) Close() error        { s.closed++; return nil }

func testTool(name string, readOnly bool) app.SessionTool {
	s := schema.JSONSchema{Type: "object", Properties: map[string]schema.Property{}, AdditionalProperties: false}
	raw, _ := json.Marshal(s)
	return app.SessionTool{Name: name, Title: name, Domain: "people", Command: name, Description: "A test action", SchemaRevision: "v1", InputSchema: s, RawSchema: raw, Reviewed: true, ReadOnly: readOnly}
}

func result(text, state string) *app.ToolExecution {
	return &app.ToolExecution{Profile: "alpha", Result: mcpclient.NewToolOutcome("test", &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: map[string]any{"state": state},
	})}
}

func testModel(t *testing.T, tools ...app.SessionTool) (*model, *fakeSession) {
	t.Helper()
	s := &fakeSession{name: "alpha", catalogue: &app.Catalogue{Tools: tools}, execution: result("Current server result", "returned")}
	registry := &sessionRegistry{sessions: make(map[uint64]app.Session)}
	m := newModel(context.Background(), Options{Profile: "alpha", OpenSession: func(context.Context, string) (app.Session, error) { return s, nil }}, registry)
	t.Cleanup(registry.closeAll)
	complete(t, m, m.connect("alpha"))
	return m, s
}

func complete(t *testing.T, m *model, command tea.Cmd) {
	t.Helper()
	if command == nil {
		return
	}
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, child := range batch {
			complete(t, m, child)
		}
		return
	}
	_, next := m.Update(message)
	if next != nil {
		complete(t, m, next)
	}
}

func press(m *model, value string) tea.Cmd {
	key := tea.KeyPressMsg{}
	switch value {
	case "enter":
		key.Code = tea.KeyEnter
	case "esc":
		key.Code = tea.KeyEscape
	case "tab":
		key.Code = tea.KeyTab
	case "down":
		key.Code = tea.KeyDown
	case "up":
		key.Code = tea.KeyUp
	case "home":
		key.Code = tea.KeyHome
	case "end":
		key.Code = tea.KeyEnd
	case "backspace":
		key.Code = tea.KeyBackspace
	case "f2":
		key.Code = tea.KeyF2
	default:
		if strings.HasPrefix(value, "ctrl+") {
			key.Code = []rune(strings.TrimPrefix(value, "ctrl+"))[0]
			key.Mod = tea.ModCtrl
		} else {
			key.Code = []rune(value)[0]
			key.Text = value
		}
	}
	_, command := m.Update(key)
	return command
}

func TestStartupLoadsOnlyCapabilities(t *testing.T) {
	m, s := testModel(t, testTool("list_employees", true), testTool("create_employee", false))
	if m.page != pageWorkspace || s.catalogues != 1 || len(s.invocations) != 0 || len(s.readURIs) != 0 {
		t.Fatalf("unexpected startup: page=%v catalogue=%d calls=%v", m.page, s.catalogues, s.invocations)
	}
	if len(m.visibleEntries()) != 1 || m.visibleEntries()[0].tool.Name != "list_employees" {
		t.Fatal("workspace must include only available reviewed read shortcuts")
	}
}

func TestReadFormAndRefreshPreserveLastGoodResult(t *testing.T) {
	m, s := testModel(t, testTool("list_employees", true))
	press(m, "enter")
	if m.page != pageForm || len(s.invocations) != 0 {
		t.Fatal("opening a form dispatched an action")
	}
	press(m, "enter")
	if len(s.invocations) != 0 {
		t.Fatal("Enter inside form dispatched a read")
	}
	complete(t, m, press(m, "ctrl+s"))
	if m.page != pageResult || len(s.invocations) != 1 {
		t.Fatal("explicit read did not complete")
	}
	if s.invocations[0].SchemaRevision != "v1" {
		t.Fatal("displayed schema revision was lost")
	}
	if len(m.recent) != 1 {
		t.Fatal("read was not recorded in session recents")
	}
	press(m, "j")
	if !strings.Contains(m.viewport.GetContent(), "structuredContent") {
		t.Fatal("JSON inspector omitted raw result")
	}
	press(m, "j")
	s.invokeErr = errors.New("temporary network failure")
	complete(t, m, press(m, "ctrl+r"))
	if !m.stale || !strings.Contains(m.viewport.View(), "Current server result") {
		t.Fatal("refresh failure discarded last good result")
	}
}

func TestEveryWriteRequiresSeparateArgumentReview(t *testing.T) {
	for _, destructive := range []bool{false, true} {
		t.Run(fmt.Sprint(destructive), func(t *testing.T) {
			tool := testTool("create_employee", false)
			tool.Destructive = destructive
			m, s := testModel(t, tool)
			m.openEntry(m.entries[0])
			press(m, "enter")
			if len(s.invocations) != 0 {
				t.Fatal("Enter submitted form")
			}
			if cmd := press(m, "ctrl+s"); cmd != nil || m.page != pageReview {
				t.Fatal("write skipped local review")
			}
			if !strings.Contains(ansi.Strip(m.View().Content), "may execute immediately") {
				t.Fatal("missing immediate-execution warning")
			}
			press(m, "enter")
			if m.page != pageForm || len(s.invocations) != 0 {
				t.Fatal("default review control submitted")
			}
			press(m, "ctrl+s")
			press(m, "tab")
			command := press(m, "enter")
			if command == nil || !m.writing {
				t.Fatal("explicit submission did not start")
			}
			if duplicate := press(m, "enter"); duplicate != nil {
				t.Fatal("duplicate submission was dispatched")
			}
			if switchCommand := press(m, "ctrl+p"); switchCommand != nil || m.overlay == overlayProfiles {
				t.Fatal("profile switch allowed during write")
			}
			press(m, "esc")
			if !m.writing {
				t.Fatal("Escape canceled an in-flight write")
			}
			complete(t, m, command)
			if len(s.invocations) != 1 || m.page != pageResult {
				t.Fatal("write failed to complete exactly once")
			}
		})
	}
}

func TestUnknownWriteIsNotRetriedOrReturnedToPopulatedForm(t *testing.T) {
	m, s := testModel(t, testTool("create_employee", false))
	s.invokeErr = &app.OutcomeUnknownError{Tool: "create_employee", Cause: errors.New("connection lost")}
	m.openEntry(m.entries[0])
	press(m, "ctrl+s")
	press(m, "tab")
	complete(t, m, press(m, "enter"))
	if !m.unknown || m.page != pageResult || !strings.Contains(m.View().Content, "OUTCOME UNKNOWN") {
		t.Fatal("unknown write not labeled")
	}
	if command := press(m, "ctrl+r"); command != nil || len(s.invocations) != 1 {
		t.Fatal("unknown write retried")
	}
	press(m, "esc")
	if m.page != pageWorkspace || m.active != nil {
		t.Fatal("unknown write retained a replay-ready form")
	}
}

func TestMissingPreviewIDCannotConfirm(t *testing.T) {
	m, s := testModel(t, testTool("create_employee", false))
	s.execution = result("EXACT PREVIEW: review the server text", "preview")
	m.openEntry(m.entries[0])
	press(m, "ctrl+s")
	press(m, "tab")
	complete(t, m, press(m, "enter"))
	if !strings.Contains(m.viewport.View(), "EXACT PREVIEW: review the server text") {
		t.Fatal("preview text changed")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "cannot be confirmed") {
		t.Fatal("missing preview handle not explained")
	}
	press(m, "tab")
	press(m, "enter")
	if s.confirmations != 0 {
		t.Fatal("confirmed preview without valid handle")
	}
}

func TestReadCancellationIgnoresLateAndCrossSessionResults(t *testing.T) {
	m, _ := testModel(t, testTool("list_employees", true))
	m.openEntry(m.entries[0])
	command := press(m, "ctrl+s")
	message := command().(executionMsg)
	press(m, "esc")
	m.Update(message)
	if m.page == pageResult || m.result != nil {
		t.Fatal("canceled read overwrote form")
	}
	message.sequence = m.requestSequence
	message.generation--
	m.Update(message)
	if m.result != nil {
		t.Fatal("cross-session response accepted")
	}
}

func TestCandidateProfileKeepsOldUntilValidatedThenClearsData(t *testing.T) {
	m, old := testModel(t, testTool("list_employees", true))
	m.recent = []string{"tool:list_employees"}
	m.result = result("ALPHA PRIVATE DATA", "returned")
	candidate := &fakeSession{name: "beta", catalogue: &app.Catalogue{Tools: []app.SessionTool{testTool("list_invoices", true)}}}
	m.options.OpenSession = func(context.Context, string) (app.Session, error) { return candidate, nil }
	command := m.connect("beta")
	if m.session != old || m.profile != "alpha" {
		t.Fatal("old session replaced before validation")
	}
	complete(t, m, command)
	if m.profile != "beta" || m.session != candidate || old.closed != 1 {
		t.Fatal("validated candidate did not replace old")
	}
	if m.result != nil || m.active != nil || len(m.recent) != 0 || strings.Contains(m.View().Content, "ALPHA PRIVATE DATA") {
		t.Fatal("profile switch leaked data")
	}
}

func TestFailedAndStaleProfileCandidatesAreClosed(t *testing.T) {
	m, old := testModel(t, testTool("list_employees", true))
	failed := &fakeSession{name: "beta", catalogueErr: errors.New("offline")}
	m.options.OpenSession = func(context.Context, string) (app.Session, error) { return failed, nil }
	complete(t, m, m.connect("beta"))
	if m.session != old || m.profile != "alpha" || failed.closed != 1 {
		t.Fatal("failed candidate replaced old or leaked")
	}
	stale := &fakeSession{name: "beta", catalogue: &app.Catalogue{}}
	m.options.OpenSession = func(context.Context, string) (app.Session, error) { return stale, nil }
	command := m.connect("beta")
	message := command()
	press(m, "esc")
	_, cleanup := m.Update(message)
	complete(t, m, cleanup)
	if m.session != old || stale.closed != 1 {
		t.Fatal("stale candidate leaked or replaced old")
	}
}

func TestAuthIsExplicitAndGenericFailuresDoNotPromptSignIn(t *testing.T) {
	for _, failure := range []error{errors.New("resource not found"), errors.New("connection refused"), clioutput.Forbidden("permission denied")} {
		if isAuthError(failure) {
			t.Fatalf("generic failure became auth: %v", failure)
		}
	}
	m, _ := testModel(t)
	logins := 0
	m.options.Login = func(_ context.Context, profile string, show func(string)) error {
		logins++
		if profile != "alpha" {
			t.Fatalf("unexpected login profile: %s", profile)
		}
		show("https://example.invalid/oauth")
		return errors.New("authorization declined")
	}
	m.emit = func(message tea.Msg) { m.Update(message) }
	m.authError(clioutput.Auth("expired grant"), "alpha")
	if logins != 0 || !m.frozen {
		t.Fatal("auth error started login or failed to freeze")
	}
	complete(t, m, press(m, "enter"))
	if logins != 1 || m.authenticating || !strings.Contains(m.status, "authorization declined") {
		t.Fatal("explicit auth recovery failed")
	}
	m.Update(loginURLMsg{sequence: m.connectSequence - 1, url: "STALE URL"})
	if m.loginURL == "STALE URL" {
		t.Fatal("stale URL displayed")
	}
}

func TestSessionChangedRequiresExplicitReconnect(t *testing.T) {
	m, s := testModel(t, testTool("list_employees", true))
	s.invokeErr = app.ErrSessionChanged
	m.openEntry(m.entries[0])
	complete(t, m, press(m, "ctrl+s"))
	if !m.frozen || m.page != pageConnecting {
		t.Fatal("changed grant did not freeze session")
	}
	if command := press(m, "enter"); command == nil {
		t.Fatal("explicit reconnect not available")
	}
}

func TestCatalogueGrantReplacementFreezesAllFurtherCommands(t *testing.T) {
	m, s := testModel(t, testTool("list_employees", true))
	m.recent = []string{"tool:list_employees"}
	s.catalogueErr = app.ErrSessionChanged
	complete(t, m, press(m, "ctrl+r"))
	if !m.frozen || m.page != pageConnecting || m.overlay != overlayNone {
		t.Fatal("catalogue grant replacement did not freeze workspace")
	}
	m.openEntry(m.entries[0])
	if command := m.submitForm(); command != nil || len(s.invocations) != 0 || m.page != pageConnecting {
		t.Fatal("submission after grant replacement was allowed")
	}
	replacement := &fakeSession{name: "alpha", catalogue: &app.Catalogue{}}
	m.options.OpenSession = func(context.Context, string) (app.Session, error) { return replacement, nil }
	complete(t, m, press(m, "enter"))
	if m.frozen || m.active != nil || m.result != nil || len(m.recent) != 0 || m.session != replacement {
		t.Fatal("reconnection retained previous grant data")
	}
}

func TestSchemaDriftPreservesDraftAndRequiresNewReview(t *testing.T) {
	tool := testTool("create_employee", false)
	tool.InputSchema.Properties["name"] = schema.Property{Type: "string"}
	m, s := testModel(t, tool)
	m.openEntry(m.entries[0])
	if err := m.form.SetArguments(map[string]any{"name": "Draft"}); err != nil {
		t.Fatal(err)
	}
	changed := tool
	changed.SchemaRevision = "v2"
	changed.InputSchema.Required = []string{"name"}
	s.invokeErr = &app.SchemaChangedError{Tool: changed}
	press(m, "ctrl+s")
	press(m, "tab")
	complete(t, m, press(m, "enter"))
	arguments, err := m.form.Arguments()
	if err != nil || arguments["name"] != "Draft" || m.page != pageForm || !m.hasDraft() {
		t.Fatal("schema change discarded draft")
	}
	if m.active.tool.SchemaRevision != "v2" || !strings.Contains(m.formError, "review again") {
		t.Fatal("schema change not surfaced")
	}
	press(m, "esc")
	if m.overlay != overlayDiscard {
		t.Fatal("restored draft discarded without warning")
	}
}

func TestFormTypingOwnsGlobalPrintableShortcutsAndDiscardWarning(t *testing.T) {
	tool := testTool("create_employee", false)
	tool.InputSchema.Properties["name"] = schema.Property{Type: "string"}
	m, _ := testModel(t, tool)
	m.openEntry(m.entries[0])
	for _, key := range []string{"/", "?", "j", "q"} {
		press(m, key)
	}
	if m.overlay != overlayNone || !m.form.Dirty() {
		t.Fatal("form typing triggered global shortcuts")
	}
	arguments, err := m.form.Arguments()
	if err != nil || arguments["name"] != "/?jq" {
		t.Fatalf("form input lost: %#v %v", arguments, err)
	}
	press(m, "ctrl+p")
	if m.overlay != overlayDiscard {
		t.Fatal("switching profile dropped an edited form")
	}
	press(m, "enter")
	if m.page != pageForm || m.overlay != overlayNone {
		t.Fatal("default discard control lost form")
	}
	press(m, "esc")
	press(m, "tab")
	press(m, "enter")
	if m.page != pageWorkspace || m.active != nil {
		t.Fatal("explicit discard failed")
	}
}

func TestQuitWarnsForInFlightWrite(t *testing.T) {
	m, _ := testModel(t, testTool("create_employee", false))
	m.openEntry(m.entries[0])
	press(m, "ctrl+s")
	press(m, "tab")
	press(m, "enter")
	if command := press(m, "ctrl+c"); command != nil || m.overlay != overlayQuit {
		t.Fatal("quit bypassed write warning")
	}
	if command := press(m, "ctrl+c"); command != nil {
		t.Fatal("repeated Ctrl+C bypassed warning")
	}
	press(m, "tab")
	command := press(m, "enter")
	if command == nil {
		t.Fatal("explicit leave not available")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("explicit leave did not quit")
	}
}

func TestReviewHelpAndNestedQuitRestoreExactArguments(t *testing.T) {
	tool := testTool("create_employee", false)
	tool.InputSchema.Properties["name"] = schema.Property{Type: "string"}
	m, s := testModel(t, tool)
	m.openEntry(m.entries[0])
	press(m, "Alice")
	press(m, "ctrl+s")
	want := m.viewport.GetContent()
	for _, nested := range []bool{false, true} {
		press(m, "?")
		if m.overlay != overlayHelp {
			t.Fatal("review help not opened")
		}
		if nested {
			press(m, "ctrl+c")
			if m.overlay != overlayQuit {
				t.Fatal("nested quit did not warn")
			}
			press(m, "enter") // Stay is the default.
		} else {
			press(m, "?")
		}
		if m.overlay != overlayNone || m.page != pageReview || m.viewport.GetContent() != want {
			t.Fatal("closing overlay replaced reviewed arguments")
		}
	}
	if len(s.invocations) != 0 {
		t.Fatal("help interaction dispatched write")
	}
}

func TestReviewControlsStayVisibleAtMinimumSupportedSize(t *testing.T) {
	tool := testTool("create_employee", false)
	tool.Destructive = true
	m, _ := testModel(t, tool)
	m.openEntry(m.entries[0])
	press(m, "ctrl+s")
	for _, size := range [][2]int{{40, 12}, {60, 16}, {80, 24}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, "Submit action") || !strings.Contains(view, "Back to edit") {
			t.Fatalf("review controls clipped at %v:\n%s", size, view)
		}
		if !strings.Contains(view, "HIGH IMPACT") || !strings.Contains(view, "immediately") {
			t.Fatal("execution warning clipped at minimum size")
		}
	}
}

func TestTinyTerminalCannotActivateHiddenActionsAndExitWarningIsVisible(t *testing.T) {
	tool := testTool("create_employee", false)
	tool.InputSchema.Properties["name"] = schema.Property{Type: "string"}
	m, s := testModel(t, tool)
	m.openEntry(m.entries[0])
	press(m, "Draft")
	press(m, "ctrl+s")
	press(m, "tab")
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
	for _, key := range []string{"enter", "tab", "ctrl+s"} {
		if command := press(m, key); command != nil {
			t.Fatal("hidden action was activated")
		}
	}
	if len(s.invocations) != 0 {
		t.Fatal("hidden write was dispatched")
	}
	press(m, "ctrl+c")
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Leave TalentoHQ?") || !strings.Contains(view, "Stay") || !strings.Contains(view, "Leave") {
		t.Fatalf("tiny exit warning hidden:\n%s", view)
	}
	press(m, "tab")
	command := press(m, "enter")
	if command == nil {
		t.Fatal("visible tiny exit control did not work")
	}
}

func TestUnknownOutcomeSurvivesHelpOverlay(t *testing.T) {
	m, s := testModel(t, testTool("create_employee", false))
	s.invokeErr = &app.OutcomeUnknownError{Tool: "create_employee", Cause: errors.New("lost response")}
	m.openEntry(m.entries[0])
	press(m, "ctrl+s")
	press(m, "tab")
	complete(t, m, press(m, "enter"))
	want := m.viewport.GetContent()
	press(m, "?")
	press(m, "?")
	if !m.unknown || m.viewport.GetContent() != want {
		t.Fatal("unknown outcome warning lost after help")
	}
}

func TestWriteQuitControlsVisibleAtMinimumSupportedSize(t *testing.T) {
	m, _ := testModel(t, testTool("create_employee", false))
	m.openEntry(m.entries[0])
	press(m, "ctrl+s")
	press(m, "tab")
	press(m, "enter")
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	press(m, "ctrl+c")
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "[Stay]") || !strings.Contains(view, "[Leave]") || !strings.Contains(view, "unknown") || !strings.Contains(view, "cannot cancel") {
		t.Fatalf("exit controls or warning clipped:\n%s", view)
	}
}

func TestExactJSONReviewAndInspectorEscapeWithoutChangingValues(t *testing.T) {
	tool := testTool("create_employee", false)
	tool.InputSchema.Properties["name"] = schema.Property{Type: "string"}
	m, _ := testModel(t, tool)
	m.openEntry(m.entries[0])
	value := "trusted\u202eabc\u200b" + strings.Repeat("\u0301", 15) + "👩🏽‍💻"
	if err := m.form.SetArguments(map[string]any{"name": value}); err != nil {
		t.Fatal(err)
	}
	press(m, "ctrl+s")
	review := m.viewport.GetContent()
	if !strings.Contains(review, `\u202e`) || !strings.Contains(review, `\u200b`) || strings.Count(review, `\u0301`) != 15 {
		t.Fatalf("JSON review did not preserve unsafe characters as escapes: %s", review)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(review), &decoded); err != nil || decoded["name"] != value {
		t.Fatal("review differs from actual arguments")
	}
	m.result = result(value, "returned")
	m.page, m.resultJSON = pageResult, true
	m.setResultContent()
	var source app.ToolExecution
	if err := json.Unmarshal([]byte(m.viewport.GetContent()), &source); err != nil {
		t.Fatal(err)
	}
	if source.HumanText() != value {
		t.Fatal("source inspector silently removed Unicode")
	}
}

func TestResourceTemplateReadEscapesParameters(t *testing.T) {
	m, s := testModel(t)
	s.resource = &mcpclient.ResourceOutcome{URI: "talento://employees/42", Result: &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{Text: "Resource result"}}}}
	e := entry{id: "template:employees", title: "Employee", kind: templateEntry, uri: "talento://employees/{id}"}
	m.openEntry(e)
	if err := m.form.SetArguments(map[string]any{"id": "42/other?x=1"}); err != nil {
		t.Fatal(err)
	}
	complete(t, m, press(m, "ctrl+s"))
	if len(s.readURIs) != 1 || s.readURIs[0] != "talento://employees/42%2Fother%3Fx%3D1" {
		t.Fatalf("unsafe expansion: %v", s.readURIs)
	}
	if !strings.Contains(m.viewport.View(), "Resource result") {
		t.Fatal("resource result absent")
	}
}

func TestModelViewsFitAndSanitizeUntrustedText(t *testing.T) {
	tool := testTool("list_employees", true)
	tool.Description = "description\x1b]52;c;ATTACK\a\x1b[31mred\u202espoof"
	m, s := testModel(t, tool)
	m.profile = "company\x1b]8;;https://evil\a"
	m.noColor = true
	for _, size := range [][2]int{{110, 30}, {80, 24}, {60, 20}, {40, 12}, {20, 8}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View()
		if !view.AltScreen {
			t.Fatal("alternate screen not requested")
		}
		if strings.Contains(view.Content, "]52;") || strings.Contains(view.Content, "]8;") || strings.ContainsRune(view.Content, '\u202e') {
			t.Fatal("terminal escape reached view")
		}
		if lipgloss.Height(view.Content) > size[1] {
			t.Fatal("view exceeded terminal height")
		}
		for _, line := range strings.Split(view.Content, "\n") {
			if lipgloss.Width(line) > size[0] {
				t.Fatal("view exceeded terminal width")
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.openEntry(m.entries[0])
	s.execution = result("Safe\x1b]52;c;BAD\a\u202e text", "returned")
	complete(t, m, press(m, "ctrl+s"))
	if strings.Contains(m.View().Content, "]52;") || strings.ContainsRune(m.View().Content, '\u202e') {
		t.Fatal("result was not sanitized")
	}
	if !strings.Contains(s.execution.HumanText(), "]52;") {
		t.Fatal("raw result was mutated")
	}
}

func TestCatalogueGroupingFilteringAndFuzzySearch(t *testing.T) {
	confirmed := testTool("confirm_action", false)
	read := testTool("list_employees", true)
	write := testTool("create_invoice", false)
	write.Domain = "invoices"
	write.Destructive = true
	live := testTool("new_server_action", true)
	live.Reviewed = false
	entries, groups := catalogueEntries(&app.Catalogue{Tools: []app.SessionTool{confirmed, read, write, live}, Resources: []*mcp.Resource{nil, {Name: "A resource", URI: "talento://example/1"}}, ResourceTemplates: []*mcp.ResourceTemplate{{Name: "A template", URITemplate: "talento://example/{id}"}}})
	if len(entries) != 5 || len(groups) != 4 {
		t.Fatalf("unexpected catalogue: %v %v", entries, groups)
	}
	for _, e := range entries {
		if e.tool.Name == "confirm_action" {
			t.Fatal("internal confirm exposed")
		}
	}
	if got := searchEntries(entries, "list_employees", nil); len(got) != 1 || got[0].tool.Name != read.Name {
		t.Fatal("raw tool name not searchable")
	}
	if got := searchEntries(entries, "lstemp", nil); len(got) != 1 {
		t.Fatal("subsequence search failed")
	}
	if got := searchEntries(entries, "new_server_action", nil); len(got) != 1 || got[0].group != "Advanced / Live schema" {
		t.Fatal("unreviewed live action not advanced")
	}
	if got := searchEntries(entries, "nonexistent", nil); len(got) != 0 {
		t.Fatal("search returned nonexistent action")
	}
	if got := searchEntries(entries, "", []string{"tool:create_invoice"}); got[0].tool.Name != "create_invoice" {
		t.Fatal("recent not ranked first")
	}
}

func TestProfilesAreSessionOnlyAndNavigationCanReachAllGroups(t *testing.T) {
	m, _ := testModel(t, testTool("list_employees", true))
	m.options.Profiles = func() ([]string, error) { return []string{"beta", "alpha", "beta"}, nil }
	complete(t, m, press(m, "ctrl+p"))
	if !reflect.DeepEqual(m.profiles, []string{"alpha", "beta"}) || m.profile != "alpha" {
		t.Fatal("profile listing changed active selection")
	}
	press(m, "esc")
	press(m, "tab")
	press(m, "down")
	if m.page != pageActions || m.navIndex != 1 {
		t.Fatal("navigation cannot reach group")
	}
	press(m, "enter")
	press(m, "enter")
	if m.page != pageForm {
		t.Fatal("group action did not open form")
	}
	press(m, "f2")
	if m.overlay != overlaySchema {
		t.Fatal("schema inspection missing")
	}
	press(m, "esc")
	press(m, "esc")
	press(m, "?")
	if m.overlay != overlayHelp {
		t.Fatal("help missing")
	}
}

func TestRegistryClosesLateCandidatesExactlyOnce(t *testing.T) {
	registry := &sessionRegistry{sessions: make(map[uint64]app.Session)}
	session := &fakeSession{name: "alpha"}
	id := registry.add(session)
	registry.close(id)
	registry.close(id)
	if session.closed != 1 {
		t.Fatal("session closed more than once")
	}
	registry.closeAll()
	late := &fakeSession{name: "late"}
	if registry.add(late) != 0 || late.closed != 1 {
		t.Fatal("late candidate leaked")
	}
}

func TestRegistrySupportsConcurrentLateCleanup(t *testing.T) {
	registry := &sessionRegistry{sessions: make(map[uint64]app.Session)}
	var wait sync.WaitGroup
	sessions := make([]*fakeSession, 50)
	for i := range sessions {
		sessions[i] = &fakeSession{}
		wait.Add(1)
		go func(session *fakeSession) {
			defer wait.Done()
			id := registry.add(session)
			registry.close(id)
		}(sessions[i])
	}
	registry.closeAll()
	wait.Wait()
	for _, session := range sessions {
		if session.closed != 1 {
			t.Fatal("concurrent cleanup did not close exactly once")
		}
	}
}

func TestRunRequiresAdapters(t *testing.T) {
	if Run(context.Background(), Options{Profile: "alpha"}) == nil {
		t.Fatal("missing session opener accepted")
	}
	if Run(context.Background(), Options{OpenSession: func(context.Context, string) (app.Session, error) { return nil, nil }}) == nil {
		t.Fatal("missing profile accepted")
	}
}
