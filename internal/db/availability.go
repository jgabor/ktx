package db

import (
	"errors"
	"fmt"
	"time"
)

const (
	UnavailableClassCoordinatorTimeout = "coordinator-timeout"
	UnavailableClassSQLiteLock         = "sqlite-lock-exhaustion"

	OperationOpen        = "open"
	OperationMigration   = "migration"
	OperationSource      = "source"
	OperationArtifact    = "artifact"
	OperationDisposition = "disposition"
	OperationQuery       = "query"
)

// DatabaseUnavailable describes a bounded database availability failure.
type DatabaseUnavailable struct {
	Class       string
	Phase       string
	ElapsedWait time.Duration
	err         error
}

func (e *DatabaseUnavailable) Error() string {
	return fmt.Sprintf("database unavailable during %s", e.Phase)
}
func (e *DatabaseUnavailable) Unwrap() error { return e.err }

// AttributeDatabaseUnavailable adds bounded attribution to a lock failure.
func AttributeDatabaseUnavailable(phase string, started time.Time, err error) error {
	if err == nil || !IsDatabaseUnavailable(err) {
		return err
	}
	var existing *DatabaseUnavailable
	if errors.As(err, &existing) {
		return err
	}
	class := UnavailableClassSQLiteLock
	if errors.Is(err, errCoordinationUnavailable) {
		class = UnavailableClassCoordinatorTimeout
	}
	return &DatabaseUnavailable{Class: class, Phase: phase, ElapsedWait: time.Since(started), err: err}
}

// DatabaseUnavailableDetails returns bounded attribution when present.
func DatabaseUnavailableDetails(err error) (*DatabaseUnavailable, bool) {
	var details *DatabaseUnavailable
	return details, errors.As(err, &details)
}
