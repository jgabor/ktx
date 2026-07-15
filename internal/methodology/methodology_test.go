package methodology

import (
	"os"
	"testing"
)

func TestLoadIsIndependentOfWorkingDirectory(t *testing.T) {
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	for _, version := range []string{"v1", "v2-tooluse", "v3", "v5-cache-aware"} {
		spec, source, err := Load(version)
		if err != nil {
			t.Fatalf("Load(%q): %v", version, err)
		}
		wantSource := "tools/bench/methodology-" + version + ".yaml"
		if spec.Version != version || source != wantSource {
			t.Fatalf("got version=%q source=%q", spec.Version, source)
		}
	}
}

func TestParseReportsAllRequiredFields(t *testing.T) {
	_, err := Parse(`methodology_version: "v1"`)
	if err == nil {
		t.Fatal("Parse succeeded with missing fields")
	}
}
