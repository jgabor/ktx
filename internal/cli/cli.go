// Package cli implements ktx's command tree: global flag parsing, subcommand
// dispatch, the error envelope, and the help/version surfaces.
//
// The dispatch and the `ktx db path` path-resolution contract
// live here. All other subcommands (run, write, read, get, list, init,
// doctor) are stubbed and exit 1 with a usage error so the surface is
// honest about what is not yet built.
//
// Global flags (--store dropped):
//
//	--format string   text|json (default json)
//	--quiet, -q       suppress run summary
//	--help, -h        show help
//	--version, -v     show version
//
// Exit codes:
//
//	0 = ok, 1 = usage, 2 = not-found, 3 = conflict, 4 = internal
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/jgabor/ktx/internal/version"
)

// Output formats.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// Options carries the parsed global flags to subcommands.
type Options struct {
	Format string
	Quiet  bool
	// RecordStream is set by `ktx run --record-stream` (opt-in stream
	// capture for the wrapper path). It is a verb-local flag threaded
	// through Options so executeRun can branch on it without growing
	// its parameter list. Zero value (false) preserves the
	// transparent-proxy default (ktx run does not persist the stream).
	RecordStream bool
}

// command describes a top-level ktx verb. run executes the command with the
// args left after global flag parsing and the parsed Options; it returns the
// process exit code.
type command struct {
	name        string
	description string
	run         func(args []string, opts Options) int
}

// commands is the dispatch table in the order shown by `ktx --help`.
//
// `ktx read` and `ktx write` were removed from this
// table and re-registered through dispatch.Register (see read.go's
// init() and write.go's init()). They are still routed by runWith,
// because the dispatch registry is consulted before the legacy
// commandIndex.
//
// `ktx get`, `ktx list`, `ktx run`, and `ktx bench`
// were also removed from this table and re-registered through
// dispatch.Register (see get.go's init(), list.go's init(),
// run.go's init(), and bench.go's init()).
//
// `ktx init`, `ktx db`, `ktx prices`, and
// `ktx doctor` were the final four verbs to migrate. They are now
// re-registered through dispatch.Register (see init.go's init(),
// db.go's init(), prices.go's init(), and doctor.go's init()). The
// legacy commands slice is now empty — every subcommand lives in the
// dispatch registry. runWith still consults commandIndex (now empty)
// as a final fall-through; a verb that lives in both tables would
// have the registry win, but no current verb does.
var commands = []command{}

// commandIndex maps command names to their handler for O(1) dispatch.
var commandIndex = func() map[string]command {
	m := make(map[string]command, len(commands))
	for _, c := range commands {
		m[c.name] = c
	}
	return m
}()

// Run is the ktx entrypoint. It parses global flags, dispatches to the
// matched subcommand, and returns the process exit code.
//
// args is os.Args[1:] (without the program name) so the dispatch is
// testable without touching os.Args directly.
func Run(args []string) int {
	return runWith(args, os.Stdout, os.Stderr)
}

// runWith is the testable core of Run. stdout/stderr are injected so
// acceptance checks can capture output without subprocess plumbing.
func runWith(args []string, stdout, stderr io.Writer) int {
	opts, rest, err := parseGlobalFlags(args)
	if err != nil {
		// parseGlobalFlags wraps non-help flag errors as usage errors and
		// passes flag.ErrHelp through unchanged.
		if isFlagErr(err) {
			// Defensive: flag.ErrHelp is returned only when -h/--help are
			// not explicitly defined. They are defined above, so this is
			// not normally reached; if it ever is, show help and exit 0.
			printHelp(stdout)
			return ExitOK
		}
		printError(stderr, err, FormatJSON)
		return ExitUsage
	}

	if opts.showHelp {
		printHelp(stdout)
		return ExitOK
	}
	if opts.showVersion {
		fmt.Fprintln(stdout, version.Version)
		return ExitOK
	}

	if len(rest) == 0 {
		// No subcommand: show help. Friendlier than erroring; matches
		// docker/kubectl convention.
		printHelp(stdout)
		return ExitOK
	}

	name := rest[0]
	verbArgs := rest[1:]

	// New dispatch registry wins: the legacy verbs are migrated onto
	// dispatch.Register; the legacy commandIndex
	// below covers any not yet migrated. Verbs registered via dispatch.Register
	// (demo verbs, plus anything new added after this plan)
	// are picked up here. If a verb lives in both tables the registry
	// wins — the legacy entry is dead code the moment a verb migrates.
	//
	// opts.Quiet is forwarded to the dispatch path so a global
	// `--quiet` flag (e.g. `ktx --quiet run <agent>`) propagates
	// through DispatchContext.Quiet, matching the legacy commandIndex
	// surface that already passes opts.Quiet to runXWith.
	if _, registered := dispatchRegistry[name]; registered {
		return runDispatch(name, verbArgs, opts.Quiet, stdout, stderr)
	}

	cmd, ok := commandIndex[name]
	if !ok {
		printError(stderr, usageError("unknown command %q; run 'ktx --help' for the command list", name), opts.Format)
		return ExitUsage
	}
	return cmd.run(verbArgs, opts.Options)
}

// parsedFlags holds the global flags plus the help/version switches. help
// and version are consumed by the dispatcher, not passed down as Options.
type parsedFlags struct {
	Options
	showHelp    bool
	showVersion bool
}

// parseGlobalFlags parses the global flags from the front of args and
// returns the parsed values, the remaining args (subcommand + its args),
// and any error. The stdlib flag package stops at the first non-flag
// argument, so `ktx --format json db path` leaves ["db", "path"] in rest.
func parseGlobalFlags(args []string) (parsedFlags, []string, error) {
	var p parsedFlags
	fs := flag.NewFlagSet("ktx", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.StringVar(&p.Format, "format", FormatJSON, "output format: text|json")
	fs.BoolVar(&p.Quiet, "quiet", false, "suppress run summary")
	fs.BoolVar(&p.Quiet, "q", false, "suppress run summary (shorthand)")
	fs.BoolVar(&p.showHelp, "help", false, "show help")
	fs.BoolVar(&p.showHelp, "h", false, "show help (shorthand)")
	fs.BoolVar(&p.showVersion, "version", false, "show version")
	fs.BoolVar(&p.showVersion, "v", false, "show version (shorthand)")

	if err := fs.Parse(args); err != nil {
		// ErrHelp is pass-through so the dispatcher can act on it; every
		// other flag error (unknown flag, bad value) is a usage error.
		if err == flag.ErrHelp {
			return p, nil, err
		}
		return p, nil, usageError("%v", err)
	}

	if p.Format != FormatJSON && p.Format != FormatText {
		return p, nil, usageError("invalid --format %q: want %q or %q", p.Format, FormatJSON, FormatText)
	}

	return p, fs.Args(), nil
}

// isFlagErr reports whether err originated from the flag package (e.g.
// flag.ErrHelp), which the flag set already reported via its Usage hook
// before we suppressed output.
func isFlagErr(err error) bool {
	return err == flag.ErrHelp
}

// printHelp writes the top-level help, listing every subcommand. After
// the legacy commands slice was emptied, the help list is sourced
// from the dispatch registry (RegisteredVerbs). Verbs that registered
// with a non-empty Description are listed; verbs without a description
// are still callable but omitted from the help (rare; legacy behaviour
// was to always list every verb).
func printHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx — make every coding agent measurable and cheaper")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx [global flags] <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --format string   output format: text|json (default \"json\")")
	fmt.Fprintln(w, "  --quiet, -q       suppress run summary")
	fmt.Fprintln(w, "  --help, -h        show help")
	fmt.Fprintln(w, "  --version, -v     show version")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	verbs := RegisteredVerbs()
	// Sort by name for deterministic help output. The dispatch
	// registry is a map; the order must be stable across runs.
	sort.Slice(verbs, func(i, j int) bool { return verbs[i].Name < verbs[j].Name })
	for _, v := range verbs {
		if v.Description == "" {
			continue
		}
		fmt.Fprintf(w, "  %-8s %s\n", v.Name, v.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx <command> --help' for command-specific help.")
}
