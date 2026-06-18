// Package version holds the ktx version string.
//
// Version is overridden at build time via ldflags:
//
//	go build -ldflags "-X github.com/jgabor/ktx/internal/version.Version=1.0.0"
//
// When not overridden, it carries the dev marker so `ktx --version` always
// reports something meaningful from a clean checkout.
package version

// Version is the ktx build version. Defaults to a dev marker; override via
// -ldflags "-X github.com/jgabor/ktx/internal/version.Version=<value>".
var Version = "0.1.0-dev"
