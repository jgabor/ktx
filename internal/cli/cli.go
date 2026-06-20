// Package cli implements ktx's command tree: global flag parsing, subcommand
// dispatch, the error envelope, and the help/version surfaces.
//
// Task 1 wires the dispatch and the `ktx db path` path-resolution contract
// (Decision 8). All other subcommands (run, write, read, get, list, init,
// doctor) are stubbed and exit 1 with a usage error so the surface is
// honest about what is not yet built.
//
// Global flags (Decision 4 as superseded by Decision 8 — --store dropped):
//
//	--format string   text|json (default json)
//	--quiet, -q       suppress run summary
//	--help, -h        show help
//	--version, -v     show version
//
// Exit codes (Decision 4):
//
//	0 = ok, 1 = usage, 2 = not-found, 3 = conflict, 4 = internal
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

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
var commands = []command{
	{name: "run", description: "wrap an agent invocation and record the session", run: runRun},
	{name: "write", description: "persist a tool_call record from sidecar input", run: runWrite},
	{name: "read", description: "read a raw record by ID", run: runRead},
	{name: "get", description: "query a record by ID (formatted)", run: runGet},
	{name: "list", description: "list sessions (formatted)", run: runList},
	{name: "init", description: "install a runtime adapter", run: runInit},
	{name: "db", description: "database operations (path, prune)", run: runDB},
	{name: "prices", description: "price cache operations (update)", run: runPrices},
	{name: "doctor", description: "check ktx health", run: runDoctor},
}

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
	cmd, ok := commandIndex[name]
	if !ok {
		printError(stderr, usageError("unknown command %q; run 'ktx --help' for the command list", name), opts.Format)
		return ExitUsage
	}
	return cmd.run(rest[1:], opts.Options)
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

// printHelp writes the top-level help, listing every subcommand.
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
	for _, c := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx <command> --help' for command-specific help.")
}
