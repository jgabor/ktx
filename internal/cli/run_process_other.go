//go:build !linux

package cli

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// Preserve the existing direct-child CommandContext behavior off Linux.
// No native process-tree cancellation guarantee is made on these platforms.
func configureRunProcess(_ *exec.Cmd) error { return nil }

func prepareRunSubreaper(_ *exec.Cmd) (func() error, error) {
	return func() error { return nil }, nil
}

func superviseRunProcess(ctx context.Context, cmd *exec.Cmd, stdout io.Closer) func(error) runShutdown {
	done := make(chan struct{})
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		select {
		case <-ctx.Done():
			timer := time.NewTimer(runTerminationGrace)
			defer timer.Stop()
			select {
			case <-timer.C:
				_ = stdout.Close()
			case <-done:
			}
		case <-done:
		}
	}()
	return func(err error) runShutdown {
		if err != nil {
			_ = stdout.Close()
			_ = cmd.Process.Kill()
		}
		close(done)
		<-settled
		return runShutdown{interrupted: ctx.Err() != nil}
	}
}
