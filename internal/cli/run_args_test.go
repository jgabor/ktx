package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// The same table exercises dispatch in-process and the explicitly selected
// local build. The child is a real executable and captures NUL-delimited argv.
func TestRunArgumentOwnership(t *testing.T) {
	for _, mode := range []string{"dispatch", "binary"} {
		t.Run(mode, func(t *testing.T) {
			binary := os.Getenv("KTX_TEST_RUN_BINARY")
			if mode == "binary" && binary == "" {
				t.Skip("set KTX_TEST_RUN_BINARY to the mage-built local CLI")
			}
			for _, tc := range []struct {
				name       string
				args       []string
				want       []string
				quiet      bool
				help       bool
				usage      bool
				record     bool
				collection string
				executable string
				exit       int
			}{
				{name: "before false", args: []string{"run", "--quiet=false", "--help=false", "CHILD", "plain"}, want: []string{"plain"}},
				{name: "after false", args: []string{"run", "CHILD", "--quiet=false", "--help=false", "plain"}, want: []string{"plain"}},
				{name: "numeric false", args: []string{"run", "--quiet=1", "CHILD", "-q=0", "-h=0"}},
				{name: "boolean spellings", args: []string{"run", "-quiet=T", "-help=F", "CHILD", "--quiet=False", "--help=False"}},
				{name: "global quiet inherited", args: []string{"--quiet", "run", "CHILD"}, quiet: true},
				{name: "last false", args: []string{"--quiet", "run", "--quiet", "CHILD", "--quiet=false"}},
				{name: "after true", args: []string{"run", "CHILD", "-q=true", "--record-stream=0"}, quiet: true},
				{name: "before boundary", args: []string{"run", "--quiet", "--", "CHILD", "--help", "--record-stream", "--collection=bad", "--quiet=false"}, want: []string{"--help", "--record-stream", "--collection=bad", "--quiet=false"}, quiet: true},
				{name: "after boundary", args: []string{"run", "CHILD", "--", "--record-stream", "--help=false", "--", ""}, want: []string{"--record-stream", "--help=false", "--", ""}},
				{name: "unknown child values escaped", args: []string{"run", "CHILD", "--", "--prompt", "--quiet", "--format", "--help"}, want: []string{"--prompt", "--quiet", "--format", "--help"}},
				{name: "unknown arity not guessed", args: []string{"run", "CHILD", "--prompt", "--quiet"}, want: []string{"--prompt"}, quiet: true},
				{name: "child values attached", args: []string{"run", "CHILD", "--prompt=--quiet", "--format=json", "--format", "json"}, want: []string{"--prompt=--quiet", "--format=json", "--format", "json"}},
				{name: "record false after true", args: []string{"run", "--record-stream", "CHILD", "--record-stream=false"}},
				{name: "record before bare", args: []string{"run", "--record-stream", "CHILD"}, record: true},
				{name: "record after bare", args: []string{"run", "CHILD", "--record-stream"}, record: true},
				{name: "record before true", args: []string{"run", "--record-stream=true", "CHILD"}, record: true},
				{name: "record after true", args: []string{"run", "CHILD", "--record-stream=true"}, record: true},
				{name: "record before one", args: []string{"run", "--record-stream=1", "CHILD"}, record: true},
				{name: "record after one", args: []string{"run", "CHILD", "--record-stream=1"}, record: true},
				{name: "record before false", args: []string{"run", "--record-stream=false", "CHILD"}},
				{name: "record before zero", args: []string{"run", "--record-stream=0", "CHILD"}},
				{name: "collection before", args: []string{"run", "--collection=abcdefghijklmnop", "CHILD"}, collection: "abcdefghijklmnop"},
				{name: "collection after", args: []string{"run", "CHILD", "--collection=abcdefghijklmnop"}, collection: "abcdefghijklmnop"},
				{name: "executable named false", args: []string{"run", "--record-stream", "CHILD", "tail"}, executable: "false", want: []string{"tail"}, record: true},
				{name: "executable named true", args: []string{"run", "--quiet", "CHILD"}, executable: "true", quiet: true},
				{name: "executable named zero", args: []string{"run", "--record-stream", "CHILD"}, executable: "0", record: true},
				{name: "executable named one", args: []string{"run", "--quiet", "CHILD"}, executable: "1", quiet: true},
				{name: "executable escaped", args: []string{"run", "--", "CHILD", "--help"}, executable: "--quiet", want: []string{"--help"}},
				{name: "child exit", args: []string{"run", "CHILD"}, exit: 23},
				{name: "help before", args: []string{"run", "--help"}, help: true},
				{name: "help after", args: []string{"run", "CHILD", "--help"}, help: true},
				{name: "help last false", args: []string{"run", "--help", "CHILD", "--help=false"}},
				{name: "global format", args: []string{"--format", "text", "run", "CHILD"}},
				{name: "consumed global format", args: []string{"--format", "--help", "run", "CHILD"}, usage: true},
				{name: "consumed collection value", args: []string{"run", "CHILD", "--collection=--help"}, usage: true},
				{name: "missing executable", args: []string{"run"}, usage: true},
				{name: "empty boundary", args: []string{"run", "--"}, usage: true},
				{name: "missing format", args: []string{"--format"}, usage: true},
				{name: "empty format", args: []string{"--format=", "run", "CHILD"}, usage: true},
				{name: "missing collection", args: []string{"run", "--collection", "CHILD"}, usage: true},
				{name: "empty collection", args: []string{"run", "CHILD", "--collection="}, usage: true},
				{name: "duplicate collection", args: []string{"run", "--collection=abcdefghijklmnop", "CHILD", "--collection=ponmlkjihgfedcba"}, usage: true},
				{name: "empty quiet", args: []string{"run", "CHILD", "--quiet="}, usage: true},
				{name: "empty quiet before", args: []string{"run", "--quiet=", "CHILD"}, usage: true},
				{name: "malformed quiet", args: []string{"run", "CHILD", "--quiet=maybe"}, usage: true},
				{name: "empty help", args: []string{"run", "CHILD", "--help="}, usage: true},
				{name: "empty help before", args: []string{"run", "--help=", "CHILD"}, usage: true},
				{name: "malformed help", args: []string{"run", "CHILD", "--help=maybe"}, usage: true},
				{name: "empty recording", args: []string{"run", "CHILD", "--record-stream="}, usage: true},
				{name: "empty recording before", args: []string{"run", "--record-stream=", "CHILD"}, usage: true},
				{name: "malformed recording", args: []string{"run", "CHILD", "--record-stream=maybe"}, usage: true},
				{name: "unknown wrapper option", args: []string{"run", "--unknown", "CHILD"}, usage: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
						t.Setenv(key, filepath.Join(root, key))
					}
					t.Setenv("KTX_DB", "")
					if err := os.Unsetenv("KTX_DB"); err != nil {
						t.Fatal(err)
					}
					t.Setenv(runReceiptEnv, "")
					capture := filepath.Join(root, "argv")
					t.Setenv("KTX_TEST_ARGV", capture)
					t.Setenv("KTX_TEST_EXIT", strconv.Itoa(tc.exit))
					child := filepath.Join(root, "child")
					if tc.executable != "" {
						child = filepath.Join(root, tc.executable)
						t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
					}
					if err := os.WriteFile(child, []byte("#!/bin/sh\n: > \"$KTX_TEST_ARGV\"\nfor arg do printf '%s\\0' \"$arg\" >> \"$KTX_TEST_ARGV\"; done\nprintf 'child output\\n'\nexit \"$KTX_TEST_EXIT\"\n"), 0o700); err != nil {
						t.Fatal(err)
					}
					if tc.executable != "" {
						child = tc.executable
					}
					args := append([]string(nil), tc.args...)
					for i := range args {
						if args[i] == "CHILD" {
							args[i] = child
						}
					}
					var stdout, stderr bytes.Buffer
					var code int
					if mode == "dispatch" {
						code = runWith(args, &stdout, &stderr)
					} else {
						cmd := exec.Command(binary, args...)
						cmd.Stdout, cmd.Stderr = &stdout, &stderr
						if err := cmd.Run(); err != nil {
							if cmd.ProcessState == nil {
								t.Fatal(err)
							}
							code = cmd.ProcessState.ExitCode()
						}
					}
					wantCode := tc.exit
					if tc.usage {
						wantCode = ExitUsage
					}
					if code != wantCode {
						t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", code, wantCode, &stdout, &stderr)
					}
					captured, err := os.ReadFile(capture)
					if tc.usage || tc.help {
						if !os.IsNotExist(err) {
							t.Fatalf("child ran: capture=%q err=%v", captured, err)
						}
						if tc.help && (!strings.Contains(stdout.String(), "ktx run —") || stderr.Len() != 0) {
							t.Fatalf("help stdout=%q stderr=%q", &stdout, &stderr)
						}
						if tc.usage && (stdout.Len() != 0 || !json.Valid(bytes.TrimSpace(stderr.Bytes()))) {
							t.Fatalf("usage stdout=%q stderr=%q", &stdout, &stderr)
						}
						if _, err := os.Stat(filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db")); !os.IsNotExist(err) {
							t.Fatalf("help/usage touched database: %v", err)
						}
						return
					}
					want := ""
					for _, arg := range tc.want {
						want += arg + "\x00"
					}
					if err != nil || string(captured) != want || stdout.String() != "child output\n" {
						t.Fatalf("argv=%q want=%q err=%v stdout=%q", captured, want, err, &stdout)
					}
					if (stderr.Len() == 0) != tc.quiet || (!tc.quiet && !json.Valid(bytes.TrimSpace(stderr.Bytes()))) {
						t.Fatalf("quiet=%t stderr=%q", tc.quiet, &stderr)
					}
					db, err := dbpkg.OpenReadOnly(filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					var recordings int
					if err := db.DB().QueryRow("SELECT count(*) FROM recorded_streams").Scan(&recordings); err != nil {
						t.Fatal(err)
					}
					if (recordings == 1) != tc.record {
						t.Fatalf("recordings=%d want recording=%t", recordings, tc.record)
					}
					if tc.collection != "" {
						var collection string
						if err := db.DB().QueryRow("SELECT id FROM collection_intervals").Scan(&collection); err != nil || collection != tc.collection {
							t.Fatalf("collection=%q want=%q err=%v", collection, tc.collection, err)
						}
					}
				})
			}
		})
	}
}
