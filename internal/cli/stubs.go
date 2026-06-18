package cli

import (
	"io"
	"os"
)

// stubCommand is the placeholder for subcommands that are not yet
// implemented. It prints a machine-parseable usage error explaining the
// command is not yet implemented and exits 1.
//
// The stubs are deliberately honest: the surface exists so dispatch and
// help are complete, but the behavior is flagged as unavailable rather
// than silently doing nothing. As later tasks land, their `runFoo`
// functions are removed from this file and live next to the code that
// implements them (e.g., `runGet` is in get.go, `runWrite` in write.go).
func stubCommand(verb string, _ []string, opts Options) int {
	return stubCommandWith(verb, opts, os.Stdout, os.Stderr)
}

func stubCommandWith(verb string, opts Options, _ io.Writer, stderr io.Writer) int {
	printError(stderr, usageError("ktx %s: not implemented", verb), opts.Format)
	return ExitUsage
}

func runDoctor(args []string, opts Options) int { return stubCommand("doctor", args, opts) }
