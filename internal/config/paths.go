package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	Endpoint = "https://mcp.talentohq.com/mcp"
	Scope    = "mcp_access"
)

type Paths struct {
	ConfigDir     string
	ConfigFile    string
	CredentialDir string
	HomeDir       string
}

func ResolvePaths() (Paths, error) {
	configDir := os.Getenv("TALENTO_CONFIG_DIR")
	if configDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		configDir = filepath.Join(base, "talento")
	}

	home := os.Getenv("TALENTO_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve home directory: %w", err)
		}
	}

	return Paths{
		ConfigDir:     configDir,
		ConfigFile:    filepath.Join(configDir, "config.json"),
		CredentialDir: filepath.Join(configDir, "secrets"),
		HomeDir:       home,
	}, nil
}
