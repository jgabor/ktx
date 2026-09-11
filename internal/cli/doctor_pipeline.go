package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/pipeline/stages"
)

func doctorPipelineChecks() []doctorCheck {
	path, cfg, exists, err := config.Inspect()
	if err != nil {
		state := "inaccessible"
		var pe *config.ParseError
		if errors.As(err, &pe) {
			state = "invalid"
		}
		return []doctorCheck{{
			Name: "pipeline_config", Status: doctorStatusError,
			Detail: fmt.Sprintf("%s: %q: %v", state, path, err),
		}}
	}
	state := "readable and valid"
	if !exists {
		state = "absent; using passthrough defaults"
	}
	checks := []doctorCheck{{
		Name: "pipeline_config", Status: doctorStatusOK,
		Detail: fmt.Sprintf("%s: %q; static configuration only, not observed traffic or plugin viability", state, path),
	}}
	for i, route := range cfg.Routes {
		checks = append(checks, doctorCheck{
			Name: "pipeline_route", Status: doctorStatusOK,
			Detail: fmt.Sprintf("route %d (first match wins): tool=%q command_prefix=%q subcommand=%q exit_status=%q -> profile=%q stages=%s",
				i+1, route.Tool, route.CommandPrefix, route.Subcommand, route.ExitStatus, route.Profile,
				doctorStageList(cfg.Profiles[route.Profile].Stages)),
		})
	}
	checks = append(checks, doctorCheck{
		Name: "pipeline_fallback", Status: doctorStatusOK,
		Detail: "no route matches -> profile=\"default\" stages=" + doctorStageList(cfg.PipelineDefault),
	})

	reg := pipeline.NewRegistry()
	stages.RegisterBuiltins(reg)
	builtins := make(map[string]bool)
	for _, name := range stages.BuiltinStageNames() {
		builtins[name] = true
	}
	winners := make(map[string]int)
	for i, plugin := range cfg.Plugins {
		winners[plugin.Name] = i + 1
		reg.RegisterExternalBuilder(plugin.Name, plugin.Command)
	}
	seen := make(map[string]int)
	for i, plugin := range cfg.Plugins {
		status := doctorStatusOK
		detail := fmt.Sprintf("plugin %d name=%q; effective winner: plugin %d (last writer wins); command not executed or probed", i+1, plugin.Name, winners[plugin.Name])
		if builtins[plugin.Name] {
			status = doctorStatusWarning
			detail += "; shadows built-in"
		}
		if previous := seen[plugin.Name]; previous != 0 {
			status = doctorStatusWarning
			detail += fmt.Sprintf("; duplicate name of plugin %d", previous)
		}
		checks = append(checks, doctorCheck{Name: "pipeline_plugin", Status: status, Detail: detail})
		seen[plugin.Name] = i + 1
	}
	profiles := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	for _, name := range profiles {
		profile := cfg.Profiles[name]
		checks = append(checks, doctorCheck{
			Name: "pipeline_profile", Status: doctorStatusOK,
			Detail: fmt.Sprintf("profile=%q stages=%s", name, doctorStageList(profile.Stages)),
		})
		for i, token := range profile.Stages {
			// Lookup constructs trusted built-ins or an externalStage; it never
			// calls Apply. Register plugins first so overrides own argument handling.
			_, found, err := reg.Lookup(token)
			status := doctorStatusOK
			detail := fmt.Sprintf("profile=%q stage %d token=%q", name, i+1, token)
			base, _, _ := strings.Cut(token, ":")
			switch {
			case !found:
				status = doctorStatusWarning
				detail += "; unknown stage; skipped at runtime"
			case err != nil:
				status = doctorStatusWarning
				detail += fmt.Sprintf("; invalid arguments: %v; skipped at runtime", err)
			case winners[base] != 0:
				detail += fmt.Sprintf("; resolves to plugin %d (not executed or probed)", winners[base])
			default:
				detail += "; resolves to built-in"
			}
			checks = append(checks, doctorCheck{Name: "pipeline_stage", Status: status, Detail: detail})
		}
	}
	return checks
}

func doctorStageList(tokens []string) string {
	if len(tokens) == 0 {
		return "[] (passthrough)"
	}
	return fmt.Sprintf("%q", tokens)
}
