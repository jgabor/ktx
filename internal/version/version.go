// Package version holds the ktx version string.
//
// A build installed with `go install github.com/jgabor/ktx/cmd/ktx@vX.Y.Z`
// reports vX.Y.Z, read from Go build info. A build without a module version,
// such as `mage build` or a local build without version control stamping,
// reports Dev. A value set at build time takes precedence over both:
//
//	go build -ldflags "-X github.com/jgabor/ktx/internal/version.Version=1.0.0"
package version

import "runtime/debug"

// Dev is the version a build without a module version reports.
const Dev = "dev"

// Version is the ktx build version. An -ldflags "-X" value is set before init
// runs and is kept; otherwise init fills it from Go build info.
var Version = ""

func init() {
	Version = resolve(Version, debug.ReadBuildInfo)
}

// resolve returns override when set, else the main module version from build
// info, else Dev. Go reports "(devel)" for a main module it could not version.
func resolve(override string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if override != "" {
		return override
	}
	if info, ok := readBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Dev
}
