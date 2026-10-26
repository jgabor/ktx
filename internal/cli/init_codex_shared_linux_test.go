package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func codexSharedCommand(t *testing.T, target, operation string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCodexSharedProcess$", "-test.v", "--", target, operation)
	cmd.Env = os.Environ()
	root := t.TempDir()
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR", "TMP", "TEMP"} {
		path := filepath.Join(root, key)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		cmd.Env = append(cmd.Env, key+"="+path)
	}
	cmd.Env = append(cmd.Env, "KTX_CODEX_SHARED_PROCESS=1")
	for i := len(cmd.Env) - 1; i >= 0; i-- {
		if strings.HasPrefix(cmd.Env[i], "KTX_DB=") {
			cmd.Env = append(cmd.Env[:i], cmd.Env[i+1:]...)
		}
	}
	return cmd
}

func TestCodexSharedProcess(t *testing.T) {
	if os.Getenv("KTX_CODEX_SHARED_PROCESS") != "1" {
		return
	}
	target, op := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	t.Logf("operation=%s target=%s", op, target)
	if strings.HasPrefix(op, "limited-") {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 4096, Max: 4096}); err != nil {
			t.Fatal(err)
		}
		op = strings.TrimPrefix(op, "limited-")
	}
	var err error
	if op != "remove" && op != "external-edit" && !strings.HasPrefix(op, "update-") {
		t.Fatalf("unknown operation %q", op)
	}
	if op == "external-edit" {
		if err := os.WriteFile(target, []byte(`{"external":"new document"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	} else if strings.HasPrefix(op, "update-") {
		mutation, err := beginCodexMutation(target, false)
		if err != nil {
			t.Fatal(err)
		}
		defer mutation.close()
		doc, _, err := decodeCodexHooks(mutation.raw, mutation.info != nil)
		if err != nil {
			t.Fatal(err)
		}
		doc[op] = true
		// Deliberately widen the read/publish interval while other processes
		// install, remove, and update this same shared document.
		time.Sleep(15 * time.Millisecond)
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := mutation.publish(raw); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err = removeCodexLegacyHook(target); err != nil {
		t.Fatal(err)
	}
}

func TestCodexSharedConcurrentMutations(t *testing.T) {
	target := filepath.Join(t.TempDir(), "hooks.json")
	seed := `{"unrelated":{"precise":9007199254740993},"hooks":{"PostToolUse":[` +
		`{"matcher":".*","hooks":[{"type":"command","command":"ktx hook","statusMessage":"ktx: recording tool call"}]},` +
		`{"matcher":".*","hooks":[{"type":"command","command":"'/old/ktx' hook","statusMessage":"ktx: recording tool call"}]}]}}`
	if err := os.WriteFile(target, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	type process struct {
		cmd    *exec.Cmd
		output bytes.Buffer
	}
	var processes []*process
	for i := 0; i < 8; i++ {
		for _, op := range []string{fmt.Sprintf("update-%d", i), "remove"} {
			p := &process{cmd: codexSharedCommand(t, target, op)}
			p.cmd.Stdout, p.cmd.Stderr = &p.output, &p.output
			if err := p.cmd.Start(); err != nil {
				t.Fatal(err)
			}
			processes = append(processes, p)
		}
	}
	for _, p := range processes {
		if err := p.cmd.Wait(); err != nil {
			t.Errorf("concurrent mutation: %v %s", err, &p.output)
		}
	}
	doc, _, err := loadCodexHooks(target)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if doc[fmt.Sprintf("update-%d", i)] != true {
			t.Errorf("lost update %d: %v", i, doc)
		}
	}
	if got := doc["unrelated"].(map[string]any)["precise"]; got != json.Number("9007199254740993") {
		t.Fatalf("unknown number changed: %v", got)
	}
	if _, count := filterKtxEntries(codexPostToolUseArray(codexHooksObject(doc))); count != 0 {
		t.Fatalf("ktx hooks remain: %v", doc)
	}
}

func TestCodexSharedCheckedPublication(t *testing.T) {
	for _, fault := range []string{"interrupt", "external-edit", "external-replace", "external-mode", "external-symlink", "external-directory"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "hooks.json")
			doc := map[string]any{
				"unrelated": "keep",
				"hooks":     map[string]any{"PostToolUse": []any{ktxCodexPostToolUseEntry("'/old/ktx' hook")}},
			}
			before, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, before, 0o600); err != nil {
				t.Fatal(err)
			}
			want := before
			original := codexWriteDocument
			t.Cleanup(func() { codexWriteDocument = original })
			called := false
			codexWriteDocument = func(path string, data []byte, perm fs.FileMode, check func() error) error {
				return original(path, data, perm, func() error {
					called = true
					switch fault {
					case "interrupt":
						return syscall.EINTR
					case "external-edit":
						want = []byte(`{"external":"new document"}`)
						if out, err := codexSharedCommand(t, target, "external-edit").CombinedOutput(); err != nil {
							t.Fatalf("external editor: %v %s", err, out)
						}
					case "external-replace":
						// Even an identical replacement is an observed edit.
						if err := os.WriteFile(target+".new", before, 0o600); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(target+".new", target); err != nil {
							t.Fatal(err)
						}
					case "external-mode":
						if err := os.Chmod(target, 0o400); err != nil {
							t.Fatal(err)
						}
					case "external-symlink":
						if err := os.Rename(target, target+".saved"); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target+".saved", target); err != nil {
							t.Fatal(err)
						}
					case "external-directory":
						if err := os.Rename(dir, dir+".saved"); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = os.RemoveAll(dir + ".saved") })
						if err := os.Mkdir(dir, 0o700); err != nil {
							t.Fatal(err)
						}
						want = []byte(`{"external":"replacement directory"}`)
						if err := os.WriteFile(target, want, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					return check()
				})
			}
			_, err = removeCodexLegacyHook(target)
			if err == nil || !called {
				t.Fatalf("publication bypassed failure check: %v called=%v", err, called)
			}
			if fault != "interrupt" && !strings.Contains(err.Error(), "changed during update") {
				t.Errorf("missing conflict diagnostic: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("failed publication changed destination: %s %v", got, err)
			}
		})
	}
}

func TestCodexSharedRemoveDetectsExternalEdit(t *testing.T) {
	target := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := beginCodexMutation(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	want := `{"external":"keep"}`
	if err := os.WriteFile(target, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.remove(); err == nil {
		t.Fatal("external edit deleted")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != want {
		t.Fatalf("external document lost: %s %v", got, err)
	}
}

func TestCodexSharedPublishDetectsExternalEdit(t *testing.T) {
	target := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(target, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := beginCodexMutation(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	if out, err := codexSharedCommand(t, target, "external-edit").CombinedOutput(); err != nil {
		t.Fatalf("external editor: %v %s", err, out)
	}
	if err := m.publish([]byte(`{"old":true,"installed":true}`)); err == nil {
		t.Fatal("external edit overwritten")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != `{"external":"new document"}` {
		t.Fatalf("external document lost: %s %v", got, err)
	}
}

func TestCodexSharedSymlinkAndPermissions(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	owned, err := json.Marshal(map[string]any{"unrelated": true, "hooks": map[string]any{"PostToolUse": []any{ktxCodexPostToolUseEntry(ktxHookCommand)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, owned, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "hooks.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := removeCodexLegacyHook(link); err == nil {
		t.Fatal("symlink destination accepted")
	}
	got, err := os.ReadFile(real)
	if err != nil || !bytes.Equal(got, owned) {
		t.Fatalf("symlink target changed: %s %v", got, err)
	}
	if removed, err := removeCodexLegacyHook(real); err != nil || removed != 1 {
		t.Fatalf("removed = %d, error = %v", removed, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions changed: %v", info.Mode())
	}
	doc, _, err := loadCodexHooks(real)
	if err != nil || doc["unrelated"] != true || doc["hooks"] != nil {
		t.Fatalf("single unknown field lost or empty hooks kept: %v %v", doc, err)
	}
}

// generatedHook decodes one inner hook as hooks files hold it, so numbers are
// json.Number as they are when ktx reads a hooks file.
func generatedHook(t *testing.T, raw string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var hook map[string]any
	if err := decoder.Decode(&hook); err != nil {
		t.Fatal(err)
	}
	return hook
}

func TestCodexSharedOwnershipGrammar(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	recognized := []string{
		`{"type":"command","command":"ktx hook","statusMessage":"ktx: recording tool call"}`,
		`{"type":"command","command":"ktx hook","timeout":10,"statusMessage":"ktx: recording tool call"}`,
		`{"type":"command","command":"'/old/ktx' --expect-runtime-sha256 ` + digest + ` hook","statusMessage":"ktx: recording tool call"}`,
	}
	for _, path := range []string{"/old path/ktx", "/renamed/program", "/literal $HOME/quote'and;$(bad)/ktx"} {
		command, _ := json.Marshal(quoteCodexHookPath(path) + " hook")
		recognized = append(recognized, `{"type":"command","command":`+string(command)+`,"statusMessage":"ktx: recording tool call"}`)
	}
	for _, raw := range recognized {
		if !isKtxCodexHook(generatedHook(t, raw)) {
			t.Errorf("generated hook not recognized: %s", raw)
		}
	}
	rejected := []string{
		// Customized fields are user-owned.
		`{"type":"command","command":"ktx hook","statusMessage":"ktx: recording tool call","custom":true}`,
		`{"type":"command","command":"ktx hook","timeout":"10","statusMessage":"ktx: recording tool call"}`,
		`{"type":"command","command":"'/old/ktx' hook","timeout":7,"statusMessage":"ktx: recording tool call"}`,
		`{"type":"command","command":"ktx hook"}`,
		`{"type":"command","command":"'/old/ktx' hook"}`,
		`{"type":"prompt","command":"ktx hook","statusMessage":"ktx: recording tool call"}`,
		// Other commands are not ktx's.
		`{"type":"command","command":"ktx hook --runtime claude-code","statusMessage":"ktx: recording tool call"}`,
		`{"type":"command","command":"'/old/ktx' --expect-runtime-sha256 notadigest hook","statusMessage":"ktx: recording tool call"}`,
	}
	for _, command := range []string{"'/other/tool' hook --custom", "'/a/../tool' hook", "'relative' hook", "'/tool' hook; echo bad", "'/tool' hook # custom", "'/a'$(touch /bad)'b' hook", "'/tool'  hook", "/tool hook"} {
		encoded, _ := json.Marshal(command)
		rejected = append(rejected, `{"type":"command","command":`+string(encoded)+`,"statusMessage":"ktx: recording tool call"}`)
	}
	for _, raw := range rejected {
		if isKtxCodexHook(generatedHook(t, raw)) {
			t.Errorf("user-owned hook recognized: %s", raw)
		}
	}
}

func TestCodexSharedInterruptedWrite(t *testing.T) {
	target := filepath.Join(t.TempDir(), "hooks.json")
	doc := map[string]any{
		"unrelated": strings.Repeat("shared-data", 8192),
		"future":    true,
		"hooks":     map[string]any{"PostToolUse": []any{ktxCodexPostToolUseEntry(ktxHookCommand)}},
	}
	original, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := codexSharedCommand(t, target, "limited-remove").CombinedOutput(); err == nil {
		t.Fatalf("expected interrupted write: %s", out)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("interrupted removal changed shared document: length=%d want=%d error=%v", len(got), len(original), err)
	}
}

func TestCodexSharedDirectoryCoordination(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hooks.json")
	owned := `{"hooks":{"PostToolUse":[{"matcher":".*","hooks":[{"type":"command","command":"ktx hook","statusMessage":"ktx: recording tool call"}]}]}`
	if err := os.WriteFile(target, []byte(owned+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	cmd := codexSharedCommand(t, target, "remove")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("removal bypassed shared document lease: %v %s", err, &output)
	case <-time.After(200 * time.Millisecond):
	}
	if err := os.WriteFile(target, []byte(owned+`,"external":{"precise":9007199254740993}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("removal: %v %s", err, &output)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Contains(got, []byte("9007199254740993")) || bytes.Contains(got, []byte("ktx hook")) {
		t.Fatalf("lost shared field or kept the ktx hook: %s %v", got, err)
	}
}

func TestCodexSharedRelocatedGeneratedCommand(t *testing.T) {
	target := filepath.Join(t.TempDir(), "hooks.json")
	old := ktxCodexPostToolUseEntry(`'/old installation/ktx' hook`)
	old["unknown"] = "outer-kept"
	inner := old["hooks"].([]any)
	old["hooks"] = append(inner, map[string]any{"type": "command", "command": "'/another/program' hook", "custom": true})
	raw, err := json.Marshal(map[string]any{"future": 9001, "hooks": map[string]any{"PostToolUse": []any{old}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := findCodexLegacyHooks([]string{target}); err != nil || len(found) != 1 || found[0].Entries != 1 {
		t.Errorf("relocated hook found = %v %v", found, err)
	}
	if removed, err := removeCodexLegacyHook(target); err != nil || removed != 1 {
		t.Fatalf("removed = %d, error = %v", removed, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Contains(got, []byte("outer-kept")) || !bytes.Contains(got, []byte("/another/program")) || bytes.Contains(got, []byte("old installation")) {
		t.Fatalf("removal lost siblings or kept the stale hook: %s %v", got, err)
	}
}

func TestCodexSharedMalformedUnchanged(t *testing.T) {
	for i, raw := range []string{`{`, `null`, `{"hooks":false}`, `{"hooks":{"PostToolUse":{}}}`, `{"hooks":{"PostToolUse":null}}`} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "hooks.json")
			if err := os.WriteFile(target, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := findCodexLegacyHooks([]string{target}); err == nil {
				t.Error("malformed document inspected")
			}
			if _, err := removeCodexLegacyHook(target); err == nil {
				t.Error("malformed document accepted")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != raw {
				t.Fatalf("malformed document changed: %s %v", got, err)
			}
		})
	}
}
