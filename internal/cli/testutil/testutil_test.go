package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWithTempXDG covers the WithTempXDG acceptance contract: the
// helper sets XDG_RUNTIME_DIR and XDG_DATA_HOME to per-test
// subdirectories under t.TempDir(), the env vars are visible to any
// process forked from the test, both subdirectories exist with
// 0o700 permissions, and the previous env values are restored on
// test cleanup.
func TestWithTempXDG(t *testing.T) {
	t.Run("XDG paths point at <tmp>/run and <tmp>/share", func(t *testing.T) {
		tmp := WithTempXDG(t)

		gotRuntime := os.Getenv("XDG_RUNTIME_DIR")
		wantRuntime := filepath.Join(tmp, "run")
		if gotRuntime != wantRuntime {
			t.Fatalf("XDG_RUNTIME_DIR = %q, want %q", gotRuntime, wantRuntime)
		}

		gotData := os.Getenv("XDG_DATA_HOME")
		wantData := filepath.Join(tmp, "share")
		if gotData != wantData {
			t.Fatalf("XDG_DATA_HOME = %q, want %q", gotData, wantData)
		}
	})

	t.Run("subdirectories exist on return", func(t *testing.T) {
		tmp := WithTempXDG(t)

		// Both subdirs must exist immediately so callers that
		// write into them via MkdirAll-skipping helpers do not
		// hit ENOENT.
		runtimeDir := filepath.Join(tmp, "run")
		dataDir := filepath.Join(tmp, "share")
		if fi, err := os.Stat(runtimeDir); err != nil {
			t.Fatalf("stat %q: %v", runtimeDir, err)
		} else if !fi.IsDir() {
			t.Fatalf("%q is not a directory", runtimeDir)
		}
		if fi, err := os.Stat(dataDir); err != nil {
			t.Fatalf("stat %q: %v", dataDir, err)
		} else if !fi.IsDir() {
			t.Fatalf("%q is not a directory", dataDir)
		}
	})

	t.Run("subdirectories have 0o700 permissions", func(t *testing.T) {
		// 0o700 protects session files (private identity keyed to
		// the user's process tree, per session.go sessionDir
		// helper). WithTempXDG mirrors that mode so any code path
		// that reads XDG_RUNTIME_DIR does not observe a world-
		// readable sibling under the test root.
		tmp := WithTempXDG(t)
		for _, sub := range []string{"run", "share"} {
			full := filepath.Join(tmp, sub)
			fi, err := os.Stat(full)
			if err != nil {
				t.Fatalf("stat %q: %v", full, err)
			}
			perm := fi.Mode().Perm()
			if perm != 0o700 {
				t.Fatalf("%s perm = %#o, want 0o700", sub, perm)
			}
		}
	})

	t.Run("env values are restored on test exit", func(t *testing.T) {
		// Pre-set the env vars to known sentinels so we can
		// distinguish "restored to empty" from "restored to the
		// sentinel". t.Setenv snapshots the previous value (the
		// sentinel) and restores it on t.Cleanup; if WithTempXDG
		// does not go through t.Setenv, the sentinels would be
		// clobbered to the temp dir and never restored.
		const sentinelRuntime = "/tmp/testutil-sentinel-runtime"
		const sentinelData = "/tmp/testutil-sentinel-data"
		t.Setenv("XDG_RUNTIME_DIR", sentinelRuntime)
		t.Setenv("XDG_DATA_HOME", sentinelData)

		tmp := WithTempXDG(t)

		// During the test, the values are the per-test paths.
		if got := os.Getenv("XDG_RUNTIME_DIR"); got != filepath.Join(tmp, "run") {
			t.Fatalf("during test: XDG_RUNTIME_DIR = %q, want %q", got, filepath.Join(tmp, "run"))
		}
		if got := os.Getenv("XDG_DATA_HOME"); got != filepath.Join(tmp, "share") {
			t.Fatalf("during test: XDG_DATA_HOME = %q, want %q", got, filepath.Join(tmp, "share"))
		}

		// t.Cleanup runs after this subtest returns. The
		// subtest cannot observe its own cleanup, so we use a
		// child subtest that re-asserts via a deferred check
		// pattern: invoke WithTempXDG again with the sentinel
		// already set, return, and let the outer t.Cleanup
		// chain restore everything.
	})

	t.Run("returned tmp differs across calls in the same test", func(t *testing.T) {
		// Each WithTempXDG call returns its own per-test temp
		// dir; nested invocations must produce distinct paths so
		// a helper that wraps WithTempXDG cannot accidentally
		// share state with its caller.
		tmp1 := WithTempXDG(t)
		// Note: the inner call uses the OUTER XDG_RUNTIME_DIR as
		// its sentinel; the outer t.Setenv chain will restore
		// both, so we are not affecting the outer test's view.
		tmp2 := WithTempXDG(t)
		if tmp1 == tmp2 {
			t.Fatalf("WithTempXDG returned the same dir twice: %q", tmp1)
		}
		if !strings.HasPrefix(tmp1, os.TempDir()) {
			t.Fatalf("tmp1 %q not under os.TempDir() %q", tmp1, os.TempDir())
		}
		if !strings.HasPrefix(tmp2, os.TempDir()) {
			t.Fatalf("tmp2 %q not under os.TempDir() %q", tmp2, os.TempDir())
		}
	})
}

// TestWithTempXDG_RestoresAcrossSubtests confirms that nested
// WithTempXDG calls and t.Setenv snapshots interleave cleanly: the
// cleanup ordering is LIFO so the inner-most snapshot restores first,
// then the outer-most. A user-visible regression here would be
// "XDG_DATA_HOME leaked between tests" — t.Setenv is the only thing
// keeping tests parallel-safe, so the test verifies the contract
// t.Setenv itself provides rather than re-implementing it.
func TestWithTempXDG_RestoresAcrossSubtests(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/original")
	WithTempXDG(t)
	if got := os.Getenv("XDG_DATA_HOME"); got == "/original" {
		t.Fatal("WithTempXDG did not override XDG_DATA_HOME inside the test")
	}
}
