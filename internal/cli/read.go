// `ktx read` — the I/O command that fetches a persisted record by nanoid ID
// and prints the raw JSON to stdout. It is the mechanical counterpart to
// `ktx write`: one ID in, one record's JSON out.
//
// Both the tool_calls and sessions tables are addressed by 16-char nanoid;
// `ktx read` checks tool_calls first (the higher-volume table) and falls
// back to sessions. Either result is printed verbatim using the JSON shape
// of the row — no field transformation, no pretty-printing. Reversibility:
// a record fetched by `ktx read` is the same shape one could re-persist
// via `ktx write` (where applicable).
//
// Like `ktx write`, this is an I/O primitive: it always emits raw JSON.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runReadWith executes `ktx read` with injected output streams.
// stdout/stderr are injected so the read flow can be exercised without
// touching os.Stdout. The body opens the database, delegates to
// readLogic for the actual work, and translates the returned error
// into an exit-code + stderr envelope.
func runReadWith(args []string, opts Options, stdout, stderr io.Writer) int {
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx read: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	db, err := openQueryDatabase(path, "read")
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()

	_, err = readLogic(args, &db, stdout)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// readLogic reads one record from an open database and writes raw JSON.
//
// Step-by-step:
//  1. Parse --help/-h.
//  2. If --help, render printReadHelp and return.
//  3. Validate exactly one positional (the ID).
//  4. db.GetRecord(id): tool_calls first, then sessions.
//  5. Close the retrieved record's database handle and clear db.
//  6. json.NewEncoder(stdout).Encode(record): raw JSON to stdout.
//
// Anything that fails becomes a typed *Error (usage / not-found /
// internal) so the caller can map it to the right exit code.
func readLogic(args []string, db **dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx read", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		// The dispatcher removes --format before invoking this function;
		// direct callers receive normal unknown-flag validation.
		return nil, usageError("ktx read: %v", err)
	}
	if showHelp {
		printReadHelp(stdout)
		return nil, nil
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return nil, usageError("ktx read: missing required argument: <id>")
	}
	if len(rest) > 1 {
		return nil, usageError("ktx read: unexpected argument %q; ktx read takes a single ID", rest[1])
	}
	id := rest[0]

	record, err := (*db).GetRecord(id)
	if err != nil {
		if errors.Is(err, dbpkg.ErrNotFound) {
			return nil, notFoundError("ktx read: record %q not found", id)
		}
		return nil, internalError("ktx read: get record: %v", err)
	}

	// GetRecord has copied the row into Go-owned values. Release the
	// read-only handle before stdout can block on a large record.
	database := *db
	*db = nil
	_ = database.Close()

	// Raw JSON, no --format awareness. json.NewEncoder writes a
	// trailing newline, which matches the convention used by `ktx
	// write`'s id output and keeps stdout pipe-friendly.
	if err := json.NewEncoder(stdout).Encode(record); err != nil {
		return nil, internalError("ktx read: write JSON: %v", err)
	}
	return nil, nil
}

// readCommandFunc is the dispatch wrapper. The dispatcher already
// opened the DB read-only and parsed --help/--format; the verb
// receives a *DispatchContext carrying the populated DB and a clean
// positional Args tail. The verb's only responsibility is to apply
// readLogic and surface the typed error to the dispatcher's
// printError + exitCodeOf pipeline.
func readCommandFunc(ctx *DispatchContext) (any, error) {
	return readLogic(ctx.Args, &ctx.DB, ctx.Stdout)
}

// init registers `ktx read` with the dispatch module. After init
// runs, the verb is reachable through runWith.
func init() {
	Register("read", RegisterOptions{
		Description:       "fetch a persisted record by ID (raw JSON)",
		DatabaseAccess:    DatabaseReadOnly,
		PassthroughStdout: true,
		Help:              printReadHelp,
	}, readCommandFunc)
}

// printReadHelp writes the `ktx read` help. Mirrors `ktx write --help`
// style so the two I/O primitives present consistently. Used both as
// the dispatcher's Help hook (renderHelp invokes it on --help) and
// from readLogic when runReadWith is invoked directly with --help.
func printReadHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx read — fetch a persisted record by nanoid ID")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx read <id>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Fetches a record (tool_call or session) from the database by its 16-char")
	fmt.Fprintln(w, "nanoid ID and prints the raw JSON representation to stdout. tool_calls")
	fmt.Fprintln(w, "are checked first; sessions are checked second (both tables")
	fmt.Fprintln(w, "are addressed by nanoid ID).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  record found and printed")
	fmt.Fprintln(w, "  1  usage error (missing/invalid argument, unknown flag)")
	fmt.Fprintln(w, "  2  record not found")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output: raw JSON (the record as stored). No --format flag.")
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx write   persist a tool_call record from sidecar input")
	fmt.Fprintln(w, "  ktx get     query a record by ID (formatted, JSON default)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
