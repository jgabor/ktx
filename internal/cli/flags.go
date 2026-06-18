// Subcommand-level flag helpers for the query commands (`ktx get`,
// `ktx list`, future `ktx bench`).
//
// The global --format flag is parsed by the dispatcher (cli.go) from the
// front of args, before the subcommand verb. The stdlib flag.Parse stops
// at the first non-flag argument, so a subcommand flag appearing AFTER a
// positional (e.g. `ktx get <id> --format text`) is not seen by the
// subcommand's flag.NewFlagSet either.
//
// To accept --format in either position we pre-scan args for it and pull
// the value out, then hand the remaining args to the stdlib flag set for
// --help. The subcommand's --format value (when set) takes precedence
// over the global opts.Format; the subcommand's default is seeded from
// opts.Format, so a subcommand flag overrides the global one when both
// are set.
//
// This helper is intentionally narrow. It only handles --format, not
// arbitrary flags, because the subcommand's positional contract is
// different per command and a generic flag-filter would invite silent
// bugs (passing agent flags through `ktx get`).
package cli

import "strings"

// extractFormat scans args for --format <value> or --format=<value> and
// returns the value plus the args with --format removed. Returns "" when
// no --format is present, so the caller can fall back to the global
// opts.Format.
//
// Both --format value and --format=value are accepted because both forms
// appear in shell pipelines. The short form -f is intentionally NOT
// accepted: keeping the alphabet narrow avoids confusion with future
// per-command flags.
func extractFormat(args []string) (string, []string) {
	var format string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--format" && i+1 < len(args) {
			format = args[i+1]
			i++ // skip the value
			continue
		}
		if strings.HasPrefix(a, "--format=") {
			format = strings.TrimPrefix(a, "--format=")
			continue
		}
		rest = append(rest, a)
	}
	return format, rest
}
