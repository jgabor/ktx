// Known-value binding protects command operands from common format/help scans.
// Namespace ordering permits owned options around subcommands without guessing
// child option arity. The run wrapper owns its separate passthrough boundary.
package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

func printOwnedArgumentHelp(w io.Writer) {
	fmt.Fprintln(w, "Owned flags may appear before or after the subcommand or runtime.")
	fmt.Fprintln(w, "Value flags consume the next token even when it starts with --; -- ends option handling.")
	fmt.Fprintln(w, "Use --help=false to disable help. Remaining operands must match the command's syntax.")
	fmt.Fprintln(w, "Local --format overrides global --format; the last local value wins (json or text).")
	fmt.Fprintln(w)
}

// joinOwnedValues binds only known value-taking flags before independent
// command/common scans. A flag-looking value is still a value; -- ends ownership.
func joinOwnedValues(args []string, names string) ([]string, error) {
	known := strings.Fields(names)
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(result, args[i:]...), nil
		}
		if slices.Contains(known, a) {
			if i+1 == len(args) {
				return nil, fmt.Errorf("%s requires a value", a)
			}
			i++
			a += "=" + args[i]
		}
		result = append(result, a)
	}
	return result, nil
}

// normalizeCommandValues covers the remaining command-owned value contracts.
// It never runs for run: child option arity is not ours to infer.
func normalizeCommandValues(verb string, args []string) ([]string, error) {
	var names string
	switch verb {
	case "init":
	case "db":
		names = "--path --output -output"
	case "stats":
		names = "--limit --window --from --to --collection --ingress --candidate --matrix --receipt -from -to -collection -ingress -candidate -matrix -receipt"
	case "plugin":
		names = "--description"
	case "optimize":
		names = "--config --candidate --model --variant --max-attempts --pricing-snapshot -config -candidate -model -variant -max-attempts -pricing-snapshot"
	case "bench":
		names = "--spec --replay --replay-file --output --population --config --anchor-config --model --variant --task --baseline --candidate --auth --image --experiment-id --campaign-id --repo-root --pricing-snapshot --pricing-sha256 --runtime-config --read-description-treatment --first-arm --estimated-cost-limit --followup --cache-control --warmup --target-valid --rounds --max-attempts --attempt-timeout --termination-grace -m --m -format"
		// Benchmark FlagSets also accept the stdlib single-dash spelling.
		for _, name := range strings.Fields(names) {
			if strings.HasPrefix(name, "--") {
				names += " " + name[1:]
			}
		}
	default:
		return args, nil
	}
	return joinOwnedValues(args, names+" --format")
}

// namespaceArgs keeps the subcommand first, followed by options and operands.
// Values must already be bound. The first -- and everything after it stay put.
func namespaceArgs(args []string) []string {
	var options, operands []string
	for i, arg := range args {
		if arg == "--" {
			operands = append(operands, args[i:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			options = append(options, arg)
		} else {
			operands = append(operands, arg)
		}
	}
	if len(operands) == 0 || operands[0] == "--" {
		return append(options, operands...)
	}
	return append(append([]string{operands[0]}, options...), operands[1:]...)
}

func extractFormatChecked(args []string) (string, []string, error) {
	bound, err := joinOwnedValues(args, "--format")
	if err != nil {
		return "", nil, err
	}
	set := false
	for _, arg := range bound {
		if arg == "--" {
			break
		}
		set = set || strings.HasPrefix(arg, "--format=")
	}
	format, rest := extractFormat(bound)
	if set && format == "" {
		return "", nil, fmt.Errorf("invalid --format %q: want %q or %q", "", FormatJSON, FormatText)
	}
	return format, rest, nil
}

// extractFormat scans args for --format <value> or --format=<value> and
// returns the value plus the args with --format removed. Returns "" when
// no --format is present, so the caller can fall back to the global
// opts.Format.
//
// Both --format value and --format=value are accepted because both forms
// appear in shell pipelines. The short form -f is intentionally NOT
// accepted to keep the command surface narrow.
func extractFormat(args []string) (string, []string) {
	var format string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
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
