package cli

import (
	"runtime/debug"
	"strings"
)

// buildVersion is set for release binaries with -ldflags -X.
var buildVersion string

func currentVersion() string {
	moduleVersion := ""
	revision := ""
	modified := false
	if info, ok := debug.ReadBuildInfo(); ok {
		moduleVersion = info.Main.Version
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}
	return resolveVersion(buildVersion, moduleVersion, revision, modified)
}

func resolveVersion(linkedVersion, moduleVersion, revision string, modified bool) string {
	for _, candidate := range []string{linkedVersion, moduleVersion} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || candidate == "(devel)" {
			continue
		}
		return strings.TrimPrefix(candidate, "v")
	}

	revision = strings.TrimSpace(revision)
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	version := "dev-" + revision
	if modified {
		version += "-dirty"
	}
	return version
}
