// Package buildinfo reports which build of mise is running.
package buildinfo

import "runtime/debug"

// version is set at build time with
// -ldflags "-X github.com/raphi011/mise/internal/buildinfo.version=...".
var version = ""

// Version returns the release version the binary was built as, else the
// VCS revision it was built from (with "-dirty" for uncommitted changes),
// else "dev".
func Version() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
