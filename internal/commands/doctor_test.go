package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/managed"
	clioutput "github.com/talentohq/talento-cli/internal/output"
)

func TestDoctorReportExitAndOutputContracts(t *testing.T) {
	tests := []struct {
		name    string
		report  doctorReport
		wantErr bool
	}{
		{
			name:   "healthy",
			report: doctorReport{Status: "pass", Healthy: true, Checks: []doctorCheck{{Name: "coverage", Status: "pass", Message: "coverage is current"}}},
		},
		{
			name:   "warning",
			report: doctorReport{Status: "warn", Healthy: true, Checks: []doctorCheck{{Name: "updates", Status: "warn", Message: "update check unavailable"}}},
		},
		{
			name:    "unhealthy",
			report:  doctorReport{Status: "fail", Healthy: false, Checks: []doctorCheck{{Name: "oauth-discovery", Status: "fail", Message: "metadata unavailable"}}},
			wantErr: true,
		},
	}
	modes := []struct {
		name  string
		apply func(*app.GlobalOptions)
	}{
		{name: "human", apply: func(*app.GlobalOptions) {}},
		{name: "json", apply: func(options *app.GlobalOptions) { options.JSON = true }},
		{name: "agent", apply: func(options *app.GlobalOptions) { options.Agent = true }},
	}

	for _, test := range tests {
		for _, mode := range modes {
			t.Run(test.name+"/"+mode.name, func(t *testing.T) {
				options := &app.GlobalOptions{}
				mode.apply(options)
				var stdout, stderr bytes.Buffer
				talento := &app.App{Global: options, Stdout: &stdout, Stderr: &stderr}
				command := newDoctorCommandWithRunner(talento, nil, func(context.Context, *app.App, fs.FS, bool) doctorReport {
					return test.report
				})
				command.SilenceErrors = true
				command.SilenceUsage = true

				err := command.Execute()
				if (err != nil) != test.wantErr {
					t.Fatalf("Execute() error = %v, wantErr %v", err, test.wantErr)
				}
				if err != nil {
					if clioutput.ExitCode(err) == 0 {
						t.Fatal("unhealthy doctor report returned a zero exit code")
					}
					if writeErr := talento.Output().Error(err); writeErr != nil {
						t.Fatalf("render error: %v", writeErr)
					}
				}

				assertDoctorOutput(t, mode.name, test.report, stdout.String(), stderr.String())
			})
		}
	}
}

func TestDoctorHumanTextKeepsEachCheckOnOneTerminalRecord(t *testing.T) {
	report := doctorReport{
		Status: "fail",
		Checks: []doctorCheck{{
			Name: "mcp", Status: "fail", Message: "denied\nAction completed and persisted.\t\x1b[2Jforged",
		}},
	}
	if got, want := report.HumanText(), "Talento CLI doctor: FAIL\n[FAIL] mcp — denied Action completed and persisted. forged"; got != want {
		t.Fatalf("HumanText() = %q, want %q", got, want)
	}
}

func TestAdapterDoctorChecksReportHealthyStaleModifiedMissingAndUndetected(t *testing.T) {
	diagnoses := []managed.Diagnosis{
		{Agent: managed.Agent{ID: "codex", Name: "Codex"}, Status: "healthy", Installed: true, Method: managed.MethodManagedFile},
		{Agent: managed.Agent{ID: "claude-code", Name: "Claude Code"}, Status: "stale", Installed: true, Method: managed.MethodManagedFile, InstalledVersion: "1.0.0", ExpectedVersion: "2.0.0"},
		{Agent: managed.Agent{ID: "cursor", Name: "Cursor"}, Status: "modified", Installed: true, Method: managed.MethodManagedFile},
		{Agent: managed.Agent{ID: "gemini", Name: "Gemini CLI"}, Status: "missing", Installed: true, Method: managed.MethodManagedFile},
		{Agent: managed.Agent{ID: "windsurf", Name: "Windsurf"}, Status: "not-installed", Detection: managed.Detection{}},
		{Agent: managed.Agent{ID: "opencode", Name: "OpenCode"}, Status: "not-installed", Detection: managed.Detection{Detected: true}},
	}
	checks := adapterDoctorChecks(diagnoses)
	want := []string{"pass", "warn", "warn", "warn", "skip", "warn"}
	if len(checks) != len(want) {
		t.Fatalf("checks = %#v", checks)
	}
	for index, check := range checks {
		if check.Status != want[index] || check.Name != "agent-"+diagnoses[index].Agent.ID {
			t.Fatalf("check %d = %#v", index, check)
		}
		if check.Data["integration"] == nil {
			t.Fatalf("check %d lacks verbose integration data", index)
		}
	}
}

func assertDoctorOutput(t *testing.T, mode string, report doctorReport, stdout, stderr string) {
	t.Helper()
	wantStream, otherStream := stdout, stderr
	if !report.Healthy {
		wantStream, otherStream = stderr, stdout
	}
	if otherStream != "" {
		t.Fatalf("unexpected output on other stream: %q", otherStream)
	}

	if mode == "human" {
		want := "Talento CLI doctor: " + strings.ToUpper(report.Status)
		if !bytes.Contains([]byte(wantStream), []byte(want)) {
			t.Fatalf("human output = %q, want %q", wantStream, want)
		}
		if bytes.Contains([]byte(wantStream), []byte("Error:")) {
			t.Fatalf("human report was duplicated with a generic error: %q", wantStream)
		}
		return
	}

	var value map[string]any
	if err := json.Unmarshal([]byte(wantStream), &value); err != nil {
		t.Fatalf("invalid %s JSON output %q: %v", mode, wantStream, err)
	}
	data := value
	if mode == "json" || !report.Healthy {
		dataValue, ok := value["data"]
		if !ok {
			t.Fatalf("%s output has no report data: %#v", mode, value)
		}
		data, ok = dataValue.(map[string]any)
		if !ok {
			t.Fatalf("%s report data = %#v", mode, dataValue)
		}
	}
	if data["status"] != report.Status || data["healthy"] != report.Healthy {
		t.Fatalf("%s report = %#v, want status=%q healthy=%v", mode, data, report.Status, report.Healthy)
	}
	if report.Healthy && mode == "json" && value["ok"] != true {
		t.Fatalf("healthy JSON envelope = %#v", value)
	}
	if !report.Healthy && value["ok"] != false {
		t.Fatalf("unhealthy error envelope = %#v", value)
	}
}
