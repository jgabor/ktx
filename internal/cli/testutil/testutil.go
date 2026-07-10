// Package testutil provides shared test helpers for the internal/cli
// package. The helpers exist so each subcommand test can stand up an
// isolated filesystem and XDG environment without repeating boilerplate.
//
// WithTempXDG points XDG_RUNTIME_DIR and XDG_DATA_HOME at per-test
// subdirectories under t.TempDir(). Session-file code paths consult
// XDG_RUNTIME_DIR ($XDG_RUNTIME_DIR/ktx/sessions/<ppid>.json, per the
// session.go sessionDir helper); database-path resolution consults
// XDG_DATA_HOME ($XDG_DATA_HOME/ktx/ktx.db, per dbpkg.DefaultPath).
// Setting both to per-test paths keeps tests parallel-safe and
// deterministic — a parallel test cannot observe another test's
// session file or price cache, and a stale ~/.local/share from a
// previous run cannot leak into the current run.
//
// t.Setenv restores the previous values on test cleanup, so a single
// WithTempXDG call covers the entire test body. Callers do not need to
// remember to unset. Subdirectories are created eagerly so any code
// path that reads the directory rather than the env var still works.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// WithTempXDG sets XDG_RUNTIME_DIR and XDG_DATA_HOME to per-test
// subdirectories under t.TempDir() and returns the base temp dir.
//
//	<XDG_RUNTIME_DIR> = <tmp>/run
//	<XDG_DATA_HOME>   = <tmp>/share
//
// Both subdirectories are created eagerly with 0o700 so the session-file
// helper (sessionDir -> MkdirAll) and the prices-file helper (PricesPath
// -> MkdirAll) operate against existing parents. t.Setenv restores the
// previous env values on test cleanup; callers do not need to unset.
//
// The base temp dir is returned for callers that want to assert against
// the resolved session-file path or pre-seed a price cache file.
func WithTempXDG(t *testing.T) string {
	t.Helper()

	tmp := t.TempDir()
	runtime := filepath.Join(tmp, "run")
	data := filepath.Join(tmp, "share")
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		t.Fatalf("testutil: create XDG_RUNTIME_DIR %q: %v", runtime, err)
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatalf("testutil: create XDG_DATA_HOME %q: %v", data, err)
	}

	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_DATA_HOME", data)

	return tmp
}
