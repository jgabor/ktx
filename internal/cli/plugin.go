// `ktx plugin` — manage compression plugins.
//
// A structured editor over [[plugin]] blocks in ktx.toml. The user
// (or agent) never authors TOML by hand — `ktx plugin add/remove`
// reads, modifies, and writes the config atomically; `ktx plugin list`
// returns a unified JSON view of built-in stages and user plugins.
//
// Subcommands:
//
//	ktx plugin add <name> <command> [--description <text>]
//	ktx plugin list [--json]
//	ktx plugin remove <name>
//
// Choice: structured editor over ktx.toml (single source of truth).
// Block fields: name (required), command (required),
// description (optional). Unified list returns builtins
// (builtin=true, removable=false) + user plugins (builtin=false,
// removable=true). Precedence: RegisterExternalBuilder last-writer-wins
// so a user plugin named 'head' overrides the built-in head.
//
// DatabaseNone: plugin management operates on ktx.toml only; no
// database access required.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/durablefile"
	"github.com/jgabor/ktx/internal/pipeline/stages"
)

// pluginCommandFunc is the dispatch entry point. It routes to the
// subcommand handler based on the first positional arg.
func pluginCommandFunc(ctx *DispatchContext) (any, error) {
	args := ctx.Args
	if len(args) == 0 {
		return nil, usageError("ktx plugin: missing subcommand; run 'ktx plugin --help'")
	}
	sub := args[0]
	subArgs := args[1:]
	switch sub {
	case "add":
		return pluginAdd(subArgs, ctx.Stdout)
	case "list":
		if len(subArgs) > 1 || (len(subArgs) == 1 && subArgs[0] != "--json") {
			return nil, usageError("ktx plugin list: unknown argument %q", subArgs[0])
		}
		return pluginList(ctx.Stdout)
	case "remove":
		return pluginRemove(subArgs, ctx.Stdout)
	default:
		return nil, usageError("ktx plugin: unknown subcommand %q; run 'ktx plugin --help'", sub)
	}
}

// pluginAdd appends a new [[plugin]] block to ktx.toml. When no
// ktx.toml exists, a new one is created at the user-level config
// path ($XDG_CONFIG_HOME/ktx/ktx.toml). When one exists, the block
// is appended preserving existing content.
// A duplicate name is rejected with a conflict error.
func pluginAdd(args []string, stdout io.Writer) (any, error) {
	description, rest := extractDescription(args)
	if len(rest) < 2 {
		return nil, usageError("ktx plugin add: missing required arguments: <name> <command>")
	}
	if len(rest) > 2 {
		return nil, usageError("ktx plugin add: unexpected argument %q", rest[2])
	}
	name := rest[0]
	command := rest[1]

	// Check for duplicate name in existing plugins.
	cfg, err := config.Load()
	if err != nil {
		return nil, configPluginError("add", err)
	}
	if cfg != nil {
		for _, p := range cfg.Plugins {
			if p.Name == name {
				return nil, conflictError("ktx plugin add: plugin %q already exists", name)
			}
		}
	}

	// Resolve the target file: existing config, or create at user-level path.
	path, exists := config.ConfigPath()
	if !exists {
		var err error
		path, err = config.UserConfigPath()
		if err != nil {
			return nil, internalError("ktx plugin add: resolve config path: %v", err)
		}
	}

	// Read existing content (empty when creating new file).
	content := ""
	if exists {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, internalError("ktx plugin add: read ktx.toml: %v", err)
		}
		content = string(data)
	}

	// Append the new [[plugin]] block.
	block := buildPluginBlock(name, command, description)
	content = appendPluginText(content, block)

	if err := durablefile.Write(path, []byte(content), 0o644); err != nil {
		return nil, internalError("ktx plugin add: write ktx.toml: %v", err)
	}

	envelope := map[string]any{
		"plugin": map[string]any{
			"name":        name,
			"command":     command,
			"description": description,
		},
	}
	_ = json.NewEncoder(stdout).Encode(envelope)
	return nil, nil
}

// pluginList returns a unified JSON view of all valid stage names:
// built-in stages (builtin=true, removable=false) followed by user
// plugins (builtin=false, removable=true) in document order.
func pluginList(stdout io.Writer) (any, error) {
	type pluginEntry struct {
		Name        string `json:"name"`
		Builtin     bool   `json:"builtin"`
		Removable   bool   `json:"removable"`
		Command     string `json:"command,omitempty"`
		Description string `json:"description,omitempty"`
	}

	var plugins []pluginEntry

	// Built-in stages first, in registration order.
	for _, name := range stages.BuiltinStageNames() {
		plugins = append(plugins, pluginEntry{
			Name:      name,
			Builtin:   true,
			Removable: false,
		})
	}

	// User plugins from ktx.toml.
	cfg, err := config.Load()
	if err != nil {
		return nil, configPluginError("list", err)
	}
	if cfg != nil {
		for _, p := range cfg.Plugins {
			plugins = append(plugins, pluginEntry{
				Name:        p.Name,
				Builtin:     false,
				Removable:   true,
				Command:     p.Command,
				Description: p.Description,
			})
		}
	}

	envelope := map[string]any{
		"plugins": plugins,
	}
	_ = json.NewEncoder(stdout).Encode(envelope)
	return nil, nil
}

// pluginRemove deletes a [[plugin]] block from ktx.toml by name.
// Removing a built-in stage name without a user override returns a
// self-correcting usage error listing removable plugins. Removing a
// user override of a built-in name restores the built-in.
func pluginRemove(args []string, stdout io.Writer) (any, error) {
	if len(args) < 1 {
		return nil, usageError("ktx plugin remove: missing required argument: <name>")
	}
	if len(args) > 1 {
		return nil, usageError("ktx plugin remove: unexpected argument %q", args[1])
	}
	name := args[0]

	// Check if the name is a built-in stage.
	isBuiltin := false
	for _, b := range stages.BuiltinStageNames() {
		if b == name {
			isBuiltin = true
			break
		}
	}

	// Load config to find user plugins.
	cfg, err := config.Load()
	if err != nil {
		return nil, configPluginError("remove", err)
	}

	// Find the user plugin with matching name.
	found := -1
	if cfg != nil {
		for i, p := range cfg.Plugins {
			if p.Name == name {
				found = i
				break
			}
		}
	}

	if found < 0 {
		// No user plugin with this name.
		if isBuiltin {
			// Self-correcting error: list removable plugins.
			var removable []string
			if cfg != nil {
				for _, p := range cfg.Plugins {
					removable = append(removable, p.Name)
				}
			}
			removableStr := "(none)"
			if len(removable) > 0 {
				removableStr = strings.Join(removable, ", ")
			}
			return nil, &Error{
				Code:    CodeUsage,
				Message: fmt.Sprintf(`cannot remove built-in stage %q; removable plugins: %s`, name, removableStr),
				Extra: map[string]any{
					"valid_examples": []string{fmt.Sprintf("ktx plugin remove <%s>", name)},
				},
			}
		}
		return nil, notFoundError("ktx plugin remove: plugin %q not found", name)
	}

	removed := cfg.Plugins[found]

	// Remove the block from the TOML file.
	path, exists := config.ConfigPath()
	if !exists {
		return nil, notFoundError("ktx plugin remove: ktx.toml not found")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, internalError("ktx plugin remove: read ktx.toml: %v", err)
	}

	newContent, ok := removePluginBlock(string(content), name)
	if !ok {
		return nil, notFoundError("ktx plugin remove: plugin %q not found in ktx.toml", name)
	}

	if err := durablefile.Write(path, []byte(newContent), 0o644); err != nil {
		return nil, internalError("ktx plugin remove: write ktx.toml: %v", err)
	}

	envelope := map[string]any{
		"removed": map[string]any{
			"name":        removed.Name,
			"command":     removed.Command,
			"description": removed.Description,
		},
	}
	_ = json.NewEncoder(stdout).Encode(envelope)
	return nil, nil
}

func configPluginError(operation string, err error) error {
	return usageError("ktx plugin %s: load configuration: %v", operation, err)
}

// extractDescription scans args for --description <value> or
// --description=<value> and returns the description and remaining
// args. Mirrors extractFormat's scanning approach so --description
// can appear anywhere in the arg list.
func extractDescription(args []string) (description string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--description" && i+1 < len(args) {
			description = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(a, "--description=") {
			description = strings.TrimPrefix(a, "--description=")
			continue
		}
		rest = append(rest, a)
	}
	return description, rest
}

// buildPluginBlock renders a [[plugin]] block as TOML text. Values are
// escaped for TOML basic-string safety (backslash and double-quote).
func buildPluginBlock(name, command, description string) string {
	var b strings.Builder
	// Ensure a blank line before the block when appending to non-empty
	// content.
	b.WriteString("\n[[plugin]]\n")
	fmt.Fprintf(&b, "name = %s\n", tomlQuote(name))
	fmt.Fprintf(&b, "command = %s\n", tomlQuote(command))
	if description != "" {
		fmt.Fprintf(&b, "description = %s\n", tomlQuote(description))
	}
	return b.String()
}

// appendPluginText appends a TOML block to existing content, handling
// the blank-line separator. When content is empty, the leading
// newline from buildPluginBlock is trimmed so the file starts clean.
func appendPluginText(content, block string) string {
	if content == "" {
		return strings.TrimPrefix(block, "\n")
	}
	// Ensure existing content ends with a newline before appending.
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + block
}

// tomlQuote returns s as a TOML basic string (double-quoted, escaped).
func tomlQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// removePluginBlock removes the [[plugin]] block whose name field
// matches the given name from the TOML text. Returns the new content
// and whether a block was removed. The removal is text-based to
// preserve existing formatting (comments, key order, multi-line
// arrays) in unaffected sections.
func removePluginBlock(content, name string) (string, bool) {
	lines := strings.Split(content, "\n")
	var out []string
	i := 0
	removed := false
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "[[plugin]]" {
			// Start of a plugin block — collect until next section
			// header or EOF.
			blockStart := i
			blockLines := []string{line}
			i++
			for i < len(lines) {
				l := strings.TrimSpace(lines[i])
				if strings.HasPrefix(l, "[[") || (strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]")) {
					break
				}
				blockLines = append(blockLines, lines[i])
				i++
			}
			blockText := strings.Join(blockLines, "\n")
			if matchesPluginName(blockText, name) {
				removed = true
				// Skip the block; trailing blank line cleanup.
				if blockStart > 0 && len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
					out = out[:len(out)-1]
				}
				continue
			}
			out = append(out, blockLines...)
			continue
		}
		out = append(out, line)
		i++
	}
	return strings.Join(out, "\n"), removed
}

// matchesPluginName checks whether a [[plugin]] block's text contains
// a name = "target" field matching the given name.
func matchesPluginName(blockText, name string) bool {
	for _, line := range strings.Split(blockText, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "name") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, "\"'")
		if val == name {
			return true
		}
	}
	return false
}

// init registers the `ktx plugin` verb with the dispatch module.
func init() {
	Register("plugin", RegisterOptions{
		Description:   "manage compression plugins",
		FormatDefault: FormatJSON,
		Help:          printPluginHelp,
	}, pluginCommandFunc)
}

// printPluginHelp writes the `ktx plugin` help.
func printPluginHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx plugin — manage compression plugins")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx plugin <subcommand> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  add <name> <command> [--description <text>]  register a new plugin")
	fmt.Fprintln(w, "  list [--json]                                 list all plugins (builtins + user)")
	fmt.Fprintln(w, "  remove <name>                                 remove a user plugin")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Plugins are stored as [[plugin]] blocks in ktx.toml. A plugin extends")
	fmt.Fprintln(w, "or overrides the compression pipeline by running an external command")
	fmt.Fprintln(w, "(stdin=input bytes, stdout=output bytes).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  ktx plugin add mycat /bin/cat --description \"passthrough\"")
	fmt.Fprintln(w, "  ktx plugin list --json")
	fmt.Fprintln(w, "  ktx plugin remove mycat")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx config show  inspect the active pipeline configuration")
	fmt.Fprintln(w, "  ktx init          install a runtime adapter (not plugin management)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
