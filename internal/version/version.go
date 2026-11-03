package version

import "runtime/debug"

// Build is set with -ldflags '-X .../internal/version.Build=<tag-or-commit>'.
var Build string

func String() string {
	if Build != "" {
		return Build
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "devel"
}
