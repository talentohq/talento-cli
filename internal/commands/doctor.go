package commands

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/talentohq/talento-cli/internal/app"
	"github.com/talentohq/talento-cli/internal/auth"
	"github.com/talentohq/talento-cli/internal/buildinfo"
	"github.com/talentohq/talento-cli/internal/managed"
	clioutput "github.com/talentohq/talento-cli/internal/output"
	"github.com/talentohq/talento-cli/internal/schema"
	"github.com/talentohq/talento-cli/internal/terminal"
	"github.com/talentohq/talento-cli/internal/upgrade"
)

type doctorCheck struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type doctorReport struct {
	Status  string        `json:"status"`
	Healthy bool          `json:"healthy"`
	Checks  []doctorCheck `json:"checks"`
}

func (r doctorReport) HumanText() string {
	lines := []string{"Talento CLI doctor: " + strings.ToUpper(r.Status)}
	for _, check := range r.Checks {
		lines = append(lines, fmt.Sprintf("[%s] %s — %s", terminal.SanitizeLine(strings.ToUpper(check.Status)), terminal.SanitizeLine(check.Name), terminal.SanitizeLine(check.Message)))
	}
	return strings.Join(lines, "\n")
}

func newDoctorCommand(talento *app.App, assets fs.FS) *cobra.Command {
	return newDoctorCommandWithRunner(talento, assets, runDoctor)
}

type doctorRunner func(context.Context, *app.App, fs.FS, bool) doctorReport

func newDoctorCommandWithRunner(talento *app.App, assets fs.FS, runner doctorRunner) *cobra.Command {
	var verbose bool
	command := &cobra.Command{
		Use: "doctor", Short: "Check CLI, gateway, profile, coverage, and agent integration health.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := runner(cmd.Context(), talento, assets, verbose)
			summary := "Doctor completed with status " + report.Status + "."
			if !report.Healthy {
				return clioutput.WithRenderedData(clioutput.API(summary, nil), report)
			}
			return talento.Output().Success(report, summary, nil, map[string]any{"healthy": report.Healthy})
		},
	}
	command.Flags().BoolVar(&verbose, "verbose", false, "include safe diagnostic detail (never secrets or prompt content)")
	return command
}

func runDoctor(ctx context.Context, talento *app.App, assets fs.FS, verbose bool) doctorReport {
	report := doctorReport{Status: "pass", Healthy: true}
	add := func(check doctorCheck) {
		if !verbose {
			check.Data = nil
		}
		report.Checks = append(report.Checks, check)
		if check.Status == "fail" {
			report.Status, report.Healthy = "fail", false
		} else if check.Status == "warn" && report.Status == "pass" {
			report.Status = "warn"
		}
	}

	info := buildinfo.Current()
	provenanceStatus := "pass"
	provenanceMessage := "release build provenance is present"
	if strings.Contains(info.Version, "dev") || info.Commit == "unknown" || info.Source == "development" {
		provenanceStatus, provenanceMessage = "warn", "development build; verified self-upgrade is disabled"
	}
	add(doctorCheck{Name: "binary", Status: provenanceStatus, Message: provenanceMessage, Data: map[string]any{"version": info.Version, "commit": info.Commit, "date": info.Date, "source": info.Source}})

	if err := schema.ValidateCoverage(talento.Snapshot, talento.SnapshotData, talento.Manifest); err != nil {
		add(doctorCheck{Name: "coverage", Status: "fail", Message: err.Error()})
	} else {
		add(doctorCheck{Name: "coverage", Status: "pass", Message: fmt.Sprintf("snapshot covers %d tools and %d resources", len(talento.Snapshot.Tools), len(talento.Snapshot.Resources)), Data: map[string]any{"snapshot_version": talento.Snapshot.SnapshotVersion}})
	}

	cfg, configErr := talento.Config.Load()
	if configErr != nil {
		add(doctorCheck{Name: "config", Status: "fail", Message: configErr.Error()})
	} else {
		add(doctorCheck{Name: "config", Status: "pass", Message: fmt.Sprintf("%d profile(s), %d managed file(s)", len(cfg.Profiles), len(cfg.ManagedFiles)), Data: map[string]any{"path": talento.Config.Path(), "default_profile": cfg.DefaultProfile}})
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	discovery, discoveryErr := auth.Discover(discoveryCtx, nil)
	cancel()
	if discoveryErr != nil {
		add(doctorCheck{Name: "oauth-discovery", Status: "fail", Message: discoveryErr.Error()})
	} else {
		add(doctorCheck{Name: "oauth-discovery", Status: "pass", Message: "generic gateway OAuth metadata is valid", Data: map[string]any{"issuer": discovery.Authorization.Issuer, "resource": discovery.Resource.Resource}})
	}

	profile, profileErr := talento.ResolveProfile(false)
	if profileErr != nil {
		add(doctorCheck{Name: "profile", Status: "fail", Message: profileErr.Error()})
	} else if profile == "" {
		add(doctorCheck{Name: "profile", Status: "warn", Message: "no profile selected; run `talento auth login`"})
	} else {
		add(doctorCheck{Name: "profile", Status: "pass", Message: "selected profile " + profile})
	}

	authenticated := false
	service, credentialsErr := talento.AuthService(false)
	if credentialsErr != nil {
		add(doctorCheck{Name: "credential-store", Status: "warn", Message: credentialsErr.Error()})
	} else {
		storage := "owner-only file fallback"
		if service.Credentials.UsingKeyring() {
			storage = "system credential store"
		}
		add(doctorCheck{Name: "credential-store", Status: "pass", Message: storage})
		if profile != "" {
			status, err := service.Status(profile)
			if err != nil {
				add(doctorCheck{Name: "authentication", Status: "fail", Message: err.Error()})
			} else if !status.Authenticated {
				add(doctorCheck{Name: "authentication", Status: "warn", Message: "selected profile is not authenticated"})
			} else if status.Expired {
				add(doctorCheck{Name: "authentication", Status: "warn", Message: "selected profile token is expired; refresh will be attempted on use"})
				authenticated = true
			} else {
				add(doctorCheck{Name: "authentication", Status: "pass", Message: "selected profile grant is present", Data: map[string]any{"expires_at": status.ExpiresAt, "scope": status.Scope}})
				authenticated = true
			}
		}
	}

	if authenticated {
		mcpCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		client, selected, err := talento.MCP(mcpCtx)
		if err != nil {
			add(doctorCheck{Name: "mcp", Status: "fail", Message: err.Error()})
		} else {
			tools, toolsErr := client.ListTools(mcpCtx)
			resources, resourcesErr := client.ListResources(mcpCtx)
			_ = client.Close()
			if toolsErr != nil || resourcesErr != nil {
				add(doctorCheck{Name: "mcp", Status: "fail", Message: fmt.Sprintf("tool discovery: %v; resource discovery: %v", toolsErr, resourcesErr)})
			} else {
				unknownTools, unknownResources := unknownLiveEntries(talento, tools, resources)
				status, message := "pass", fmt.Sprintf("profile %s exposes %d tools and %d resources", selected, len(tools), len(resources))
				if len(unknownTools)+len(unknownResources) > 0 {
					status, message = "fail", "live gateway exposes entries absent from the reviewed coverage manifest"
				}
				add(doctorCheck{Name: "mcp", Status: status, Message: message, Data: map[string]any{"tools": len(tools), "resources": len(resources), "unknown_tools": unknownTools, "unknown_resources": unknownResources}})
			}
		}
		cancel()
	} else {
		add(doctorCheck{Name: "mcp", Status: "skip", Message: "requires an authenticated profile"})
	}

	manager := managed.NewManager(assets, talento.Config, talento.Paths.HomeDir)
	integrity, err := manager.Integrity()
	if err != nil {
		add(doctorCheck{Name: "skills", Status: "fail", Message: err.Error()})
	} else {
		broken := 0
		for _, item := range integrity {
			if item.Status != "ok" {
				broken++
			}
		}
		status := "pass"
		message := fmt.Sprintf("%d tracked managed file(s) are intact", len(integrity))
		if broken > 0 {
			status, message = "warn", fmt.Sprintf("%d of %d tracked managed files are missing or modified", broken, len(integrity))
		}
		add(doctorCheck{Name: "skills", Status: status, Message: message, Data: map[string]any{"files": integrity}})
	}
	diagnoses, diagnosisErr := manager.Diagnose(ctx)
	if diagnosisErr != nil {
		add(doctorCheck{Name: "agents", Status: "fail", Message: diagnosisErr.Error()})
	} else {
		for _, check := range adapterDoctorChecks(diagnoses) {
			add(check)
		}
	}

	if strings.Contains(buildinfo.Version, "dev") {
		add(doctorCheck{Name: "updates", Status: "skip", Message: "development builds do not check stable self-updates"})
	} else {
		updateCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		check, err := upgrade.NewClient().Check(updateCtx, buildinfo.Version)
		cancel()
		if err != nil {
			add(doctorCheck{Name: "updates", Status: "warn", Message: "could not check releases: " + err.Error()})
		} else if check.Available {
			add(doctorCheck{Name: "updates", Status: "warn", Message: "version " + check.Latest + " is available", Data: map[string]any{"latest": check.Latest}})
		} else {
			add(doctorCheck{Name: "updates", Status: "pass", Message: "current release is up to date", Data: map[string]any{"latest": check.Latest}})
		}
	}
	return report
}

func adapterDoctorChecks(diagnoses []managed.Diagnosis) []doctorCheck {
	checks := make([]doctorCheck, 0, len(diagnoses))
	for _, diagnosis := range diagnoses {
		status := "skip"
		if diagnosis.Status == "healthy" {
			status = "pass"
		} else if diagnosis.Installed || diagnosis.Detection.Detected {
			status = "warn"
		}
		checks = append(checks, doctorCheck{
			Name: "agent-" + diagnosis.Agent.ID, Status: status, Message: diagnosis.Summary(),
			Data: map[string]any{"integration": diagnosis},
		})
	}
	return checks
}

func unknownLiveEntries(talento *app.App, tools []*mcp.Tool, resources []*mcp.Resource) ([]string, []string) {
	knownTools := make(map[string]bool, len(talento.Manifest.Tools))
	for _, mapping := range talento.Manifest.Tools {
		knownTools[mapping.Tool] = true
	}
	unknownTools := make([]string, 0)
	for _, tool := range tools {
		if !knownTools[tool.Name] {
			unknownTools = append(unknownTools, tool.Name)
		}
	}
	knownResources := make(map[string]bool, len(talento.Manifest.Resources))
	for _, mapping := range talento.Manifest.Resources {
		knownResources[mapping.Resource] = true
	}
	unknownResources := make([]string, 0)
	for _, resource := range resources {
		if !knownResources[resource.Name] {
			unknownResources = append(unknownResources, resource.Name)
		}
	}
	sort.Strings(unknownTools)
	sort.Strings(unknownResources)
	return unknownTools, unknownResources
}
