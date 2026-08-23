// `ktx decompress` resolves a compressed record's source and returns the
// original tool output verbatim.
//
// Reversibility is OUT-OF-BAND by id: there is no per-stage inverse,
// no in-band hash marker, no CCR hash store. The tool_calls table
// holds append-only originals (INSERT-only, content_hash for
// integrity, nanoid addressability), and the compressed row records
// what compressed it (source_id + pipeline JSON). Decompression is a
// single indexed SELECT against tool_calls.output.
//
// Flow:
//
//  1. Fetch the compressed row by id (db.GetCompressed).
//     Compressed-not-found maps to exit 2.
//  2. Resolve source_id → tool_calls.output via db.GetSourceOutput.
//     Source-not-found (the tool_call row was deleted, violating the
//     append-only contract) also maps to exit 2 — the typed
//     ErrSourceNotFound wraps ErrNotFound so the same exit-2 surface
//     applies.
//  3. Print the source's original output bytes verbatim to stdout.
//     No --format flag (I/O primitive); the bytes are the bytes, no
//     JSON wrapping.
//
// The command does not verify content_hash; it is a retrieval primitive and
// returns the stored original.
//
// Registration uses read-only database access and passthrough stdout because
// the command returns raw bytes rather than a JSON envelope.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runDecompressWith is the testable core of `ktx decompress`. stdout/stderr
// are injected so the decompress flow can be exercised without touching
// os.Stdout or os.Stderr. The body opens the database, delegates to
// decompressLogic, and translates the returned error into an exit-code +
// stderr envelope.
func runDecompressWith(args []string, opts Options, stdout, stderr io.Writer) int {
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx decompress: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	db, err := openQueryDatabase(path, "decompress")
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	defer db.Close()

	_, err = decompressLogic(args, db, stdout)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// decompressLogic restores one compressed record from an open database.
//
// Step-by-step:
//  1. Parse --help/-h.
//  2. Validate exactly one positional (the compressed id).
//  3. Fetch the compressed row by id. Missing compressed row → exit 2.
//  4. Resolve the source_id → tool_calls.output via GetSourceOutput.
//     Missing source row (append-only contract violated by a downstream tool) → exit 2.
//  5. Print the source's original output bytes verbatim to stdout.
func decompressLogic(args []string, db *dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx decompress", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx decompress: %v", err)
	}
	if showHelp {
		printDecompressHelp(stdout)
		return nil, nil
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return nil, usageError("ktx decompress: missing required argument: <compressed-id>")
	}
	if len(rest) > 1 {
		return nil, usageError("ktx decompress: unexpected argument %q; ktx decompress takes a single compressed id", rest[1])
	}
	compressedID := rest[0]

	// Fetch the compressed row. A missing compressed row is the
	// canonical not-found case for this verb — the user addressed a
	// compressed id, and we cannot find it.
	comp, err := db.GetCompressed(compressedID)
	if err != nil {
		if errors.Is(err, dbpkg.ErrNotFound) {
			return nil, notFoundError("ktx decompress: compressed %q not found", compressedID)
		}
		return nil, internalError("ktx decompress: get compressed: %v", err)
	}

	// Resolve the source row. The compressed.source_id is an
	// FK-referenced tool_calls.id, so this should always hit — but
	// a tool that violates the append-only contract (by deleting a
	// tool_calls row) would leave a dangling compressed row. The
	// typed error surface maps that to exit 2 as well, so the user
	// sees a not-found rather than a JSON error envelope.
	sourceOutput, err := db.GetSourceOutput(comp.SourceID)
	if err != nil {
		if errors.Is(err, dbpkg.ErrNotFound) {
			return nil, notFoundError("ktx decompress: source tool_call %q not found (source row deleted)", comp.SourceID)
		}
		return nil, internalError("ktx decompress: get source output: %v", err)
	}

	// Print the source's original output bytes verbatim. Distinct
	// from `ktx compress` (which prints a JSON-encoded id): here
	// the artifact IS the output, and emitting it raw is the only
	// faithful representation (a JSON wrapping would escape
	// newlines, double-encode any embedded JSON, etc.).
	fmt.Fprint(stdout, sourceOutput)
	return nil, nil
}

// decompressCommandFunc is the dispatch wrapper. The dispatcher
// already opened the DB read-only, parsed --help/--format, and
// exposed a clean positional Args tail. The verb's only
// responsibility is to apply decompressLogic and surface the typed
// error to the dispatcher's printError + exitCodeOf pipeline.
func decompressCommandFunc(ctx *DispatchContext) (any, error) {
	return decompressLogic(ctx.Args, ctx.DB, ctx.Stdout)
}

// init registers `ktx decompress` with the dispatch module. After
// init runs, the verb is reachable through runWith.
//
// DatabaseReadOnly: decompress reads a compressed row + the source
// tool_call row.
// PassthroughStdout=true: the verb writes the raw source output
// bytes (distinct from `ktx compress`, which JSON-encodes the id).
func init() {
	Register("decompress", RegisterOptions{
		Description:       "reverse a compression (return source output verbatim)",
		DatabaseAccess:    DatabaseReadOnly,
		PassthroughStdout: true,
		Help:              printDecompressHelp,
	}, decompressCommandFunc)
}

// printDecompressHelp writes the `ktx decompress` help. Mirrors
// `ktx compress --help` style so the I/O pair present consistently.
// Cross-references `ktx compress` (the forward direction) so users
// can discover the pair without reading the design rationale
// (the --help cross-reference rule).
func printDecompressHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx decompress — reverse a compression (return source output verbatim)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx decompress <compressed-id>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Resolves the compressed row's source_id and returns the source tool_call's")
	fmt.Fprintln(w, "original output bytes verbatim. The reversal is out-of-band by id:")
	fmt.Fprintln(w, "no per-stage inverse runs, no in-band hash is consulted. The tool_calls")
	fmt.Fprintln(w, "table holds append-only originals (with content_hash); a compressed row references the")
	fmt.Fprintln(w, "original by id, and decompression is a single indexed SELECT against")
	fmt.Fprintln(w, "tool_calls.output.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output: the source's original bytes, written verbatim to stdout. No JSON")
	fmt.Fprintln(w, "wrapping, no --format flag (the bytes are the bytes — distinct from")
	fmt.Fprintln(w, "`ktx compress`, which JSON-encodes the new id). This is the I/O primitive")
	fmt.Fprintln(w, "satisfying the reversal acceptance contract: a compressed output can be retrieved as")
	fmt.Fprintln(w, "the original bytes via the public surface.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  source output written to stdout")
	fmt.Fprintln(w, "  1  usage error (missing/invalid compressed-id)")
	fmt.Fprintln(w, "  2  compressed row not found, OR source tool_call deleted (append-only violated)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx compress  compress a tool_call's output (reversible via this verb)")
	fmt.Fprintln(w, "  ktx read      fetch any persisted record by ID (raw JSON)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
