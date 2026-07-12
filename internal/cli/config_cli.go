// `ktx config` — inspect active pipeline configuration.
//
// Reads-only verb that returns the parsed ktx.toml's pipeline
// configuration. Pair with `ktx plugin list` (which shows the
// registry: what stages CAN run) vs `ktx config show` (which shows
// what stages WILL run per profile). SOLID separation: plugin owns
// CRUD, config owns inspect.
//
// Subcommands:
//
//	ktx config show [--format json|text]
//
// JSON output includes "profiles" (per-profile stage lists) and
// "routes" (route rules mapping tool-call metadata to profiles) keys.
//
// NeedsDB=false: config inspection operates on ktx.toml only.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/jgabor/ktx/internal/config"
)

// configCommandFunc is the dispatch entry point.
func configCommandFunc(ctx *DispatchContext) (any, error) {
	args := ctx.Args
	if len(args) == 0 {
		return nil, usageError("ktx config: missing subcommand; run 'ktx config --help'")
	}
	sub := args[0]
	switch sub {
	case "show":
		return configShow(ctx.Format, ctx.Stdout)
	default:
		return nil, usageError("ktx config: unknown subcommand %q; run 'ktx config --help'", sub)
	}
}

// configShow reads ktx.toml and prints the active pipeline
// configuration. JSON format returns a {profiles:{<name>:{stages:[...]}}, routes:[...]}
// envelope; text format renders a one-row-per-profile table.
func configShow(format string, stdout io.Writer) (any, error) {
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}

	profiles := cfg.Profiles
	if profiles == nil {
		profiles = map[string]config.Profile{}
	}
	// Always include "default" even when the ktx.toml has no
	// [pipeline.default] table — the passthrough path.
	if _, ok := profiles["default"]; !ok {
		profiles["default"] = config.Profile{}
	}

	if format == FormatText {
		return configShowText(stdout, profiles)
	}

	// JSON (default)
	return configShowJSON(stdout, profiles, cfg.Routes)
}

// configShowJSON renders the profiles and routes as a JSON envelope.
// Empty stages arrays are emitted as [] (not null) so agents can
// unmarshal without nil-checking. Empty routes is emitted as [].
func configShowJSON(stdout io.Writer, profiles map[string]config.Profile, routes []config.RouteRule) (any, error) {
	type profileEntry struct {
		Stages []string `json:"stages"`
	}
	out := make(map[string]profileEntry, len(profiles))
	for name, p := range profiles {
		stages := p.Stages
		if stages == nil {
			stages = []string{}
		}
		out[name] = profileEntry{Stages: stages}
	}
	type routeEntry struct {
		Tool          string `json:"tool"`
		CommandPrefix string `json:"command_prefix"`
		ExitStatus    string `json:"exit_status"`
		Profile       string `json:"profile"`
	}
	routeOut := make([]routeEntry, 0, len(routes))
	for _, r := range routes {
		routeOut = append(routeOut, routeEntry{
			Tool:          r.Tool,
			CommandPrefix: r.CommandPrefix,
			ExitStatus:    r.ExitStatus,
			Profile:       r.Profile,
		})
	}
	envelope := map[string]any{
		"profiles": out,
		"routes":   routeOut,
	}
	_ = json.NewEncoder(stdout).Encode(envelope)
	return nil, nil
}

// configShowText renders a human-readable table: one row per profile
// with name and comma-separated stages list.
func configShowText(stdout io.Writer, profiles map[string]config.Profile) (any, error) {
	fmt.Fprintln(stdout, "PROFILE    STAGES")
	// Sorted with "default" first, then alphabetical.
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		if n != "default" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	names = append([]string{"default"}, names...)
	for _, name := range names {
		stages := profiles[name].Stages
		stagesStr := "(none)"
		if len(stages) > 0 {
			stagesStr = ""
			for i, s := range stages {
				if i > 0 {
					stagesStr += ", "
				}
				stagesStr += s
			}
		}
		fmt.Fprintf(stdout, "%-10s %s\n", name, stagesStr)
	}
	return nil, nil
}

// init registers the `ktx config` verb.
func init() {
	Register("config", RegisterOptions{
		Description:   "inspect active pipeline configuration",
		NeedsDB:       false,
		FormatDefault: FormatJSON,
		Help:          printConfigHelp,
	}, configCommandFunc)
}

// printConfigHelp writes the `ktx config` help.
func printConfigHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx config — inspect active pipeline configuration")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx config show [--format json|text]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Shows the pipeline stages configured in ktx.toml, per profile.")
	fmt.Fprintln(w, "The 'default' profile is always present (the passthrough path when no")
	fmt.Fprintln(w, "ktx.toml exists).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Format:")
	fmt.Fprintln(w, "  json   {profiles:{default:{stages:[...]}, ...}, routes:[...]}")
	fmt.Fprintln(w, "  text   one row per profile (name + comma-separated stages)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Example:")
	fmt.Fprintln(w, "  ktx config show --format json")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx plugin list  show all valid stage names (builtins + user plugins)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
