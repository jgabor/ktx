package cli

import (
	"github.com/jgabor/ktx/internal/binarycap"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

const runtimeProofCorrection = "run `ktx binary-capabilities`, verify the selected executable, then retry with `--expect-runtime-sha256 <executable_sha256>`"

func authorizeDatabaseTarget(target dbpkg.Target, expectedSHA256 string) error {
	if !target.RequiresRuntimeProof() {
		return nil
	}
	if expectedSHA256 == "" {
		failure := conflictError("ambient database access requires proof of the exact running executable; %s", runtimeProofCorrection)
		failure.Extra = map[string]any{
			"failure_class": "runtime-proof-required",
			"correction":    runtimeProofCorrection,
		}
		return failure
	}
	identity, err := currentBinaryCapabilities()
	if err != nil {
		failure := internalError("runtime identity is unverifiable before ambient database access: %v; %s", err, runtimeProofCorrection)
		failure.Extra = map[string]any{
			"failure_class": "runtime-proof-unverifiable",
			"correction":    runtimeProofCorrection,
		}
		return failure
	}
	if identity.ExecutableSHA256 != expectedSHA256 {
		failure := conflictError("runtime digest mismatch before ambient database access; %s", runtimeProofCorrection)
		failure.Extra = map[string]any{
			"failure_class":   "runtime-proof-mismatch",
			"expected_sha256": expectedSHA256,
			"actual_sha256":   identity.ExecutableSHA256,
			"executable_path": identity.ExecutablePath,
			"correction":      runtimeProofCorrection,
		}
		return failure
	}
	return nil
}

func validateRuntimeDigest(value string) error {
	if value != "" && !binarycap.ValidDigest(value) {
		return usageError("invalid --expect-runtime-sha256 %q: want exactly 64 lowercase hexadecimal characters", value)
	}
	return nil
}
