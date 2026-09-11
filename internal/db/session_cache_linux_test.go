package db

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const cacheTestTime = "2026-09-06T00:00:00Z"

func TestSessionCachePartialPublication(t *testing.T) {
	isolateSessionCache(t)
	d := openTestCacheDB(t)
	owner, err := d.OpenNamedSession("original", os.Getpid(), cacheTestTime)
	if err != nil {
		t.Fatal(err)
	}
	path := sessionFilePath(os.Getpid())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := sessionCacheCommand(t, "partial")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("partial writer: %v: %s", err, out)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("prior cache changed: before=%q after=%q err=%v", before, after, err)
	}
	sf, err := readSessionFile(os.Getpid())
	if err != nil || sf.SessionID != owner {
		t.Fatalf("prior identity unusable: %v, %v", sf, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions: %v, %v", info, err)
	}
}

func isolateSessionCache(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	t.Setenv("KTX_DB", "")
	if err := os.Unsetenv("KTX_DB"); err != nil {
		t.Fatal(err)
	}
}

func sessionCacheCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionCacheProcess$")
	cmd.Env = os.Environ()
	root := t.TempDir()
	// Only the disposable runtime cache and explicitly selected DB are shared.
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		cmd.Env = append(cmd.Env, key+"="+filepath.Join(root, key))
	}
	cmd.Env = append(cmd.Env, "KTX_TEST_CACHE_MODE="+mode)
	return cmd
}

func openTestCacheDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "owned.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestSessionCacheMalformedPreservesEvidence(t *testing.T) {
	for _, content := range []string{"{", "null", "[]", "{}", `{"session_id":"invalid","ppid":1,"ppid_start_time":0}`, `{"session_id":"OWNER","ppid":1,"ppid_start_time":0}`, `{"session_id":"OWNER","ppid":PARENT}`, `{"session_id":"OWNER","ppid":PARENT,"ppid_start_time":null}`, `{"session_id":"OWNER","ppid":PARENT,"ppid_start_time":-1}`, `{"session_id":"OWNER",`} {
		t.Run(content, func(t *testing.T) {
			isolateSessionCache(t)
			d := openTestCacheDB(t)
			owner, err := d.OpenNamedSession("original", os.Getppid(), cacheTestTime)
			if err != nil {
				t.Fatal(err)
			}
			content = strings.ReplaceAll(strings.ReplaceAll(content, "OWNER", owner), "PARENT", fmt.Sprint(os.Getppid()))
			path := sessionFilePath(os.Getppid())
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := d.ResolveSessionID("", cacheTestTime)
			if err == nil || id != "" || !strings.Contains(err.Error(), "preserve") || !strings.Contains(err.Error(), "explicit session_id") {
				t.Errorf("unsafe malformed recovery: %q, %v", id, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != content {
				t.Fatalf("malformed evidence changed: %q, %v", after, err)
			}
			var count int
			if err := d.db.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 1 {
				t.Fatalf("sessions=%d: %v", count, err)
			}
			if id, err := d.ResolveSessionID(owner, cacheTestTime); err != nil || id != owner {
				t.Fatalf("explicit recovery: %q, %v", id, err)
			}
		})
	}
}

func TestSessionCacheConcurrentParentOwnership(t *testing.T) {
	isolateSessionCache(t)
	path := filepath.Join(t.TempDir(), "owned.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const workers = 6
	commands := make([]*exec.Cmd, workers)
	outputs := make([]*bufio.Scanner, workers)
	errors := make([]bytes.Buffer, workers)
	err = d.WithHookWriter(func(*HookWriter) error {
		for i := range workers {
			cmd := sessionCacheCommand(t, "resolve")
			cmd.Env = append(cmd.Env, "KTX_TEST_CACHE_DB="+path, "KTX_TEST_CACHE_SOURCE="+strings.Repeat(string(rune('a'+i)), 16))
			cmd.Stderr = &errors[i]
			out, err := cmd.StdoutPipe()
			if err != nil {
				return err
			}
			if err := cmd.Start(); err != nil {
				return err
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			commands[i], outputs[i] = cmd, bufio.NewScanner(out)
			if !outputs[i].Scan() || outputs[i].Text() != "ready" {
				return fmt.Errorf("worker not ready: %s", errors[i].String())
			}
		}
		// All children enter resolution while the existing DB lease is held.
		// Before the fix they read the absent cache and queue independent inserts.
		time.Sleep(200 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for i, cmd := range commands {
		if !outputs[i].Scan() {
			t.Fatalf("no identity from worker %d: %s", i, errors[i].String())
		}
		id := outputs[i].Text()
		if first == "" {
			first = id
		}
		if id != first {
			t.Errorf("losing resolver reassociated: %s != %s", id, first)
		}
		for outputs[i].Scan() {
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("worker %d: %v: %s", i, err, errors[i].String())
		}
	}
	sf, err := readSessionFile(os.Getpid())
	if err != nil || sf.SessionID != first {
		t.Fatalf("cache owner differs: %v, %v", sf, err)
	}
	var sessions, sources int
	if err := d.db.QueryRow("SELECT count(*) FROM sessions").Scan(&sessions); err != nil || sessions != 1 {
		t.Errorf("sessions=%d: %v", sessions, err)
	}
	if err := d.db.QueryRow("SELECT count(*) FROM tool_calls WHERE session_id = ?", first).Scan(&sources); err != nil || sources != workers {
		t.Errorf("sources for owner=%d: %v", sources, err)
	}
}

func TestSessionCacheLifetimeAndDatabaseAuthority(t *testing.T) {
	for _, state := range []string{"fresh", "stale", "missing-session", "other-database"} {
		t.Run(state, func(t *testing.T) {
			isolateSessionCache(t)
			d := openTestCacheDB(t)
			owner, err := d.OpenNamedSession("original", os.Getppid(), cacheTestTime)
			if err != nil {
				t.Fatal(err)
			}
			sf, err := readSessionFile(os.Getppid())
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case "stale":
				sf.PPIDStartTime++
				if err := writeSessionFile(os.Getppid(), sf); err != nil {
					t.Fatal(err)
				}
			case "missing-session":
				sf.SessionID = "zzzzzzzzzzzzzzzz"
				if err := writeSessionFile(os.Getppid(), sf); err != nil {
					t.Fatal(err)
				}
			}
			selected := d
			if state == "other-database" {
				selected = openTestCacheDB(t)
			}
			got, err := selected.ResolveSessionID("", cacheTestTime)
			if err != nil {
				t.Fatal(err)
			}
			if (got == owner) != (state == "fresh") {
				t.Fatalf("identity %s -> %s for %s", owner, got, state)
			}
			if reused, err := selected.ResolveSessionID("", cacheTestTime); err != nil || reused != got {
				t.Fatalf("new owner not stable: %s, %v", reused, err)
			}
			var ended string
			if err := d.db.QueryRow("SELECT COALESCE(ended_at, '') FROM sessions WHERE id = ?", owner).Scan(&ended); err != nil {
				t.Fatal(err)
			}
			if (ended != "") != (state == "stale") {
				t.Fatalf("original lifetime changed: %q for %s", ended, state)
			}
		})
	}
}

func TestSessionCacheSchemaGuard(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			isolateSessionCache(t)
			d := openTestCacheDB(t)
			if _, err := d.OpenNamedSession("original", os.Getppid(), cacheTestTime); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(sessionFilePath(os.Getppid()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
				t.Fatal(err)
			}
			if named {
				_, err = d.OpenNamedSession("replacement", os.Getppid(), cacheTestTime)
			} else {
				_, err = d.ResolveSessionID("", cacheTestTime)
			}
			requireNewerSchema(t, err)
			after, err := os.ReadFile(sessionFilePath(os.Getppid()))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("schema rejection changed cache: %q, %v", after, err)
			}
			if n := tableRowCount(t, d.db, "sessions"); n != 1 {
				t.Fatalf("schema rejection changed sessions: %d", n)
			}
		})
	}
}

func TestSessionCacheProcess(t *testing.T) {
	switch os.Getenv("KTX_TEST_CACHE_MODE") {
	case "partial":
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 16, Max: 16}); err != nil {
			t.Fatal(err)
		}
		if err := writeSessionFile(os.Getppid(), &SessionFile{SessionID: "zzzzzzzzzzzzzzzz", PPID: os.Getppid()}); err == nil {
			t.Fatal("partial write unexpectedly succeeded")
		}
	case "resolve":
		d, err := OpenHook(os.Getenv("KTX_TEST_CACHE_DB"))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		fmt.Println("ready")
		id, err := d.ResolveSessionID("", cacheTestTime)
		if err != nil {
			t.Fatal(err)
		}
		tc := &ToolCall{ID: os.Getenv("KTX_TEST_CACHE_SOURCE"), SessionID: id, Timestamp: cacheTestTime, Tool: "read", Input: "{}", Output: "original"}
		if err := d.PersistToolCall(tc); err != nil {
			t.Fatal(err)
		}
		// The hook path must reuse the same owner without reacquiring its lease.
		if err := d.WithHookWriter(func(w *HookWriter) error {
			reused, err := w.ResolveSessionID("", cacheTestTime)
			if err == nil && reused != id {
				return fmt.Errorf("hook changed owner: %s != %s", reused, id)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		fmt.Println(id)
	default:
		t.Skip("subprocess helper")
	}
}
