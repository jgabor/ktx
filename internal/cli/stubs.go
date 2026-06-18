package cli

import (
	"io"
	"os"
)

// stubCommand is the Task 1 placeholder for subcommands that Task 2+ will
// implement. It prints a machine-parseable usage error explaining the
// command is not yet implemented and exits 1.
//
// The stubs are deliberately honest: the surface exists so dispatch and
// help are complete, but the behavior is flagged as unavailable rather
// than silently doing nothing.
func stubCommand(verb string, _ []string, opts Options) int {
	return stubCommandWith(verb, opts, os.Stdout, os.Stderr)
}

func stubCommandWith(verb string, opts Options, _ io.Writer, stderr io.Writer) int {
	printError(stderr, usageError("ktx %s: not implemented", verb), opts.Format)
	return ExitUsage
}

func runGet(args []string, opts Options) int    { return stubCommand("get", args, opts) }
func runList(args []string, opts Options) int   { return stubCommand("list", args, opts) }
func runInit(args []string, opts Options) int   { return stubCommand("init", args, opts) }
func runDoctor(args []string, opts Options) int { return stubCommand("doctor", args, opts) }
