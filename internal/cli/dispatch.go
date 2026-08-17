// Package dispatch support owns the shared subcommand lifecycle: common flag
// parsing, database setup, help, error rendering, and exit-code mapping.
//
// A verb registers once (typically via an init() in its own file) with
//
//	dispatch.Register(verb, RegisterOptions{}, func(ctx *DispatchContext) (any, error) {
//	    ...
//	})
//
// and from then on the dispatcher (runWith, or a test) invokes it by
// name. The verb owns whatever it writes to ctx.Stdout; the module owns
// the flag set, the DB, the error envelope, and the exit code.
//
// Module owns:
//
//   - flag.NewFlagSet + SetOutput(io.Discard) + --help/-h
//   - subcommand-local --format extraction (via flags.extractFormat)
//   - database resolution and read-only/read-write open from DatabaseAccess
//   - error envelope rendering (printError(stderr, toKtxError(err), format))
//   - exit-code mapping (exitCodeOf; dbpkg.ErrNotFound → 2,
//     *Error{CodeUsage} → 1, *Error{CodeNotFound} → 2, etc.)
//
// Module does NOT own:
//
//   - JSON or text rendering of success output (the verb writes it)
//   - subprocess orchestration (ktx run)
//   - event parsing (ktx run / ktx write)
//
// Format contract:
//
//	FormatDefault is the seed value used when no subcommand-local
//	--format is passed. "" means "no format" (the verb renders
//	whatever it wants; PassthroughStdout verbs always land here).
//	FormatJSON / FormatText are the only validated values. Any
//	other value passed via --format is a usage error.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// RegisterOptions describes a subcommand's contract. Every field is a
// declaration of what the verb needs; the dispatcher reads them to decide
// whether to open the DB, capture stdin, validate --format, and render
// the help block. A zero-value RegisterOptions is a valid verb that does
// nothing and writes nothing — the baseline.
type RegisterOptions struct {
	// Description is the one-line summary shown by `ktx --help`.
	// Optional; when empty the verb is omitted from the help list
	// while remaining callable.
	Description string

	// DatabaseAccess asks the dispatcher to skip the database, open a
	// persistent read-only query handle, or use the normal create/migrate/write
	// path. The opened *dbpkg.DB lives at ctx.DB and is normally closed by the
	// dispatcher.
	DatabaseAccess DatabaseAccess

	// NeedsStdin asks the dispatcher to expose os.Stdin as ctx.Stdin.
	// Verbs that read sidecar JSON (ktx write) set this; verbs that
	// do not touch stdin leave it false so a stray pipe does not
	// block on EOF.
	NeedsStdin bool

	// PassthroughStdout signals that the verb writes its own raw
	// output (raw JSON without an envelope for ktx read / ktx write;
	// ktx run is a transparent proxy).
	// The dispatcher still renders errors to stderr but does not
	// impose any envelope on stdout.
	PassthroughStdout bool

	// FormatDefault is the seed for --format extraction. FormatJSON
	// for the query commands (ktx get / ktx list / ktx bench report);
	// "" for PassthroughStdout verbs and verbs that ignore format
	// (ktx init). The value is validated only when --format is
	// explicitly set on the command line.
	FormatDefault string

	// Help writes the verb's --help block to w. When nil, the dispatcher
	// reports that help is unavailable and exits successfully.
	Help func(io.Writer)

	// ParseArgs lets a verb consume command-specific flags before the shared
	// flag set runs. Parsed values are carried to the command in
	// DispatchContext.ParsedArgs, avoiding package-level handoff state.
	ParseArgs func(args []string) (rest []string, parsed any, err error)
}

// DatabaseAccess describes a command's persistent database behavior.
type DatabaseAccess uint8

const (
	DatabaseNone DatabaseAccess = iota
	DatabaseReadOnly
	DatabaseReadWrite
)

// CommandFunc is a command's logic. Commands write success output to
// ctx.Stdout and return errors for the dispatcher to render.
type CommandFunc func(ctx *DispatchContext) (any, error)

// DispatchContext contains the resources and parsed values available to a
// command. The dispatcher populates it before invocation.
type DispatchContext struct {
	// Args is the positional tail left after --help and any
	// subcommand flags. The verb reads it like os.Args[1:].
	Args []string

	// ParsedArgs is the command-specific value returned by ParseArgs. Commands
	// that do not register a parser receive nil.
	ParsedArgs any

	// Format is the resolved format string: the subcommand-local
	// --format if given, else FormatDefault, else "". Verbs that
	// ignore format (PassthroughStdout=true) leave it untouched.
	Format string

	// Stdin is os.Stdin when NeedsStdin is true; nil otherwise. A
	// verb that reads stdin without setting NeedsStdin would block
	// the dispatcher, which is why the option exists.
	Stdin io.Reader

	// Stdout / Stderr are the writers the verb should use. They are
	// always non-nil; tests inject bytes.Buffer here.
	Stdout io.Writer
	Stderr io.Writer

	// DB is the opened *dbpkg.DB when DatabaseAccess is not DatabaseNone; nil otherwise.
	// The dispatcher closes it after the command returns unless the command closes
	// and clears it first.
	DB *dbpkg.DB

	// Passthrough mirrors RegisterOptions.PassthroughStdout so the
	// verb can branch on it without keeping a reference to its own
	// options struct.
	Passthrough bool

	// Quiet is true when --quiet/-q was passed either as a
	// subcommand-local flag (parsed by the dispatcher's flag set)
	// or as a global flag (parsed by parseGlobalFlags and
	// forwarded via runDispatch). The dispatcher consumes the
	// flag so ctx.Args never sees it; verbs that respect --quiet
	// read this field rather than re-parsing ctx.Args.
	Quiet bool
}

// registeredVerb pairs the per-verb options with the verb's logic.
type registeredVerb struct {
	opts RegisterOptions
	do   CommandFunc
}

// dispatchRegistry is populated by command registrations during package init.
var dispatchRegistry = map[string]registeredVerb{}

// Register registers a verb with the dispatch table. It returns ExitOK
// after a successful registration and does not itself dispatch — the
// verb runs the next time runDispatch (or the dispatcher) looks it up.
//
// A nil do or empty verb name is treated as a programmer error and
// returns ExitUsage without touching the registry. Production code
// registers verbs from init(); tests register from a setup helper that
// also unregisters on t.Cleanup so parallel tests cannot observe a
// leaked registration.
func Register(verb string, opts RegisterOptions, do CommandFunc) int {
	if verb == "" || do == nil {
		return ExitUsage
	}
	dispatchRegistry[verb] = registeredVerb{opts: opts, do: do}
	return ExitOK
}

// RegisteredVerbs returns a snapshot of (name, description) pairs for
// every verb in the dispatch registry. The slice is sorted by name so
// the help output is deterministic. `ktx --help` uses this snapshot.
func RegisteredVerbs() []struct {
	Name        string
	Description string
} {
	out := make([]struct {
		Name        string
		Description string
	}, 0, len(dispatchRegistry))
	for name, reg := range dispatchRegistry {
		out = append(out, struct {
			Name        string
			Description string
		}{Name: name, Description: reg.opts.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// runDispatch looks up verb in the dispatch registry and runs it.
// Returns a usage-error envelope with exit 1 if the verb is not registered.
//
// globalQuiet is the global --quiet parsed by parseGlobalFlags
// (cli.go); the dispatch module forwards it to the verb via
// DispatchContext.Quiet. The forward is essential so `ktx --quiet run
// <agent>` and `ktx run <agent> --quiet` produce the same behaviour;
// without it the verb would only see the subcommand-local --quiet.
func runDispatch(verb string, args []string, globalQuiet bool, stdout, stderr io.Writer) int {
	return runDispatchWithOptions(verb, args, Options{Quiet: globalQuiet}, stdout, stderr)
}

func runDispatchWithOptions(verb string, args []string, global Options, stdout, stderr io.Writer) int {
	reg, ok := dispatchRegistry[verb]
	if !ok {
		printError(stderr, usageError("unknown command %q", verb), FormatJSON)
		return ExitUsage
	}
	return runRegisteredVerb(verb, reg, args, global, stdout, stderr)
}

// runRegisteredVerb executes a single registered verb end-to-end.
// The pipeline is:
//
//  1. ParseArgs (optional) — verb-specific arg filter and typed result.
//  2. extractFormat       — pull --format out of the remaining args;
//     SKIPPED for verbs with PassthroughStdout=true
//     (the run verb): a passthrough verb forwards its
//     args including flags to a child agent, so
//     --format there belongs to the agent, not ktx.
//  3. flag set            — --help/-h and --quiet/-q; everything else
//     is an error. --quiet/-q is consumed so
//     ctx.Args never sees it; the resolved
//     value is exposed via ctx.Quiet.
//  4. DB open             — according to DatabaseAccess.
//  5. Stdin capture       — only when NeedsStdin.
//  6. Invoke the verb     — pass *DispatchContext.
//  7. Render + exit       — printError + exitCodeOf on err.
//
// Each step is conditional so a verb that needs nothing (the
// baseline) does no I/O.
func runRegisteredVerb(verb string, reg registeredVerb, args []string, global Options, stdout, stderr io.Writer) int {
	// 1. ParseArgs: optional verb-specific parsing.
	rest := args
	var parsedArgs any
	if reg.opts.ParseArgs != nil {
		parsed, values, err := reg.opts.ParseArgs(args)
		if err != nil {
			printError(stderr, usageError("%v", err), FormatJSON)
			return ExitUsage
		}
		rest = parsed
		parsedArgs = values
	}

	// 2. extractFormat: pull --format out of rest. Skipped for verbs
	// with PassthroughStdout=true (the run verb). A passthrough verb
	// forwards its args to a child binary that owns its own --format
	// (e.g. `opencode run --format json ...`); ktx must not steal it.
	// For query verbs (get, list, bench, doctor) --format is ktx's own
	// query-render flag and extractFormat continues to handle it in
	// either position (per get_test.go:9, list_test.go:5).
	formatStr := reg.opts.FormatDefault
	if !reg.opts.PassthroughStdout {
		if extracted, afterFormat := extractFormat(rest); extracted != "" {
			formatStr = extracted
			rest = afterFormat
		}
	}
	if formatStr != "" && formatStr != FormatJSON && formatStr != FormatText {
		printError(stderr, usageError("invalid --format %q: want %q or %q", formatStr, FormatJSON, FormatText), FormatJSON)
		return ExitUsage
	}

	// 3. Flag set: --help/-h and --quiet/-q. The dispatcher
	// consumes --quiet so the verb does not see it as an unknown
	// flag (e.g. the run verb) and exposes the resolved value via
	// ctx.Quiet.
	fs := flag.NewFlagSet("ktx", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var showHelp bool
	fs.BoolVar(&showHelp, "help", false, "show help for this command")
	fs.BoolVar(&showHelp, "h", false, "show help for this command (shorthand)")
	var subQuiet bool
	fs.BoolVar(&subQuiet, "quiet", false, "suppress non-essential output")
	fs.BoolVar(&subQuiet, "q", false, "suppress non-essential output (shorthand)")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return renderHelp(reg.opts.Help, stdout)
		}
		printError(stderr, usageError("%v", err), formatStr)
		return ExitUsage
	}
	if showHelp {
		return renderHelp(reg.opts.Help, stdout)
	}
	// 4. Open the database according to the command's declared access mode.
	var db *dbpkg.DB
	var databasePath string
	if reg.opts.DatabaseAccess != DatabaseNone {
		openStarted := time.Now()
		path, err := dbpkg.ResolvePath("")
		if err != nil {
			printError(stderr, internalError("dispatch: resolve db path: %v", err), formatStr)
			return ExitInternal
		}
		databasePath = path
		var opened *dbpkg.DB
		switch reg.opts.DatabaseAccess {
		case DatabaseReadOnly:
			opened, err = openQueryDatabase(path, verb)
		case DatabaseReadWrite:
			if verb == "hook" {
				opened, err = openHookDatabase(path, verb)
			} else {
				opened, err = openDatabase(path, verb)
			}
		default:
			err = internalError("dispatch: unsupported database access mode %d", reg.opts.DatabaseAccess)
		}
		if err != nil {
			if classified := classifyDatabaseLockError(databasePath, verb, err, dbpkg.OperationOpen, openStarted); classified != nil {
				err = classified
			}
			printError(stderr, err, formatStr)
			return exitCodeOf(err)
		}
		db = opened
	}

	// 5. Capture stdin when NeedsStdin.
	var stdin io.Reader
	if reg.opts.NeedsStdin {
		stdin = os.Stdin
	}

	// 6. Build the context and invoke the verb.
	ctx := &DispatchContext{
		Args:        fs.Args(),
		ParsedArgs:  parsedArgs,
		Format:      formatStr,
		Stdin:       stdin,
		Stdout:      stdout,
		Stderr:      stderr,
		DB:          db,
		Passthrough: reg.opts.PassthroughStdout,
		Quiet:       global.Quiet || subQuiet,
	}
	defer func() {
		if ctx.DB != nil {
			_ = ctx.DB.Close()
		}
	}()
	operationStarted := time.Now()
	if _, err := reg.do(ctx); err != nil {
		var status *commandExit
		if errors.As(err, &status) {
			return status.code
		}
		phase := dbpkg.OperationSource
		if reg.opts.DatabaseAccess == DatabaseReadOnly {
			phase = dbpkg.OperationQuery
		}
		if classified := classifyDatabaseLockError(databasePath, verb, err, phase, operationStarted); classified != nil {
			err = classified
		}
		printError(stderr, err, formatStr)
		return exitCodeOf(err)
	}
	return ExitOK
}

// renderHelp invokes the registered renderer or reports that help is
// unavailable. Help always returns ExitOK.
func renderHelp(help func(io.Writer), stdout io.Writer) int {
	if help != nil {
		help(stdout)
		return ExitOK
	}
	fmt.Fprintln(stdout, "no help available")
	return ExitOK
}
