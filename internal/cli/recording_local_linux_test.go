package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestRecordingLocalCLI(t *testing.T) {
	binary := os.Getenv("KTX_RECORDING_CLI")
	if binary == "" {
		t.Skip("set KTX_RECORDING_CLI to the locally built binary")
	}
	for _, size := range []int{1, 8, 16} {
		for _, exit := range []int{0, 23} {
			t.Run(strconv.Itoa(size)+"MiB-exit"+strconv.Itoa(exit), func(t *testing.T) {
				root := t.TempDir()
				var env []string
				for _, item := range os.Environ() {
					key, _, _ := strings.Cut(item, "=")
					if key != "KTX_DB" && key != "HOME" && key != "TMPDIR" && key != "PATH" && !strings.HasPrefix(key, "XDG_") {
						env = append(env, item)
					}
				}
				for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"} {
					dir := filepath.Join(root, key)
					if err := os.Mkdir(dir, 0o700); err != nil {
						t.Fatal(err)
					}
					env = append(env, key+"="+dir)
				}
				env = append(env, "PATH="+root+":/usr/bin:/bin", "RECORDING_ROOT="+root)
				line := `{"type":"text","content":"padding"}` + "\n"
				text := strings.Repeat(line, size*1024*1024/len(line)) + `{"type":"step_finish","part":{"cost":0.25,"model":"offline-model","provider":"offline","tokens":{"input":11,"output":7,"reasoning":3,"cache":{"read":5,"write":2}}}}` + "\n" + `{"type":"text","content":"4"}` + "\n"
				diagnostic := " \r\n\x00" + strings.Repeat("d", size*1024*1024) + "\t"
				for name, data := range map[string]string{"stdout": text, "stderr": diagnostic} {
					if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				script := "#!/bin/sh\ncat \"$RECORDING_ROOT/stdout\"\ncat \"$RECORDING_ROOT/stderr\" >&2\nexit " + strconv.Itoa(exit) + "\n"
				if err := os.WriteFile(filepath.Join(root, "opencode"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(binary, "bench", "run", "opencode", "glm-5.2-short-fast-flex", "addition_v1")
				cmd.Env, cmd.Dir = env, root
				var out, diag bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &diag
				if err := cmd.Run(); err != nil {
					t.Fatalf("bench failed: %v %s", err, diag.Bytes())
				}
				var headline struct {
					Headline float64 `json:"headline"`
				}
				if err := json.Unmarshal(out.Bytes(), &headline); err != nil || headline.Headline != 0.25 {
					t.Fatalf("headline %s: %v", out.Bytes(), err)
				}
				if exit == 0 && bytes.Contains(diag.Bytes(), []byte("--- wrapper stderr ---")) {
					t.Fatal("successful diagnostics forwarded")
				}
				if exit != 0 && !bytes.Contains(diag.Bytes(), []byte("--- wrapper stderr ---\n"+diagnostic)) {
					t.Fatal("failed diagnostics changed bytes or order")
				}
				path := filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db")
				d, err := dbpkg.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				var score, cost float64
				var in, output, read, write, reasoning int64
				var model, agent string
				if err := d.DB().QueryRow(`SELECT score,cost,tokens_in,tokens_out,cache_read,cache_write,reasoning,model,agent FROM bench_runs`).Scan(&score, &cost, &in, &output, &read, &write, &reasoning, &model, &agent); err != nil {
					t.Fatal(err)
				}
				if score != 1 || cost != 0.25 || in != 11 || output != 7 || read != 5 || write != 2 || reasoning != 3 || model != "offline-model" || agent != "offline/offline-model" {
					t.Fatal("benchmark metrics changed")
				}
				var recordings int
				if err := d.DB().QueryRow(`SELECT count(*) FROM recorded_streams`).Scan(&recordings); err != nil {
					t.Fatal(err)
				}
				lowered := os.Getenv("KTX_TEST_RECORDING_LOWERED") == "1"
				if lowered {
					if recordings != 0 || !strings.Contains(diag.String(), "exceeds SQLite length limit 4096") {
						t.Fatal("over-limit publication was not recording-only failure")
					}
					files, err := filepath.Glob(filepath.Join(root, "XDG_DATA_HOME", "ktx", "recording-*", "stream"))
					if err != nil || len(files) != 1 {
						t.Fatalf("shared last copy count %v %v", files, err)
					}
					data, err := os.ReadFile(files[0])
					if err != nil || string(data) != text {
						t.Fatal("retained stdout mismatch", err)
					}
					for _, entry := range []struct {
						path string
						mode os.FileMode
					}{{files[0], 0o600}, {filepath.Dir(files[0]), 0o700}} {
						info, err := os.Stat(entry.path)
						if err != nil || info.Mode().Perm() != entry.mode {
							t.Fatal("private modes", err)
						}
					}
				} else if recordings != 1 {
					t.Fatalf("recordings %d", recordings)
				}
			})
		}
	}
}

func TestRecordingCrashRetentionLocalProcess(t *testing.T) {
	if stage := os.Getenv("KTX_TEST_RECORDING_CRASH_CHILD"); stage != "" {
		s, err := newRecordingSpool()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write([]byte("surviving bytes\n")); err != nil {
			t.Fatal(err)
		}
		if stage != "capturing" {
			if err := s.finish(); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "completed" {
			if err := s.complete("abcdefghijklmnop"); err != nil {
				t.Fatal(err)
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(s.path); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	for _, stage := range []string{"capturing", "closed", "completed"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			var env []string
			for _, item := range os.Environ() {
				key, _, _ := strings.Cut(item, "=")
				if key != "KTX_DB" && key != "HOME" && key != "TMPDIR" && !strings.HasPrefix(key, "XDG_") {
					env = append(env, item)
				}
			}
			for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"} {
				dir := filepath.Join(root, key)
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				env = append(env, key+"="+dir)
			}
			env = append(env, "KTX_TEST_RECORDING_CRASH_CHILD="+stage)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecordingCrashRetentionLocalProcess$")
			cmd.Env = env
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var path string
			if err := json.NewDecoder(output).Decode(&path); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("crash fixture exited normally")
			}
			data, err := os.ReadFile(filepath.Join(path, "stream"))
			if err != nil || string(data) != "surviving bytes\n" {
				t.Fatal("crash lost last copy", err)
			}
			_, err = os.Stat(filepath.Join(path, "completion.json"))
			if (err == nil) != (stage == "completed") {
				t.Fatalf("crash changed completion state: %v", err)
			}
		})
	}
}

func TestRecordingSpoolConfinesReopen(t *testing.T) {
	s := spoolFixture(t, "owned")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.root.Remove("stream"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.path, "stream")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := interpretRecording(s); err == nil {
		t.Fatal("recording reopened outside its owner root")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "outside" {
		t.Fatal("outside artifact changed", err)
	}
}

func TestRecordingRequiredFailureLocalCLI(t *testing.T) {
	binary, mode := os.Getenv("KTX_RECORDING_CLI"), os.Getenv("KTX_TEST_RECORDING_FAILURE")
	if binary == "" || (mode != "capture" && mode != "read") {
		t.Skip("requires an isolated fault-instrumented local build")
	}
	root := t.TempDir()
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key != "KTX_DB" && key != "HOME" && key != "TMPDIR" && key != "PATH" && !strings.HasPrefix(key, "XDG_") {
			env = append(env, item)
		}
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"} {
		dir := filepath.Join(root, key)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		env = append(env, key+"="+dir)
	}
	env = append(env, "PATH="+root+":/usr/bin:/bin")
	if err := os.WriteFile(filepath.Join(root, "opencode"), []byte("#!/bin/sh\nprintf '{\"type\":\"text\",\"content\":\"4\"}\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "bench", "run", "opencode", "glm-5.2-short-fast-flex", "addition_v1")
	cmd.Dir, cmd.Env = root, env
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != ExitInternal || out.Len() != 0 || !strings.Contains(diag.String(), "stdout ") {
		t.Fatalf("partial headline: err %v stdout %s stderr %s", err, out.String(), diag.String())
	}
	d, err := dbpkg.Open(filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var count int
	if err := d.DB().QueryRow(`SELECT (SELECT count(*) FROM bench_runs)+(SELECT count(*) FROM recorded_streams)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial evidence: %d %v", count, err)
	}
	files, err := filepath.Glob(filepath.Join(root, "XDG_DATA_HOME", "ktx", "recording-*", "stream"))
	if err != nil || len(files) == 0 {
		t.Fatal("last copies lost", err)
	}
}

func TestRecordingStandaloneLocalCLI(t *testing.T) {
	binary := os.Getenv("KTX_RECORDING_CLI")
	if binary == "" {
		t.Skip("set KTX_RECORDING_CLI to the locally built binary")
	}
	for _, mode := range []string{"empty", "nonzero", "publication-failure"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			var env []string
			for _, item := range os.Environ() {
				key, _, _ := strings.Cut(item, "=")
				if key != "KTX_DB" && key != "HOME" && key != "TMPDIR" && !strings.HasPrefix(key, "XDG_") {
					env = append(env, item)
				}
			}
			for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"} {
				dir := filepath.Join(root, key)
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				env = append(env, key+"="+dir)
			}
			path := filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db")
			d, err := dbpkg.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if mode == "publication-failure" {
				if _, err := d.DB().Exec(`CREATE TRIGGER fail_recording BEFORE INSERT ON recorded_streams BEGIN SELECT RAISE(ABORT,'offline failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			script, want := "exit 0", ""
			if mode != "empty" {
				script = "printf ' a\\r\\nb\\r'; exit 23"
				want = " a\nb\n"
			}
			cmd := exec.Command(binary, "run", "--quiet", "--record-stream", "sh", "-c", script)
			cmd.Env, cmd.Dir = env, root
			var out, diag bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diag
			err = cmd.Run()
			if (err == nil) != (mode == "empty") || out.String() != want {
				t.Fatalf("run: %v stdout %q stderr %s", err, out.String(), diag.String())
			}
			if mode != "empty" && cmd.ProcessState.ExitCode() != 23 {
				t.Fatalf("child exit changed: %v", err)
			}
			var count int
			if err := d.DB().QueryRow(`SELECT count(*) FROM recorded_streams`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if mode == "publication-failure" {
				if count != 0 || !strings.Contains(diag.String(), "private copy retained") {
					t.Fatalf("failed publication: rows %d diagnostics %s", count, diag.String())
				}
				files, err := filepath.Glob(filepath.Join(root, "XDG_DATA_HOME", "ktx", "recording-*", "stream"))
				if err != nil || len(files) != 1 {
					t.Fatal("missing private copy", err)
				}
				data, err := os.ReadFile(files[0])
				if err != nil || string(data) != want {
					t.Fatal("retained bytes", err)
				}
			} else {
				var text, hash, link string
				if count != 1 {
					t.Fatalf("recordings %d", count)
				}
				if err := d.DB().QueryRow(`SELECT stream_text,content_hash,coalesce(bench_run_id,'') FROM recorded_streams`).Scan(&text, &hash, &link); err != nil {
					t.Fatal(err)
				}
				if text != want || hash != dbpkg.ContentHash(want) || link != "" {
					t.Fatal("ordinary recording changed")
				}
			}
		})
	}
}
