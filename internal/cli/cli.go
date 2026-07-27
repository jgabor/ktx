// Package cli implements ktx's command tree: global flag parsing, subcommand
// dispatch, the error envelope, and the help/version surfaces.
//
// Commands register with the shared dispatcher in their implementation files.
//
// Global flags (--store dropped):
//
//	--format string   text|json (default json)
//	--quiet, -q       suppress run summary
//	--expect-runtime-sha256 string  prove exact runtime for ambient DB opens
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
	Format                string
	Quiet                 bool
	ExpectedRuntimeSHA256 string
}

// Run is the ktx entrypoint. It parses global flags, dispatches to the
// matched subcommand, and returns the process exit code.
//
// args is os.Args[1:] (without the program name) so the dispatch is
// testable without touching os.Args directly.
func Run(args []string) int {
	return runWith(args, os.Stdout, os.Stderr)
}

// runWith executes the CLI with injected output streams.
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

	// Forward global quiet state through the shared dispatch context.
	if _, registered := dispatchRegistry[name]; registered {
		return runDispatchWithOptions(name, verbArgs, opts.Options, stdout, stderr)
	}
	printError(stderr, usageError("unknown command %q; run 'ktx --help' for the command list", name), opts.Format)
	return ExitUsage
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
	fs.StringVar(&p.ExpectedRuntimeSHA256, "expect-runtime-sha256", "", "expected SHA-256 of the running executable for ambient database access")
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
	if err := validateRuntimeDigest(p.ExpectedRuntimeSHA256); err != nil {
		return p, nil, err
	}

	return p, fs.Args(), nil
}

// isFlagErr reports whether err originated from the flag package (e.g.
// flag.ErrHelp), which the flag set already reported via its Usage hook
// before we suppressed output.
func isFlagErr(err error) bool {
	return err == flag.ErrHelp
}

// printHelp writes the top-level help from the command registry.
func printHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx — make every coding agent measurable and cheaper")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx [global flags] <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --format string   output format: text|json (default \"json\")")
	fmt.Fprintln(w, "  --quiet, -q       suppress run summary")
	fmt.Fprintln(w, "  --expect-runtime-sha256 string")
	fmt.Fprintln(w, "                    prove the exact running executable before ambient database access")
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
