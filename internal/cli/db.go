// `ktx db` — the database namespace (Decision 8).
//
//	ktx db                    -> ktx db path (print current database path)
//	ktx db path               -> print current database path
//	ktx db --path <file> path -> print <file> (per-invocation override)
//	ktx db prune              -> stubbed (Task 1: not implemented)
//
// The --path flag is per-invocation only; nothing is persisted. Path
// precedence (Decision 8): --path flag > KTX_DB env > $XDG_DATA_HOME/ktx/ktx.db.
//
// Task 7 migration: the verb registers via dispatch.Register with
// NeedsDB=false (it only resolves and prints the path — the verb does
// not touch the database). The legacy runDBWith remains the test
// seam (Task 3 surface) and still parses its own flags; the dispatch
// wrapper extracts --path via a stdlib flag.NewFlagSet inside the
// shared dbLogic core so both call sites have identical semantics.
// The dispatch registry is consulted before the legacy commandIndex
// (cli.go), so the registered verb shadows the legacy entry as soon
// as init() runs.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runDBWith is the testable core of `ktx db` (Task 3 seam).
// stdout/stderr are injected so the path-resolution flow can be
// exercised without touching os.Stdout. The body parses the
// subcommand's --path flag and dispatches on the subcommand name
// (path -> dbPathLogic; prune -> stubbed).
func runDBWith(args []string, opts Options, stdout, stderr io.Writer) int {
	_, err := dbLogic(args, opts.Format, stdout, stderr)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// dbLogic is the shared core of `ktx db`. Both runDBWith (the legacy
// test seam) and the dispatch wrapper dbCommandFunc call it. Returns
// (any, error) so the dispatcher can map the typed error envelope
// through its own printError + exitCodeOf pipeline.
//
// The verb does NOT take --help/-h at the dispatch level (the
// dispatcher handles --help globally); it parses only --path. The
// subcommand dispatch follows the same pattern as the legacy
// runDBWith: `ktx db` without a subcommand defaults to `ktx db path`.
//
// `format` is the resolved --format (from the dispatcher's
// extractFormat + FormatDefault). The verb uses it for error
// envelope rendering on the legacy seam; the dispatch wrapper
// passes ctx.Format directly.
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
	// `ktx db` without a subcommand defaults to `ktx db path` (Decision 8).
	sub := "path"
	if len(rest) > 0 {
		sub = rest[0]
		rest = rest[1:]
	}

	switch sub {
	case "path":
		return dbPathLogic(pathOverride, stdout)
	case "prune":
		// Future orphaned-session cleanup (D1 revised). Stubbed
		// for Task 1; preserved as a usage error so the CLI
		// surface reserves the verb without claiming behavior
		// that does not exist yet.
		return nil, usageError("ktx db prune: not implemented")
	default:
		return nil, usageError("ktx db: unknown subcommand %q; run 'ktx db --help'", sub)
	}
}

// dbPathLogic resolves the database path (Decision 8 precedence:
// --path flag > KTX_DB env > $XDG_DATA_HOME/ktx/ktx.db) and prints it
// on stdout. Output is the bare path so it pipes cleanly; the format
// flag does not apply to I/O commands (Decision 8: ktx db path
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
// receives a *DispatchContext carrying a clean positional Args tail
// (the dbPreParse hook extracted --path before the dispatcher flag
// set ran, and the value lives in dbScratch.pathOverride). The
// verb's only responsibility is to apply dbLogic with the
// stashed --path override and surface the typed error to the
// dispatcher's printError + exitCodeOf pipeline.
//
// NeedsDB=false: ktx db never touches the database — it only
// resolves and prints the path. The dispatcher's dbpkg.ResolvePath
// + dbpkg.Open calls are skipped, so a missing or unreadable
// database file does not block this verb (the AC contract that
// `ktx db path` works without an existing database).
func dbCommandFunc(ctx *DispatchContext) (any, error) {
	// Rebuild the args from dbScratch.pathOverride + ctx.Args
	// so dbLogic's own flag set can re-parse --path
	// consistently. dbLogic uses the same flag set on both
	// call sites (legacy seam and dispatch wrapper), so the
	// dispatch path's behavior is identical to the legacy
	// seam's.
	args := ctx.Args
	if dbScratch.pathOverride != "" {
		args = append([]string{"--path", dbScratch.pathOverride}, args...)
	}
	return dbLogic(args, ctx.Format, ctx.Stdout, ctx.Stderr)
}

// dbPreParse is the dispatcher's PreParse hook for `ktx db`. It
// extracts the verb-specific --path flag (per-invocation path
// override, Decision 8) and stashes the value in dbScratch. The
// dispatcher consumes --path because the per-verb flag set in
// runRegisteredVerb only knows about --help and --quiet; if we
// let the verb parse --path directly, the dispatcher's flag set
// would reject it ("flag provided but not defined: -path"). The
// PreParse hook extracts --path before the dispatcher's flag set
// runs and returns the remaining args (so --path is consumed).
//
// Hand-rolled rather than stdlib flag: the dispatcher's flag set
// already handles --help/-h, --quiet/-q; the PreParse hook must
// pass those through unchanged. stdlib flag.NewFlagSet would
// reject unknown flags (including --help if --help is not
// registered on this set), so a hand-rolled walk is simpler.
func dbPreParse(args []string) ([]string, error) {
	dbScratchReset()
	var pathOverride string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--path" {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--path requires a value")
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
	dbScratch.pathOverride = pathOverride
	return rest, nil
}

// dbScratch holds the PreParse result for ktx db. The dispatch
// module's PreParse signature is `(args) (rest, err)`; db needs
// to surface (pathOverride) to the verb body. A package-level
// scratch variable bridges the gap; safe because
// runRegisteredVerb is synchronous.
type dbScratchValue struct {
	pathOverride string
}

var dbScratch dbScratchValue

func dbScratchReset() {
	dbScratch = dbScratchValue{}
}

// init registers `ktx db` with the dispatch module. After init runs,
// the verb is reachable through runWith (the legacy commandIndex
// entry in cli.go is removed in Task 7).
func init() {
	Register("db", RegisterOptions{
		Description: "database operations (path, prune)",
		NeedsDB:     false,
		PreParse:    dbPreParse,
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
	fmt.Fprintln(w, "  prune   remove orphaned sessions (not yet implemented)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}