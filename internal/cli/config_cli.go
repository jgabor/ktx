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
// JSON output includes profiles, routes, and effective logging settings.
//
// DatabaseNone: config inspection operates on ktx.toml only.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/logging"
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
// configuration. JSON preserves the profiles and routes fields and adds logs;
// text renders the profile table followed by effective logging settings.
func configShow(format string, stdout io.Writer) (any, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, usageError("ktx config show: %v", err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	if cfg.LogsError != nil {
		return nil, usageError("ktx config show: %v", cfg.LogsError)
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

	logs, err := effectiveLogConfig(cfg.Logs)
	if err != nil {
		return nil, usageError("ktx config show: %v", err)
	}
	if format == FormatText {
		return configShowText(stdout, profiles, logs)
	}

	// JSON (default)
	return configShowJSON(stdout, profiles, cfg.Routes, logs)
}

type effectiveLogs struct {
	Enabled     bool   `json:"enabled"`
	Level       string `json:"level"`
	Destination string `json:"destination"`
}

func effectiveLogConfig(configured *config.LogConfig) (effectiveLogs, error) {
	if configured == nil {
		return effectiveLogs{Level: "off"}, nil
	}
	logs := effectiveLogs{Level: configured.Level}
	if configured.Level == "off" {
		return logs, nil
	}
	destination, err := logging.ResolvePath(configured.Path)
	if err != nil {
		return effectiveLogs{}, err
	}
	logs.Enabled = true
	logs.Destination = destination
	return logs, nil
}

// configShowJSON renders the profiles and routes as a JSON envelope.
// Empty stages arrays are emitted as [] (not null) so agents can
// unmarshal without nil-checking. Empty routes is emitted as [].
func configShowJSON(stdout io.Writer, profiles map[string]config.Profile, routes []config.RouteRule, logs effectiveLogs) (any, error) {
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
		"logs":     logs,
	}
	_ = json.NewEncoder(stdout).Encode(envelope)
	return nil, nil
}

// configShowText renders a human-readable table: one row per profile
// with name and comma-separated stages list.
func configShowText(stdout io.Writer, profiles map[string]config.Profile, logs effectiveLogs) (any, error) {
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
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "LOGGING  LEVEL  DESTINATION")
	destination := logs.Destination
	if destination == "" {
		destination = "(none)"
	}
	fmt.Fprintf(stdout, "%-8t %-6s %s\n", logs.Enabled, logs.Level, destination)
	return nil, nil
}

// init registers the `ktx config` verb.
func init() {
	Register("config", RegisterOptions{
		Description:   "inspect active pipeline configuration",
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
	fmt.Fprintln(w, "Shows pipeline stages and the effective file-logging configuration.")
	fmt.Fprintln(w, "The 'default' profile is always present (the passthrough path when no")
	fmt.Fprintln(w, "ktx.toml exists).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Format:")
	fmt.Fprintln(w, "  json   {profiles:{default:{stages:[...]}, ...}, routes:[...], logs:{...}}")
	fmt.Fprintln(w, "  text   profile rows followed by logging enabled, level, and destination")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Logging:")
	fmt.Fprintln(w, "  No [logs] section means off. A present section defaults to level error.")
	fmt.Fprintln(w, "  Levels are inclusive: debug, info, warn, error, or off.")
	fmt.Fprintln(w, "  The default destination is $XDG_STATE_HOME/ktx/ktx.log, with")
	fmt.Fprintln(w, "  ~/.local/state/ktx/ktx.log as the home fallback. Relative paths resolve")
	fmt.Fprintln(w, "  beneath the state directory; absolute paths are explicit overrides.")
	fmt.Fprintln(w, "  Inspection resolves the destination but does not create it.")
	fmt.Fprintln(w, "  Records are typed JSONL, at most 16 KiB each. The private active file")
	fmt.Fprintln(w, "  is limited to 5 MiB and rotation retains one previous file.")
	fmt.Fprintln(w, "  File logging is best effort and independent of stderr diagnostics.")
	fmt.Fprintln(w, "  Paths and hashes in records are sensitive retained identifiers.")
	fmt.Fprintln(w, "  Coordination makes one attempt with a nominal 25 ms target, no retries.")
	fmt.Fprintln(w, "  Contention drops only the file event. A 50 ms wall-clock")
	fmt.Fprintln(w, "  safety ceiling allows scheduler tolerance; it is not a hard real-time guarantee.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Example:")
	fmt.Fprintln(w, "  ktx config show --format json")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx plugin list  show all valid stage names (builtins + user plugins)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
