package experiment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerUsesFixedRootAndRejectsCollision(t *testing.T) {
	root := t.TempDir()
	checkout, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, ".ktx", "experiments", "collision")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, code, err := NewRunner().Compare(context.Background(), CompareOptions{
		Model:           "openai/test",
		RepoRoot:        root,
		ExperimentID:    "collision",
		Task:            Task{ID: "task", Prompt: "work", ExpectedFacts: []string{"done"}},
		MethodologyPath: filepath.Join(checkout, "tools", "bench", "specs", "eval-matrix.yaml"),
		DockerBin:       "docker",
	})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "keep" {
		t.Fatalf("collision changed existing artifact: data=%q err=%v", data, readErr)
	}
}

func TestRepositoryWorkspaceExcludesKTX(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(filepath.Join(source, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(source, ".ktx", "experiments", "ignored")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generated, "manifest.json"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareWorkspace(source, destination, "repo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "keep.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".ktx")); !os.IsNotExist(err) {
		t.Fatalf(".ktx was copied into workspace: %v", err)
	}
}
