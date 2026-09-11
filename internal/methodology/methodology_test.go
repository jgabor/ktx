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

	spec, source, err := Load("exact-match")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "v8-exact-match" || source != "tools/bench/specs/exact-match.yaml" {
		t.Fatalf("got version=%q source=%q", spec.Version, source)
	}
}

func TestParseReportsAllRequiredFields(t *testing.T) {
	_, err := Parse(`methodology_version: "v1"`)
	if err == nil {
		t.Fatal("Parse succeeded with missing fields")
	}
}
