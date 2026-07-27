// `ktx prices` — the price-cache namespace.
//
// The price cache at $XDG_DATA_HOME/ktx/prices.json is normally managed
// by the write path (PersistToolCall's deriveCost helper fetches on
// cold-cache miss and writes the file atomically). `ktx prices update`
// is the manual refresh command for cases the write path cannot handle:
//
//   - the cache is missing a model the user knows should be there
//     (model was added to models.dev after the cache was written);
//   - the cache is corrupt or stale and the user wants a clean
//     refetch without writing a tool_call to trigger it.
//
// The command's exit-code contract differs from the write path's
// soft-fail contract: the user explicitly invoked `prices update`,
// so a fetch failure is a hard error (ExitInternal = 4).
// The write path's soft-fail (cost stays NULL, no error
// returned) is the right behavior for an agent-driven tool_call
// because the agent is not waiting on a manual decision; the manual
// hook's hard-fail is the right behavior here because the user IS
// waiting on the refresh and a silent "nothing happened" would be a
// bad UX.
//
// Subcommands:
//
//	ktx prices update    fetch from https://models.dev/api.json and
//	                     atomically rewrite the cache file
//	ktx prices path      print the cache file path (precedence:
//	                     KTX_PRICES env > $XDG_DATA_HOME/ktx/prices.json)
//
// The command does not open the ktx database. Refresh failures are hard
// errors because the user explicitly requested a network operation.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runPricesWith executes `ktx prices` with injected output streams.
// stdout/stderr are injected so the manual-refresh flow can be
// exercised without touching os.Stdout. The body parses the
// subcommand's flags and dispatches on the subcommand name
// (update -> pricesUpdateLogic; path -> pricesPathLogic).
func runPricesWith(args []string, opts Options, stdout, stderr io.Writer) int {
	_, err := pricesLogic(args, opts.Format, stdout, stderr)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// pricesLogic routes price-cache subcommands.
func pricesLogic(args []string, format string, stdout, stderr io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx prices", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx prices: %v", err)
	}
	if showHelp {
		printPricesHelp(stdout)
		return nil, nil
	}

	rest := fs.Args()
	// Default subcommand is `update`. The namespace exists primarily
	// to host the manual refresh hook; defaulting to update means
	// `ktx prices` and `ktx prices update` are interchangeable, which
	// matches the "no flag-only intent" pattern (subcommands,
	// not flags).
	sub := "update"
	if len(rest) > 0 {
		sub = rest[0]
	}

	switch sub {
	case "update":
		return pricesUpdateLogic(format, stdout, stderr)
	case "path":
		return pricesPathLogic(stdout)
	default:
		return nil, usageError("ktx prices: unknown subcommand %q; run 'ktx prices --help'", sub)
	}
}

// pricesUpdateLogic forces a refetch from https://models.dev/api.json
// and atomically rewrites the cache file at
// $XDG_DATA_HOME/ktx/prices.json. The fetch
// failure contract is hard error (exit 4) because the user
// explicitly requested the refresh — a silent no-op would defeat
// the purpose of the manual hook.
//
// Output contract: JSON default with --format text for humans.
//
//	{"refreshed": true, "providers": N, "models": M, "path": "..."}
//
// The `providers` and `models` counts are the size of the just-written
// cache; they let the caller verify the refresh produced a populated
// file without re-reading the cache.
func pricesUpdateLogic(format string, stdout, stderr io.Writer) (any, error) {
	path, err := dbpkg.PricesPath()
	if err != nil {
		return nil, internalError("ktx prices update: resolve cache path: %v", err)
	}

	// Use a fresh cache instance so update always fetches, regardless of the
	// process-wide cache state.
	p := dbpkg.NewPrices(path)
	if err := p.RefreshAndWrite(); err != nil {
		// Hard error: the user asked for the refresh and got a
		// failure. ExitInternal (4) maps to the ktx internal-error
		// envelope; a network error is a 4-class failure here
		// because the user explicitly chose to depend on the
		// network ("a fetch failure on the update command is
		// a hard error").
		return nil, internalError("ktx prices update: %v", err)
	}

	providers, models := p.Size()
	switch format {
	case FormatText:
		fmt.Fprintf(stdout, "ktx prices update — refreshed\n")
		fmt.Fprintf(stdout, "path:      %s\n", path)
		fmt.Fprintf(stdout, "providers: %d\n", providers)
		fmt.Fprintf(stdout, "models:    %d\n", models)
	default:
		// JSON default: machine-parseable, pipe-friendly.
		out := map[string]any{
			"refreshed": true,
			"providers": providers,
			"models":    models,
			"path":      path,
		}
		if err := json.NewEncoder(stdout).Encode(out); err != nil {
			return nil, internalError("ktx prices update: write JSON: %v", err)
		}
	}
	return nil, nil
}

// pricesPathLogic prints the resolved cache file path. Mirrors
// `ktx db path` for the database file — useful for scripts that
// want to inspect or back up the cache.
func pricesPathLogic(stdout io.Writer) (any, error) {
	path, err := dbpkg.PricesPath()
	if err != nil {
		return nil, internalError("ktx prices path: resolve cache path: %v", err)
	}
	fmt.Fprintln(stdout, path)
	return nil, nil
}

// pricesCommandFunc applies the resolved output format to price-cache
// operations. Price commands do not use the ktx database.
func pricesCommandFunc(ctx *DispatchContext) (any, error) {
	return pricesLogic(ctx.Args, ctx.Format, ctx.Stdout, ctx.Stderr)
}

// init registers `ktx prices` with the dispatch module. After init
// runs, the verb is reachable through runWith.
func init() {
	Register("prices", RegisterOptions{
		Description:   "price cache operations (update)",
		FormatDefault: FormatJSON,
		Help:          printPricesHelp,
	}, pricesCommandFunc)
}

// printPricesHelp writes the `ktx prices --help` text. Mirrors the
// help style of the other ktx namespaces.
func printPricesHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx prices — price cache operations")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx prices <subcommand>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  update    fetch from https://models.dev/api.json and atomically")
	fmt.Fprintln(w, "            rewrite the cache file (hard error on fetch failure)")
	fmt.Fprintln(w, "  path      print the cache file path")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Cache file: $XDG_DATA_HOME/ktx/prices.json (XDG base directory")
	fmt.Fprintln(w, "spec; falls back to ~/.local/share/ktx/prices.json when")
	fmt.Fprintln(w, "XDG_DATA_HOME is unset).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The price cache is the local source of truth for per-token")
	fmt.Fprintln(w, "pricing. ktx fetches from models.dev on cold-cache miss during")
	fmt.Fprintln(w, "tool_call persistence; `ktx prices update` is the manual")
	fmt.Fprintln(w, "refresh hook for the cases the write path cannot handle")
	fmt.Fprintln(w, "(stale cache, missing model, missing cache rates, corrupt file).")
	fmt.Fprintln(w, "A warm cache is never refetched implicitly; run `ktx prices update`")
	fmt.Fprintln(w, "when estimated API cost is NULL because pricing is incomplete.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  cache refreshed successfully")
	fmt.Fprintln(w, "  1  usage error (invalid flag, unexpected argument)")
	fmt.Fprintln(w, "  4  fetch failure (network error, non-2xx response, malformed JSON)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format: json (default), text (--format text).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
