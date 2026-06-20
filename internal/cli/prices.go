// `ktx prices` — the price-cache namespace (Decision 11 / plan Task 2).
//
// The price cache at $XDG_DATA_HOME/ktx/prices.json is normally managed
// by the write path (PersistToolCall's deriveCost helper fetches on
// cold-cache miss and writes the file atomically). `ktx prices update`
// is the manual refresh hook for cases the write path cannot handle:
//
//   - the cache is missing a model the user knows should be there
//     (model was added to models.dev after the cache was written);
//   - the cache is corrupt or stale and the user wants a clean
//     refetch without writing a tool_call to trigger it;
//   - the cache TTL policy (deferred per D11 reasoning) is eventually
//     driven by a future `ktx prices refresh` cron; the manual hook
//     is the PoC substitute.
//
// The command's exit-code contract differs from the write path's
// soft-fail contract: the user explicitly invoked `prices update`,
// so a fetch failure is a hard error (ExitInternal = 4, plan Task 2
// AC8). The write path's soft-fail (cost stays NULL, no error
// returned) is the right behavior for an agent-driven tool_call
// because the agent is not waiting on a manual decision; the manual
// hook's hard-fail is the right behavior here because the user IS
// waiting on the refresh and a silent "nothing happened" would be a
// bad UX.
//
// Subcommands (extensible; v1 ships only `update`):
//
//	ktx prices update    fetch from https://models.dev/api.json and
//	                     atomically rewrite the cache file
//	ktx prices path      print the cache file path (precedence:
//	                     KTX_PRICES env > $XDG_DATA_HOME/ktx/prices.json)
//	ktx prices show      print the cache contents (debug; deferred
//	                     to a future cycle if needed)
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jgabor/ktx/internal/store"
)

// runPrices dispatches the `ktx prices` namespace.
//
//	ktx prices                  -> ktx prices update (default; matches
//	                              the explicit-update semantic — the
//	                              namespace is most often used to
//	                              force a refresh)
//	ktx prices update           -> fetch from models.dev and atomically
//	                              rewrite the cache file
//	ktx prices path             -> print the cache file path
//
// Future subcommands (ktx prices show, ktx prices lookup) are out of
// scope for the M3 plan and would land in a future cycle. The
// namespace shape is reserved by the D2 design rule (namespaces for
// managed resources; v1 ships only the manual refresh hook).
func runPrices(args []string, opts Options) int {
	return runPricesWith(args, opts, os.Stdout, os.Stderr)
}

func runPricesWith(args []string, opts Options, stdout, stderr io.Writer) int {
	var showHelp bool
	fs := flag.NewFlagSet("ktx prices", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		printError(stderr, usageError("ktx prices: %v", err), opts.Format)
		return ExitUsage
	}
	if showHelp {
		printPricesHelp(stdout)
		return ExitOK
	}

	rest := fs.Args()
	// Default subcommand is `update`. The namespace exists primarily
	// to host the manual refresh hook; defaulting to update means
	// `ktx prices` and `ktx prices update` are interchangeable, which
	// matches the D2 "no flag-only intent" pattern (D2 subcommands,
	// not flags).
	sub := "update"
	if len(rest) > 0 {
		sub = rest[0]
		rest = rest[1:]
	}

	switch sub {
	case "update":
		return pricesUpdate(opts, stdout, stderr)
	case "path":
		return pricesPath(opts, stdout, stderr)
	default:
		printError(stderr, usageError("ktx prices: unknown subcommand %q; run 'ktx prices --help'", sub), opts.Format)
		return ExitUsage
	}
}

// pricesUpdate forces a refetch from https://models.dev/api.json and
// atomically rewrites the cache file at $XDG_DATA_HOME/ktx/prices.json
// (plan Task 2 AC8). The fetch failure contract is hard error (exit 4)
// because the user explicitly requested the refresh — a silent
// no-op would defeat the purpose of the manual hook.
//
// Output contract: JSON default with --format text for humans.
//
//	{"refreshed": true, "providers": N, "models": M, "path": "..."}
//
// The `providers` and `models` counts are the size of the just-written
// cache; they let the caller verify the refresh produced a populated
// file without re-reading the cache.
func pricesUpdate(opts Options, stdout, stderr io.Writer) int {
	path, err := store.PricesPath()
	if err != nil {
		printError(stderr, internalError("ktx prices update: resolve cache path: %v", err), opts.Format)
		return ExitInternal
	}

	// Build a fresh Prices bound to the default path. We bypass
	// LoadPrices() because the manual refresh must refetch regardless
	// of cache state — a stale or corrupt cache is exactly what the
	// user is trying to fix, and re-using the singleton would re-load
	// the stale data on a second invocation. Resetting the singleton
	// is unnecessary here because RefreshAndWrite replaces the
	// in-memory copy and writes the on-disk file atomically; the
	// next LoadPrices() call would see the new file on disk.
	p := store.NewPrices(path)
	if err := p.RefreshAndWrite(); err != nil {
		// Hard error: the user asked for the refresh and got a
		// failure. ExitInternal (4) maps to the ktx internal-error
		// envelope; a network error is a 4-class failure here
		// because the user explicitly chose to depend on the
		// network (AC8: "a fetch failure on the update command is
		// a hard error").
		printError(stderr, internalError("ktx prices update: %v", err), opts.Format)
		return ExitInternal
	}

	providers, models := p.Size()
	switch opts.Format {
	case FormatText:
		fmt.Fprintf(stdout, "ktx prices update — refreshed\n")
		fmt.Fprintf(stdout, "path:      %s\n", path)
		fmt.Fprintf(stdout, "providers: %d\n", providers)
		fmt.Fprintf(stdout, "models:    %d\n", models)
	default:
		// JSON default (Decision 4): machine-parseable, pipe-friendly.
		out := map[string]any{
			"refreshed": true,
			"providers": providers,
			"models":    models,
			"path":      path,
		}
		if err := json.NewEncoder(stdout).Encode(out); err != nil {
			printError(stderr, internalError("ktx prices update: write JSON: %v", err), opts.Format)
			return ExitInternal
		}
	}
	return ExitOK
}

// pricesPath prints the resolved cache file path. Mirrors
// `ktx db path` for the database file — useful for scripts that
// want to inspect or back up the cache.
func pricesPath(opts Options, stdout, stderr io.Writer) int {
	path, err := store.PricesPath()
	if err != nil {
		printError(stderr, internalError("ktx prices path: resolve cache path: %v", err), opts.Format)
		return ExitInternal
	}
	fmt.Fprintln(stdout, path)
	return ExitOK
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
	fmt.Fprintln(w, "(stale cache, missing model, corrupt file).")
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
