package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/logging"
)

func openDatabase(path, command string) (*dbpkg.DB, error) {
	database, err := dbpkg.Open(path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(path, command, err)
}

func openHookDatabase(path, command string) (*dbpkg.DB, error) {
	database, err := dbpkg.OpenHook(path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(path, command, err)
}

func openExistingDatabase(path, command string) (*dbpkg.DB, error) {
	database, err := dbpkg.OpenExisting(path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(path, command, err)
}

func openQueryDatabase(path, command string) (*dbpkg.DB, error) {
	database, err := dbpkg.OpenQuery(path)
	if err == nil {
		return database, nil
	}
	return nil, queryDatabaseOpenError(path, command, err)
}

func queryDatabaseOpenError(path, command string, err error) error {
	return databaseOpenError(path, command, err)
}

func databaseOpenError(path, command string, err error) error {
	if classified := classifyDatabaseLockError(path, command, err, dbpkg.OperationOpen, time.Now()); classified != nil {
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

func classifyDatabaseLockError(path, command string, err error, fallbackPhase string, started time.Time) error {
	if path == "" || !dbpkg.IsDatabaseUnavailable(err) {
		return nil
	}
	details, ok := dbpkg.DatabaseUnavailableDetails(err)
	if !ok {
		err = dbpkg.AttributeDatabaseUnavailable(fallbackPhase, started, err)
		details, _ = dbpkg.DatabaseUnavailableDetails(err)
	}
	return databaseUnavailableError(path, command, details)
}

func databaseUnavailableError(path, command string, details *dbpkg.DatabaseUnavailable) error {
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
	writeDatabaseUnavailableEvent(command, canonicalDB, identity.ExecutablePath, identity.ExecutableSHA256, details)
	return failure
}

func writeDatabaseUnavailableEvent(command, databasePath, executablePath, executableSHA256 string, details *dbpkg.DatabaseUnavailable) {
	cfg, err := config.LoadLogs()
	if err != nil || cfg == nil {
		return
	}
	level, ok := logging.ParseLevel(cfg.Level)
	if !ok {
		return
	}
	path := cfg.Path
	if path == "" {
		path, err = logging.DefaultPath()
		if err != nil {
			return
		}
	}
	blockers := make([]logging.LockBlocker, len(details.LockHolders))
	for i, holder := range details.LockHolders {
		blockers[i] = logging.LockBlocker{PID: holder.PID, Mode: holder.Mode, Command: holder.Command}
	}
	logging.AppendDatabaseUnavailable(logging.Config{Level: level, Path: path}, databasePath, logging.DatabaseUnavailable{
		Command:                      command,
		ExecutablePath:               executablePath,
		ExecutableSHA256:             executableSHA256,
		DatabasePath:                 databasePath,
		FailureClass:                 details.Class,
		OperationPhase:               details.Phase,
		ElapsedWaitNanoseconds:       details.ElapsedWait.Nanoseconds(),
		CoordinatorRequestedMode:     details.RequestedLockMode,
		CoordinatorSnapshotAvailable: details.OwnershipSnapshotAvailable,
		CoordinatorBlockers:          blockers,
	})
}
