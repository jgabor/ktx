package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/logging"
)

func openDatabase(target dbpkg.Target, expectedSHA256, command string) (*dbpkg.DB, error) {
	if err := authorizeDatabaseTarget(target, expectedSHA256); err != nil {
		return nil, err
	}
	database, err := dbpkg.Open(target.Path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(target.Path, command, err)
}

func openHookDatabase(target dbpkg.Target, expectedSHA256, command string) (*dbpkg.DB, error) {
	if err := authorizeDatabaseTarget(target, expectedSHA256); err != nil {
		return nil, err
	}
	database, err := dbpkg.OpenHook(target.Path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(target.Path, command, err)
}

func openExistingDatabase(target dbpkg.Target, expectedSHA256, command string) (*dbpkg.DB, error) {
	if err := authorizeDatabaseTarget(target, expectedSHA256); err != nil {
		return nil, err
	}
	database, err := dbpkg.OpenExisting(target.Path)
	if err == nil {
		return database, nil
	}
	return nil, databaseOpenError(target.Path, command, err)
}

func openQueryDatabase(target dbpkg.Target, expectedSHA256, command string) (*dbpkg.DB, error) {
	if err := authorizeDatabaseTarget(target, expectedSHA256); err != nil {
		return nil, err
	}
	database, err := dbpkg.OpenQuery(target.Path)
	if err == nil {
		return database, nil
	}
	return nil, queryDatabaseOpenError(target.Path, command, err)
}

func queryDatabaseOpenError(path, command string, err error) error {
	return databaseOpenError(path, command, err)
}

func databaseOpenError(path, command string, err error) error {
	if classified := classifyDatabaseError(path, command, err, dbpkg.OperationOpen, time.Now()); classified != nil {
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

func classifyDatabaseError(path, command string, err error, fallbackPhase string, started time.Time) error {
	if classified := classifyDatabaseLockError(path, command, err, fallbackPhase, started); classified != nil {
		return classified
	}
	if path == "" {
		return nil
	}
	details, ok := dbpkg.DeadlineDetails(err)
	if !ok {
		if !errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		attribution := dbpkg.TimeoutOperation
		switch fallbackPhase {
		case dbpkg.OperationOpen:
			attribution = dbpkg.TimeoutRuntimeStartup
		case dbpkg.OperationMigration:
			attribution = dbpkg.TimeoutMigration
		}
		details = &dbpkg.DeadlineExceeded{Attribution: attribution, Phase: fallbackPhase, Elapsed: time.Since(started)}
	}
	if details.Phase == "" {
		details.Phase = fallbackPhase
	}
	return databaseDeadlineError(path, details)
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

func databaseDeadlineError(path string, details *dbpkg.DeadlineExceeded) error {
	canonicalDB, err := dbpkg.CanonicalPath(path)
	if err != nil {
		return internalError("canonicalize timed-out database path: %v", err)
	}
	identity, err := currentBinaryCapabilities()
	if err != nil {
		return internalError("runtime identity is unavailable after %s: %v", details.Attribution, err)
	}
	correction := "retry the command; if the deadline repeats, reduce competing database work or use a faster local database target"
	failure := newError(ErrorCode(details.Attribution),
		"database operation at %s exceeded the KTX-owned %s deadline; %s", canonicalDB, details.Phase, correction)
	failure.Extra = map[string]any{
		"database_path":            canonicalDB,
		"executable_path":          identity.ExecutablePath,
		"executable_sha256":        identity.ExecutableSHA256,
		"operation_phase":          details.Phase,
		"elapsed_wait_nanoseconds": details.Elapsed.Nanoseconds(),
		"correction":               correction,
	}
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
