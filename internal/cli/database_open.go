package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/logging"
)

func openDatabase(path string) (*dbpkg.DB, error) {
	return openDatabaseForSurface(path, "")
}

func openDatabaseForSurface(path, runtimeSurface string) (*dbpkg.DB, error) {
	database, err := dbpkg.Open(path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(path, err, runtimeSurface)
}

func openExistingDatabase(path string) (*dbpkg.DB, error) {
	database, err := dbpkg.OpenExisting(path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(path, err)
}

func openQueryDatabase(path string) (*dbpkg.DB, error) {
	return openQueryDatabaseForSurface(path, "")
}

func openQueryDatabaseForSurface(path, runtimeSurface string) (*dbpkg.DB, error) {
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
	return nil, databaseOpenError(path, err, runtimeSurface)
}

func databaseOpenError(path string, err error, runtimeSurface ...string) error {
	if classified := classifyDatabaseLockError(path, err, dbpkg.OperationOpen, time.Now(), runtimeSurface...); classified != nil {
		return classified
	}
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

func classifyDatabaseLockError(path string, err error, fallbackPhase string, started time.Time, runtimeSurface ...string) error {
	if path == "" || !dbpkg.IsDatabaseUnavailable(err) {
		return nil
	}
	details, ok := dbpkg.DatabaseUnavailableDetails(err)
	if !ok {
		err = dbpkg.AttributeDatabaseUnavailable(fallbackPhase, started, err)
		details, _ = dbpkg.DatabaseUnavailableDetails(err)
	}
	surface := ""
	if len(runtimeSurface) > 0 {
		surface = runtimeSurface[0]
	}
	return databaseUnavailableError(path, surface, details)
}

func databaseUnavailableError(path, runtimeSurface string, details *dbpkg.DatabaseUnavailable) error {
	canonicalDB, err := dbpkg.CanonicalPath(path)
	if err != nil {
		return internalError("canonicalize unavailable database path: %v", err)
	}
	identity, err := currentBinaryCapabilities()
	if err != nil {
		return internalError("runtime identity is unavailable after detecting database unavailability: %v", err)
	}
	correction := "wait for the other database client to finish, then retry"
	failure := newError(CodeDatabaseUnavailable,
		"database at %s is unavailable; %s", canonicalDB, correction)
	failure.Extra = map[string]any{
		"database_path":            canonicalDB,
		"executable_path":          identity.ExecutablePath,
		"executable_sha256":        identity.ExecutableSHA256,
		"failure_class":            details.Class,
		"operation_phase":          details.Phase,
		"elapsed_wait_nanoseconds": details.ElapsedWait.Nanoseconds(),
		"correction":               correction,
	}
	if runtimeSurface != "" {
		writeDatabaseUnavailableEvent(runtimeSurface, canonicalDB, identity.ExecutablePath, identity.ExecutableSHA256, details, correction)
	}
	return failure
}

func writeDatabaseUnavailableEvent(runtimeSurface, databasePath, executablePath, executableSHA256 string, details *dbpkg.DatabaseUnavailable, correction string) {
	cfg, err := config.Load()
	if err != nil || cfg.Logs == nil {
		return
	}
	level, ok := logging.ParseLevel(cfg.Logs.Level)
	if !ok {
		return
	}
	logging.New(level, cfg.Logs.Path, databasePath).Write(logging.DatabaseUnavailable{
		RuntimeSurface:         runtimeSurface,
		ExecutablePath:         executablePath,
		ExecutableSHA256:       executableSHA256,
		DatabasePath:           databasePath,
		FailureClass:           details.Class,
		OperationPhase:         details.Phase,
		ElapsedWaitNanoseconds: details.ElapsedWait.Nanoseconds(),
		Correction:             correction,
	})
}
