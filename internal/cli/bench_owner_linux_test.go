package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// These regressions use only an explicitly supplied local build and an offline
// fake agent. Each invocation gets its own application roots and database.
func TestBenchOwnedSessionLocalCLI(t *testing.T) {
	binary := os.Getenv("KTX_BENCH_OWNER_CLI")
	if binary == "" {
		t.Skip("set KTX_BENCH_OWNER_CLI to the mage-built binary")
	}
	for _, mode := range []string{"concurrent", "startup-failure", "missing-agent", "nonzero", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			var env []string
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != "KTX_DB" && key != "KTX_PRICES" && key != "HOME" && key != "PATH" && !strings.HasPrefix(key, "XDG_") {
					env = append(env, entry)
				}
			}
			for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
				env = append(env, key+"="+filepath.Join(root, key))
			}
			env = append(env, "PATH="+root+":/usr/bin:/bin", "OWNER_ROOT="+root)
			path := filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db")
			d, err := dbpkg.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			history := "abcdefghijklmnop"
			if err := d.InsertSession(&dbpkg.Session{ID: history, Agent: "opencode", StartedAt: "2000-01-01T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			if err := captureStreamForRun(d, history, []byte("historical evidence\n")); err != nil {
				t.Fatal(err)
			}
			if mode == "startup-failure" {
				// Fail before a wrapper session exists, without affecting bench's
				// readiness or the retained historical session.
				if _, err := d.DB().Exec(`CREATE TRIGGER fail_session BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT, 'offline startup failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			stream := "{\"tool\":\"read\",\"output\":\"owned one\"}\n{\"tool\":\"read\",\"output\":\"owned two\"}\n" +
				"{\"type\":\"step_finish\",\"part\":{\"cost\":0.25,\"tokens\":{\"input\":11,\"output\":7,\"reasoning\":0,\"cache\":{\"read\":0,\"write\":0}}}}\n" +
				"{\"type\":\"text\",\"part\":{\"text\":\"4\"}}\n"
			if err := os.WriteFile(filepath.Join(root, "stream"), []byte(stream), 0o600); err != nil {
				t.Fatal(err)
			}
			if mode != "missing-agent" {
				script := "#!/bin/sh\nprintf '%s' \"$KTX_SESSION_ID\" > \"$OWNER_ROOT/ready\"\nwhile [ ! -f \"$OWNER_ROOT/release\" ]; do sleep 0.01; done\ncat \"$OWNER_ROOT/stream\"\n"
				if mode == "nonzero" {
					script += "exit 23\n"
				}
				if mode == "interrupted" {
					script += "kill -TERM \"$PPID\"\nwhile :; do sleep 0.01; done\n"
				}
				if err := os.WriteFile(filepath.Join(root, "opencode"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				// Do not discover an installed agent during the missing-agent case.
				env = append(env, "PATH="+root)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "bench", "run", "opencode", "glm-5.2-short-fast-flex", "addition_v1")
			cmd.Dir, cmd.Env = root, env
			var output, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &diagnostic
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- cmd.Wait() }()
			var owner string
			if mode == "concurrent" || mode == "nonzero" || mode == "interrupted" {
				for owner == "" {
					data, _ := os.ReadFile(filepath.Join(root, "ready"))
					owner = string(data)
					select {
					case err := <-wait:
						t.Fatalf("exited before fake agent: %v: %s", err, &diagnostic)
					case <-ctx.Done():
						t.Fatal("fake agent readiness timed out")
					default:
					}
					time.Sleep(10 * time.Millisecond)
				}
				for _, id := range []string{"ponmlkjihgfedcba", "bbbbbbbbbbbbbbbb"} {
					if err := d.InsertSession(&dbpkg.Session{ID: id, Agent: "opencode", StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(root, "release"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err = <-wait
			if mode == "startup-failure" || mode == "missing-agent" || mode == "interrupted" {
				if err == nil || output.Len() != 0 || !strings.Contains(diagnostic.String(), "owned session") {
					t.Errorf("startup failure produced false evidence: err=%v stdout=%s stderr=%s", err, &output, &diagnostic)
				}
				var count int
				if err := d.DB().QueryRow(`SELECT count(*) FROM bench_runs`).Scan(&count); err != nil || count != 0 {
					t.Errorf("bench rows=%d err=%v", count, err)
				}
				if err := d.DB().QueryRow(`SELECT count(*) FROM recorded_streams`).Scan(&count); err != nil || count != 1 {
					t.Errorf("incomplete execution recorded a stream: rows=%d err=%v", count, err)
				}
				copies, err := filepath.Glob(filepath.Join(root, "XDG_DATA_HOME", "ktx", "recording-*", "stream"))
				if err != nil || len(copies) != 1 {
					t.Fatalf("incomplete private copies: %v %v", copies, err)
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(copies[0]), "completion.json")); !os.IsNotExist(err) {
					t.Fatalf("missing receipt acquired completion metadata: %v", err)
				}
				if mode == "interrupted" {
					data, err := os.ReadFile(copies[0])
					if err != nil || string(data) != stream {
						t.Fatalf("interrupted private bytes lost: %v", err)
					}
				}
			} else {
				if err != nil {
					t.Fatalf("bench: %v: %s", err, &diagnostic)
				}
				var envelope struct {
					Rows []dbpkg.BenchRun `json:"rows"`
				}
				if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || len(envelope.Rows) != 1 {
					t.Fatalf("headline: %s: %v", &output, err)
				}
				br := envelope.Rows[0]
				if br.Score != 1 || br.TokensIn != 11 || br.TokensOut != 7 || br.Cost == nil || *br.Cost != 0.25 {
					t.Errorf("score/count/cost mismatch: %+v", br)
				}
				var gotOwner, gotStream, hash string
				if err := d.DB().QueryRow(`SELECT session_id, stream_text, content_hash FROM recorded_streams WHERE bench_run_id = ?`, br.ID).Scan(&gotOwner, &gotStream, &hash); err != nil {
					t.Fatal(err)
				}
				if gotOwner != owner || gotStream != stream || hash != dbpkg.ContentHash(stream) {
					t.Errorf("recorded owner=%q want=%q; stream matches=%v hash matches=%v", gotOwner, owner, gotStream == stream, hash == dbpkg.ContentHash(stream))
				}
				var rollup, calls int
				if err := d.DB().QueryRow(`SELECT tool_call_count, (SELECT count(*) FROM tool_calls WHERE session_id = ?) FROM sessions WHERE id = ?`, owner, owner).Scan(&rollup, &calls); err != nil || rollup != 2 || calls != 2 {
					t.Errorf("owned count=%d rollup=%d err=%v", calls, rollup, err)
				}
				var unrelated int
				if err := d.DB().QueryRow(`SELECT count(*) FROM recorded_streams WHERE session_id NOT IN (?, ?)`, owner, history).Scan(&unrelated); err != nil || unrelated != 0 {
					t.Errorf("unrelated sessions received %d streams: %v", unrelated, err)
				}
			}
			var retained string
			if err := d.DB().QueryRow(`SELECT stream_text FROM recorded_streams WHERE session_id = ?`, history).Scan(&retained); err != nil || retained != "historical evidence\n" {
				t.Errorf("history changed: %q err=%v", retained, err)
			}
		})
	}
}
