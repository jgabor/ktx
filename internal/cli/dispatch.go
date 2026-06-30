// The dispatch deep module. Owns every piece of subcommand boilerplate
// that used to live at the top of each runXWith: flag.NewFlagSet + Discard
// output + --help/-h, subcommand-local --format extraction, database open
// + close, error envelope rendering, and exit-code mapping.
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
// Tasks 5-7 migrate the existing 10 subcommands onto this module. Until
// then the legacy commandIndex in cli.go continues to dispatch the
// existing verbs; runWith falls through to this registry for any verb
// the legacy table does not recognise, which is what the Task 4 demo
// verbs use to prove the integration end-to-end.
//
// Module owns (Decision 2 / Decision 4 surface):
//
//   - flag.NewFlagSet + SetOutput(io.Discard) + --help/-h
//   - subcommand-local --format extraction (via flags.extractFormat)
//   - dbpkg.ResolvePath + dbpkg.Open + defer db.Close when NeedsDB
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

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// RegisterOptions describes a subcommand's contract. Every field is a
// declaration of what the verb needs; the dispatcher reads them to decide
// whether to open the DB, capture stdin, validate --format, and render
// the help block. A zero-value RegisterOptions is a valid verb that does
// nothing and writes nothing — the AC1 baseline.
type RegisterOptions struct {
	// Description is the one-line summary shown by `ktx --help`.
	// Optional; when empty the verb is omitted from the help list
	// (the verb is still callable, just not advertised). The legacy
	// commands slice carried a parallel description field; moving
	// descriptions onto RegisterOptions keeps the help surface
	// working after the legacy slice is emptied in Task 7.
	Description string

	// NeedsDB asks the dispatcher to open the ktx database before
	// invoking the verb. The opened *dbpkg.DB lives at ctx.DB; the
	// dispatcher defers db.Close. If NeedsDB is false the verb never
	// touches the database, which is the contract that keeps
	// ktx db / ktx prices / ktx init fast and side-effect-free.
	NeedsDB bool

	// NeedsStdin asks the dispatcher to expose os.Stdin as ctx.Stdin.
	// Verbs that read sidecar JSON (ktx write) set this; verbs that
	// do not touch stdin leave it false so a stray pipe does not
	// block on EOF.
	NeedsStdin bool

	// PassthroughStdout signals that the verb writes its own raw
	// output (the I/O half of Decision 2: ktx read / ktx write emit
	// raw JSON without an envelope; ktx run is a transparent proxy).
	// The dispatcher still renders errors to stderr but does not
	// impose any envelope on stdout.
	PassthroughStdout bool

	// FormatDefault is the seed for --format extraction. FormatJSON
	// for the query commands (ktx get / ktx list / ktx bench report);
	// "" for PassthroughStdout verbs and verbs that ignore format
	// (ktx init). The value is validated only when --format is
	// explicitly set on the command line.
	FormatDefault string

	// Help writes the verb's --help block to w. Optional; when nil
	// the dispatcher prints a one-line stub so --help is still a
	// graceful exit-0 path.
	Help func(io.Writer)

	// PreParse lets a verb pre-process args before the flag set sees
	// them. ktx init uses this to keep its hand-rolled parseInitArgs
	// (the init flag surface is intentionally not stdlib-flag). The
	// returned rest is what the flag set parses; an error short-
	// circuits to a usage error.
	PreParse func(args []string) (rest []string, err error)
}

// CommandFunc is the verb's logic. It receives a populated
// DispatchContext and returns (any, error). The return value is reserved
// for future envelope-rendering verbs; today the verb writes whatever it
// wants to ctx.Stdout and returns nil for the first slot.
type CommandFunc func(ctx *DispatchContext) (any, error)

// DispatchContext is what the verb sees at runtime. Every field is set
// by the dispatcher before the verb runs; the verb must not mutate any
// field (they are passed by pointer for symmetry with future envelope
// rendering, not because mutation is supported).
type DispatchContext struct {
	// Args is the positional tail left after --help and any
	// subcommand flags. The verb reads it like os.Args[1:].
	Args []string

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

	// DB is the opened *dbpkg.DB when NeedsDB is true; nil otherwise.
	// The dispatcher owns the Close; the verb must not call it.
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

// dispatchRegistry is the lookup table populated by Register and read
// by runDispatch. Package-level so init()-time registrations land
// before any runWith call; tests unregister their verbs via t.Cleanup
// to stay hermetic.
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
// the help output is deterministic. The dispatcher uses this in
// place of the legacy commands slice (Task 7 emptied it) so
// `ktx --help` still lists every registered verb.
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
// Returns a usage-error envelope with exit 1 if the verb is not
// registered; that path matches the legacy commandIndex "unknown
// command" surface so a caller's experience is identical whether the
// verb is legacy or new.
//
// globalQuiet is the global --quiet parsed by parseGlobalFlags
// (cli.go); the dispatch module forwards it to the verb via
// DispatchContext.Quiet. The forward is essential so `ktx --quiet run
// <agent>` and `ktx run <agent> --quiet` produce the same behaviour;
// without it the verb would only see the subcommand-local --quiet.
func runDispatch(verb string, args []string, globalQuiet bool, stdout, stderr io.Writer) int {
	reg, ok := dispatchRegistry[verb]
	if !ok {
		printError(stderr, usageError("unknown command %q", verb), FormatJSON)
		return ExitUsage
	}
	return runRegisteredVerb(reg, args, globalQuiet, stdout, stderr)
}

// runRegisteredVerb executes a single registered verb end-to-end.
// The pipeline is:
//
//  1. PreParse (optional) — verb-specific arg filter.
//  2. extractFormat       — pull --format out of the remaining args;
//                           SKIPPED for verbs with PassthroughStdout=true
//                           (the run verb): a passthrough verb forwards its
//                           args including flags to a child agent, so
//                           --format there belongs to the agent, not ktx.
//  3. flag set            — --help/-h and --quiet/-q; everything else
//                           is an error. --quiet/-q is consumed so
//                           ctx.Args never sees it; the resolved
//                           value is exposed via ctx.Quiet.
//  4. DB open             — only when NeedsDB.
//  5. Stdin capture       — only when NeedsStdin.
//  6. Invoke the verb     — pass *DispatchContext.
//  7. Render + exit       — printError + exitCodeOf on err.
//
// Each step is conditional so a verb that needs nothing (the AC1
// baseline) does no I/O.
func runRegisteredVerb(reg registeredVerb, args []string, globalQuiet bool, stdout, stderr io.Writer) int {
	// 1. PreParse: optional verb-specific arg filter.
	rest := args
	if reg.opts.PreParse != nil {
		parsed, err := reg.opts.PreParse(args)
		if err != nil {
			printError(stderr, usageError("%v", err), FormatJSON)
			return ExitUsage
		}
		rest = parsed
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
	// flag (Task 6 run verb) and exposes the resolved value via
	// ctx.Quiet (Task 6 DispatchContext extension).
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

	// 4. Open the database when NeedsDB.
	// ResolvePath / Open are the same calls the legacy runXWith
	// wrappers make (see bench.go / read.go / write.go); failures
	// surface as internal errors because the database layer is the
	// source of truth for that mapping.
	var db *dbpkg.DB
	if reg.opts.NeedsDB {
		path, err := dbpkg.ResolvePath("")
		if err != nil {
			printError(stderr, internalError("dispatch: resolve db path: %v", err), formatStr)
			return ExitInternal
		}
		opened, err := dbpkg.Open(path)
		if err != nil {
			printError(stderr, internalError("dispatch: open db: %v", err), formatStr)
			return ExitInternal
		}
		defer opened.Close()
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
		Format:      formatStr,
		Stdin:       stdin,
		Stdout:      stdout,
		Stderr:      stderr,
		DB:          db,
		Passthrough: reg.opts.PassthroughStdout,
		Quiet:       globalQuiet || subQuiet,
	}
	if _, err := reg.do(ctx); err != nil {
		printError(stderr, err, formatStr)
		return exitCodeOf(err)
	}
	return ExitOK
}

// renderHelp invokes the verb's Help hook if set, otherwise prints a
// minimal stub. Either way it returns ExitOK; --help is never an error.
func renderHelp(help func(io.Writer), stdout io.Writer) int {
	if help != nil {
		help(stdout)
		return ExitOK
	}
	fmt.Fprintln(stdout, "no help available")
	return ExitOK
}
