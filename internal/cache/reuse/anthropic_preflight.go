package reuse

import (
	"fmt"
)

func validateAnthropicProposal(p Proposal) error {
	if err := validateCommonProposal(proposalCommon(p)); err != nil {
		return err
	}
	seenDirect, seenRuntime := false, false
	for _, path := range p.Paths {
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
