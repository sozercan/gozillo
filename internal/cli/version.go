package cli

import (
	"runtime/debug"
	"strings"
)

// buildVersion is set for release binaries with -ldflags -X.
var buildVersion string

func currentVersion() string {
	moduleVersion := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		moduleVersion = info.Main.Version
	}
	return resolveVersion(buildVersion, moduleVersion)
}

func resolveVersion(linkedVersion, moduleVersion string) string {
	for _, candidate := range []string{linkedVersion, moduleVersion} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return strings.TrimPrefix(candidate, "v")
		}
	}
	return "unknown"
}
