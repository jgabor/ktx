//go:build linux

package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseProcLocksMatchesAndBoundsBlockers(t *testing.T) {
	const device = uint64(0x21)
	const inode = uint64(12345)
	data := "1: POSIX ADVISORY WRITE 9 00:21:12345 0 EOF\n" +
		"2: FLOCK ADVISORY READ 30 00:21:12345 0 EOF\n" +
		"3: FLOCK ADVISORY WRITE 20 00:21:12345 0 EOF\n" +
		"4: FLOCK ADVISORY WRITE 20 00:21:12345 0 EOF\n" +
		"5: FLOCK ADVISORY WRITE 10 00:22:12345 0 EOF\n"
	for pid := 40; pid < 50; pid++ {
		data += fmt.Sprintf("%d: FLOCK ADVISORY WRITE %d 00:21:12345 0 EOF\n", pid, pid)
	}

	holders, available := parseProcLocks([]byte(data), device, inode, coordinationExclusive, nil)
	if !available || len(holders) != maxReportedLockHolders {
		t.Fatalf("holders=%#v available=%v", holders, available)
	}
	want := []LockHolder{{PID: 20, Mode: "exclusive", Command: "unknown"}, {PID: 30, Mode: "shared", Command: "unknown"}, {PID: 40, Mode: "exclusive", Command: "unknown"}}
	for i := range want {
		if holders[i] != want[i] {
			t.Fatalf("holders[%d]=%#v, want %#v", i, holders[i], want[i])
		}
	}

	sharedHolders, available := parseProcLocks([]byte(data), device, inode, coordinationShared, nil)
	if !available || len(sharedHolders) != maxReportedLockHolders || sharedHolders[0] != (LockHolder{PID: 20, Mode: "exclusive", Command: "unknown"}) {
		t.Fatalf("shared holders=%#v available=%v", sharedHolders, available)
	}
}

func TestInspectProcessCommandFailsClosed(t *testing.T) {
	root := t.TempDir()
	executablePath := filepath.Join(root, "ktx")
	if err := os.WriteFile(executablePath, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	currentExecutable, err := os.Stat(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	procRoot := filepath.Join(root, "proc")
	pid := 42
	directory := filepath.Join(procRoot, strconv.Itoa(pid))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executablePath, filepath.Join(directory, "exe")); err != nil {
		t.Fatal(err)
	}
	currentOwner, err := processOwner(directory)
	if err != nil {
		t.Fatal(err)
	}
	writeStat := func(start string) {
		t.Helper()
		fields := append([]string{"S"}, make([]string, 18)...)
		for i := 1; i < len(fields); i++ {
			fields[i] = "0"
		}
		fields = append(fields, start)
		if err := os.WriteFile(filepath.Join(directory, "stat"), []byte("42 (ktx worker) "+strings.Join(fields, " ")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeStat("100")
	payload := "/private/path token=secret payload-derived-value"
	if err := os.WriteFile(filepath.Join(directory, "cmdline"), []byte("ktx\x00--format\x00json\x00hook\x00"+payload+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner, func() bool { return true }); got != "hook" {
		t.Fatalf("verified command = %q, want hook", got)
	}
	t.Run("foreign ownership", func(t *testing.T) {
		if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner+1, func() bool { return true }); got != "unknown" {
			t.Fatalf("foreign owner command = %q, want unknown", got)
		}
	})
	if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner, func() bool { return false }); got != "unknown" {
		t.Fatalf("lost lock command = %q, want unknown", got)
	}
	if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner, func() bool {
		writeStat("101")
		return true
	}); got != "unknown" {
		t.Fatalf("reused pid command = %q, want unknown", got)
	}
	writeStat("100")
	foreignPath := filepath.Join(root, "foreign")
	if err := os.WriteFile(foreignPath, []byte("foreign"), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign, err := os.Stat(foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := inspectProcessCommand(procRoot, pid, foreign, currentOwner, func() bool { return true }); got != "unknown" {
		t.Fatalf("foreign executable command = %q, want unknown", got)
	}
	exePath := filepath.Join(directory, "exe")
	t.Run("executable replaced during inspection", func(t *testing.T) {
		if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner, func() bool {
			if err := os.Remove(exePath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(foreignPath, exePath); err != nil {
				t.Fatal(err)
			}
			return true
		}); got != "unknown" {
			t.Fatalf("replaced executable command = %q, want unknown", got)
		}
	})
	if err := os.Remove(exePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executablePath, exePath); err != nil {
		t.Fatal(err)
	}
	t.Run("executable disappears during inspection", func(t *testing.T) {
		if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner, func() bool {
			if err := os.Remove(exePath); err != nil {
				t.Fatal(err)
			}
			return true
		}); got != "unknown" {
			t.Fatalf("disappeared executable command = %q, want unknown", got)
		}
	})
	if err := os.Symlink(executablePath, exePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "cmdline"), []byte("ktx\x00unsupported\x00"+payload+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner, func() bool { return true }); got != "unknown" {
		t.Fatalf("unsupported command = %q, want unknown", got)
	}
	if err := os.Remove(filepath.Join(directory, "cmdline")); err != nil {
		t.Fatal(err)
	}
	if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner, func() bool { return true }); got != "unknown" {
		t.Fatalf("inspection failure command = %q, want unknown", got)
	}
	if err := os.WriteFile(filepath.Join(directory, "cmdline"), []byte("ktx\x00hook\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := inspectProcessCommand(procRoot, pid, currentExecutable, currentOwner, func() bool {
		if err := os.Remove(filepath.Join(directory, "stat")); err != nil {
			t.Fatal(err)
		}
		return true
	}); got != "unknown" {
		t.Fatalf("disappeared process command = %q, want unknown", got)
	}
}

func TestReadProcessArgvRejectsTruncatedCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cmdline")
	if err := os.WriteFile(path, []byte("ktx\x00hook"), 0o600); err != nil {
		t.Fatal(err)
	}
	argv, err := readProcessArgv(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := BlockerCommandLabel(argv); got != "unknown" {
		t.Fatalf("truncated command = %q, want unknown", got)
	}
}

func TestParseProcLocksSnapshotAvailability(t *testing.T) {
	const device = uint64(0x21)
	if holders, available := parseProcLocks([]byte("1: FLOCK ADVISORY WRITE 10 00:21:999 0 EOF\n"), device, 123, coordinationExclusive, nil); !available || len(holders) != 0 {
		t.Fatalf("unmatched holders=%#v available=%v", holders, available)
	}
	if holders, available := parseProcLocks([]byte("1: FLOCK malformed\n"), device, 123, coordinationExclusive, nil); available || holders != nil {
		t.Fatalf("malformed holders=%#v available=%v", holders, available)
	}
}

func TestInspectProcessFileDescriptorsReportsIncompleteMiss(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(targetPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Symlink(filepath.Join(directory, "missing"), filepath.Join(directory, "broken")); err != nil {
		t.Fatal(err)
	}
	match, complete := inspectProcessFileDescriptors(directory, target)
	if match || complete {
		t.Fatalf("match=%v complete=%v, want false, false", match, complete)
	}
}

func TestCoordinationTimeoutObservesHolderByPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observed.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("pre-expired acquisition", func(t *testing.T) {
		holder, err := newWriterCoordinator(path)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := holder.acquire(context.Background(), false, coordinationShared)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()

		waiter, err := newWriterCoordinator(path)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		cancel()
		_, err = waiter.acquire(ctx, false, coordinationExclusive)
		assertObservedLockHolder(t, err, "shared")
	})

	t.Run("process-local queue", func(t *testing.T) {
		coordinator, err := newWriterCoordinator(path)
		if err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- coordinator.withLock(context.Background(), false, func() error {
				close(entered)
				<-release
				return nil
			})
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("first coordinator did not acquire")
		}
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
		})

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err = coordinator.withLock(ctx, false, func() error {
			return errors.New("unexpected acquisition")
		})
		cancel()
		assertObservedLockHolder(t, err, "exclusive")

		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func assertObservedLockHolder(t *testing.T, err error, mode string) {
	t.Helper()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	var timeout *coordinationTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("error = %T, want coordination timeout", err)
	}
	if !timeout.OwnershipSnapshotAvailable {
		t.Fatal("ownership snapshot unavailable")
	}
	want := LockHolder{PID: os.Getpid(), Mode: mode, Command: "unknown"}
	for _, holder := range timeout.LockHolders {
		if holder == want {
			return
		}
	}
	t.Fatalf("lock holders = %#v, want %#v", timeout.LockHolders, want)
}
