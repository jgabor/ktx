package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"golang.org/x/sys/unix"
)

func TestRunSignalWrapperProcess(t *testing.T) {
	if os.Getenv("KTX_TEST_SIGNAL_WRAPPER") == "1" {
		before, err := runSubreaperState()
		if err != nil {
			t.Fatal(err)
		}
		code := executeRun("/bin/sh", []string{os.Getenv("KTX_TEST_SIGNAL_SCRIPT")}, runOptions{RecordStream: true}, os.Stdout, os.Stderr)
		if after, err := runSubreaperState(); err != nil || after != before {
			t.Fatalf("subreaper state=%d want=%d: %v", after, before, err)
		}
		os.Exit(code)
	}
}

// The same offline regressions run against a test subprocess or an explicitly
// supplied local build. Every wrapper invocation owns fresh application roots.
func TestRunSignals(t *testing.T) {
	for _, mode := range []string{"graceful", "resistant", "leader-first", "closed-stdout"} {
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(fmt.Sprintf("%s/%s", mode, sig), func(t *testing.T) {
				root := t.TempDir()
				script := filepath.Join(root, "child.sh")
				body := `#!/bin/sh
echo '{"tool":"read","output":"committed"}'
trap 'echo graceful > "$SIGNAL_ROOT/trapped"; exit 37' TERM
echo $$ > "$SIGNAL_ROOT/leader"
case "$SIGNAL_MODE" in
resistant|leader-first)
  sh -c 'trap "" TERM; echo $$ > "$SIGNAL_ROOT/descendant"; while :; do sleep 1; done' &
  while [ ! -f "$SIGNAL_ROOT/descendant" ]; do sleep 0.01; done
  ;;
closed-stdout) exec 1>&- ;;
esac
echo ready > "$SIGNAL_ROOT/ready"
if [ "$SIGNAL_MODE" = leader-first ]; then exit 23; fi
while :; do sleep 0.05; done
`
				if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
				command := exec.Command(os.Args[0], "-test.run=^TestRunSignalWrapperProcess$")
				if binary := os.Getenv("KTX_RUN_SIGNAL_CLI"); binary != "" {
					command = exec.Command(binary, "run", "--record-stream", "/bin/sh", script)
				}
				// Exercise non-terminal group ownership even under an interactive
				// developer shell. Terminal ownership is covered separately below.
				command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				for _, entry := range os.Environ() {
					key, _, _ := strings.Cut(entry, "=")
					if key != "KTX_DB" && key != "HOME" && !strings.HasPrefix(key, "XDG_") {
						command.Env = append(command.Env, entry)
					}
				}
				for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
					command.Env = append(command.Env, key+"="+filepath.Join(root, key))
				}
				command.Env = append(command.Env, "KTX_TEST_SIGNAL_WRAPPER=1", "KTX_TEST_SIGNAL_SCRIPT="+script, "SIGNAL_ROOT="+root, "SIGNAL_MODE="+mode)
				var output, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &output, &diagnostic
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				wait := make(chan error, 1)
				go func() { wait <- command.Wait() }()
				t.Cleanup(func() { _ = command.Process.Kill() })
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("child did not become ready")
					}
					time.Sleep(10 * time.Millisecond)
				}
				// An unrelated process in a separate group must never receive cleanup.
				unrelated := exec.Command("sleep", "30")
				unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := unrelated.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
				for _, name := range []string{"leader", "descendant"} {
					data, err := os.ReadFile(filepath.Join(root, name))
					if err != nil {
						continue
					}
					pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
					// Only failure cleanup; successful checks below prove no survivor.
					t.Cleanup(func() {
						if runSignalProcessAlive(pid) {
							_ = syscall.Kill(pid, syscall.SIGKILL)
						}
					})
				}
				start := time.Now()
				if err := command.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				select {
				case <-wait: //nolint:errcheck // the exit status is checked through ProcessState below
				case <-time.After(5 * time.Second):
					t.Fatal("wrapper cancellation exceeded 5 seconds")
				}
				t.Logf("shutdown=%s exit=%d diagnostics=%s", time.Since(start), command.ProcessState.ExitCode(), &diagnostic)
				wantExit := 37
				if mode == "leader-first" {
					wantExit = 23
				}
				// A deadline-closing read can win the race with pipe EOF after
				// escalation. Preserve the collection-failure exit contract.
				closedRead := (mode == "resistant" || mode == "leader-first") &&
					command.ProcessState.ExitCode() == ExitInternal &&
					strings.Contains(diagnostic.String(), "read agent stdout")
				if command.ProcessState.ExitCode() != wantExit && !closedRead {
					t.Errorf("exit=%d want=%d", command.ProcessState.ExitCode(), wantExit)
				}
				if mode != "leader-first" {
					if _, err := os.Stat(filepath.Join(root, "trapped")); err != nil {
						t.Error("graceful TERM trap not exercised")
					}
				}
				for _, name := range []string{"leader", "descendant"} {
					data, _ := os.ReadFile(filepath.Join(root, name))
					pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
					if pid > 0 && runSignalProcessAlive(pid) {
						t.Errorf("owned %s %d survived", name, pid)
					}
				}
				if !runSignalProcessAlive(unrelated.Process.Pid) {
					t.Error("unrelated process was killed")
				}
				d, err := dbpkg.Open(filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				for query, want := range map[string]int{
					`SELECT count(*) FROM sessions WHERE ended_at IS NOT NULL`:            1,
					`SELECT count(*) FROM ingress_continuity WHERE state = 'interrupted'`: 1,
					`SELECT count(*) FROM recorded_streams`:                               0,
					`SELECT count(*) FROM tool_calls WHERE output = 'committed'`:          1,
				} {
					var got int
					if err := d.DB().QueryRow(query).Scan(&got); err != nil || got != want {
						t.Errorf("%s = %d want %d: %v", query, got, want, err)
					}
				}
			})
		}
	}
}

func runSignalProcessAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && !strings.Contains(string(data), ") Z ")
}

func TestRunForegroundStdin(t *testing.T) {
	root := t.TempDir()
	master, slave := runSignalTerminal(t)
	script := filepath.Join(root, "stdin.sh")
	if err := os.WriteFile(script, []byte("read line\nprintf '%s' \"$line\" > \"$SIGNAL_ROOT/result\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRunSignalWrapperProcess$")
	if binary := os.Getenv("KTX_RUN_SIGNAL_CLI"); binary != "" {
		command = exec.Command(binary, "run", "/bin/sh", script)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "KTX_DB" && key != "HOME" && !strings.HasPrefix(key, "XDG_") {
			command.Env = append(command.Env, entry)
		}
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		command.Env = append(command.Env, key+"="+filepath.Join(root, key))
	}
	command.Env = append(command.Env, "KTX_TEST_SIGNAL_WRAPPER=1", "KTX_TEST_SIGNAL_SCRIPT="+script, "SIGNAL_ROOT="+root)
	command.Stdin = slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	if _, err := master.WriteString("hello\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("wrapper failed: %v: %s", err, &diagnostic)
		}
	case <-time.After(3 * time.Second):
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-wait: //nolint:errcheck // the test fails below regardless of the wait result
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-wait //nolint:errcheck // the test fails below regardless of the wait result
		}
		t.Fatal("wrapper child cannot read inherited foreground terminal stdin")
	}
	got, err := os.ReadFile(filepath.Join(root, "result"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("stdin result=%q error=%v", got, err)
	}
}

func runSignalTerminal(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func TestRunTerminalDescendantProcess(t *testing.T) {
	if os.Getenv("KTX_TEST_TERMINAL_SESSION") != "1" {
		return
	}
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	root, name := os.Getenv("SIGNAL_ROOT"), "descendant"
	if os.Getenv("KTX_TEST_TERMINAL_LEAF") == "1" {
		name = "leaf"
	} else if os.Getenv("SIGNAL_MODE") == "nested" {
		child := exec.Command(os.Args[0], "-test.run=^TestRunTerminalDescendantProcess$")
		child.Env = append(os.Environ(), "KTX_TEST_TERMINAL_LEAF=1")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(root, "leaf")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("nested child did not become ready")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

// This session leader stands in for an interactive caller. It remains in the
// foreground group with the wrapper, and must survive cancellation and be able
// to read the same terminal after the wrapper exits. Owned descendants must stop
// before wrapper exit, without signaling this shared group. The caller adopts
// survivors only for failure cleanup.
func TestRunTerminalSessionProcess(t *testing.T) {
	if os.Getenv("KTX_TEST_TERMINAL_SESSION") != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGINT)
	fd := int(os.Stdin.Fd())
	before, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	group, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil || group != syscall.Getpgrp() {
		t.Fatalf("caller foreground group=%d: %v", group, err)
	}
	root, mode := os.Getenv("SIGNAL_ROOT"), os.Getenv("SIGNAL_MODE")
	// This sibling of the wrapper shares the terminal group but is not its
	// descendant. It inherits the caller's ignored INT (so VINTR is harmless),
	// but would be killed by an incorrectly group-wide TERM or KILL.
	unrelated := exec.Command("sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unrelated.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		var exitErr *exec.ExitError
		if err := unrelated.Wait(); err != nil && !errors.As(err, &exitErr) {
			t.Error(err)
		}
	})
	if got, err := syscall.Getpgid(unrelated.Process.Pid); err != nil || got != group {
		t.Fatalf("unrelated process group=%d want=%d: %v", got, group, err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRunSignalWrapperProcess$")
	if binary := os.Getenv("KTX_RUN_SIGNAL_CLI"); binary != "" {
		command = exec.Command(binary, "run", "--record-stream", "/bin/sh", os.Getenv("KTX_TEST_SIGNAL_SCRIPT"))
	}
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if mode == "redirected" {
		command.Stdin = nil
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wrapper"), []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	want := 7
	if mode != "normal" && mode != "redirected" {
		// The resistant descendant retains stdout past the leader's TERM trap.
		want = ExitInternal
	}
	if command.ProcessState.ExitCode() != want {
		t.Fatalf("wrapper exit=%d want=%d: %v", command.ProcessState.ExitCode(), want, err)
	}
	if !runSignalProcessAlive(unrelated.Process.Pid) {
		t.Fatal("unrelated shared-terminal sibling was killed")
	}
	if want == ExitInternal {
		names := []string{"descendant"}
		if mode == "nested" {
			names = append(names, "leaf")
		}
		for _, name := range names {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil || pid <= 0 {
				t.Fatalf("descendant PID=%q: %v", data, err)
			}
			if runSignalProcessAlive(pid) {
				t.Errorf("owned terminal %s survived wrapper shutdown", name)
				// Failure cleanup only: success must not depend on the caller
				// terminating work left behind by the wrapper.
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				if _, err := syscall.Wait4(pid, nil, 0, nil); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	after, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil || *before != *after {
		t.Fatalf("terminal settings changed: %v", err)
	}
	if got, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err != nil || got != group {
		t.Fatalf("terminal foreground group=%d want=%d: %v", got, group, err)
	}
	if err := os.WriteFile(filepath.Join(root, "settled"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || line != "after\n" {
		t.Fatalf("caller terminal read=%q: %v", line, err)
	}
}

func TestRunTerminalLifecycle(t *testing.T) {
	for _, mode := range []string{"normal", "redirected", "ctrl-c", "term", "leader-first", "nested"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			master, slave := runSignalTerminal(t)
			settings, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			settings.Lflag |= unix.TOSTOP
			if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, settings); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(root, "child.sh")
			body := `echo '{"tool":"read","output":"committed"}'
echo $$ > "$SIGNAL_ROOT/leader"
printf 'foreground write\n' > /dev/tty
case "$SIGNAL_MODE" in
normal|redirected)
  if [ "$SIGNAL_MODE" = redirected ]; then read line < /dev/tty; else read line; fi
  printf '%s' "$line" > "$SIGNAL_ROOT/result"
  exit 7 ;;
esac
trap '' INT
trap 'echo graceful > "$SIGNAL_ROOT/trapped"; exit 37' TERM
"$KTX_TEST_EXE" -test.run=^TestRunTerminalDescendantProcess$ &
while [ ! -f "$SIGNAL_ROOT/descendant" ]; do sleep 0.01; done
echo ready > "$SIGNAL_ROOT/ready"
if [ "$SIGNAL_MODE" = leader-first ]; then exit 23; fi
while :; do sleep 0.05; done
`
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestRunTerminalSessionProcess$")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != "KTX_DB" && key != "HOME" && !strings.HasPrefix(key, "XDG_") {
					command.Env = append(command.Env, entry)
				}
			}
			for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
				command.Env = append(command.Env, key+"="+filepath.Join(root, key))
			}
			command.Env = append(command.Env, "KTX_TEST_TERMINAL_SESSION=1", "KTX_TEST_SIGNAL_WRAPPER=1", "KTX_TEST_SIGNAL_SCRIPT="+script, "SIGNAL_ROOT="+root, "SIGNAL_MODE="+mode, "KTX_TEST_EXE="+os.Args[0])
			command.Stdin = slave
			command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- command.Wait() }()
			t.Cleanup(func() {
				_ = command.Process.Kill()
				for _, name := range []string{"wrapper", "leader", "descendant", "leaf"} {
					data, _ := os.ReadFile(filepath.Join(root, name))
					if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 0 && runSignalProcessAlive(pid) {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
			await := func(name string) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(root, name)); err == nil {
						return
					}
					select {
					case err := <-wait:
						t.Fatalf("caller exited before %s: %v: %s", name, err, &output)
					default:
					}
					if time.Now().After(deadline) {
						t.Fatalf("terminal lifecycle exceeded five seconds waiting for %s", name)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			state, recordings := "stopped", 1
			start := time.Now()
			if mode != "normal" && mode != "redirected" {
				state, recordings = "interrupted", 0
				await("ready")
				if mode == "leader-first" {
					data, err := os.ReadFile(filepath.Join(root, "leader"))
					if err != nil {
						t.Fatal(err)
					}
					pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
					if err != nil || pid <= 0 {
						t.Fatalf("leader PID=%q: %v", data, err)
					}
					deadline := time.Now().Add(time.Second)
					for {
						stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
						if err != nil {
							t.Fatalf("leader must remain unreaped before cancellation: %v", err)
						}
						if strings.Contains(string(stat), ") Z ") {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("leader did not exit before cancellation")
						}
						time.Sleep(time.Millisecond)
					}
				}
				start = time.Now()
				if mode == "ctrl-c" {
					// Actual terminal VINTR, not kill(wrapper, SIGINT). The child
					// ignores INT, so terminal transfer alone would hang here.
					if _, err := master.Write([]byte{3}); err != nil {
						t.Fatal(err)
					}
				} else {
					data, err := os.ReadFile(filepath.Join(root, "wrapper"))
					if err != nil {
						t.Fatal(err)
					}
					pid, err := strconv.Atoi(string(data))
					if err != nil || pid <= 0 {
						t.Fatalf("wrapper PID=%q: %v", data, err)
					}
					if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
						t.Fatal(err)
					}
				}
			} else if _, err := master.WriteString("hello\n"); err != nil {
				t.Fatal(err)
			}
			await("settled")
			elapsed := time.Since(start)
			if recordings == 0 && elapsed > 2*runTerminationGrace {
				t.Errorf("terminal descendant settlement took %s, exceeding grace plus settlement budget", elapsed)
			}
			t.Logf("terminal usable after %s", elapsed)
			if _, err := master.WriteString("after\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-wait:
				if err != nil {
					t.Fatalf("caller failed: %v: %s", err, &output)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("caller could not read terminal after wrapper exit")
			}
			if recordings == 1 {
				if data, err := os.ReadFile(filepath.Join(root, "result")); err != nil || string(data) != "hello" {
					t.Fatalf("agent terminal read=%q: %v", data, err)
				}
			} else if _, err := os.Stat(filepath.Join(root, "trapped")); err != nil && mode != "leader-first" {
				t.Fatal("terminal child TERM trap was not exercised")
			}
			d, err := dbpkg.Open(filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			for query, want := range map[string]int{
				`SELECT count(*) FROM sessions WHERE ended_at IS NOT NULL`:              1,
				`SELECT count(*) FROM ingress_continuity WHERE state = '` + state + `'`: 1,
				`SELECT count(*) FROM recorded_streams`:                                 recordings,
				`SELECT count(*) FROM tool_calls WHERE output = 'committed'`:            1,
			} {
				var got int
				if err := d.DB().QueryRow(query).Scan(&got); err != nil || got != want {
					t.Errorf("%s = %d want %d: %v", query, got, want, err)
				}
			}
		})
	}
}
