// Package correlation is the shared, runtime-neutral contract for native
// tool-call identity.
//
// A native correlation tuple is the (runtime, native session id, native
// tool-call id) triple that recognized agent runtimes expose — OpenCode's
// (input.sessionID, input.callID) from both the hook adapter and the wrapper
// stream, Codex's (session_id, tool_use_id) from the PostToolUse hook, and
// Claude Code's (session_id, tool_use_id) from its PostToolUse hook.
// This package owns that tuple shape, the provenance that vouches a tuple
// arrived through a trusted adapter/schema path, and the resolution of a
// tuple into the persisted KTX source identity.
//
// The contract enforces the identity decision's eligibility rule:
// deterministic identity applies ONLY when a recognized trusted ingress
// vouches for a complete, valid tuple. Eligibility is an internal
// trusted-ingress/schema property set by KTX's own adapter and parser code
// paths — it is never inferred from caller-provided runtime text, so a
// generic flat or sidecar payload that carries runtime:"opencode" cannot
// opt itself into deterministic identity. Missing, malformed, or ineligible
// tuples fall back to a fresh NanoID with no deduplication guarantee.
//
// The KTX SessionID (the wrapper/hook session association) is intentionally
// absent from this package: it is a separate concern that may change while
// the native tuple stays the same. Source identity and session ownership
// remain independently defined.
package correlation

import (
	"fmt"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/identity"
)

// ProvenanceCodexPostToolUse is the trusted provenance label for the Codex
// PostToolUse command hook, whose input schema KTX authors and recognizes.
const ProvenanceCodexPostToolUse = "codex:post-tool-use"

// ProvenanceClaudeCodePostToolUse is the trusted provenance label for the
// Claude Code PostToolUse command hook that `ktx init` generates. The hook
// selects this ingress with `ktx hook --runtime claude-code`, and the payload
// must carry the required native fields.
const ProvenanceClaudeCodePostToolUse = "claude-code:post-tool-use"

// ProvenanceOpenCodeHook is the trusted provenance label for the OpenCode
// plugin adapter hook that `ktx init` generates (tool.execute.after). The
// adapter is trusted because KTX authors the plugin and forwards the native
// input object.
const ProvenanceOpenCodeHook = "opencode:hook"

// ProvenanceOpenCodeWrapper is the trusted provenance label for the
// `ktx run opencode` wrapper parser branch, which reads the native tuple
// from the retained JSON stream (top.sessionID + part.callID).
const ProvenanceOpenCodeWrapper = "opencode:wrapper"

// NativeTuple is the runtime-native correlation triple.
//
// It is deliberately separate from the KTX SessionID. Runtime is the agent
// runtime literal (e.g. "codex", "opencode", "claude-code"). SessionID is the
// runtime's OWN native session identifier (OpenCode input.sessionID, Codex and
// Claude Code session_id) — not the KTX session. CallID is the runtime's OWN
// native tool-call identifier (OpenCode callID, Codex and Claude Code
// tool_use_id). The KTX SessionID is a
// separate association the wrapper or hook resolves independently; it may
// change while the native tuple is unchanged.
type NativeTuple struct {
	Runtime   string
	SessionID string
	CallID    string
}

// Provenance records who vouched for a NativeTuple. It is an internal
// trusted-ingress/schema property set by KTX's own adapter and parser code
// paths — never derived from caller-provided runtime text. Only a recognized
// adapter/schema path calls Trusted; generic flat or sidecar compatibility
// input uses Untrusted.
type Provenance struct {
	trusted bool
	source  string
}

// Trusted marks a tuple as arriving through a recognized KTX adapter or
// parser path whose native schema KTX authors and recognizes (e.g.
// ProvenanceCodexPostToolUse, ProvenanceClaudeCodePostToolUse,
// ProvenanceOpenCodeHook, ProvenanceOpenCodeWrapper). Only trusted code paths call this; source is
// a diagnostic label and is not part of identity.
func Trusted(source string) Provenance {
	return Provenance{trusted: true, source: source}
}

// Untrusted marks a tuple as arriving through generic flat or sidecar
// compatibility input that KTX neither authors nor recognizes. Such input
// cannot opt itself into deterministic identity even when it claims a
// runtime label, because eligibility is provenance-driven, not text-driven.
func Untrusted() Provenance {
	return Provenance{trusted: false, source: "compatibility"}
}

// Trusted reports whether the provenance vouches for a recognized adapter
// schema. Deterministic identity applies only when this is true AND the
// native tuple is complete and valid.
func (p Provenance) Trusted() bool { return p.trusted }

// Source is the diagnostic label naming the recognized ingress path, or
// "compatibility" for untrusted input. It is not part of identity.
func (p Provenance) Source() string { return p.source }

// Source is the resolved persisted identity for one tool-call delivery.
//
// Exactly one of Eligible or Fresh describes the identity:
//
//   - Eligible: the tuple arrived through a trusted adapter/schema provenance
//     AND every field was complete and valid. The ID is derived
//     deterministically from the tuple via the unchanged canonical
//     derivation (domain ktx-tool-call-id-v1, 16 lowercase letters). A later
//     exact retry — through the same trusted path, a different trusted path,
//     another process, or another machine — converges to the same ID.
//   - Fresh: the provenance was untrusted, or a trusted ingress carried a
//     missing, malformed, or invalid tuple. The ID is a fresh
//     cryptographically random NanoID with no deduplication guarantee;
//     redelivery produces a distinct observation.
//
// Persist callers must read Eligible to choose retry semantics: an eligible
// retry reuses the existing source row, a fresh delivery always inserts.
// Derived and fresh IDs share the same 16-lowercase-letter domain, so the ID
// shape alone cannot tell them apart — read Eligible.
type Source struct {
	ID string

	// Eligible is true when the identity is deterministic for the tuple and
	// a later exact retry is expected to converge. Persist accordingly.
	Eligible bool

	// Fresh is true when the identity is a fresh NanoID with no deduplication
	// guarantee. Mutually exclusive with Eligible.
	Fresh bool

	// Reason explains why a Fresh fallback occurred (untrusted provenance,
	// or a trusted ingress with an incomplete/invalid tuple). Empty for an
	// Eligible source.
	Reason string
}

// String renders a Source for diagnostics.
func (s Source) String() string {
	switch {
	case s.Eligible:
		return fmt.Sprintf("eligible source %q", s.ID)
	case s.Fresh:
		return fmt.Sprintf("fresh source %q (%s)", s.ID, s.Reason)
	default:
		return fmt.Sprintf("source %q (unresolved)", s.ID)
	}
}

// Resolve maps a native correlation tuple to its persisted source identity
// for one tool-call delivery.
//
// A trusted AND complete/valid tuple derives its ID deterministically. Every
// other case — untrusted provenance, or a trusted ingress with a missing,
// malformed, or invalid tuple — receives a fresh NanoID with no
// deduplication guarantee. The only error path is NanoID generation failure
// (effectively impossible). Callers that persist must read Source.Eligible
// to choose retry semantics.
//
// For input-evidence equality on retry (canonical JSON comparison of the
// tool/input evidence and output bytes), the evidence identity/equality
// layer applies; resolution here only establishes the tuple-derived ID.
func Resolve(t NativeTuple, p Provenance) (Source, error) {
	if p.trusted {
		if id, err := identity.Derive(t.Runtime, t.SessionID, t.CallID); err == nil {
			return Source{ID: id, Eligible: true}, nil
		}
		// Trusted ingress but the tuple was incomplete or invalid:
		// deterministic derivation rejects it, so fall back to fresh.
		return fresh(reasonIncomplete)
	}
	return fresh(reasonUntrusted)
}

// IsCanonical reports whether id belongs to the persisted KTX identity
// domain (16 lowercase letters). Derived and fresh IDs share this domain, so
// this cannot distinguish an eligible retry from a fresh fallback — read
// Source.Eligible for that distinction.
func IsCanonical(id string) bool {
	return identity.IsCanonical(id)
}

const (
	reasonIncomplete = "trusted ingress with incomplete or invalid native tuple"
	reasonUntrusted  = "untrusted provenance; generic flat or compatibility input cannot self-qualify"
)

func fresh(reason string) (Source, error) {
	id, err := dbpkg.NewID()
	if err != nil {
		return Source{}, fmt.Errorf("correlation: generate fresh identity: %w", err)
	}
	return Source{ID: id, Fresh: true, Reason: reason}, nil
}
