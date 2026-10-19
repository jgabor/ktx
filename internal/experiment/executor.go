package experiment

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type Command struct {
	Args    []string
	Dir     string
	Env     []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Timeout time.Duration
	Grace   time.Duration
}

type CommandResult struct {
	ExitCode int
	TimedOut bool
}

// CommandExecutor runs external commands for the experiment module.
type CommandExecutor interface {
	Run(context.Context, Command) (CommandResult, error)
}

// OSExecutor runs each command in its own process group. On timeout it sends
// SIGTERM to the group, waits for Grace, then sends SIGKILL.
type OSExecutor struct{}

func (OSExecutor) Run(ctx context.Context, command Command) (CommandResult, error) {
	if len(command.Args) == 0 {
		return CommandResult{}, errors.New("empty command")
	}
	if command.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, command.Timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	grace := command.Grace
	if grace <= 0 {
		grace = 5 * time.Second
	}
	cmd := exec.Command(command.Args[0], command.Args[1:]...)
	cmd.Dir, cmd.Env = command.Dir, command.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = command.Stdin, command.Stdout, command.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A descendant can retain a copied-output pipe after its leader exits.
	cmd.WaitDelay = grace
	if err := cmd.Start(); err != nil {
		return CommandResult{}, err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case err := <-wait:
		// Leader exit is not proof that the owned group has exited.
		cleanupErr := killProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
		result, processErr := processResult(err, false)
		return result, errors.Join(processErr, cleanupErr)
	case <-ctx.Done():
		termErr := killProcessGroup(cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(grace)
		defer timer.Stop()
		// Do not skip escalation when only the leader has exited.
		<-timer.C
		killErr := killProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
		<-wait //nolint:errcheck // the killed group's wait error only restates the timeout
		return CommandResult{ExitCode: 124, TimedOut: true}, errors.Join(termErr, killErr)
	}
}

func killProcessGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processResult(err error, timedOut bool) (CommandResult, error) {
	if err == nil {
		return CommandResult{ExitCode: 0, TimedOut: timedOut}, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return CommandResult{ExitCode: exitError.ExitCode(), TimedOut: timedOut}, nil
	}
	return CommandResult{}, err
}

func commandEnvironment(overrides map[string]string) []string {
	environment := os.Environ()
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}
