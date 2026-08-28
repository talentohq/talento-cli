// Package tui is the human, terminal-only front end to Talento's authenticated
// app sessions. It neither shells out to CLI commands nor interprets server text.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	baseoutput "github.com/basecamp/cli/output"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	"github.com/talentohq/talento-cli/internal/tui/form"
)

// Options contains the process-local adapters provided by the command layer.
// Selecting a profile does not change the persisted default. Login is invoked
// only after the user chooses Sign in; its callback displays the OAuth URL.
type Options struct {
	Profile     string
	Stdin       io.Reader
	Stdout      io.Writer
	Profiles    func() ([]string, error)
	OpenSession func(context.Context, string) (app.Session, error)
	Login       func(context.Context, string, func(string)) error
}

// Run restores the terminal before returning any error to the command layer.
// The caller must enforce real-TTY, flags, and project-trust checks first.
func Run(ctx context.Context, options Options) error {
	if options.OpenSession == nil {
		return errors.New("TUI requires an authenticated session opener")
	}
	if options.Profile == "" {
		return errors.New("TUI requires an explicit profile")
	}
	ctx, cancel := context.WithCancel(ctx)
	registry := &sessionRegistry{sessions: make(map[uint64]app.Session)}
	defer func() {
		cancel()
		registry.closeAll()
	}()
	m := newModel(ctx, options, registry)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if options.Stdin != nil {
		programOptions = append(programOptions, tea.WithInput(options.Stdin))
	}
	if options.Stdout != nil {
		programOptions = append(programOptions, tea.WithOutput(options.Stdout))
	}
	program := tea.NewProgram(m, programOptions...)
	m.emit = program.Send
	_, err := program.Run()
	return err
}

// The registry also owns candidates returned after the user has quit. Adding a
// late candidate closes it immediately, instead of leaking a live MCP session.
type sessionRegistry struct {
	mu       sync.Mutex
	next     uint64
	closed   bool
	sessions map[uint64]app.Session
}

func (r *sessionRegistry) add(session app.Session) uint64 {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = session.Close()
		return 0
	}
	r.next++
	id := r.next
	r.sessions[id] = session
	r.mu.Unlock()
	return id
}

func (r *sessionRegistry) close(id uint64) {
	r.mu.Lock()
	session := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}

func (r *sessionRegistry) closeAll() {
	r.mu.Lock()
	r.closed = true
	sessions := r.sessions
	r.sessions = make(map[uint64]app.Session)
	r.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
}

type page uint8

const (
	pageConnecting page = iota
	pageAuth
	pageWorkspace
	pageActions
	pageForm
	pageReview
	pageResult
)

type overlay uint8

const (
	overlayNone overlay = iota
	overlayPalette
	overlayProfiles
	overlayHelp
	overlaySchema
	overlayDiscard
	overlayQuit
)

type model struct {
	ctx      context.Context
	options  Options
	registry *sessionRegistry
	emit     func(tea.Msg)

	profile, pendingProfile string
	session                 app.Session
	sessionID, generation   uint64
	catalogue               *app.Catalogue
	entries                 []entry
	groups                  []group
	recent                  []string

	page         page
	overlay      overlay
	navFocus     bool
	navIndex     int
	selection    int
	query        string
	profiles     []string
	active       *entry
	form         form.Model
	arguments    map[string]any
	result       *app.ToolExecution
	failedResult *app.ToolExecution
	resource     *mcpclient.ResourceOutcome
	resultJSON   bool
	stale        bool
	unknown      bool
	unknownText  string
	button       int
	viewport     viewport.Model
	status       string
	formError    string
	discardTo    string
	keepDraft    bool

	width, height              int
	dark, noColor              bool
	connectSequence            uint64
	connectCancel              context.CancelFunc
	connecting, authenticating bool
	loginURL                   string
	authTarget                 string
	frozen                     bool
	requestSequence            uint64
	requestCancel              context.CancelFunc
	busy, writing              bool
	profilesSequence           uint64
}

func newModel(ctx context.Context, options Options, registry *sessionRegistry) *model {
	m := &model{ctx: ctx, options: options, registry: registry, profile: options.Profile,
		page: pageConnecting, width: 80, height: 24, dark: true, noColor: os.Getenv("NO_COLOR") != "",
		viewport: viewport.New(), emit: func(tea.Msg) {}}
	m.viewport.SoftWrap = true
	m.resize()
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.connect(m.profile), tea.RequestBackgroundColor)
}

type connectionMsg struct {
	sequence  uint64
	profile   string
	session   app.Session
	sessionID uint64
	catalogue *app.Catalogue
	err       error
}

type loginMsg struct {
	sequence uint64
	profile  string
	err      error
}

type loginURLMsg struct {
	sequence uint64
	url      string
}

type profilesMsg struct {
	sequence uint64
	profiles []string
	err      error
}

type catalogueMsg struct {
	generation, sequence uint64
	catalogue            *app.Catalogue
	err                  error
}

type executionMsg struct {
	generation, sequence uint64
	execution            *app.ToolExecution
	resource             *mcpclient.ResourceOutcome
	err                  error
}

func (m *model) connect(profile string) tea.Cmd {
	m.cancelRead()
	m.invalidatePreviews()
	if m.connectCancel != nil {
		m.connectCancel()
	}
	m.connectSequence++
	sequence := m.connectSequence
	ctx, cancel := context.WithCancel(m.ctx)
	m.connectCancel = cancel
	m.connecting = true
	m.pendingProfile = profile
	m.status = "Connecting to " + profile + "…"
	open, registry := m.options.OpenSession, m.registry
	return func() tea.Msg {
		session, err := open(ctx, profile)
		message := connectionMsg{sequence: sequence, profile: profile, session: session, err: err}
		if err != nil {
			if session != nil {
				_ = session.Close()
				message.session = nil
			}
			return message
		}
		if session == nil {
			message.err = errors.New("session opener returned no session")
			return message
		}
		if session.Profile() != profile {
			_ = session.Close()
			message.session = nil
			message.err = errors.New("session opener returned a different profile")
			return message
		}
		message.sessionID = registry.add(session)
		if message.sessionID == 0 {
			message.err = context.Canceled
			return message
		}
		message.catalogue, message.err = session.Catalogue(ctx)
		if message.err == nil && message.catalogue == nil {
			message.err = errors.New("server returned no capability catalogue")
		}
		if message.err != nil || ctx.Err() != nil {
			registry.close(message.sessionID)
			message.session = nil
			if message.err == nil {
				message.err = ctx.Err()
			}
		}
		return message
	}
}

func (m *model) login() tea.Cmd {
	if m.authenticating || m.options.Login == nil {
		return nil
	}
	m.cancelRead()
	m.invalidatePreviews()
	if m.authTarget == m.profile {
		m.frozen = true
	}
	if m.connectCancel != nil {
		m.connectCancel()
	}
	m.connectSequence++
	sequence, profile := m.connectSequence, m.authTarget
	ctx, cancel := context.WithCancel(m.ctx)
	m.connectCancel = cancel
	m.authenticating = true
	m.loginURL = ""
	m.status = "Waiting for TalentoHQ sign-in…"
	login, emit := m.options.Login, m.emit
	return func() tea.Msg {
		err := login(ctx, profile, func(url string) { emit(loginURLMsg{sequence: sequence, url: url}) })
		return loginMsg{sequence: sequence, profile: profile, err: err}
	}
}

func (m *model) request() (context.Context, uint64, uint64) {
	if m.requestCancel != nil {
		m.requestCancel()
	}
	m.requestSequence++
	ctx, cancel := context.WithCancel(m.ctx)
	m.requestCancel = cancel
	m.busy = true
	return ctx, m.generation, m.requestSequence
}

func (m *model) cancelRead() {
	if m.writing {
		return
	}
	if m.requestCancel != nil {
		m.requestCancel()
		m.requestCancel = nil
	}
	m.requestSequence++
	m.busy = false
}

func (m *model) invalidatePreviews() {
	if m.session != nil {
		m.session.InvalidatePreviews()
	}
	if m.result != nil {
		m.result.PreviewHandle = app.PreviewHandle{}
	}
	m.button = 0
}

func (m *model) refreshCatalogue() tea.Cmd {
	if m.session == nil || m.frozen || m.connecting || m.writing {
		return nil
	}
	m.invalidatePreviews()
	ctx, generation, sequence := m.request()
	session := m.session
	m.status = "Refreshing available actions…"
	return func() tea.Msg {
		catalogue, err := session.Catalogue(ctx)
		return catalogueMsg{generation: generation, sequence: sequence, catalogue: catalogue, err: err}
	}
}

func (m *model) invoke() tea.Cmd {
	if m.session == nil || m.active == nil || m.busy || m.connecting || m.frozen {
		return nil
	}
	m.invalidatePreviews()
	ctx, generation, sequence := m.request()
	session, active := m.session, *m.active
	arguments := m.arguments
	m.writing = active.kind == toolEntry && !active.tool.ReadOnly
	m.status = "Reading…"
	if m.writing {
		m.status = "Submitting action. Do not retry until the outcome is known."
	}
	m.unknown = false
	return func() tea.Msg {
		message := executionMsg{generation: generation, sequence: sequence}
		if active.kind == toolEntry {
			message.execution, message.err = session.Invoke(ctx, app.Invocation{
				Tool: active.tool.Name, SchemaRevision: active.tool.SchemaRevision, Arguments: arguments,
			})
		} else {
			uri, err := resourceURI(active, arguments)
			if err != nil {
				message.err = err
				return message
			}
			message.resource, message.err = session.ReadResource(ctx, uri)
		}
		return message
	}
}

func (m *model) confirm() tea.Cmd {
	if m.session == nil || m.result == nil || !m.result.PreviewHandle.Valid() || m.busy || m.frozen || m.connecting {
		return nil
	}
	handle := m.result.PreviewHandle
	// Clear the UI handle before dispatch. The app additionally enforces atomic
	// single-use consumption; repeated Enter can never submit this twice.
	m.result.PreviewHandle = app.PreviewHandle{}
	ctx, generation, sequence := m.request()
	session := m.session
	m.writing = true
	m.button = 0
	m.status = "Confirming the exact server preview. Do not retry."
	return func() tea.Msg {
		execution, err := session.Confirm(ctx, handle)
		return executionMsg{generation: generation, sequence: sequence, execution: execution, err: err}
	}
}

func isAuthError(err error) bool {
	var outputError *baseoutput.Error
	return errors.As(err, &outputError) && outputError.Code == baseoutput.CodeAuth
}

func (m *model) authError(err error, profile string) {
	m.authTarget = profile
	m.page = pageAuth
	m.overlay = overlayNone
	m.status = err.Error()
	m.loginURL = ""
	m.invalidatePreviews()
	if profile == m.profile {
		m.frozen = true
	}
}

func closeSession(registry *sessionRegistry, id uint64) tea.Cmd {
	if id == 0 {
		return nil
	}
	return func() tea.Msg { registry.close(id); return nil }
}

func (m *model) applyCatalogue(catalogue *app.Catalogue) {
	m.catalogue = catalogue
	m.entries, m.groups = catalogueEntries(catalogue)
	if m.navIndex > len(m.groups) {
		m.navIndex = 0
	}
	m.selection = 0
}

func (m *model) rememberActive() {
	if m.active == nil {
		return
	}
	recent := []string{m.active.id}
	for _, id := range m.recent {
		if id != m.active.id && len(recent) < 5 {
			recent = append(recent, id)
		}
	}
	m.recent = recent
}

func (m *model) openProfiles() tea.Cmd {
	if m.writing || m.connecting || m.authenticating {
		m.status = "Wait for the current operation before switching profiles."
		return nil
	}
	m.cancelRead()
	m.overlay, m.selection = overlayProfiles, 0
	m.profiles = nil
	m.profilesSequence++
	sequence, list := m.profilesSequence, m.options.Profiles
	if list == nil {
		m.profiles = []string{m.profile}
		return nil
	}
	return func() tea.Msg {
		profiles, err := list()
		return profilesMsg{sequence: sequence, profiles: profiles, err: err}
	}
}

func (m *model) unavailable() error {
	if m.frozen {
		return errors.New("sign in again before using this profile")
	}
	if m.connecting || m.authenticating {
		return errors.New("wait for the connection to finish")
	}
	if m.session == nil {
		return fmt.Errorf("profile %q is not connected", m.profile)
	}
	return nil
}
