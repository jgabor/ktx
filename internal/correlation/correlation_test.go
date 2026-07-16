// Pure-function coverage of the shared correlation contract includes
// cross-boundary convergence, rejection of untrusted claims, fresh fallback,
// stable derivation, and separation from KTX session ownership.
package correlation

import (
	"strings"
	"sync"
	"testing"

	"github.com/jgabor/ktx/internal/identity"
)

// TestResolve_TrustedCompleteIsDeterministic is the derivation-unchanged
// anchor: a trusted and complete tuple derives the exact ID the unchanged
// canonical derivation produces, the result is canonical, and two calls
// agree. This proves Resolve delegates to identity.Derive without altering
// its bytes or output.
func TestResolve_TrustedCompleteIsDeterministic(t *testing.T) {
	t.Parallel()

	tuple := NativeTuple{Runtime: "codex", SessionID: "session", CallID: "exec-d0a88822-65de-4d16-8c37-df6a0aae9a48"}
	first, err := Resolve(tuple, Trusted(ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !first.Eligible || first.Fresh {
		t.Fatalf("got %+v, want eligible source", first)
	}
	// The identity_test.go golden vector for the same tuple is
	// "yeidpaaatyvvzrfo"; Resolve must reproduce it byte-for-byte.
	if first.ID != "yeidpaaatyvvzrfo" {
		t.Fatalf("ID = %q, want yeidpaaatyvvzrfo", first.ID)
	}
	derived, err := identity.Derive("codex", "session", "exec-d0a88822-65de-4d16-8c37-df6a0aae9a48")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != derived {
		t.Fatalf("Resolve ID %q != identity.Derive %q (derivation changed)", first.ID, derived)
	}
	if !IsCanonical(first.ID) {
		t.Fatalf("ID %q is not canonical", first.ID)
	}

	second, err := Resolve(tuple, Trusted(ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("Resolve is not deterministic: %q vs %q", first.ID, second.ID)
	}
}

// TestResolve_ConvergesAcrossBoundaries proves equivalent verified
// tuples crossing process, machine, hook, and wrapper boundaries produce
// the same source ID. The OpenCode hook adapter exposes
// (input.sessionID, input.callID) and the wrapper parser exposes
// (top.sessionID, part.callID); for one event they are the same native
// tuple. A different trusted provenance source label and a concurrent
// resolver must not change the result.
func TestResolve_ConvergesAcrossBoundaries(t *testing.T) {
	t.Parallel()

	tuple := NativeTuple{Runtime: "opencode", SessionID: "ses-fixture", CallID: "call-fixture"}
	hook, err := Resolve(tuple, Trusted(ProvenanceOpenCodeHook))
	if err != nil {
		t.Fatalf("hook resolve: %v", err)
	}
	wrapper, err := Resolve(tuple, Trusted(ProvenanceOpenCodeWrapper))
	if err != nil {
		t.Fatalf("wrapper resolve: %v", err)
	}
	if !hook.Eligible || !wrapper.Eligible {
		t.Fatalf("want both eligible; got hook=%+v wrapper=%+v", hook, wrapper)
	}
	// Same native tuple crosses the hook and wrapper boundaries → one ID.
	if hook.ID != wrapper.ID {
		t.Fatalf("hook ID %q != wrapper ID %q for the same native tuple", hook.ID, wrapper.ID)
	}

	// Provenance source label is diagnostic, not identity: the same tuple
	// arriving under a different (still trusted) adapter name converges.
	otherLabel, err := Resolve(tuple, Trusted("opencode:future-adapter"))
	if err != nil {
		t.Fatal(err)
	}
	if otherLabel.ID != hook.ID {
		t.Fatalf("provenance source label changed the derived ID: %q vs %q", otherLabel.ID, hook.ID)
	}

	// Concurrency: Resolve is a pure function of the tuple; two resolvers
	// in separate goroutines (independent "processes") converge.
	const goroutines = 16
	ids := make([]string, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			src, err := Resolve(tuple, Trusted(ProvenanceCodexPostToolUse))
			ids[i], errs[i] = src.ID, err
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if ids[i] != hook.ID {
			t.Fatalf("goroutine %d ID %q != %q (not stable across concurrent resolvers)", i, ids[i], hook.ID)
		}
	}

	// Cross-process/machine stability is inherited from identity.Derive's
	// purity and is also exercised by the retained offline verification probe.
	// Here the contract delegates to the same derivation, so the property holds.
	direct, err := identity.Derive(tuple.Runtime, tuple.SessionID, tuple.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if hook.ID != direct {
		t.Fatalf("contract ID %q != identity.Derive %q", hook.ID, direct)
	}
}

// TestResolve_UntrustedCannotSelfQualify proves unknown or
// compatibility ingress that claims a runtime label cannot opt itself into
// deterministic identity. The tuple below carries runtime:"opencode" and
// complete session/call fields — exactly the shape generic flat ingress
// could forge — but provenance is Untrusted, so eligibility is denied even
// though the text claims a recognized runtime.
func TestResolve_UntrustedCannotSelfQualify(t *testing.T) {
	t.Parallel()

	// A flat caller-supplied payload that happens to carry runtime:"opencode"
	// with complete-looking native fields. This is the self-qualify attack.
	flatClaim := NativeTuple{Runtime: "opencode", SessionID: "ses-flat", CallID: "call-flat"}
	src, err := Resolve(flatClaim, Untrusted())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src.Eligible {
		t.Fatalf("untrusted flat ingress self-qualified: %+v", src)
	}
	if !src.Fresh {
		t.Fatal("untrusted ingress did not fall back to fresh")
	}
	if !IsCanonical(src.ID) {
		t.Fatalf("fresh fallback ID %q is not canonical", src.ID)
	}
	// The fresh ID must NOT equal the deterministic derivation for the same
	// tuple text: provenance, not runtime text, gates eligibility.
	derived, err := identity.Derive(flatClaim.Runtime, flatClaim.SessionID, flatClaim.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if src.ID == derived {
		t.Fatalf("untrusted ingress produced the deterministic ID %q; want a fresh random ID", src.ID)
	}
	if !strings.Contains(src.Reason, "untrusted") {
		t.Fatalf("reason %q does not explain untrusted fallback", src.Reason)
	}
}

// TestResolve_FreshFallback proves missing, malformed, or ineligible
// tuple fields on an accepted event receive a fresh NanoID with no
// deduplication guarantee. Every fresh fallback is canonical, distinct from
// the deterministic derivation, and distinct across repeated fallbacks
// (redelivery of a fresh event is a distinct observation).
func TestResolve_FreshFallback(t *testing.T) {
	t.Parallel()

	trustedBad := []struct {
		name  string
		tuple NativeTuple
	}{
		{"empty runtime", NativeTuple{SessionID: "s", CallID: "c"}},
		{"whitespace session", NativeTuple{Runtime: "codex", SessionID: " \t\n", CallID: "c"}},
		{"invalid call utf8", NativeTuple{Runtime: "codex", SessionID: "s", CallID: string([]byte{0xff})}},
		{"empty session", NativeTuple{Runtime: "codex", CallID: "c"}},
		{"empty call", NativeTuple{Runtime: "codex", SessionID: "s"}},
	}
	for _, tc := range trustedBad {
		tc := tc
		t.Run("trusted/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src, err := Resolve(tc.tuple, Trusted(ProvenanceCodexPostToolUse))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if src.Eligible {
				t.Fatalf("trusted ingress with %s self-qualified: %+v", tc.name, src)
			}
			if !src.Fresh {
				t.Fatalf("want fresh fallback; got %+v", src)
			}
			if !IsCanonical(src.ID) {
				t.Fatalf("fresh fallback ID %q is not canonical", src.ID)
			}
			if !strings.Contains(src.Reason, "incomplete or invalid") {
				t.Fatalf("reason %q does not explain trusted-incomplete fallback", src.Reason)
			}
		})
	}

	t.Run("untrusted complete tuple falls back", func(t *testing.T) {
		t.Parallel()
		src, err := Resolve(NativeTuple{Runtime: "opencode", SessionID: "s", CallID: "c"}, Untrusted())
		if err != nil {
			t.Fatal(err)
		}
		if src.Eligible {
			t.Fatalf("untrusted self-qualified: %+v", src)
		}
		if !src.Fresh {
			t.Fatal("want fresh fallback")
		}
	})

	t.Run("redelivery of a fresh event stays distinct (no dedup)", func(t *testing.T) {
		t.Parallel()
		first, err := Resolve(NativeTuple{Runtime: "codex", SessionID: " \t", CallID: "c"}, Trusted(ProvenanceCodexPostToolUse))
		if err != nil {
			t.Fatal(err)
		}
		second, err := Resolve(NativeTuple{Runtime: "codex", SessionID: " \t", CallID: "c"}, Trusted(ProvenanceCodexPostToolUse))
		if err != nil {
			t.Fatal(err)
		}
		if first.ID == second.ID {
			t.Fatalf("fresh fallback converged on retry (%q); want distinct observations", first.ID)
		}
	})
}

// TestResolve_SessionIndependence proves KTX session association may
// change while native correlation is unchanged, and source identity and
// session ownership remain separately defined. The contract has no KTX
// SessionID input or output — NativeTuple.SessionID is the RUNTIME-native
// session, not the KTX session — so a native identity is invariant to KTX
// session association by construction.
func TestResolve_SessionIndependence(t *testing.T) {
	t.Parallel()

	tuple := NativeTuple{Runtime: "codex", SessionID: "codex-session", CallID: "runtime-id"}

	// Two separate "KTX sessions" deliver the same native tuple. Because
	// Resolve has no KTX-session input, both resolve to the same eligible
	// source identity — session ownership is bound later, independently.
	first, err := Resolve(tuple, Trusted(ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(tuple, Trusted(ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Eligible {
		t.Fatalf("want eligible; got %+v", first)
	}
	if first.ID != second.ID {
		t.Fatalf("native identity changed across KTX sessions: %q vs %q", first.ID, second.ID)
	}

	// Fresh fallbacks are also session-independent: two sessions redelivering
	// the same incomplete tuple produce two distinct fresh observations, not
	// a single session-owned identity — there is no dedup to own.
	bad := NativeTuple{Runtime: "codex", SessionID: " ", CallID: "c"}
	freshA, err := Resolve(bad, Trusted(ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatal(err)
	}
	freshB, err := Resolve(bad, Trusted(ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatal(err)
	}
	if freshA.ID == freshB.ID {
		t.Fatalf("fresh fallback converged across sessions (%q); identity must be per-delivery", freshA.ID)
	}

	// Structural separation: Source carries only identity + eligibility, no
	// KTX session field. The runtime-native session lives on NativeTuple,
	// distinct from any KTX association resolved elsewhere.
	if first.Reason != "" {
		t.Fatalf("eligible source must have empty reason; got %q", first.Reason)
	}
	// NativeTuple.SessionID is the runtime's own session, not KTX's: changing
	// it changes the identity (it IS an identity input), which is exactly
	// why it must not be conflated with KTX SessionID.
	other, err := Resolve(NativeTuple{Runtime: "codex", SessionID: "codex-session-2", CallID: "runtime-id"},
		Trusted(ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatal("changing the native session did not change identity; native session is NOT an identity input")
	}
}

// TestResolve_CodexVectorStable asserts the Codex identity vector exercised
// by the existing hook tests resolves to the same canonical ID through the
// shared contract, anchoring the contract to the Codex identity behavior
// that must remain unchanged.
func TestResolve_CodexVectorStable(t *testing.T) {
	t.Parallel()

	tuple := NativeTuple{Runtime: "codex", SessionID: "codex-test-session", CallID: "exec-runtime-id"}
	src, err := Resolve(tuple, Trusted(ProvenanceCodexPostToolUse))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !src.Eligible {
		t.Fatalf("want eligible; got %+v", src)
	}
	// Matches identity.Derive directly (the same function the current
	// Codex hook path at internal/cli/hook.go:225 calls unchanged).
	derived, err := identity.Derive("codex", "codex-test-session", "exec-runtime-id")
	if err != nil {
		t.Fatal(err)
	}
	if src.ID != derived {
		t.Fatalf("contract ID %q != identity.Derive %q", src.ID, derived)
	}
	if !IsCanonical(src.ID) {
		t.Fatalf("ID %q is not canonical", src.ID)
	}
}
