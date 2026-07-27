package db

import (
	"path/filepath"
	"testing"
)

func TestResolveTargetPreservesOwnershipProvenance(t *testing.T) {
	defaultRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", defaultRoot)
	t.Setenv("KTX_DB", "")

	target, err := ResolveTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if target.Path != filepath.Join(defaultRoot, "ktx", "ktx.db") || target.Source != TargetAmbientDefault || !target.RequiresRuntimeProof() {
		t.Fatalf("default target = %#v", target)
	}

	environmentPath := filepath.Join(t.TempDir(), "environment.db")
	t.Setenv("KTX_DB", environmentPath)
	target, err = ResolveTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if target.Path != environmentPath || target.Source != TargetEnvironment || target.RequiresRuntimeProof() {
		t.Fatalf("environment target = %#v", target)
	}

	overridePath := filepath.Join(t.TempDir(), "override.db")
	target, err = ResolveTarget(overridePath)
	if err != nil {
		t.Fatal(err)
	}
	if target.Path != overridePath || target.Source != TargetOverride || target.RequiresRuntimeProof() {
		t.Fatalf("override target = %#v", target)
	}
}

func TestOwnedTargetRejectsEmptyPath(t *testing.T) {
	if _, err := OwnedTarget(""); err == nil {
		t.Fatal("OwnedTarget accepted an empty path")
	}
}
