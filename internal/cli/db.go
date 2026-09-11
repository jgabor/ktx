// `ktx db` — the database namespace.
//
//	ktx db                    -> ktx db path (print current database path)
//	ktx db path               -> print current database path
//	ktx db --path <file> path -> print <file> (per-invocation override)
//	ktx db migrate            -> migrate an existing database
//	ktx db prune              -> unavailable
//
// The --path flag is per-invocation only; nothing is persisted. Path
// precedence: --path flag > KTX_DB env > $XDG_DATA_HOME/ktx/ktx.db.
//
// `db path` resolves and prints a path without opening the database. The
// unavailable `db prune` returns before opening one. `db report` opens the
// selected existing database through genuinely read-only access and never
// creates or migrates it. `db migrate` owns the explicit forward migration.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	return runDispatchWithOptions("db", args, opts, stdout, stderr)
}

type dbMigrateResult struct {
	DatabasePath string `json:"database_path"`
	FromSchema   int    `json:"from_schema"`
	ToSchema     int    `json:"to_schema"`
	Migrated     bool   `json:"migrated"`
}

func dbMigrateLogic(pathOverride string, args []string, format string, stdout io.Writer) (any, error) {
	if len(args) > 0 {
		return nil, usageError("ktx db migrate: unexpected argument %q", args[0])
	}
	target, err := dbpkg.ResolveTarget(pathOverride)
	if err != nil {
		return nil, internalError("ktx db migrate: resolve database path: %v", err)
	}
	canonical, err := dbpkg.CanonicalPath(target.Path)
	if err != nil {
		return nil, internalError("ktx db migrate: canonicalize database path: %v", err)
	}
	readOnly, err := dbpkg.OpenReadOnly(target.Path)
	if err != nil {
		if _, statErr := os.Stat(target.Path); errors.Is(statErr, os.ErrNotExist) {
			return nil, notFoundError("ktx db migrate: database %q does not exist", canonical)
		}
		return nil, databaseOpenError(target.Path, "db migrate", err)
	}
	var before int
	readErr := readOnly.DB().QueryRow(`PRAGMA user_version`).Scan(&before)
	closeErr := readOnly.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, internalError("ktx db migrate: read schema version: %v", err)
	}
	if before > dbpkg.MaxSupportedSchema() {
		return nil, databaseOpenError(target.Path, "db migrate", &dbpkg.SchemaTooNewError{Actual: before, Maximum: dbpkg.MaxSupportedSchema()})
	}

	after := before
	if before < dbpkg.MaxSupportedSchema() {
		database, err := openExistingDatabase(target, "db migrate")
		if err != nil {
			return nil, err
		}
		after, err = database.SchemaVersion()
		closeErr := database.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return nil, internalError("ktx db migrate: verify migrated schema: %v", err)
		}
	}

	result := dbMigrateResult{DatabasePath: canonical, FromSchema: before, ToSchema: after, Migrated: after != before}
	if format == FormatText {
		fmt.Fprintf(stdout, "database: %s\nfrom_schema: %d\nto_schema: %d\nmigrated: %t\n", result.DatabasePath, result.FromSchema, result.ToSchema, result.Migrated)
		return nil, nil
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return nil, internalError("ktx db migrate: encode result: %v", err)
	}
	return nil, nil
}

// dbReportLogic prepares an existing supported older database, then renders a
// read-only evidence report.
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
	target, err := dbpkg.ResolveTarget(pathOverride)
	if err != nil {
		return nil, internalError("ktx db report: resolve database path: %v", err)
	}
	aliasesSource, err := destinationAliasesSource(target.Path, output)
	if err != nil {
		return nil, internalError("ktx db report: compare output with source database: %v", err)
	}
	if aliasesSource {
		return nil, sourceDatabaseDestinationError("ktx db report", "invalid-database-report-destination", target.Path)
	}
	report, err := dbreport.Generate(target.Path, includeIdentifiers, time.Now())
	if err != nil {
		return nil, queryDatabaseOpenError(target.Path, "db report", err)
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
// DatabaseNone skips the dispatcher-managed database open. `db path`
// and unavailable `db prune` are DB-free; `db report` owns its genuinely
// read-only open; `db migrate` owns its migration-capable open.
func dbCommandFunc(ctx *DispatchContext) (any, error) {
	parsed, ok := ctx.ParsedArgs.(dbParsedArgs)
	if !ok {
		return nil, internalError("ktx db: command arguments were not parsed")
	}
	sub := "path"
	rest := ctx.Args
	if len(rest) > 0 {
		sub, rest = rest[0], rest[1:]
	}
	switch sub {
	case "path":
		if len(rest) != 0 {
			return nil, usageError("ktx db path: unexpected argument %q", rest[0])
		}
		return dbPathLogic(parsed.pathOverride, ctx.Stdout)
	case "report":
		return dbReportLogic(parsed.pathOverride, rest, ctx.Stdout)
	case "migrate":
		return dbMigrateLogic(parsed.pathOverride, rest, ctx.Format, ctx.Stdout)
	case "prune":
		return nil, usageError("ktx db prune: not implemented")
	default:
		return nil, usageError("ktx db: unknown subcommand %q; run 'ktx db --help'", sub)
	}
}

// parseDBDispatchArgs extracts the per-invocation path override.
func parseDBDispatchArgs(args []string) ([]string, any, error) {
	var pathOverride string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
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
	printOwnedArgumentHelp(w)
	fmt.Fprintln(w, "Global flag (per-invocation, not persisted):")
	fmt.Fprintln(w, "  --path string   override the database file path for this invocation")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Path precedence: --path flag > KTX_DB env > $XDG_DATA_HOME/ktx/ktx.db")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  path    print the current database path (default when no subcommand given)")
	fmt.Fprintln(w, "  report  emit a read-only evidence report (--output <path>|-)")
	fmt.Fprintln(w, "          output must not alias the source database by path, symlink, or hard link")
	fmt.Fprintln(w, "          JSON report schema 3; outcome population is all tool calls")
	fmt.Fprintln(w, "          is_error false/true/invalid = success/error/unknown; exit_code is diagnostic")
	fmt.Fprintln(w, "          error rate is error/known; coverage is known/total; percentages round to 2 decimals")
	fmt.Fprintln(w, "  migrate upgrade an existing database to the current schema")
	fmt.Fprintln(w, "  prune   remove orphaned sessions (not yet implemented)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
