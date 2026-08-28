package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	"github.com/talentohq/talento-cli/internal/terminal"
)

func safeText(value string) string { return terminal.Sanitize(value) }
func safeLine(value string) string { return terminal.SanitizeLine(value) }

func (m *model) resize() {
	width := max(1, m.width)
	bodyHeight := max(1, m.height-6)
	height := max(1, bodyHeight-3)
	if m.overlay == overlayNone && m.page == pageReview {
		height = max(1, bodyHeight-2-lipgloss.Height(m.reviewWarning(width)))
	}
	if m.overlay == overlayNone && m.page == pageAuth {
		height = max(1, bodyHeight-4)
	}
	if m.overlay == overlayNone && m.page == pageResult && m.hasPreview() {
		height = max(1, bodyHeight-3)
	}
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(height)
	if m.active != nil {
		formHeight := max(1, bodyHeight-3)
		if m.active.kind == toolEntry && !m.active.tool.Reviewed {
			formHeight--
		}
		if m.formError != "" {
			formHeight -= min(3, lipgloss.Height(ansi.Wrap(safeText(m.formError), width, "")))
		}
		m.form.SetSize(width, max(1, formHeight))
	}
}

func (m *model) emphasis(value string) string {
	style := lipgloss.NewStyle().Bold(true)
	if !m.noColor {
		style = style.Foreground(lipgloss.LightDark(m.dark)(lipgloss.Color("#005F73"), lipgloss.Color("#73DACA")))
	}
	return style.Render(value)
}

func (m *model) selected(value string, selected bool) string {
	if !selected {
		return "  " + value
	}
	return m.emphasis("> " + value)
}

func (m *model) View() tea.View {
	width, height := max(1, m.width), max(1, m.height)
	if width < 40 || height < 12 {
		message := "TalentoHQ\nTerminal too small.\nResize to 40×12 minimum;\n80×24 is recommended.\nCtrl+C quits."
		if m.overlay == overlayQuit && width >= 20 && height >= 6 {
			warning := "Unsent draft lost."
			if m.writing {
				warning = "Write may complete!"
			}
			message = "Leave TalentoHQ?\n" + warning + "\n" + m.selected("Stay", m.button == 0) + "\n" + m.selected("Leave", m.button == 1) + "\nTab then Enter.\nEsc stays."
		} else if m.writing || m.hasDraft() || m.authenticating {
			message = "TalentoHQ\nTerminal too small.\nResize to continue.\nCtrl+C: exit warning."
			if width < 20 || height < 6 {
				message = "Resize to exit safely."
			}
		}
		view := tea.NewView(fit(message, width, height))
		view.AltScreen = true
		return view
	}
	connection := "connected"
	if m.connecting {
		connection = "connecting"
	} else if m.authenticating {
		connection = "signing in"
	} else if m.frozen || m.session == nil {
		connection = "not connected"
	}
	header := m.emphasis("TalentoHQ") + "  " + safeLine(m.profile) + "  ·  " + connection
	bodyHeight := height - 6
	body := m.body(width, bodyHeight)
	status := safeLine(m.status)
	if m.busy && status == "" {
		status = "Working…"
	}
	if m.stale {
		status = "STALE · " + status
	}
	footer := m.footer()
	content := fit(header, width, 1) + "\n" + strings.Repeat("─", width) + "\n" +
		fit(body, width, bodyHeight) + "\n" + strings.Repeat("─", width) + "\n" +
		fit(status, width, 1) + "\n" + fit(footer, width, 1)
	view := tea.NewView(content)
	view.AltScreen = true
	return view
}

func (m *model) body(width, height int) string {
	if m.overlay != overlayNone {
		return m.overlayView(width, height)
	}
	switch m.page {
	case pageConnecting:
		if m.connecting {
			return "Connecting to " + safeLine(m.pendingProfile) + "…\n\nDiscovering live actions and resources. No business data is fetched at startup.\nEscape cancels the connection."
		}
		return "Could not connect\n\n" + safeText(m.status) + "\n\nEnter retries the connection. Ctrl+P chooses another profile."
	case pageAuth:
		lines := []string{m.emphasis("Sign in to TalentoHQ"), "Profile: " + safeLine(m.authTarget), "TalentoHQ account required; no AI subscription."}
		if m.authenticating {
			lines = append(lines, "Finish sign-in in your browser. URL (↑/↓ to scroll):")
			if m.loginURL != "" {
				lines = append(lines, m.viewport.View())
			}
		} else if m.options.Login == nil {
			lines = append(lines, "Sign-in is unavailable here. Exit and run talento auth login.")
		} else {
			lines = append(lines, m.selected("Sign in — press Enter", true), "", "Escape returns to the previous profile when it is still usable.")
		}
		return strings.Join(lines, "\n")
	case pageWorkspace, pageActions:
		if width >= 80 {
			left := fit(m.sidebar(), 23, height)
			right := fit(m.browseView(width-25, height), width-25, height)
			return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
		}
		nav := "Workspace"
		if m.navIndex > 0 && m.navIndex <= len(m.groups) {
			nav = m.groups[m.navIndex-1].name
		}
		if m.navFocus {
			nav = m.emphasis("Navigation: " + nav + "  ↑/↓")
		} else {
			nav = "Navigation: " + nav + "  (Tab)"
		}
		return nav + "\n" + m.browseView(width, height-1)
	case pageForm:
		if m.active == nil {
			return "Choose an action from Workspace or the palette."
		}
		heading := m.emphasis(safeLine(m.active.label())) + "  [" + m.active.badge() + "]"
		if m.active.kind == toolEntry && !m.active.tool.Reviewed {
			heading += "\nLIVE SCHEMA · unreviewed · validated JSON input"
		}
		description := ansi.Truncate(safeLine(m.active.description), width, "…")
		if m.active.kind != toolEntry {
			description = ansi.Truncate(safeLine(m.active.uri), width, "…")
		}
		content := heading + "\n" + description + "\n" + m.form.View()
		if m.formError != "" {
			content += "\n" + ansi.Wrap(safeText(m.formError), width, "")
		}
		return content
	case pageReview:
		return m.emphasis("Review exact arguments") + "\n" + m.reviewWarning(width) + "\n" +
			m.viewport.View() + "\n" + m.buttons("Back to edit", "Submit action")
	case pageResult:
		state := "RETURNED"
		if m.unknown {
			state = "OUTCOME UNKNOWN — inspect Talento before another action"
		} else if m.result != nil && m.result.Result != nil {
			state = strings.ToUpper(strings.ReplaceAll(string(m.result.Result.State), "_", " "))
		}
		if m.resultJSON {
			state += " · SOURCE JSON"
		}
		text := m.emphasis(state) + "\n" + m.viewport.View()
		if m.hasPreview() {
			if m.result.PreviewHandle.Valid() {
				text += "\n" + m.buttons("Back", "Confirm preview")
			} else {
				text += "\nPreview cannot be confirmed.\nMissing, invalid, or expired preview ID."
			}
		}
		return text
	}
	return ""
}

func (m *model) hasPreview() bool {
	return m.result != nil && m.result.Result != nil && m.result.Result.State == mcpclient.StatePreview && !m.unknown
}

func (m *model) reviewWarning(width int) string {
	warning := "This action may execute immediately. Talento decides whether it returns a preview."
	if m.active != nil && m.active.tool.Destructive {
		warning = "HIGH IMPACT · " + warning
	}
	return ansi.Wrap(warning, width, "")
}

func (m *model) sidebar() string {
	lines := []string{m.selected("Workspace", m.navFocus && m.navIndex == 0)}
	for i, group := range m.groups {
		label := group.name
		if len(label) > 22 {
			label = strings.TrimSuffix(label, " schema")
		}
		lines = append(lines, m.selected(label, m.navFocus && m.navIndex == i+1))
	}
	return strings.Join(lines, "\n")
}

func (m *model) browseView(width, height int) string {
	title := "Workspace"
	intro := "Shortcuts open a form; they never run automatically."
	if m.page == pageActions && m.navIndex > 0 && m.navIndex <= len(m.groups) {
		title = m.groups[m.navIndex-1].name
		intro = "Choose an action. Availability is controlled by Talento."
	}
	entries := m.visibleEntries()
	lines := []string{m.emphasis(title), ansi.Truncate(intro, width, "…"), ""}
	if len(entries) == 0 {
		if len(m.entries) == 0 {
			lines = append(lines, "No actions or resources are available to this profile.", "Ctrl+P switches profile; Ctrl+R checks availability.")
		} else {
			lines = append(lines, "No curated shortcuts are available.", "Press / to find an action, or Tab to browse areas.")
		}
	} else {
		rows := max(1, height-5)
		start := max(0, m.selection-rows+1)
		end := min(len(entries), start+rows)
		shortcutCount := len(shortcuts(m.entries))
		for i := start; i < end; i++ {
			e := entries[i]
			label := "[" + e.badge() + "] " + safeLine(e.label())
			if m.page == pageWorkspace && i >= shortcutCount {
				label = "Recent · " + label
			}
			lines = append(lines, m.selected(ansi.Truncate(label, max(1, width-2), "…"), !m.navFocus && i == m.selection))
		}
		if end < len(entries) || start > 0 {
			lines = append(lines, fmt.Sprintf("%d–%d of %d · ↑/↓ scroll", start+1, end, len(entries)))
		}
	}
	if m.catalogue != nil && len(m.catalogue.Warnings) > 0 {
		lines = append(lines, "", "Catalogue notice: "+safeLine(strings.Join(m.catalogue.Warnings, "; ")))
	}
	return strings.Join(lines, "\n")
}

func (m *model) buttons(back, submit string) string {
	return m.selected("["+back+"]", m.button == 0) + "  " + m.selected("["+submit+"]", m.button == 1)
}

func (m *model) overlayView(width, height int) string {
	switch m.overlay {
	case overlayQuit:
		warning := "Your edited form will be discarded."
		if m.writing {
			warning = "Write in flight. Leaving cannot cancel it. Its outcome may be unknown. Inspect Talento before retrying."
		} else if m.authenticating {
			warning = "Sign-in is still in progress. Leave and cancel waiting for it?"
		}
		return m.modalText("Leave TalentoHQ?", warning, m.buttons("Stay", "Leave"), width, height)
	case overlayDiscard:
		return m.modalText("Discard edited form?", "Your unsent values will be removed.", m.buttons("Keep editing", "Discard"), width, height)
	case overlayHelp:
		return m.emphasis("Keyboard help") + "\n\n" + m.viewport.View()
	case overlaySchema:
		return m.emphasis("Live schema / resource template") + "\n\n" + m.viewport.View()
	case overlayProfiles:
		lines := []string{m.emphasis("Switch profile — session only"), "The configured default will not change. Enter reconnects.", ""}
		if len(m.profiles) == 0 {
			lines = append(lines, "No profiles listed. Configure one with talento profile create.")
		}
		rows := max(1, height-4)
		start := max(0, m.selection-rows+1)
		for i := start; i < min(len(m.profiles), start+rows); i++ {
			label := safeLine(m.profiles[i])
			if m.profiles[i] == m.profile {
				label += " (current)"
			}
			lines = append(lines, m.selected(label, i == m.selection))
		}
		return strings.Join(lines, "\n")
	case overlayPalette:
		lines := []string{m.emphasis("Find an action"), "/ " + safeLine(m.query), ""}
		entries := searchEntries(m.entries, m.query, m.recent)
		if len(entries) == 0 {
			lines = append(lines, "No matching actions. Change the search or Escape to browse areas.")
		}
		rows := max(1, height-4)
		start := max(0, m.selection-rows+1)
		for i := start; i < min(len(entries), start+rows); i++ {
			e := entries[i]
			line := "[" + e.badge() + "] " + safeLine(e.label()) + " · " + e.group
			lines = append(lines, m.selected(ansi.Truncate(line, width-2, "…"), i == m.selection))
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

func (m *model) modalText(title, warning, controls string, width, height int) string {
	warning = ansi.Wrap(warning, width, "")
	separator := "\n"
	if lipgloss.Height(warning)+4 <= height {
		separator = "\n\n"
	}
	return m.emphasis(title) + separator + warning + separator + controls
}

func (m *model) footer() string {
	if m.overlay == overlayPalette {
		return "Type to search · ↑/↓ choose · Enter open · Esc close · Ctrl+C quit"
	}
	if m.overlay == overlayQuit || m.overlay == overlayDiscard {
		return "Tab choose · Enter activate · Esc cancel"
	}
	if m.overlay != overlayNone {
		return "↑/↓ scroll · Tab/Enter choose · Esc close · Ctrl+C quit"
	}
	if m.page == pageForm {
		return "Tab field · Ctrl+S run/review · F4 JSON · F2 schema · Esc back"
	}
	if m.page == pageReview {
		return "↑/↓ scroll · Tab choose · Enter activate · Esc edit · Ctrl+C quit"
	}
	if m.page == pageResult {
		return "↑/↓ scroll · j source JSON · Ctrl+R refresh read · / find · Esc back"
	}
	return "Tab focus · ↑/↓ choose · Enter open · / find · Ctrl+P profile · ? help"
}

const helpText = `Tab / Shift+Tab   Move navigation or form focus
↑ / ↓             Choose an item or scroll a result
Enter             Open an action; activate a selected control
/                 Find any available action or resource
Ctrl+P            Switch profile (does not change defaults)
Ctrl+R            Refresh a read or the live catalogue
Ctrl+S            Validate a form; read or review a write
F4 / Ctrl+J       Toggle form JSON editing (F4 works on legacy terminals)
Ctrl+O            Include / omit the focused form value
F2                Inspect the live schema while editing
j                 Toggle a result's complete source JSON
Esc               Back; cancel a read; never cancel a write
Ctrl+C            Quit (warns about edited forms or active writes)

Typing in forms takes precedence over single-character shortcuts.
Writes require argument review and may execute immediately.
A returned server preview requires a separate explicit confirmation.
Writes are never automatically retried or refreshed.
Attachment references are gateway handles, not local file paths.
No AI subscription is needed. No business data is cached on disk.`

// fit bounds both dimensions without splitting ANSI sequences or wide glyphs.
// It is applied after sanitization and trusted styling, never to raw data.
func fit(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
		lines[i] += strings.Repeat(" ", max(0, width-lipgloss.Width(lines[i])))
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines, "\n")
}
