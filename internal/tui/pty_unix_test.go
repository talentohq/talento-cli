//go:build darwin || linux

package tui_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/mcpclient"
	"github.com/talentohq/talento-cli/internal/schema"
	"github.com/talentohq/talento-cli/internal/tui"
)

// The helper is a test process, not a production bypass for authentication or
// terminal checks. It runs the real renderer with an entirely in-memory backend.
func TestTUIProcess(t *testing.T) {
	if os.Getenv("TALENTO_TUI_PTY_HELPER") != "1" {
		return
	}
	before, err := ptyTerminalState(os.Stdin.Fd())
	if err != nil {
		t.Fatal(err)
	}
	backend := &ptySession{}
	err = tui.Run(context.Background(), tui.Options{
		Profile: "fixture",
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Profiles: func() ([]string, error) {
			return []string{"fixture"}, nil
		},
		OpenSession: func(context.Context, string) (app.Session, error) {
			if os.Getenv("TALENTO_TUI_PTY_SCENARIO") == "connection-error" {
				return nil, errors.New("fixture connection failed")
			}
			return backend, nil
		},
		Login: func(context.Context, string, func(string)) error {
			return errors.New("the PTY fixture must never authenticate")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := ptyTerminalState(os.Stdin.Fd())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal settings were not restored: before=%#v after=%#v", before, after)
	}
	if os.Getenv("TALENTO_TUI_PTY_SCENARIO") != "connection-error" && !backend.closed.Load() {
		t.Fatal("active session was not closed")
	}
	fmt.Println("TUI_EXITED_CLEANLY")
}

func TestTUIAlternateScreenReadResizeAndExit(t *testing.T) {
	process := startTUIProcess(t, "read")
	process.waitText(t, "Workspace")
	if !strings.Contains(process.output.String(), "\x1b[?1049h") {
		t.Fatal("TUI did not enter the alternate screen")
	}
	if err := pty.Setsize(process.terminal, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	process.send(t, "/")
	process.waitText(t, "Find an action")
	// Incremental terminal redraws can split the visible query with cursor
	// movement codes. Assert the resulting form, not contiguous output bytes.
	process.send(t, "list_employees\r")
	process.waitText(t, "Ctrl+S")
	process.send(t, "\x13")
	process.waitText(t, "PTY read result")
	process.send(t, "\x03")
	process.waitExit(t)
	if strings.Contains(process.output.String(), "\x1b]52;") {
		t.Fatal("server clipboard escape reached the terminal")
	}
}

func TestTUIConnectionErrorStillRestoresTerminal(t *testing.T) {
	process := startTUIProcess(t, "connection-error")
	process.waitText(t, "fixture connection failed")
	process.send(t, "\x03")
	process.waitExit(t)
}

// Release jobs set this to the extracted archive binary. This proves terminal
// startup/cleanup for the distributed executable without credentials or network
// access, rather than substituting a repository-built runtime for that binary.
func TestTUIPackagedStartup(t *testing.T) {
	binary := os.Getenv("TALENTO_TUI_BINARY")
	if binary == "" {
		t.Skip("set TALENTO_TUI_BINARY to an extracted release binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	process := startPTY(t, binary, []string{"tui"}, "packaged")
	process.waitText(t, "Sign in")
	process.send(t, "\x03")
	process.waitExit(t)
	if _, err := os.Stat(filepath.Join(process.scratch, "config", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("startup without sign-in unexpectedly wrote config: %v", err)
	}
}

type tuiProcess struct {
	terminal *os.File
	output   *ptyOutput
	done     <-chan error
	scratch  string
	helper   bool
}

func startTUIProcess(t *testing.T, scenario string) *tuiProcess {
	t.Helper()
	return startPTY(t, os.Args[0], []string{"-test.run=^TestTUIProcess$", "-test.v=false"}, scenario)
}

func startPTY(t *testing.T, executable string, arguments []string, scenario string) *tuiProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	scratch := t.TempDir()
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = scratch
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		switch name {
		case "CI", "TERM", "NO_COLOR", "TALENTO_PROFILE", "TALENTO_CONFIG_DIR", "TALENTO_HOME",
			"TALENTO_NONINTERACTIVE", "TALENTO_ALLOW_FILE_CREDENTIALS", "TALENTO_TUI_PTY_HELPER", "TALENTO_TUI_PTY_SCENARIO":
			continue
		}
		command.Env = append(command.Env, variable)
	}
	command.Env = append(command.Env,
		"TALENTO_TUI_PTY_HELPER=1", "TALENTO_TUI_PTY_SCENARIO="+scenario,
		"TERM=xterm-256color", "NO_COLOR=1", "TALENTO_CONFIG_DIR="+filepath.Join(scratch, "config"),
		"TALENTO_HOME="+filepath.Join(scratch, "home"),
	)
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 30, Cols: 110})
	if err != nil {
		t.Fatalf("start Unix PTY: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	output := &ptyOutput{}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(output, terminal)
	}()
	done := make(chan error, 1)
	go func() {
		err := command.Wait()
		// Wait for the PTY reader to drain the renderer's final cleanup bytes.
		select {
		case <-readDone:
		case <-time.After(time.Second):
			_ = terminal.Close()
			<-readDone
		}
		done <- err
	}()
	return &tuiProcess{terminal: terminal, output: output, done: done, scratch: scratch, helper: scenario != "packaged"}
}

func (p *tuiProcess) send(t *testing.T, keys string) {
	t.Helper()
	if _, err := io.WriteString(p.terminal, keys); err != nil {
		t.Fatal(err)
	}
}

func (p *tuiProcess) waitText(t *testing.T, expected string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(ansi.Strip(p.output.String()), expected) {
			return
		}
		select {
		case <-tick.C:
		case err := <-p.done:
			t.Fatalf("TUI exited before %q: %v\n%s", expected, err, p.output.String())
		case <-timer.C:
			t.Fatalf("timed out waiting for %q\n%s", expected, p.output.String())
		}
	}
}

func (p *tuiProcess) waitExit(t *testing.T) {
	t.Helper()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("TUI process failed: %v\n%s", err, p.output.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("TUI did not exit\n%s", p.output.String())
	}
	output := p.output.String()
	if !strings.Contains(output, "\x1b[?1049l") || !strings.Contains(output, "\x1b[?25h") {
		t.Fatalf("alternate screen or cursor not restored\n%s", output)
	}
	if p.helper && !strings.Contains(output, "TUI_EXITED_CLEANLY") {
		t.Fatalf("missing terminal-state/session cleanup proof\n%s", output)
	}
}

type ptyOutput struct {
	mu   sync.Mutex
	data strings.Builder
}

func (b *ptyOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *ptyOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type ptySession struct{ closed atomic.Bool }

func (*ptySession) Profile() string { return "fixture" }

func (*ptySession) Catalogue(context.Context) (*app.Catalogue, error) {
	raw := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	var input schema.JSONSchema
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	return &app.Catalogue{Tools: []app.SessionTool{{
		Name: "list_employees", Title: "People fixture", Domain: "people", Command: "list",
		Description: "Read the in-memory PTY fixture.", SchemaRevision: "fixture-v1",
		InputSchema: input, RawSchema: raw, Reviewed: true, ReadOnly: true,
	}}}, nil
}

func (*ptySession) Invoke(_ context.Context, invocation app.Invocation) (*app.ToolExecution, error) {
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
		Text: "PTY read result\nSafe server text\x1b]52;c;ZXZpbA==\a",
	}}}
	return &app.ToolExecution{Profile: "fixture", Result: mcpclient.NewToolOutcome(invocation.Tool, result)}, nil
}

func (*ptySession) Confirm(context.Context, app.PreviewHandle) (*app.ToolExecution, error) {
	return nil, errors.New("the PTY read fixture must never confirm a write")
}

func (*ptySession) ReadResource(context.Context, string) (*mcpclient.ResourceOutcome, error) {
	return nil, errors.New("the PTY fixture has no resources")
}

func (*ptySession) InvalidatePreviews() {}

func (s *ptySession) Close() error {
	s.closed.Store(true)
	return nil
}
