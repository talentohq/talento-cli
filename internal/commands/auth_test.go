package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/auth"
)

func TestAuthLoginHumanTextOmitsAccessTokenExpiry(t *testing.T) {
	view := authLoginStatus{auth.Status{
		Profile: "default", Authenticated: true,
		ExpiresAt: time.Date(2026, 8, 29, 9, 33, 49, 0, time.UTC),
		Scope:     "mcp_access", Storage: "system",
	}}

	text := view.HumanText()
	for _, want := range []string{"Profile default is authenticated.", "Scope: mcp_access", "Credential storage: system"} {
		if !strings.Contains(text, want) {
			t.Fatalf("login output lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Expires") || strings.Contains(text, "09:33:49") {
		t.Fatalf("login output includes access-token expiry: %s", text)
	}
	if markdown := view.MarkdownText(); !strings.Contains(markdown, "Expires: 2026-08-29 09:33:49Z") {
		t.Fatalf("login Markdown lost access-token expiry: %s", markdown)
	}
}

func TestAuthStatusHumanTextUsesLocalTimeAndExplainsRefresh(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("CEST", 2*60*60)
	t.Cleanup(func() { time.Local = previousLocal })

	view := authStatus{auth.Status{
		Profile: "default", Authenticated: true,
		ExpiresAt:   time.Date(2026, 8, 29, 9, 33, 49, 0, time.UTC),
		Refreshable: true, Scope: "mcp_access", Storage: "system",
	}}

	text := view.HumanText()
	for _, want := range []string{"Access token: valid", "Expires: 2026-08-29 11:33:49 CEST", "Refresh: automatic"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status output lacks %q: %s", want, text)
		}
	}
}

func TestAuthStatusStructuredOutputKeepsUTCContract(t *testing.T) {
	status := auth.Status{
		Profile: "default", Authenticated: true,
		ExpiresAt:   time.Date(2026, 8, 29, 9, 33, 49, 0, time.UTC),
		Refreshable: true, Scope: "mcp_access", Storage: "system",
	}

	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"expires_at":"2026-08-29T09:33:49Z"`) {
		t.Fatalf("structured expiry changed: %s", text)
	}
	if strings.Contains(text, "refreshable") {
		t.Fatalf("human-only refresh state leaked into structured contract: %s", text)
	}
}

func TestAuthLoginURLSinkReportsHumanProgressWithoutLeakingURL(t *testing.T) {
	stderr := &bytes.Buffer{}
	talento := &app.App{Global: &app.GlobalOptions{}, Stderr: stderr}
	sink := authLoginURLSink(talento, false)
	sink("https://auth.example.test/authorize?state=secret-state")

	text := stderr.String()
	if !strings.Contains(text, "Opening your browser") || !strings.Contains(text, "Waiting for authorization") {
		t.Fatalf("progress output = %q", text)
	}
	if strings.Contains(text, "secret-state") || strings.Contains(text, "auth.example.test") {
		t.Fatalf("auto-open progress leaked authorization URL: %q", text)
	}
}

func TestAuthLoginURLSinkPrintsNoOpenURL(t *testing.T) {
	stderr := &bytes.Buffer{}
	talento := &app.App{Global: &app.GlobalOptions{}, Stderr: stderr}
	sink := authLoginURLSink(talento, true)
	sink("https://auth.example.test/authorize")

	text := stderr.String()
	if !strings.Contains(text, "https://auth.example.test/authorize") || !strings.Contains(text, "Waiting for authorization") {
		t.Fatalf("no-open output = %q", text)
	}
}

func TestAuthLoginURLSinkKeepsStructuredModesQuiet(t *testing.T) {
	stderr := &bytes.Buffer{}
	talento := &app.App{Global: &app.GlobalOptions{JSON: true}, Stderr: stderr}
	if sink := authLoginURLSink(talento, false); sink != nil {
		t.Fatal("automatic browser login returned a progress sink in JSON mode")
	}

	sink := authLoginURLSink(talento, true)
	sink("https://auth.example.test/authorize")
	if !strings.Contains(stderr.String(), "https://auth.example.test/authorize") || strings.Contains(stderr.String(), "Waiting") {
		t.Fatalf("structured no-open output = %q", stderr.String())
	}
}
