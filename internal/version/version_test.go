package version

import (
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func buildInfo(mainVersion string) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/jgabor/ktx", Version: mainVersion}}, true
	}
}

func TestResolve(t *testing.T) {
	noBuildInfo := func() (*debug.BuildInfo, bool) { return nil, false }
	cases := []struct {
		name     string
		override string
		read     func() (*debug.BuildInfo, bool)
		want     string
	}{
		{"go install of a release tag", "", buildInfo("v1.2.0"), "v1.2.0"},
		{"go install of an untagged commit", "", buildInfo("v0.0.0-20261022101500-0123456789ab"), "v0.0.0-20261022101500-0123456789ab"},
		{"build without version control stamping", "", buildInfo("(devel)"), Dev},
		{"build with an empty module version", "", buildInfo(""), Dev},
		{"build without build info", "", noBuildInfo, Dev},
		{"ldflags override wins over a release", "1.0.0", buildInfo("v1.2.0"), "1.0.0"},
		{"ldflags override wins over a development build", "dev", buildInfo("(devel)"), "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolve(c.override, c.read); got != c.want {
				t.Fatalf("resolve(%q) = %q, want %q", c.override, got, c.want)
			}
		})
	}
}

// TestBuiltBinaryVersion builds ktx the way local builds do and checks what
// `ktx --version` prints with and without an ldflags override.
func TestBuiltBinaryVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the ktx binary")
	}
	root := filepath.Clean(filepath.Join("..", ".."))
	dir := t.TempDir()
	for _, c := range []struct {
		name    string
		ldflags string
		want    string
	}{
		{"development build", "", Dev},
		{"ldflags override", "-X github.com/jgabor/ktx/internal/version.Version=1.0.0", "1.0.0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			binary := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-"))
			build := exec.Command("go", "build", "-buildvcs=false", "-ldflags", c.ldflags, "-o", binary, "./cmd/ktx")
			build.Dir = root
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build ktx: %v\n%s", err, output)
			}
			output, err := exec.Command(binary, "--version").Output()
			if err != nil {
				t.Fatalf("ktx --version: %v", err)
			}
			if got := strings.TrimSpace(string(output)); got != c.want {
				t.Fatalf("ktx --version = %q, want %q", got, c.want)
			}
		})
	}
}
