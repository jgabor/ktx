package reuse

import "github.com/jgabor/ktx/internal/cache/reuse/sequence"

const (
	RequiredRuntime         = sequence.RequiredRuntime
	RequiredRuntimeVersion  = sequence.RequiredRuntimeVersion
	RequiredContractVersion = sequence.RequiredContractVersion
	FieldMeasured           = sequence.FieldMeasured
	FieldUnknown            = sequence.FieldUnknown
	FieldMalformed          = sequence.FieldMalformed
)

type (
	FieldState         = sequence.FieldState
	IntegerField       = sequence.IntegerField
	TextField          = sequence.TextField
	RuntimeContract    = sequence.RuntimeContract
	EstimateStep       = sequence.EstimateStep
	EstimatePairInput  = sequence.EstimatePairInput
	EstimatePairResult = sequence.EstimatePairResult
)

var (
	IneligibilityReasons = sequence.IneligibilityReasons
	EstimatePair         = sequence.EstimatePair
)
