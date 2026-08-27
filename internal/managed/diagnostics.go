package managed

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/talentohq/talento-cli/internal/buildinfo"
	"github.com/talentohq/talento-cli/internal/config"
)

type configSnapshot struct {
	ManagedFiles map[string]config.ManagedFile
}

type Diagnosis struct {
	Agent                Agent             `json:"agent"`
	Capabilities         Capabilities      `json:"capabilities"`
	Detection            Detection         `json:"detection"`
	ExecutableVersion    ExecutableVersion `json:"executable_version"`
	Installed            bool              `json:"installed"`
	Status               string            `json:"status"`
	Method               string            `json:"method"`
	Scopes               []string          `json:"scopes,omitempty"`
	ExpectedVersion      string            `json:"expected_version"`
	InstalledVersion     string            `json:"installed_version,omitempty"`
	RegistrationID       string            `json:"registration_id,omitempty"`
	Files                []Integrity       `json:"files,omitempty"`
	RepairCommands       []string          `json:"repair_commands,omitempty"`
	ExecutableDiagnostic string            `json:"executable_diagnostic,omitempty"`
}

func (d Diagnosis) Summary() string {
	switch d.Status {
	case "healthy":
		return fmt.Sprintf("%s integration is healthy (%s)", d.Agent.Name, d.Method)
	case "stale":
		return fmt.Sprintf("%s integration is stale (installed %s, expected %s)", d.Agent.Name, valueOrUnknown(d.InstalledVersion), d.ExpectedVersion)
	case "modified":
		return d.Agent.Name + " integration has user-modified managed files"
	case "missing":
		return d.Agent.Name + " integration has missing managed files"
	case "unreadable":
		return d.Agent.Name + " integration has unreadable managed files"
	case "version-unavailable":
		return d.Agent.Name + " was detected but its version could not be verified"
	case "runtime-missing":
		return d.Agent.Name + " integration files are intact, but the agent is not detected"
	case "not-installed":
		if d.Detection.Detected {
			return d.Agent.Name + " is detected but the Talento integration is not installed"
		}
		return d.Agent.Name + " is not detected and has no Talento integration"
	default:
		return d.Agent.Name + " integration status is " + d.Status
	}
}

func (m *Manager) Diagnose(ctx context.Context) ([]Diagnosis, error) {
	return m.DiagnoseAgents(ctx, nil)
}

func (m *Manager) DiagnoseAgents(ctx context.Context, selected []string) ([]Diagnosis, error) {
	cfg, err := m.Config.Load()
	if err != nil {
		return nil, err
	}
	integrity, err := m.Integrity()
	if err != nil {
		return nil, err
	}
	integrityByPath := make(map[string]Integrity, len(integrity))
	for _, item := range integrity {
		integrityByPath[item.Path] = item
	}
	environment := m.Runtime
	if environment.Home == "" {
		environment.Home = m.Home
	}
	if environment.LookPath == nil || environment.Runner == nil {
		defaults := DefaultAdapterEnvironment(environment.Home)
		if environment.LookPath == nil {
			environment.LookPath = defaults.LookPath
		}
		if environment.Runner == nil {
			environment.Runner = defaults.Runner
		}
		if len(environment.Environment) == 0 {
			environment.Environment = defaults.Environment
		}
		if environment.Timeout <= 0 {
			environment.Timeout = defaults.Timeout
		}
	}
	snapshot := configSnapshot{ManagedFiles: cfg.ManagedFiles}
	result := make([]Diagnosis, 0, len(adapters))
	filter := make(map[string]bool, len(selected))
	for _, id := range selected {
		if _, ok := AdapterByID(id); !ok {
			return nil, fmt.Errorf("unsupported agent %q", id)
		}
		filter[id] = true
	}
	for _, adapter := range adapters {
		if len(filter) > 0 && !filter[adapter.Agent().ID] {
			continue
		}
		detection := adapter.Detect(ctx, environment)
		version := adapter.Version(ctx, environment, detection)
		result = append(result, adapter.Diagnose(ctx, diagnosticContext{
			Config: snapshot, Integrity: integrityByPath,
			Detection: detection, Version: version,
		}))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Agent.ID < result[j].Agent.ID })
	return result, nil
}

func diagnoseManagedAdapter(adapter managedFileAdapter, details diagnosticContext) Diagnosis {
	agent := adapter.Agent()
	diagnosis := Diagnosis{
		Agent: agent, Capabilities: adapter.Capabilities(), Detection: details.Detection,
		ExecutableVersion: details.Version, Status: "not-installed", Method: MethodManagedFile,
		ExpectedVersion: buildinfo.Version,
	}
	records := make([]config.ManagedFile, 0)
	for _, managed := range details.Config.ManagedFiles {
		if managed.Agent == agent.ID || (managed.Agent == "" && pathBelongsToAgent(managed.Path, agent)) {
			records = append(records, managed)
		}
	}
	if len(records) == 0 {
		if diagnosis.Detection.Detected && diagnosis.ExecutableVersion.Status != "available" && diagnosis.ExecutableVersion.Status != "unavailable" {
			diagnosis.ExecutableDiagnostic = diagnosis.ExecutableVersion.Detail
		}
		diagnosis.RepairCommands = []string{"talento skill install --agent " + agent.ID + " --scope user"}
		return diagnosis
	}
	if agent.DependsOnSharedSkill {
		sharedRoots := make(map[string]bool)
		for _, managed := range records {
			if root, ok := adapterInstallationRoot(managed.Path, agent, managed.Scope); ok {
				sharedRoots[root+"/.agents/skills/talento"] = true
			}
		}
		for _, managed := range details.Config.ManagedFiles {
			path := filepathSlash(managed.Path)
			sharedRoot := path
			if index := strings.Index(path, "/.agents/skills/talento/"); index >= 0 {
				sharedRoot = path[:index] + "/.agents/skills/talento"
			}
			if managed.Agent == "" && managed.Kind == "shared-skill" && sharedRoots[sharedRoot] {
				records = append(records, managed)
			}
		}
	}

	diagnosis.Installed = true
	diagnosis.Status = "healthy"
	scopes := make(map[string]bool)
	versions := make(map[string]bool)
	methods := make(map[string]bool)
	registrations := make(map[string]bool)
	for _, managed := range records {
		method := managed.Method
		if method == "" {
			method = MethodManagedFile
		}
		methods[method] = true
		if managed.Scope != "" {
			scopes[managed.Scope] = true
		}
		if managed.Version != "" {
			versions[managed.Version] = true
		}
		if managed.RegistrationID != "" {
			registrations[managed.RegistrationID] = true
		}
		if item, ok := details.Integrity[managed.Path]; ok {
			diagnosis.Files = append(diagnosis.Files, item)
			switch item.Status {
			case "modified":
				diagnosis.Status = moreSevere(diagnosis.Status, "modified")
			case "missing":
				diagnosis.Status = moreSevere(diagnosis.Status, "missing")
			case "unreadable":
				diagnosis.Status = moreSevere(diagnosis.Status, "unreadable")
			}
		}
	}
	diagnosis.Scopes = sortedKeys(scopes)
	installedVersions := sortedKeys(versions)
	if len(installedVersions) == 1 {
		diagnosis.InstalledVersion = installedVersions[0]
	} else if len(installedVersions) > 1 {
		diagnosis.InstalledVersion = strings.Join(installedVersions, ",")
	}
	installedMethods := sortedKeys(methods)
	if len(installedMethods) > 0 {
		diagnosis.Method = strings.Join(installedMethods, ",")
	}
	registrationIDs := sortedKeys(registrations)
	if len(registrationIDs) > 0 {
		diagnosis.RegistrationID = strings.Join(registrationIDs, ",")
	}
	if diagnosis.Status == "healthy" && (len(installedVersions) != 1 || installedVersions[0] != buildinfo.Version) {
		diagnosis.Status = "stale"
	}
	if diagnosis.Status == "healthy" && !diagnosis.Detection.Detected {
		diagnosis.Status = "runtime-missing"
	}
	if diagnosis.Status == "healthy" && diagnosis.Detection.Detected && diagnosis.ExecutableVersion.Status != "available" && diagnosis.ExecutableVersion.Status != "unavailable" {
		diagnosis.Status = "version-unavailable"
		diagnosis.ExecutableDiagnostic = diagnosis.ExecutableVersion.Detail
	}
	if diagnosis.Status != "healthy" {
		if diagnosis.Status == "runtime-missing" {
			diagnosis.RepairCommands = []string{"install or start " + agent.Name + ", then run: talento skill status --integration " + agent.ID}
		}
		for _, scope := range diagnosis.Scopes {
			if diagnosis.Status == "runtime-missing" {
				continue
			}
			command := "talento skill update --agent " + agent.ID + " --scope " + scope
			if diagnosis.Status == "modified" {
				command += " --force"
			}
			diagnosis.RepairCommands = append(diagnosis.RepairCommands, command)
		}
		if len(diagnosis.RepairCommands) == 0 {
			diagnosis.RepairCommands = []string{"talento skill update --agent " + agent.ID + " --scope user"}
		}
	}
	sort.Slice(diagnosis.Files, func(i, j int) bool { return diagnosis.Files[i].Path < diagnosis.Files[j].Path })
	return diagnosis
}

func pathBelongsToAgent(path string, agent Agent) bool {
	clean := filepathSlash(path)
	return strings.Contains(clean, "/"+strings.TrimPrefix(agent.UserPath, "./")+"/") ||
		strings.HasSuffix(clean, "/"+strings.TrimPrefix(agent.UserPath, "./")) ||
		strings.Contains(clean, "/"+strings.TrimPrefix(agent.ProjectPath, "./")+"/") ||
		strings.HasSuffix(clean, "/"+strings.TrimPrefix(agent.ProjectPath, "./"))
}

func adapterInstallationRoot(path string, agent Agent, scope string) (string, bool) {
	relative := agent.UserPath
	if scope == "project" {
		relative = agent.ProjectPath
	}
	clean := filepathSlash(path)
	suffix := "/" + strings.TrimPrefix(relative, "./")
	if strings.HasSuffix(clean, suffix) {
		return strings.TrimSuffix(clean, suffix), true
	}
	return "", false
}

func filepathSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func moreSevere(current, candidate string) string {
	rank := map[string]int{"healthy": 0, "stale": 1, "missing": 2, "modified": 3, "unreadable": 4}
	if rank[candidate] > rank[current] {
		return candidate
	}
	return current
}

func sortedKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
