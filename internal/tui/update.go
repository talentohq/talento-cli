package tui

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/tui/form"
)

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	defer m.resize()
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
	case connectionMsg:
		if msg.sequence != m.connectSequence {
			return m, closeSession(m.registry, msg.sessionID)
		}
		m.connecting = false
		m.pendingProfile = ""
		if msg.err != nil {
			m.status = "Connection failed: " + msg.err.Error()
			if isAuthError(msg.err) {
				m.authError(msg.err, msg.profile)
			} else if m.session == nil {
				m.page = pageConnecting
			}
			return m, closeSession(m.registry, msg.sessionID)
		}
		oldID := m.sessionID
		m.cancelRead()
		m.invalidatePreviews()
		m.session, m.sessionID = msg.session, msg.sessionID
		m.generation++
		m.profile = msg.profile
		m.frozen = false
		m.applyCatalogue(msg.catalogue)
		// Nothing containing another company's data survives a successful swap.
		m.active, m.arguments, m.result, m.resource, m.failedResult = nil, nil, nil, nil, nil
		m.form = form.Model{}
		m.recent, m.profiles = nil, nil
		m.query, m.loginURL, m.authTarget, m.formError, m.unknownText = "", "", "", "", ""
		m.unknown, m.stale, m.resultJSON, m.keepDraft = false, false, false, false
		m.page, m.overlay, m.navIndex, m.selection, m.button = pageWorkspace, overlayNone, 0, 0, 0
		m.navFocus = false
		m.viewport.SetContent("")
		m.status = "Connected. Choose a shortcut or press / to find an action."
		return m, closeSession(m.registry, oldID)
	case loginURLMsg:
		if msg.sequence == m.connectSequence && m.authenticating {
			m.loginURL = msg.url
			if m.overlay == overlayNone {
				m.viewport.SetContent(safeText(msg.url))
				m.viewport.GotoTop()
			}
		}
	case loginMsg:
		if msg.sequence != m.connectSequence {
			return m, nil
		}
		m.authenticating = false
		if msg.err != nil {
			m.status = "Sign-in failed: " + msg.err.Error()
			return m, nil
		}
		m.loginURL = ""
		return m, m.connect(msg.profile)
	case profilesMsg:
		if msg.sequence != m.profilesSequence || m.overlay != overlayProfiles {
			return m, nil
		}
		if msg.err != nil {
			m.status = "Cannot list profiles: " + msg.err.Error()
			return m, nil
		}
		m.profiles = nil
		seen := make(map[string]bool)
		for _, profile := range msg.profiles {
			if profile != "" && !seen[profile] {
				m.profiles = append(m.profiles, profile)
				seen[profile] = true
			}
		}
		sort.Strings(m.profiles)
		m.selection = 0
	case catalogueMsg:
		if msg.generation != m.generation || msg.sequence != m.requestSequence {
			return m, nil
		}
		m.busy = false
		if msg.err != nil {
			m.stale = true
			m.status = "Refresh failed; showing last available actions: " + msg.err.Error()
			if m.freezeChangedSession(msg.err) {
				return m, nil
			} else if isAuthError(msg.err) {
				m.authError(msg.err, m.profile)
			}
		} else if msg.catalogue == nil {
			m.status = "Refresh failed: server returned no catalogue."
			m.stale = true
		} else {
			m.applyCatalogue(msg.catalogue)
			m.stale = false
			m.status = "Available actions refreshed."
		}
	case executionMsg:
		if msg.generation != m.generation || msg.sequence != m.requestSequence {
			return m, nil
		}
		m.busy, m.writing = false, false
		if msg.err != nil {
			return m, m.executionError(msg)
		}
		if msg.execution == nil && msg.resource == nil {
			m.status = "The server returned no result."
			return m, nil
		}
		m.result, m.resource = msg.execution, msg.resource
		m.failedResult = nil
		m.stale, m.unknown, m.resultJSON = false, false, false
		m.page, m.button = pageResult, 0
		m.overlay = overlayNone
		m.status = "Result received."
		m.formError = ""
		m.rememberActive()
		m.setResultContent()
	case tea.KeyPressMsg:
		return m, m.key(msg)
	default:
		if m.page == pageForm && m.overlay == overlayNone && !m.busy {
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *model) executionError(msg executionMsg) tea.Cmd {
	var changed *app.SchemaChangedError
	if errors.As(msg.err, &changed) && m.active != nil {
		m.invalidatePreviews()
		m.active.tool = changed.Tool
		m.active.group = "Advanced / Live schema"
		m.form = form.New(changed.Tool.InputSchema, true)
		_ = m.form.SetArguments(m.arguments)
		m.keepDraft = true
		m.resize()
		m.page = pageForm
		m.formError = "The live schema changed. Your draft is preserved; inspect the new schema and review again."
		m.status = "No action was dispatched."
		return nil
	}
	if m.freezeChangedSession(msg.err) {
		return nil
	}
	var unknown *app.OutcomeUnknownError
	if errors.As(msg.err, &unknown) {
		m.invalidatePreviews()
		m.unknown = true
		m.page, m.button = pageResult, 0
		m.status = "OUTCOME UNKNOWN: the server may have executed this action. Inspect Talento before doing anything again."
		m.unknownText = m.status + "\n\n" + safeText(msg.err.Error())
		m.viewport.SetContent(m.unknownText)
		return nil
	}
	if isAuthError(msg.err) {
		m.authError(msg.err, m.profile)
		return nil
	}
	execution := msg.execution
	if execution == nil {
		var rich interface{ ErrorData() any }
		if errors.As(msg.err, &rich) {
			execution, _ = rich.ErrorData().(*app.ToolExecution)
		}
	}
	if m.page == pageResult && m.active != nil && (m.active.kind != toolEntry || m.active.tool.ReadOnly) && (m.result != nil || m.resource != nil) {
		// A failed refresh does not replace the last successful read with a
		// partial result or an error response. The raw failed response remains
		// available in the JSON inspector when the gateway supplied one.
		m.stale = true
		m.failedResult = execution
		if m.resultJSON {
			m.setResultContent()
		}
	} else if execution != nil {
		m.result, m.resource = execution, nil
		m.page = pageResult
		m.resultJSON, m.stale = false, false
		m.setResultContent()
	} else if m.page == pageResult {
		m.stale = true
	}
	m.status = "Request failed: " + msg.err.Error()
	m.formError = msg.err.Error()
	return nil
}

func (m *model) freezeChangedSession(err error) bool {
	if !errors.Is(err, app.ErrSessionChanged) && !errors.Is(err, app.ErrSessionClosed) {
		return false
	}
	m.invalidatePreviews()
	m.frozen = true
	m.page = pageConnecting
	m.overlay = overlayNone
	m.status = "Session changed or closed. Press Enter to reconnect before continuing."
	return true
}

func (m *model) key(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if (m.width < 40 || m.height < 12) && key != "ctrl+c" {
		if m.overlay != overlayQuit || m.width < 20 || m.height < 6 {
			// Never activate hidden submit controls when a terminal shrinks.
			return nil
		}
		return m.overlayKey(msg)
	}
	if key == "ctrl+c" {
		if m.overlay == overlayQuit {
			// Even a second Ctrl+C does not bypass a possible-write warning.
			m.status = "Use Tab then Enter to leave, or Escape to stay."
			return nil
		}
		if m.writing || m.hasDraft() || m.authenticating {
			if !m.writing {
				m.cancelRead()
			}
			m.overlay, m.button = overlayQuit, 0
			return nil
		}
		m.cancelRead()
		return tea.Quit
	}
	if m.overlay != overlayNone {
		return m.overlayKey(msg)
	}
	if key == "esc" {
		return m.back()
	}
	if m.writing {
		m.status = "Action in flight; wait for its outcome. Ctrl+C opens an exit warning."
		return nil
	}
	if key == "ctrl+p" {
		if m.hasDraft() {
			m.askDiscard("profiles")
			return nil
		}
		return m.openProfiles()
	}
	if m.page == pageForm {
		if key == "f2" {
			m.overlay = overlaySchema
			m.viewport.SetContent(safeText(prettyJSON(m.active.tool.RawSchema)))
			if m.active.kind != toolEntry {
				m.viewport.SetContent(safeText(m.active.uri))
			}
			return nil
		}
		if key == "ctrl+s" {
			return m.submitForm()
		}
		if m.busy {
			return nil
		}
		// Printable characters belong to inputs, including /, ?, j, and q.
		m.invalidatePreviews()
		var cmd tea.Cmd
		m.form, cmd = m.form.Update(msg)
		return cmd
	}
	if key == "?" {
		m.overlay = overlayHelp
		m.viewport.SetContent(helpText)
		return nil
	}
	if key == "/" && m.session != nil && !m.connecting && !m.frozen {
		if m.hasDraft() {
			m.askDiscard("palette")
			return nil
		}
		m.cancelRead()
		m.invalidatePreviews()
		m.overlay, m.query, m.selection = overlayPalette, "", 0
		return nil
	}
	if key == "ctrl+r" {
		if m.page == pageResult && m.active != nil {
			if m.unknown || (m.active.kind == toolEntry && !m.active.tool.ReadOnly) {
				m.status = "Writes are never refreshed or retried. Open a read action to inspect current state."
				return nil
			}
			if m.busy {
				m.cancelRead()
			}
			return m.invoke()
		}
		return m.refreshCatalogue()
	}
	switch m.page {
	case pageConnecting:
		if key == "enter" && !m.connecting {
			return m.connect(m.profile)
		}
	case pageAuth:
		if key == "enter" && !m.connecting {
			return m.login()
		}
		m.viewport, _ = m.viewport.Update(msg)
	case pageWorkspace, pageActions:
		return m.navigationKey(key)
	case pageReview:
		switch key {
		case "tab", "shift+tab", "left", "right":
			m.button = 1 - m.button
		case "enter":
			if m.button == 0 {
				m.page = pageForm
				m.invalidatePreviews()
			} else {
				return m.invoke()
			}
		default:
			m.viewport, _ = m.viewport.Update(msg)
		}
	case pageResult:
		switch key {
		case "j":
			if !m.unknown {
				m.resultJSON = !m.resultJSON
				m.setResultContent()
			}
		case "tab", "shift+tab":
			if m.result != nil && m.result.PreviewHandle.Valid() && !m.unknown {
				m.button = 1 - m.button
			}
		case "enter":
			if m.button == 1 {
				return m.confirm()
			} else if m.result != nil && m.result.Result != nil && m.result.Result.IsPreview() {
				return m.back()
			}
		default:
			m.viewport, _ = m.viewport.Update(msg)
		}
	}
	return nil
}

func (m *model) hasDraft() bool {
	return (m.page == pageForm || m.page == pageReview) && m.active != nil && (m.form.Dirty() || m.keepDraft)
}

func (m *model) askDiscard(destination string) {
	m.cancelRead()
	m.overlay, m.discardTo, m.button = overlayDiscard, destination, 0
}

func (m *model) back() tea.Cmd {
	if m.writing {
		m.status = "This write may already be running. Wait for its result; it cannot be safely canceled."
		return nil
	}
	if m.busy {
		m.cancelRead()
		m.status = "Read canceled."
		return nil
	}
	if m.connecting || m.authenticating {
		if m.connectCancel != nil {
			m.connectCancel()
		}
		m.connectSequence++
		m.connecting, m.authenticating = false, false
		m.loginURL = ""
		m.status = "Connection canceled."
		if m.session != nil && !m.frozen {
			m.page = pageWorkspace
		}
		return nil
	}
	switch m.page {
	case pageForm:
		if m.hasDraft() {
			m.askDiscard("home")
			return nil
		}
		m.leaveAction()
	case pageReview:
		m.page = pageForm
		m.invalidatePreviews()
	case pageResult:
		m.invalidatePreviews()
		if m.unknown {
			// Do not offer an immediate replay of uncertain writes by returning
			// directly to their populated submission form.
			m.leaveAction()
		} else {
			m.page = pageForm
			m.formError = ""
		}
	case pageAuth:
		if m.session != nil && !m.frozen {
			m.page = pageWorkspace
			m.status = "Still using " + m.profile + "."
		}
	case pageActions:
		m.navIndex, m.selection, m.page = 0, 0, pageWorkspace
	}
	return nil
}

func (m *model) leaveAction() {
	m.cancelRead()
	m.invalidatePreviews()
	m.active, m.arguments, m.result, m.resource, m.failedResult = nil, nil, nil, nil, nil
	m.form = form.Model{}
	m.unknown, m.stale, m.keepDraft = false, false, false
	m.formError = ""
	m.page, m.selection, m.navFocus = pageWorkspace, 0, false
	m.navIndex = 0
}

func (m *model) overlayKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "esc" {
		m.closeOverlay()
		return nil
	}
	switch m.overlay {
	case overlayQuit, overlayDiscard:
		switch key {
		case "tab", "shift+tab", "left", "right":
			m.button = 1 - m.button
		case "enter":
			if m.button == 0 {
				m.closeOverlay()
				return nil
			}
			if m.overlay == overlayQuit {
				return tea.Quit
			}
			destination := m.discardTo
			m.leaveAction()
			m.overlay = overlayNone
			if destination == "profiles" {
				return m.openProfiles()
			}
			if destination == "palette" {
				m.overlay, m.query, m.selection = overlayPalette, "", 0
			}
		}
	case overlayHelp, overlaySchema:
		if key == "?" || key == "f2" {
			m.closeOverlay()
		} else {
			m.viewport, _ = m.viewport.Update(msg)
		}
	case overlayProfiles:
		m.moveSelection(key, len(m.profiles))
		if key == "enter" && m.selection < len(m.profiles) {
			profile := m.profiles[m.selection]
			m.overlay = overlayNone
			return m.connect(profile)
		}
	case overlayPalette:
		matches := searchEntries(m.entries, m.query, m.recent)
		m.moveSelection(key, len(matches))
		switch key {
		case "enter":
			if m.selection < len(matches) {
				m.overlay = overlayNone
				m.openEntry(matches[m.selection])
			}
		case "backspace":
			_, size := utf8.DecodeLastRuneInString(m.query)
			if size > 0 {
				m.query = m.query[:len(m.query)-size]
			}
			m.selection = 0
		default:
			if msg.Text != "" {
				m.query += msg.Text
				m.selection = 0
			}
		}
	}
	return nil
}

func (m *model) closeOverlay() {
	m.overlay, m.button = overlayNone, 0
	if m.page == pageResult {
		m.setResultContent()
	} else if m.page == pageReview {
		m.viewport.SetContent(safeText(prettyJSON(m.arguments)))
		m.viewport.GotoTop()
	} else if m.page == pageAuth && m.loginURL != "" {
		m.viewport.SetContent(safeText(m.loginURL))
	}
}

func (m *model) navigationKey(key string) tea.Cmd {
	if key == "tab" || key == "shift+tab" {
		m.navFocus = !m.navFocus
		return nil
	}
	if m.navFocus {
		switch key {
		case "up", "k":
			m.navIndex = max(0, m.navIndex-1)
		case "down", "j":
			m.navIndex = min(len(m.groups), m.navIndex+1)
		case "enter", "right":
			m.navFocus = false
		}
		m.selection = 0
		m.page = pageWorkspace
		if m.navIndex > 0 {
			m.page = pageActions
		}
		return nil
	}
	entries := m.visibleEntries()
	m.moveSelection(key, len(entries))
	if key == "enter" && m.selection < len(entries) {
		m.openEntry(entries[m.selection])
	}
	return nil
}

func (m *model) moveSelection(key string, length int) {
	switch key {
	case "up", "k":
		m.selection = max(0, m.selection-1)
	case "down", "j":
		m.selection = min(max(0, length-1), m.selection+1)
	case "home":
		m.selection = 0
	case "end":
		m.selection = max(0, length-1)
	}
}

func (m *model) visibleEntries() []entry {
	if m.page == pageActions && m.navIndex > 0 && m.navIndex <= len(m.groups) {
		return m.groups[m.navIndex-1].entries
	}
	entries := shortcuts(m.entries)
	for _, id := range m.recent {
		for _, e := range m.entries {
			if e.id == id {
				entries = append(entries, e)
				break
			}
		}
	}
	return entries
}

func (m *model) openEntry(e entry) {
	if err := m.unavailable(); err != nil {
		m.status = err.Error()
		return
	}
	m.cancelRead()
	m.invalidatePreviews()
	m.active = &e
	m.result, m.resource, m.arguments, m.failedResult = nil, nil, nil, nil
	m.formError, m.status = "", ""
	m.stale, m.unknown, m.keepDraft = false, false, false
	if e.kind == toolEntry {
		m.form = form.New(e.tool.InputSchema, !e.tool.Reviewed)
		if e.tool.SchemaError != "" {
			m.formError = "This schema cannot be validated: " + e.tool.SchemaError + ". F2 inspects it; execution is disabled."
		}
	} else {
		s, err := resourceSchema(e)
		m.form = form.New(s, false)
		if err != nil {
			m.formError = err.Error()
		}
	}
	m.page, m.navFocus, m.button = pageForm, false, 0
	m.resize()
}

func (m *model) submitForm() tea.Cmd {
	if m.busy || m.active == nil {
		return nil
	}
	if err := m.unavailable(); err != nil {
		m.formError = err.Error()
		return nil
	}
	if m.active.kind == toolEntry && m.active.tool.SchemaError != "" {
		m.formError = "Execution is disabled because the live schema cannot be validated. F2 inspects the schema."
		return nil
	}
	arguments, err := m.form.Arguments()
	if err != nil {
		m.formError = err.Error()
		return nil
	}
	if m.active.kind != toolEntry {
		if _, err := resourceURI(*m.active, arguments); err != nil {
			m.formError = err.Error()
			return nil
		}
	}
	m.arguments = arguments
	m.formError = ""
	if m.active.kind == toolEntry && !m.active.tool.ReadOnly {
		m.page, m.button = pageReview, 0
		m.viewport.SetContent(safeText(prettyJSON(arguments)))
		return nil
	}
	return m.invoke()
}

func (m *model) setResultContent() {
	if m.unknown {
		m.viewport.SetContent(m.unknownText)
		return
	}
	text := ""
	if m.resultJSON {
		if m.failedResult != nil {
			text = prettyJSON(map[string]any{"last_successful_result": m.result, "refresh_error_response": m.failedResult})
		} else if m.result != nil {
			text = prettyJSON(m.result)
		} else if m.resource != nil {
			text = prettyJSON(m.resource)
		}
	} else if m.result != nil {
		text = m.result.HumanText()
	} else if m.resource != nil {
		text = m.resource.HumanText()
	}
	if strings.TrimSpace(text) == "" {
		text = "No text content was returned. Press j to inspect the complete source JSON."
	}
	m.viewport.SetContent(safeText(text))
	m.viewport.GotoTop()
}
