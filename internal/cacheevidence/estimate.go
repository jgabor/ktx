package cacheevidence

import sequenceevidence "github.com/jgabor/ktx/internal/cacheevidence/sequence"

const (
	RequiredRuntime         = sequenceevidence.RequiredRuntime
	RequiredRuntimeVersion  = sequenceevidence.RequiredRuntimeVersion
	RequiredContractVersion = sequenceevidence.RequiredContractVersion
	FieldMeasured           = sequenceevidence.FieldMeasured
	FieldUnknown            = sequenceevidence.FieldUnknown
	FieldMalformed          = sequenceevidence.FieldMalformed
)

type (
	FieldState         = sequenceevidence.FieldState
	IntegerField       = sequenceevidence.IntegerField
	TextField          = sequenceevidence.TextField
	RuntimeContract    = sequenceevidence.RuntimeContract
	EstimateStep       = sequenceevidence.EstimateStep
	EstimatePairInput  = sequenceevidence.EstimatePairInput
	EstimatePairResult = sequenceevidence.EstimatePairResult
)

var (
	IneligibilityReasons = sequenceevidence.IneligibilityReasons
	EstimatePair         = sequenceevidence.EstimatePair
)
