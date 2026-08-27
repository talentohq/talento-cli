package buildinfo

import (
	"runtime/debug"
	"strings"
)

var (
	Version             = "0.1.0-dev"
	Commit              = "unknown"
	Date                = "unknown"
	Source              = "development"
	MetadataFingerprint = ""
	ReleasePublicKey    = ""
)

func init() {
	if !strings.Contains(Version, "dev") {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return
	}
	Version = strings.TrimPrefix(info.Main.Version, "v")
	Source = "go-install"
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && Commit == "unknown" {
			Commit = setting.Value
		}
	}
}

type Info struct {
	Version             string `json:"version"`
	Commit              string `json:"commit"`
	Date                string `json:"date"`
	Source              string `json:"source"`
	MetadataFingerprint string `json:"-"`
}

func Current() Info {
	return Info{Version: Version, Commit: Commit, Date: Date, Source: Source, MetadataFingerprint: MetadataFingerprint}
}
