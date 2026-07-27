package cli

import (
	"errors"
	"fmt"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func openDatabase(path string) (*dbpkg.DB, error) {
	database, err := dbpkg.Open(path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(path, err)
}

func openExistingDatabase(path string) (*dbpkg.DB, error) {
	database, err := dbpkg.OpenExisting(path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(path, err)
}

func openQueryDatabase(path string) (*dbpkg.DB, error) {
	database, err := dbpkg.OpenQuery(path)
	if err == nil {
		return database, nil
	}
	var schemaErr *dbpkg.SchemaTooOldError
	if errors.As(err, &schemaErr) {
		canonicalDB, canonicalErr := dbpkg.CanonicalPath(path)
		if canonicalErr != nil {
			return nil, internalError("canonicalize migration-required database path: %v", canonicalErr)
		}
		correction := fmt.Sprintf("run `ktx db migrate` to upgrade database schema %d to %d, then retry", schemaErr.Actual, schemaErr.Required)
		failure := newError(CodeSchemaMigration,
			"database schema %d at %s requires migration to schema %d before this read-only query; %s",
			schemaErr.Actual, canonicalDB, schemaErr.Required, correction)
		failure.Extra = map[string]any{
			"actual_schema":   schemaErr.Actual,
			"required_schema": schemaErr.Required,
			"database_path":   canonicalDB,
			"correction":      correction,
		}
		return nil, failure
	}
	return nil, databaseOpenError(path, err)
}

func databaseOpenError(path string, err error) error {
	var schemaErr *dbpkg.SchemaTooNewError
	if !errors.As(err, &schemaErr) {
		return err
	}
	identity, identityErr := currentBinaryCapabilities()
	if identityErr != nil {
		return internalError("runtime identity is unavailable after detecting an incompatible database schema: %v", identityErr)
	}
	canonicalDB, canonicalErr := dbpkg.CanonicalPath(path)
	if canonicalErr != nil {
		return internalError("canonicalize incompatible database path: %v", canonicalErr)
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
	return failure
}
