package cacheevidence

import sequenceevidence "github.com/jgabor/ktx/internal/cacheevidence/sequence"

type (
	CoverageState      = sequenceevidence.CoverageState
	BoundaryKind       = sequenceevidence.BoundaryKind
	SequenceProvenance = sequenceevidence.SequenceProvenance
	SequenceFact       = sequenceevidence.SequenceFact
	CoverageFact       = sequenceevidence.CoverageFact
	PairCoverage       = sequenceevidence.PairCoverage
)

const (
	CoverageCovered       = sequenceevidence.CoverageCovered
	CoverageUnavailable   = sequenceevidence.CoverageUnavailable
	CoverageContradictory = sequenceevidence.CoverageContradictory
	BoundaryRetry         = sequenceevidence.BoundaryRetry
	BoundaryCompaction    = sequenceevidence.BoundaryCompaction
	BoundaryInterruption  = sequenceevidence.BoundaryInterruption
	BoundaryRestart       = sequenceevidence.BoundaryRestart
)

var (
	AssessAttemptBoundaries    = sequenceevidence.AssessAttemptBoundaries
	AssessPair                 = sequenceevidence.AssessPair
	DecodeOpenCodeSequenceFact = sequenceevidence.DecodeOpenCodeSequenceFact
)
