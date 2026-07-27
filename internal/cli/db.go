// `ktx db` — the database namespace.
//
//	ktx db                    -> ktx db path (print current database path)
//	ktx db path               -> print current database path
//	ktx db --path <file> path -> print <file> (per-invocation override)
//	ktx db prune              -> unavailable
//
// The --path flag is per-invocation only; nothing is persisted. Path
// precedence: --path flag > KTX_DB env > $XDG_DATA_HOME/ktx/ktx.db.
//
// `db path` resolves and prints a path without opening the database. The
// unavailable `db prune` returns before opening one. `db report` opens the
// selected existing database through genuinely read-only access and never
// creates or migrates it.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/dbreport"
)

// runDBWith is the testable core of `ktx db`.
// stdout/stderr are injected so the path-resolution flow can be
// exercised without touching os.Stdout. The body parses the
// subcommand's --path flag and dispatches on the subcommand name. Path is
// DB-free, report opens read-only, and unavailable prune returns a usage error
// without opening a database.
func runDBWith(args []string, opts Options, stdout, stderr io.Writer) int {
	_, err := dbLogic(args, opts.Format, stdout, stderr)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// dbLogic routes database namespace operations.
//
// The verb does NOT take --help/-h at the dispatch level (the
// dispatcher handles --help globally); it parses only --path.
// `ktx db` without a subcommand defaults to `ktx db path`.
//
// `format` controls error rendering.
func dbLogic(args []string, format string, stdout, stderr io.Writer) (any, error) {
	var pathOverride string
	var showHelp bool
	fs := flag.NewFlagSet("ktx db", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.StringVar(&pathOverride, "path", "", "per-invocation database path override")
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx db: %v", err)
	}
	if showHelp {
		printDBHelp(stdout)
		return nil, nil
	}

	rest := fs.Args()
	// `ktx db` without a subcommand defaults to `ktx db path`.
	sub := "path"
	if len(rest) > 0 {
		sub = rest[0]
		rest = rest[1:]
	}

	switch sub {
	case "path":
		return dbPathLogic(pathOverride, stdout)
	case "report":
		return dbReportLogic(pathOverride, rest, stdout)
	case "prune":
		// The namespace reserves prune but does not implement it.
		return nil, usageError("ktx db prune: not implemented")
	default:
		return nil, usageError("ktx db: unknown subcommand %q; run 'ktx db --help'", sub)
	}
}

// dbReportLogic renders a read-only database evidence report. It uses a
// read-only SQLite connection and never runs product migrations.
func dbReportLogic(pathOverride string, args []string, stdout io.Writer) (any, error) {
	fs := flag.NewFlagSet("ktx db report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output string
	var includeIdentifiers bool
	fs.StringVar(&output, "output", "-", "output path, or - for stdout")
	fs.BoolVar(&includeIdentifiers, "include-identifiers", false, "include session IDs and absolute database path")
	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx db report: %v", err)
	}
	if len(fs.Args()) > 0 {
		return nil, usageError("ktx db report: unexpected argument %q", fs.Arg(0))
	}
	path, err := dbpkg.ResolvePath(pathOverride)
	if err != nil {
		return nil, internalError("ktx db report: resolve database path: %v", err)
	}
	report, err := dbreport.Generate(path, includeIdentifiers, time.Now())
	if err != nil {
		return nil, internalError("ktx db report: %v", err)
	}
	if output != "-" {
		if err := dbreport.WriteJSON(output, report); err != nil {
			return nil, internalError("ktx db report: %v", err)
		}
		return nil, nil
	}
	data, err := dbreport.MarshalJSON(report)
	if err != nil {
		return nil, internalError("ktx db report: %v", err)
	}
	if _, err := stdout.Write(data); err != nil {
		return nil, internalError("ktx db report: write output: %v", err)
	}
	return nil, nil
}

// dbPathLogic resolves the database path (precedence:
// --path flag > KTX_DB env > $XDG_DATA_HOME/ktx/ktx.db) and prints it
// on stdout. Output is the bare path so it pipes cleanly; the format
// flag does not apply to I/O commands (ktx db path
// reports the path, not a record).
func dbPathLogic(pathOverride string, stdout io.Writer) (any, error) {
	path, err := dbpkg.ResolvePath(pathOverride)
	if err != nil {
		return nil, internalError("ktx db: resolve database path: %v", err)
	}
	fmt.Fprintln(stdout, path)
	return nil, nil
}

// dbCommandFunc is the dispatch wrapper. The dispatcher already
// parsed --help/-h (via the Help hook) and --format; the verb
// receives a context carrying a clean positional tail and parsed path override.
//
// NeedsDB=false skips only the dispatcher-managed database open. `db path`
// and unavailable `db prune` are DB-free; `db report` owns its genuinely
// read-only open.
func dbCommandFunc(ctx *DispatchContext) (any, error) {
	parsed, ok := ctx.ParsedArgs.(dbParsedArgs)
	if !ok {
		return nil, internalError("ktx db: command arguments were not parsed")
	}
	// Rebuild the args so dbLogic remains the single path-resolution module.
	args := ctx.Args
	if parsed.pathOverride != "" {
		args = append([]string{"--path", parsed.pathOverride}, args...)
	}
	return dbLogic(args, ctx.Format, ctx.Stdout, ctx.Stderr)
}

// parseDBDispatchArgs extracts the per-invocation path override.
func parseDBDispatchArgs(args []string) ([]string, any, error) {
	var pathOverride string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--path" {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("--path requires a value")
			}
			pathOverride = args[i+1]
			i++ // skip the value
			continue
		}
		if strings.HasPrefix(a, "--path=") {
			pathOverride = strings.TrimPrefix(a, "--path=")
			continue
		}
		rest = append(rest, a)
	}
	return rest, dbParsedArgs{pathOverride: pathOverride}, nil
}

// dbParsedArgs carries command-specific flags to dbCommandFunc.
type dbParsedArgs struct {
	pathOverride string
}

// init registers `ktx db` with the dispatch module. After init runs,
// the verb is reachable through runWith.
func init() {
	Register("db", RegisterOptions{
		Description: "database path, reporting, and maintenance",
		NeedsDB:     false,
		ParseArgs:   parseDBDispatchArgs,
		Help:        printDBHelp,
	}, dbCommandFunc)
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
	fmt.Fprintln(w, "  report  emit a read-only evidence report (--output <path>|-)")
	fmt.Fprintln(w, "  prune   remove orphaned sessions (not yet implemented)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
