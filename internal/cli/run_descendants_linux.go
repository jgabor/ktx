package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// A terminal run cannot own its process group. Make the wrapper the reparenting
// boundary before starting its agent instead. executeRun is the CLI process's
// child owner; once the agent exits, its orphaned descendants become waitable
// children of this wrapper, not children of the interactive caller or init.
func prepareRunSubreaper(cmd *exec.Cmd) (func() error, error) {
	restore := func() error { return nil }
	if cmd.SysProcAttr.Setpgid {
		return restore, nil
	}
	if _, err := os.ReadDir("/proc"); err != nil {
		return nil, fmt.Errorf("discover child candidates: %w", err)
	}
	// Check identity-safe child waiting before spawning any work. Do not
	// silently fall back to signaling a PID obtained from a /proc snapshot.
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return nil, fmt.Errorf("open process identity: %w", err)
	}
	err = waitRunChild(unix.P_PIDFD, fd, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT)
	if errors.Is(err, unix.ECHILD) { // Our own process is not our child.
		err = nil
	}
	if err = errors.Join(err, unix.Close(fd)); err != nil {
		return nil, fmt.Errorf("check child identity support: %w", err)
	}
	previous, err := runSubreaperState()
	if err != nil {
		return nil, err
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return nil, err
	}
	return func() error {
		return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, uintptr(previous), 0, 0, 0)
	}, nil
}

func runSubreaperState() (int32, error) {
	var value int32
	_, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_CHILD_SUBREAPER, uintptr(unsafe.Pointer(&value)), 0, 0, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return value, nil
}

func waitRunChild(kind, id, options int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(kind, id, &info, options, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// /proc supplies candidates, never ownership authority. WNOWAIT validates that
// a candidate is our child without releasing its PID. Rechecking through the
// pidfd binds ownership, signaling and reaping to one identity even if another
// waiter reaps the candidate between discovery and pidfd_open.
func stopRunAdoptedChild(pid int) (owned bool, err error) {
	options := unix.WEXITED | unix.WNOHANG | unix.WNOWAIT
	if err := waitRunChild(unix.P_PID, pid, options); err != nil {
		if errors.Is(err, unix.ECHILD) {
			return false, nil
		}
		return false, err
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, unix.Close(fd)) }()
	if err := waitRunChild(unix.P_PIDFD, fd, options); err != nil {
		if errors.Is(err, unix.ECHILD) {
			return false, nil
		}
		return false, err
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return true, err
	}
	// Never block the shutdown budget on a single descendant. A still-running
	// child stays unreaped and is revisited in the next sweep.
	err = waitRunChild(unix.P_PIDFD, fd, unix.WEXITED|unix.WNOHANG)
	if errors.Is(err, unix.ECHILD) {
		err = nil
	}
	return true, err
}

func reapRunDescendants(leader int) error {
	// The stdout deadline has already fired. Allow one additional bounded
	// settlement interval for KILL, orphan adoption and reaping, including
	// nested descendants whose immediate parent also resisted TERM.
	deadline := time.Now().Add(runTerminationGrace)
	for {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return fmt.Errorf("discover adopted children: %w", err)
		}
		found := false
		var sweepErr error
		for _, entry := range entries {
			if time.Now().After(deadline) {
				return errors.Join(sweepErr, errors.New("adopted child settlement exceeded shutdown budget"))
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 0 || pid == leader {
				continue
			}
			owned, err := stopRunAdoptedChild(pid)
			found = found || owned
			if err != nil {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("settle adopted child %d: %w", pid, err))
			}
		}
		if sweepErr != nil || !found {
			return sweepErr
		}
		// A killed parent can expose another generation of adopted children.
		// Finish only after a fresh sweep finds none, keeping the agent leader
		// unreaped for the existing supervisor/Wait ownership contract.
		time.Sleep(time.Millisecond)
	}
}
