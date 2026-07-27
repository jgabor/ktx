package reuse

import "github.com/jgabor/ktx/internal/cache/reuse/sequence"

type (
	CoverageState      = sequence.CoverageState
	BoundaryKind       = sequence.BoundaryKind
	SequenceProvenance = sequence.SequenceProvenance
	SequenceFact       = sequence.SequenceFact
	CoverageFact       = sequence.CoverageFact
	PairCoverage       = sequence.PairCoverage
)

const (
	CoverageCovered       = sequence.CoverageCovered
	CoverageUnavailable   = sequence.CoverageUnavailable
	CoverageContradictory = sequence.CoverageContradictory
	BoundaryRetry         = sequence.BoundaryRetry
	BoundaryCompaction    = sequence.BoundaryCompaction
	BoundaryInterruption  = sequence.BoundaryInterruption
	BoundaryRestart       = sequence.BoundaryRestart
)

var (
	AssessAttemptBoundaries    = sequence.AssessAttemptBoundaries
	AssessPair                 = sequence.AssessPair
	DecodeOpenCodeSequenceFact = sequence.DecodeOpenCodeSequenceFact
)
