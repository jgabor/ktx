package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func configureRunProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	// The supervisor owns cancellation. exec must not kill or reap the
	// leader while process-group signals are still possible.
	cmd.Cancel = nil
	// A controlling terminal delivers input and job-control signals to the
	// inherited foreground group. Moving only the child breaks reads (SIGTTIN);
	// transferring the terminal instead would hide Ctrl-C from the wrapper.
	// Keep that group untouched, including when stdin is redirected and the
	// agent opens /dev/tty. We may signal a group only when we created it.
	terminal, err := os.OpenFile("/dev/tty", os.O_RDONLY|syscall.O_NOCTTY, 0)
	if err == nil {
		return terminal.Close()
	}
	if errors.Is(err, syscall.ENXIO) {
		cmd.SysProcAttr.Setpgid = true
		return nil
	}
	// If terminal discovery is unavailable, retaining the inherited group is
	// safe for input and signal ownership. Report the error, but do not make
	// access to /dev/tty a new prerequisite for running an agent.
	return err
}

// superviseRunProcess runs alongside the synchronous collector. Only the
// collector touches evidence and output; settlement joins this supervisor
// before executeRun calls Wait or closes evidence. WNOWAIT pins the leader's
// PID even if it exits first, so a later group signal cannot target a reused ID.
func superviseRunProcess(ctx context.Context, cmd *exec.Cmd, stdout io.Closer) func(error) runShutdown {
	collected := make(chan error, 1)
	settled := make(chan runShutdown, 1)
	exited := make(chan error, 1)
	go func() {
		var info unix.Siginfo
		var err error
		for {
			err = unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
		exited <- err
	}()
	go func() {
		var result runShutdown
		leader, output := exited, collected
		signalOwned := func(sig syscall.Signal) {
			var err error
			if cmd.SysProcAttr.Setpgid {
				err = syscall.Kill(-cmd.Process.Pid, sig)
			} else {
				// The terminal group can include the caller and sibling jobs.
				// Signal the leader here; adopted descendants are settled
				// individually after it exits, never via the shared group.
				err = cmd.Process.Signal(sig)
			}
			if !errors.Is(err, syscall.ESRCH) {
				result.err = errors.Join(result.err, err)
			}
		}
		stop := func(grace bool) {
			result.interrupted = true
			if grace {
				signalOwned(syscall.SIGTERM)
				// Do not let leader exit or stdout EOF skip descendant escalation.
				timer := time.NewTimer(runTerminationGrace)
				<-timer.C
			}
			signalOwned(syscall.SIGKILL)
			// Also detach inherited pipes held outside the group. Joining the
			// collector, rather than abandoning it, prevents evidence-close races.
			result.err = errors.Join(result.err, stdout.Close())
			if leader != nil {
				result.err = errors.Join(result.err, <-leader)
			}
			if !cmd.SysProcAttr.Setpgid {
				result.err = errors.Join(result.err, reapRunDescendants(cmd.Process.Pid))
			}
			settled <- result
		}
		for leader != nil || output != nil {
			select {
			case <-ctx.Done():
				stop(true)
				return
			case err := <-leader:
				leader = nil
				if err != nil {
					result.err = err
					stop(false)
					return
				}
			case err := <-output:
				output = nil
				if err != nil {
					stop(false)
					return
				}
			}
		}
		settled <- result
	}()
	return func(err error) runShutdown {
		collected <- err
		return <-settled
	}
}
