// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

var (
	buildVersion = "dev"
	buildCommit  string
	buildDate    string
)

type buildDetails struct {
	Name      string
	Version   string
	Commit    string
	Date      string
	GoVersion string
}

func currentBuildDetails() buildDetails {
	details := buildDetails{
		Name:      "assistant",
		Version:   strings.TrimSpace(buildVersion),
		Commit:    strings.TrimSpace(buildCommit),
		Date:      strings.TrimSpace(buildDate),
		GoVersion: runtime.Version(),
	}
	if details.Version == "" {
		details.Version = "dev"
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if details.Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			details.Version = info.Main.Version
		}
		applyBuildSettings(&details, info.Settings)
	}
	if details.Commit == "" {
		details.Commit = "unknown"
	}
	if details.Date == "" {
		details.Date = "unknown"
	}
	return details
}

func applyBuildSettings(details *buildDetails, settings []debug.BuildSetting) {
	modified := false
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			if details.Commit == "" {
				details.Commit = strings.TrimSpace(setting.Value)
			}
		case "vcs.time":
			if details.Date == "" {
				details.Date = strings.TrimSpace(setting.Value)
			}
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if modified && details.Commit != "" && !strings.HasSuffix(details.Commit, "-dirty") {
		details.Commit += "-dirty"
	}
}

func versionText() string {
	details := currentBuildDetails()
	return fmt.Sprintf("%s %s\ncommit: %s\nbuilt: %s\ngo: %s",
		details.Name,
		details.Version,
		details.Commit,
		details.Date,
		details.GoVersion,
	)
}
