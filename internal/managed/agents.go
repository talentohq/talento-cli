package managed

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/talentohq/talento-cli/internal/terminal"
)

const (
	MethodManagedFile   = "managed-file"
	MethodNativeCommand = "native-command"
	MethodPathProbe     = "path-probe"

	defaultAgentCommandTimeout = 3 * time.Second
)

// Agent is the stable, user-visible identity and path contract for an
// integration. Operational behavior belongs to its Adapter.
type Agent struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Executable           string `json:"executable"`
	UserPath             string `json:"user_path"`
	ProjectPath          string `json:"project_path"`
	DetectionPath        string `json:"detection_path"`
	SingleFile           bool   `json:"single_file"`
	DependsOnSharedSkill bool   `json:"-"`
}

// Capabilities makes each adapter's mutation and inspection mechanisms
// explicit. NativeCommand is used only for the read-only version probe in the
// current catalog; installation and removal remain managed-file-backed.
type Capabilities struct {
	DetectMethod   string   `json:"detect_method"`
	InstallMethod  string   `json:"install_method"`
	RemoveMethod   string   `json:"remove_method"`
	DiagnoseMethod string   `json:"diagnose_method"`
	VersionMethod  string   `json:"version_method"`
	Scopes         []string `json:"scopes"`
}

type Detection struct {
	Detected       bool   `json:"detected"`
	ExecutablePath string `json:"executable_path,omitempty"`
	DetectedBy     string `json:"detected_by,omitempty"`
}

type ExecutableVersion struct {
	Status string `json:"status"`
	Value  string `json:"value,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// CommandRunner is the subprocess seam used by safe, side-effect-free adapter
// probes. Implementations receive argument arrays and an allowlisted
// environment; adapters never build shell command strings.
type CommandRunner interface {
	Run(context.Context, string, []string, []string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, args, environment []string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- executable comes from LookPath or the fixed adapter catalog.
	command.Env = append([]string(nil), environment...)
	command.WaitDelay = time.Second
	return command.CombinedOutput()
}

type AdapterEnvironment struct {
	Home        string
	LookPath    func(string) (string, error)
	Runner      CommandRunner
	Environment []string
	Timeout     time.Duration
}

func DefaultAdapterEnvironment(home string) AdapterEnvironment {
	return AdapterEnvironment{
		Home:        home,
		LookPath:    exec.LookPath,
		Runner:      ExecRunner{},
		Environment: safeAgentEnvironment(),
		Timeout:     defaultAgentCommandTimeout,
	}
}

type adapterOperation struct {
	Manager *Manager
	Options InstallOptions
}

type diagnosticContext struct {
	Config    configSnapshot
	Integrity map[string]Integrity
	Detection Detection
	Version   ExecutableVersion
}

// Adapter is the capability boundary for a supported coding agent. Install
// and Remove return mutation plans so Manager can preserve its transactional
// multi-agent write, backup, rollback, and ownership guarantees.
type Adapter interface {
	Agent() Agent
	Capabilities() Capabilities
	Detect(context.Context, AdapterEnvironment) Detection
	Install(context.Context, adapterOperation) ([]targetFile, error)
	Remove(context.Context, adapterOperation) ([]targetFile, error)
	Diagnose(context.Context, diagnosticContext) Diagnosis
	Version(context.Context, AdapterEnvironment, Detection) ExecutableVersion
}

type managedFileAdapter struct{ agent Agent }

func (a managedFileAdapter) Agent() Agent { return a.agent }

func (a managedFileAdapter) Capabilities() Capabilities {
	return Capabilities{
		DetectMethod:   MethodPathProbe,
		InstallMethod:  MethodManagedFile,
		RemoveMethod:   MethodManagedFile,
		DiagnoseMethod: MethodManagedFile,
		VersionMethod:  MethodNativeCommand,
		Scopes:         []string{"user", "project"},
	}
}

func (a managedFileAdapter) Detect(_ context.Context, environment AdapterEnvironment) Detection {
	lookPath := environment.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if path, err := lookPath(a.agent.Executable); err == nil && strings.TrimSpace(path) != "" {
		return Detection{Detected: true, ExecutablePath: filepath.Clean(path), DetectedBy: "executable"}
	}
	if environment.Home != "" {
		fallback := filepath.Join(environment.Home, ".local", "bin", a.agent.Executable)
		if info, err := os.Stat(fallback); err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
			return Detection{Detected: true, ExecutablePath: fallback, DetectedBy: "fallback-executable"}
		}
		candidate := filepath.Join(environment.Home, filepath.FromSlash(a.agent.DetectionPath))
		if hasNonIntegrationEvidence(candidate, filepath.Join(environment.Home, filepath.FromSlash(a.agent.UserPath))) {
			return Detection{Detected: true, DetectedBy: "agent-directory"}
		}
	}
	return Detection{}
}

func (a managedFileAdapter) Install(_ context.Context, operation adapterOperation) ([]targetFile, error) {
	return operation.Manager.agentTargets(a.agent, operation.Options)
}

func (a managedFileAdapter) Remove(ctx context.Context, operation adapterOperation) ([]targetFile, error) {
	return a.Install(ctx, operation)
}

func (a managedFileAdapter) Version(parent context.Context, environment AdapterEnvironment, detection Detection) ExecutableVersion {
	if detection.ExecutablePath == "" {
		return ExecutableVersion{Status: "unavailable"}
	}
	runner := environment.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	timeout := environment.Timeout
	if timeout <= 0 {
		timeout = defaultAgentCommandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	probeEnvironment, cleanup, err := isolatedProbeEnvironment(environment.Environment)
	if err != nil {
		return ExecutableVersion{Status: "error", Detail: "prepare isolated version probe: " + terminal.SanitizeLine(err.Error())}
	}
	defer cleanup()
	output, err := runner.Run(ctx, detection.ExecutablePath, []string{"--version"}, probeEnvironment)
	value := boundedDiagnostic(output)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ExecutableVersion{Status: "timeout", Detail: "version probe timed out"}
	}
	if err != nil {
		detail := terminal.SanitizeLine(err.Error())
		if value != "" {
			detail += ": " + value
		}
		return ExecutableVersion{Status: "error", Detail: boundedText(detail, 240)}
	}
	if value == "" {
		return ExecutableVersion{Status: "malformed", Detail: "version probe returned no usable text"}
	}
	return ExecutableVersion{Status: "available", Value: value}
}

func (a managedFileAdapter) Diagnose(_ context.Context, details diagnosticContext) Diagnosis {
	return diagnoseManagedAdapter(a, details)
}

var adapters = []Adapter{
	managedFileAdapter{agent: Agent{ID: "claude-code", Name: "Claude Code", Executable: "claude", UserPath: ".claude/skills/talento", ProjectPath: ".claude/skills/talento", DetectionPath: ".claude"}},
	managedFileAdapter{agent: Agent{ID: "codex", Name: "Codex", Executable: "codex", UserPath: ".codex/skills/talento", ProjectPath: ".codex/skills/talento", DetectionPath: ".codex"}},
	managedFileAdapter{agent: Agent{ID: "gemini", Name: "Gemini CLI", Executable: "gemini", UserPath: ".gemini/skills/talento", ProjectPath: ".gemini/skills/talento", DetectionPath: ".gemini"}},
	managedFileAdapter{agent: Agent{ID: "grok", Name: "Grok", Executable: "grok", UserPath: ".grok/skills/talento", ProjectPath: ".grok/skills/talento", DetectionPath: ".grok"}},
	managedFileAdapter{agent: Agent{ID: "copilot", Name: "GitHub Copilot CLI", Executable: "copilot", UserPath: ".copilot/skills/talento", ProjectPath: ".github/skills/talento", DetectionPath: ".copilot"}},
	managedFileAdapter{agent: Agent{ID: "cursor", Name: "Cursor", Executable: "cursor", UserPath: ".cursor/rules/talento.mdc", ProjectPath: ".cursor/rules/talento.mdc", DetectionPath: ".cursor", SingleFile: true, DependsOnSharedSkill: true}},
	managedFileAdapter{agent: Agent{ID: "windsurf", Name: "Windsurf", Executable: "windsurf", UserPath: ".codeium/windsurf/memories/talento.md", ProjectPath: ".windsurf/rules/talento.md", DetectionPath: ".codeium/windsurf", SingleFile: true, DependsOnSharedSkill: true}},
	managedFileAdapter{agent: Agent{ID: "opencode", Name: "OpenCode", Executable: "opencode", UserPath: ".config/opencode/skills/talento", ProjectPath: ".opencode/skills/talento", DetectionPath: ".config/opencode"}},
}

// SupportedAgents preserves the existing setup/validation API while the
// adapters own operational behavior.
var SupportedAgents = func() []Agent {
	result := make([]Agent, 0, len(adapters))
	for _, adapter := range adapters {
		result = append(result, adapter.Agent())
	}
	return result
}()

func AdapterByID(id string) (Adapter, bool) {
	for _, adapter := range adapters {
		if adapter.Agent().ID == id {
			return adapter, true
		}
	}
	return nil, false
}

func AgentByID(id string) (Agent, bool) {
	adapter, ok := AdapterByID(id)
	if !ok {
		return Agent{}, false
	}
	return adapter.Agent(), true
}

func Detect(home string) []Agent {
	return DetectWithEnvironment(context.Background(), DefaultAdapterEnvironment(home))
}

func DetectWithEnvironment(ctx context.Context, environment AdapterEnvironment) []Agent {
	result := make([]Agent, 0)
	for _, adapter := range adapters {
		if adapter.Detect(ctx, environment).Detected {
			result = append(result, adapter.Agent())
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func safeAgentEnvironment() []string {
	allowed := []string{"PATH", "PATHEXT", "SystemRoot", "WINDIR", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL"}
	environment := make([]string, 0, len(allowed))
	for _, key := range allowed {
		if value := os.Getenv(key); value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}

func isolatedProbeEnvironment(base []string) ([]string, func(), error) {
	root, err := os.MkdirTemp("", "talento-agent-probe-*")
	if err != nil {
		return nil, func() {}, err
	}
	configDir := filepath.Join(root, "config")
	tempDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, func() {}, err
	}
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, func() {}, err
	}
	drop := map[string]bool{
		"HOME": true, "USERPROFILE": true, "XDG_CONFIG_HOME": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
	}
	environment := make([]string, 0, len(base)+6)
	for _, value := range base {
		key, _, ok := strings.Cut(value, "=")
		if ok && !drop[strings.ToUpper(key)] {
			environment = append(environment, value)
		}
	}
	environment = append(environment,
		"HOME="+root,
		"USERPROFILE="+root,
		"XDG_CONFIG_HOME="+configDir,
		"TMPDIR="+tempDir,
		"TMP="+tempDir,
		"TEMP="+tempDir,
	)
	return environment, func() { _ = os.RemoveAll(root) }, nil
}

func boundedDiagnostic(output []byte) string {
	line := strings.TrimSpace(terminal.SanitizeLine(strings.TrimSpace(string(output))))
	return boundedText(line, 240)
}

func boundedText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func hasNonIntegrationEvidence(root, integration string) bool {
	root = filepath.Clean(root)
	integration = filepath.Clean(integration)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return false
	}
	found := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		clean := filepath.Clean(path)
		if clean == integration || strings.HasPrefix(clean, integration+string(filepath.Separator)) {
			if entry.IsDir() && clean == integration {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(integration, clean+string(filepath.Separator)) {
			return nil
		}
		found = true
		return filepath.SkipAll
	})
	return found
}
