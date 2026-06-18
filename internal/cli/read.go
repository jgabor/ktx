// `ktx read` — the I/O command that fetches a persisted record by nanoid ID
// and prints the raw JSON to stdout (Decisions 2, 7, 8). It is the
// mechanical counterpart to `ktx write`: one ID in, one record's JSON out.
//
// Both the tool_calls and sessions tables are addressed by 16-char nanoid
// (Decision 7); `ktx read` checks tool_calls first (the higher-volume
// table) and falls back to sessions. Either result is printed verbatim
// using the JSON shape of the row — no field transformation, no
// pretty-printing. Reversibility: a record fetched by `ktx read` is the
// same shape one could re-persist via `ktx write` (where applicable).
//
// Like `ktx write`, this is an I/O primitive (Decision 2/8): raw JSON, no
// --format flag at the subcommand level. The global --format flag is
// parsed by the dispatcher and passed in via opts.Format but is intentionally
// ignored here — the I/O surface is mechanical, not format-aware.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jgabor/ktx/internal/store"
)

func runRead(args []string, opts Options) int {
	return runReadWith(args, opts, os.Stdout, os.Stderr)
}

// runReadWith is the testable core of `ktx read`. stdout/stderr are
// injected so the read flow can be exercised without touching os.Stdout.
func runReadWith(args []string, opts Options, stdout, stderr io.Writer) int {
	var showHelp bool
	fs := flag.NewFlagSet("ktx read", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		// This rejects `ktx read --format text <id>` and any other unknown
		// flag, matching the I/O-primitive rule (Decision 2/8): there is no
		// --format flag available on this command.
		printError(stderr, usageError("ktx read: %v", err), opts.Format)
		return ExitUsage
	}
	if showHelp {
		printReadHelp(stdout)
		return ExitOK
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printError(stderr, usageError("ktx read: missing required argument: <id>"), opts.Format)
		return ExitUsage
	}
	if len(rest) > 1 {
		printError(stderr, usageError("ktx read: unexpected argument %q; ktx read takes a single ID", rest[1]), opts.Format)
		return ExitUsage
	}
	id := rest[0]

	// Database path resolution (Decision 8): KTX_DB env > default. ktx read
	// does not take a --path flag; per-invocation override lives on `ktx db`.
	path, err := store.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx read: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	db, err := store.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx read: open database: %v", err), opts.Format)
		return ExitInternal
	}
	defer db.Close()

	record, err := db.GetRecord(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			printError(stderr, notFoundError("ktx read: record %q not found", id), opts.Format)
			return ExitNotFound
		}
		printError(stderr, internalError("ktx read: get record: %v", err), opts.Format)
		return ExitInternal
	}

	// Raw JSON, no --format awareness. json.NewEncoder writes a trailing
	// newline, which matches the convention used by `ktx write`'s id output
	// and keeps stdout pipe-friendly.
	if err := json.NewEncoder(stdout).Encode(record); err != nil {
		printError(stderr, internalError("ktx read: write JSON: %v", err), opts.Format)
		return ExitInternal
	}
	return ExitOK
}

// printReadHelp writes the `ktx read` help. Mirrors `ktx write --help`
// style so the two I/O primitives present consistently.
func printReadHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx read — fetch a persisted record by nanoid ID")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx read <id>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Fetches a record (tool_call or session) from the database by its 16-char")
	fmt.Fprintln(w, "nanoid ID and prints the raw JSON representation to stdout. tool_calls")
	fmt.Fprintln(w, "are checked first; sessions are checked second (Decision 7: both tables")
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
