package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runDB implements the `ktx db` namespace (Decision 8).
//
//	ktx db                    -> ktx db path (print current database path)
//	ktx db path               -> print current database path
//	ktx db --path <file> path -> print <file> (per-invocation override)
//	ktx db prune              -> stubbed (Task 1: not implemented)
//
// The --path flag is per-invocation only; nothing is persisted. Path
// precedence (Decision 8): --path flag > KTX_DB env > $XDG_DATA_HOME/ktx/ktx.db.
func runDB(args []string, opts Options) int {
	return runDBWith(args, opts, os.Stdout, os.Stderr)
}

func runDBWith(args []string, opts Options, stdout, stderr io.Writer) int {
	var pathOverride string
	var showHelp bool
	fs := flag.NewFlagSet("ktx db", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.StringVar(&pathOverride, "path", "", "per-invocation database path override")
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		printError(stderr, usageError("ktx db: %v", err), opts.Format)
		return ExitUsage
	}

	if showHelp {
		printDBHelp(stdout)
		return ExitOK
	}

	rest := fs.Args()
	// `ktx db` without a subcommand defaults to `ktx db path` (Decision 8).
	sub := "path"
	if len(rest) > 0 {
		sub = rest[0]
		rest = rest[1:]
	}

	switch sub {
	case "path":
		return dbPath(pathOverride, opts, stdout, stderr)
	case "prune":
		// Future orphaned-session cleanup (D1 revised). Stubbed for Task 1.
		printError(stderr, usageError("ktx db prune: not implemented"), opts.Format)
		return ExitUsage
	default:
		printError(stderr, usageError("ktx db: unknown subcommand %q; run 'ktx db --help'", sub), opts.Format)
		return ExitUsage
	}
}

// dbPath resolves and prints the database path. Output is the bare path on
// stdout so it pipes cleanly; the format flag does not apply to I/O commands
// (Decision 8: ktx db path reports the path, not a record).
func dbPath(pathOverride string, opts Options, stdout, stderr io.Writer) int {
	path, err := dbpkg.ResolvePath(pathOverride)
	if err != nil {
		printError(stderr, internalError("resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	fmt.Fprintln(stdout, path)
	return ExitOK
}

// printDBHelp writes the `ktx db` help.
func printDBHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx db — database operations")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx db [--path <file>] <subcommand>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flag (per-invocation, not persisted):")
	fmt.Fprintln(w, "  --path string   override the database file path for this invocation")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Path precedence: --path flag > KTX_DB env > $XDG_DATA_HOME/ktx/ktx.db")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  path    print the current database path (default when no subcommand given)")
	fmt.Fprintln(w, "  prune   remove orphaned sessions (not yet implemented)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
