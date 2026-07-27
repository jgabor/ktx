package cli

import (
	"errors"
	"fmt"

	"github.com/jgabor/ktx/internal/binarycap"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

const runtimeProofCorrection = "run `ktx binary-capabilities`, install or select that verified executable, then retry with `--expect-runtime-sha256 <executable_sha256>` before the command"

func openDatabase(target dbpkg.Target, expectedSHA256 string) (*dbpkg.DB, error) {
	if target.RequiresRuntimeProof() {
		if expectedSHA256 == "" {
			failure := conflictError("ambient database access requires proof of the exact running executable; %s", runtimeProofCorrection)
			failure.Extra = map[string]any{
				"failure_class": "runtime-proof-required",
				"correction":    runtimeProofCorrection,
			}
			return nil, failure
		}
		identity, err := currentBinaryCapabilities()
		if err != nil {
			failure := internalError("runtime identity is unverifiable before ambient database access: %v; %s", err, runtimeProofCorrection)
			failure.Extra = map[string]any{
				"failure_class": "runtime-proof-unverifiable",
				"correction":    runtimeProofCorrection,
			}
			return nil, failure
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
			return nil, failure
		}
	}
	database, err := dbpkg.OpenTarget(target)
	if err == nil {
		return database, nil
	}
	var schemaErr *dbpkg.SchemaTooNewError
	if !errors.As(err, &schemaErr) {
		return nil, err
	}
	identity, identityErr := currentBinaryCapabilities()
	if identityErr != nil {
		failure := internalError("runtime identity is unverifiable after detecting an incompatible database schema: %v; %s", identityErr, runtimeProofCorrection)
		failure.Extra = map[string]any{
			"failure_class": "runtime-proof-unverifiable",
			"correction":    runtimeProofCorrection,
		}
		return nil, failure
	}
	canonicalDB, canonicalErr := dbpkg.CanonicalPath(target.Path)
	if canonicalErr != nil {
		return nil, internalError("canonicalize incompatible database path: %v", canonicalErr)
	}
	correction := fmt.Sprintf("install and verify a ktx binary supporting database schema %d or newer, then retry; do not lower PRAGMA user_version", schemaErr.Actual)
	failure := newError(CodeSchemaIncompatible,
		"database schema %d at %s is newer than maximum schema %d supported by %s (%s, %s); %s",
		schemaErr.Actual, canonicalDB, schemaErr.Maximum, identity.ExecutablePath,
		identity.ExecutableSHA256, identity.BuildVersion, correction)
	failure.Extra = map[string]any{
		"actual_schema":     schemaErr.Actual,
		"maximum_schema":    schemaErr.Maximum,
		"database_path":     canonicalDB,
		"executable_path":   identity.ExecutablePath,
		"executable_sha256": identity.ExecutableSHA256,
		"build_version":     identity.BuildVersion,
		"correction":        correction,
	}
	return nil, failure
}

func validateRuntimeDigest(value string) error {
	if value != "" && !binarycap.ValidDigest(value) {
		return usageError("invalid --expect-runtime-sha256 %q: want exactly 64 lowercase hexadecimal characters", value)
	}
	return nil
}
