// Package methodology loads and validates benchmark methodology definitions.
package methodology

import (
	"fmt"
	"strings"

	benchdata "github.com/jgabor/ktx/tools/bench"
)

// Spec is the subset of a methodology needed by the product benchmark runner.
type Spec struct {
	Version            string
	ScoringFunction    string
	TaskIdentifier     string
	TaskPrompt         string
	TaskExpectedOutput string
	ModelID            string
	ModelProvider      string
}

// Load reads an embedded methodology and returns its source path for reporting.
func Load(version string) (*Spec, string, error) {
	data, source, err := benchdata.ReadMethodology(version)
	if err != nil {
		return nil, source, err
	}
	spec, err := Parse(string(data))
	if err != nil {
		return nil, source, fmt.Errorf("parse methodology %q: %w", source, err)
	}
	return spec, source, nil
}

// Parse reads the scalar subset used by the benchmark runner. Block values and
// lists are ignored, so evaluation methodologies can add richer metadata
// without changing the product runner.
func Parse(data string) (*Spec, error) {
	spec := &Spec{}
	var parents []string
	for _, raw := range strings.Split(data, "\n") {
		if idx := strings.Index(raw, "#"); idx >= 0 {
			raw = raw[:idx]
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "- ") || !strings.Contains(trimmed, ":") {
			continue
		}
		depth := leadingSpaces(raw) / 2
		for len(parents) > depth {
			parents = parents[:len(parents)-1]
		}
		key, value, _ := strings.Cut(trimmed, ":")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if value == "" {
			parents = append(parents, key)
			continue
		}
		value = unquote(value)
		parent := strings.Join(parents, ".")
		switch {
		case parent == "" && key == "methodology_version":
			spec.Version = value
		case parent == "inputs.task" && key == "identifier":
			spec.TaskIdentifier = value
		case parent == "inputs.task" && key == "prompt":
			spec.TaskPrompt = value
		case parent == "inputs.task" && key == "expected_output":
			spec.TaskExpectedOutput = value
		case parent == "inputs.model" && key == "id":
			spec.ModelID = value
		case parent == "inputs.model" && key == "provider":
			spec.ModelProvider = value
		case parent == "inputs.scoring" && key == "function":
			spec.ScoringFunction = value
		}
	}

	required := []struct {
		name  string
		value string
	}{
		{"methodology_version", spec.Version},
		{"inputs.scoring.function", spec.ScoringFunction},
		{"inputs.task.identifier", spec.TaskIdentifier},
		{"inputs.task.prompt", spec.TaskPrompt},
		{"inputs.task.expected_output", spec.TaskExpectedOutput},
		{"inputs.model.id", spec.ModelID},
		{"inputs.model.provider", spec.ModelProvider},
	}
	var missing []string
	for _, field := range required {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	return spec, nil
}

func leadingSpaces(value string) int {
	count := 0
	for _, char := range value {
		switch char {
		case ' ':
			count++
		case '\t':
			count += 4
		default:
			return count
		}
	}
	return count
}

func unquote(value string) string {
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
		(value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}
