package reuse

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateAnthropicProposal(p Proposal) error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", p.SchemaVersion)
	}
	if len(p.Paths) != 2 {
		return fmt.Errorf("anthropic proposal requires exactly one direct and one OpenCode path")
	}
	clean := filepath.Clean(p.ArtifactDestination)
	if clean == "." || filepath.IsAbs(clean) || !strings.HasPrefix(filepath.ToSlash(clean), ".bench-data/cache-evidence/") {
		return fmt.Errorf("artifact destination must be below .bench-data/cache-evidence")
	}
	if len(p.RedactionPolicy) == 0 {
		return fmt.Errorf("redaction policy is required")
	}
	seenDirect, seenRuntime := false, false
	for _, path := range p.Paths {
		if strings.TrimSpace(path.Source) == "" || strings.TrimSpace(path.Version) == "" || len(path.References) == 0 {
			return fmt.Errorf("each path requires exact source, version, and references")
		}
		for _, reference := range path.References {
			if strings.TrimSpace(reference) == "" {
				return fmt.Errorf("evidence references must not be blank")
			}
		}
		if strings.TrimSpace(path.CredentialSource) == "" || strings.TrimSpace(path.CredentialType) == "" {
			return fmt.Errorf("each path requires credential source and type")
		}
		if path.Provider != "anthropic" || path.Model != "claude-sonnet-5" || path.Variant != "" {
			return fmt.Errorf("anthropic evidence requires anthropic/claude-sonnet-5 without a variant")
		}
		switch path.Transport {
		case "direct_anthropic":
			if path.AccessMode != "documented_direct" {
				return fmt.Errorf("direct Anthropic path requires documented direct access mode")
			}
			seenDirect = true
		case "opencode_anthropic":
			if path.AccessMode != "runtime" || path.RuntimeBinary != "opencode" {
				return fmt.Errorf("anthropic OpenCode path requires opencode runtime access mode")
			}
			seenRuntime = true
		default:
			return fmt.Errorf("unsupported Anthropic transport %q", path.Transport)
		}
	}
	if !seenDirect || !seenRuntime {
		return fmt.Errorf("anthropic proposal requires one direct path and one OpenCode path")
	}
	return nil
}
