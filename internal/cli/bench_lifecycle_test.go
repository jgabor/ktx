package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/experiment"
)

func TestBenchLifecycleListsMalformedAndDuplicateManifests(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	experiments := filepath.Join(root, ".ktx", "experiments")
	writeManifest := func(directory, id string) {
		t.Helper()
		path := filepath.Join(experiments, directory)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := experiment.WriteJSON(filepath.Join(path, "manifest.json"), experiment.Manifest{ExperimentID: id}); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest("good", "good")
	writeManifest("duplicate-a", "duplicate")
	writeManifest("duplicate-b", "duplicate")
	broken := filepath.Join(experiments, "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "manifest.json"), []byte("["), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runBenchList(nil, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("list exit = %d, stderr = %s", code, stderr.String())
	}
	var entries []experimentListEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	statuses := map[string][]string{}
	for _, entry := range entries {
		statuses[entry.ID] = append(statuses[entry.ID], entry.Status+":"+entry.Detail)
	}
	if got := statuses["good"]; len(got) != 1 || got[0] != "ok:" {
		t.Fatalf("good status = %#v", got)
	}
	if got := statuses["broken"]; len(got) != 1 || got[0] != "malformed:malformed manifest" {
		t.Fatalf("broken status = %#v", got)
	}
	if got := statuses["duplicate"]; len(got) != 2 || got[0] != "duplicate:" || got[1] != "duplicate:" {
		t.Fatalf("duplicate status = %#v", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runBenchShow([]string{"good"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("show exit = %d, stderr = %s", code, stderr.String())
	}
	for _, test := range []struct {
		id      string
		message string
	}{
		{id: "missing", message: "not found"},
		{id: "../outside", message: "invalid experiment id"},
		{id: "duplicate", message: "duplicate experiment id"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := runBenchShow([]string{test.id}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitUsage {
			t.Fatalf("show %q exit = %d, stderr = %s", test.id, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), test.message) {
			t.Fatalf("show %q stderr = %q", test.id, stderr.String())
		}
	}
}

func TestBenchRankRejectsTraversalBeforeCreatingArtifacts(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := runBenchRank([]string{
		"--model", "openai/test",
		"--spec", "exact-match",
		"--task", "crossfile-nav",
		"--candidate", "conservative-a",
		"--campaign-id", "../outside",
	}, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), "invalid experiment id") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
		t.Fatalf("outside path exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".ktx")); !os.IsNotExist(err) {
		t.Fatalf("artifact root exists: %v", err)
	}
}
