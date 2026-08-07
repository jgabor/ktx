// Package evidence consolidates eligible retry persistence so that source
// insertion/comparison, session roll-up, compressed-artifact insertion/reuse,
// and the returned output cannot diverge under sequential or concurrent
// delivery.
//
// The persistence contract is split into two phases that a caller drives in
// order around its own pipeline execution:
//
//   - PersistSource: persists the source row and session roll-up. For an
//     eligible source this is insert-or-compare (one row, one roll-up
//     increment, conflict preserves first evidence). For a fresh source this
//     is ordinary distinct insertion.
//   - PersistArtifact: persists the compressed artifact and resolves the
//     authoritative output. For an eligible source this is insert-or-reuse
//     (at most one artifact; once present, the first persisted artifact is
//     returned even when the pipeline configuration changed). For a fresh
//     source this is ordinary distinct insertion.
//
// Eligible sources (correlation.Source.Eligible):
//   - Authoritative source equality is runtime, tool, input evidence under
//     strict canonical JSON comparison, and output bytes.
//   - The session roll-up increments only on the first source insertion.
//   - All other metadata is first-wins: changed cost, tokens, timestamp,
//     exit code, or KTX session association neither conflicts nor enriches.
//   - At most one compressed artifact is retained per source. Once present,
//     exact retries return the first persisted artifact even when the current
//     pipeline configuration would recompose a different result. A retry that
//     reaches the artifact phase before any artifact exists may return source
//     output; later retries return the persisted artifact.
//   - Changed authoritative evidence conflicts before compression and
//     preserves the first row unchanged.
//
// Fresh sources (correlation.Source.Fresh):
//   - Ordinary distinct insertion for both source and compressed rows. No
//     deduplication guarantee applies; redelivery produces a distinct
//     observation.
//
// The convenience Persist function calls PersistSource then PersistArtifact
// for callers (e.g. tests) that do not execute a pipeline between the two
// phases.
package evidence

import (
	"errors"
	"fmt"

	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// Result is the authoritative outcome of the artifact phase for one
// tool-call delivery.
type Result struct {
	// SourceID is the persisted source identity.
	SourceID string

	// Output is the authoritative output to return to the agent. For an
	// eligible exact retry this is the first persisted compressed artifact
	// when one exists, or the source output while none exists. For a fresh
	// delivery it is the freshly-computed artifact or the source output.
	Output string

	// CompressedID is the authoritative compressed-artifact id, or "" when
	// no compressed artifact exists for this source.
	CompressedID string

	// ArtifactPipeline is the authoritative compressed artifact's retained
	// pipeline provenance, or "" when no compressed artifact exists. Exact
	// retries receive the selected first artifact's value, never the discarded
	// current candidate's value.
	ArtifactPipeline string

	// Transformed reports whether Output is a compressed artifact rather
	// than the raw source output.
	Transformed bool

	// Inserted reports whether the artifact phase inserted a new compressed
	// row (as opposed to reusing an existing artifact). For a fresh
	// delivery this is true whenever a compressed candidate was supplied.
	Inserted bool
}

// PersistSource persists the source row and session roll-up for one
// tool-call delivery. The caller resolves identity via correlation.Resolve
// and sets tc.ID to src.ID before calling this function.
//
// For an eligible source this is insert-or-compare: one source row and one
// roll-up increment under sequential and concurrent delivery. A conflict
// (changed authoritative evidence) returns an error wrapping
// db.ErrToolCallConflict and preserves the first row unchanged; the caller
// should abort before running the pipeline. For a fresh source this is
// ordinary distinct insertion (always inserts, always rolls up).
//
// The returned inserted flag reports whether this call inserted the source
// row; PersistArtifact uses it to decide whether a no-transform delivery
// should look up a prior artifact.
func PersistSource(database *dbpkg.DB, src correlation.Source, tc *dbpkg.ToolCall) (inserted bool, err error) {
	if database == nil {
		return false, fmt.Errorf("evidence: nil database")
	}
	if src.Eligible {
		return database.PersistToolCallOnce(tc)
	}
	return false, database.PersistToolCall(tc)
}

// PersistArtifact persists the compressed artifact and resolves the
// authoritative output for one tool-call delivery. sourceInserted is the
// value returned by PersistSource for the same delivery.
//
// The compressed candidate, when non-nil, must carry the freshly-computed
// (post-footer-gate) output and content hash; PersistArtifact sets its
// SourceID to tc.ID and generates a compressed id when one is not already
// supplied.
//
// For an eligible source: when a compressed candidate is supplied, at most one
// artifact is retained and the returned output is the first persisted artifact
// rather than a losing candidate. When the candidate is nil (passthrough or
// failure-gate skip) and an artifact already exists, that artifact is returned.
// A concurrent retry that checks before the first artifact is persisted may
// return source output; later retries observe the authoritative artifact.
//
// For a fresh source: the compressed candidate is always inserted (ordinary
// distinct observation) and the returned output is the candidate's output.
func PersistArtifact(database *dbpkg.DB, src correlation.Source, tc *dbpkg.ToolCall, sourceInserted bool, compressed *dbpkg.Compressed) (Result, error) {
	if database == nil {
		return Result{}, fmt.Errorf("evidence: nil database")
	}

	// Copy the candidate so filling in SourceID / a generated id does not
	// mutate the caller's struct (the caller may share it across goroutines).
	var cand *dbpkg.Compressed
	if compressed != nil {
		cand = &dbpkg.Compressed{}
		*cand = *compressed
		cand.SourceID = tc.ID
		if cand.ID == "" {
			id, err := dbpkg.NewID()
			if err != nil {
				return Result{}, fmt.Errorf("evidence: generate compressed id: %w", err)
			}
			cand.ID = id
		}
	}

	if src.Eligible {
		return persistArtifactEligible(database, tc, sourceInserted, cand)
	}
	return persistArtifactFresh(database, tc, cand)
}

func persistArtifactEligible(database *dbpkg.DB, tc *dbpkg.ToolCall, sourceInserted bool, compressed *dbpkg.Compressed) (Result, error) {
	if compressed != nil {
		stored, inserted, err := database.InsertCompressedOnce(compressed)
		if err != nil {
			return Result{}, err
		}
		return Result{
			SourceID:         tc.ID,
			Output:           stored.Output,
			CompressedID:     stored.ID,
			ArtifactPipeline: stored.Pipeline,
			Transformed:      true,
			Inserted:         inserted,
		}, nil
	}

	// No compressed candidate (passthrough or failure-gate skip for this
	// delivery). When the source already existed, a prior delivery may have
	// persisted an artifact that the current pipeline config would not
	// reproduce; look it up so the retry returns the authoritative output.
	if !sourceInserted {
		existing, err := database.GetCompressedBySource(tc.ID)
		if err != nil {
			if errors.Is(err, dbpkg.ErrNotFound) {
				// No prior artifact: both the first delivery and this
				// retry were passthrough.
				return Result{SourceID: tc.ID, Output: tc.Output, Transformed: false}, nil
			}
			return Result{}, err
		}
		return Result{
			SourceID:         tc.ID,
			Output:           existing.Output,
			CompressedID:     existing.ID,
			ArtifactPipeline: existing.Pipeline,
			Transformed:      true,
		}, nil
	}

	// First delivery, no transform.
	return Result{SourceID: tc.ID, Output: tc.Output, Transformed: false}, nil
}

func persistArtifactFresh(database *dbpkg.DB, tc *dbpkg.ToolCall, compressed *dbpkg.Compressed) (Result, error) {
	if compressed != nil {
		if err := database.InsertCompressed(compressed); err != nil {
			return Result{}, err
		}
		return Result{
			SourceID:         tc.ID,
			Output:           compressed.Output,
			CompressedID:     compressed.ID,
			ArtifactPipeline: compressed.Pipeline,
			Transformed:      true,
			Inserted:         true,
		}, nil
	}
	return Result{SourceID: tc.ID, Output: tc.Output, Transformed: false}, nil
}

// Persist is the convenience entry point that calls PersistSource then
// PersistArtifact for callers that do not execute a pipeline between the two
// phases. PersistSource runs first; if it returns a conflict error Persist
// returns immediately without calling PersistArtifact.
func Persist(database *dbpkg.DB, src correlation.Source, tc *dbpkg.ToolCall, compressed *dbpkg.Compressed) (Result, error) {
	inserted, err := PersistSource(database, src, tc)
	if err != nil {
		return Result{}, err
	}
	return PersistArtifact(database, src, tc, inserted, compressed)
}
