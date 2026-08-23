package db

import (
	"context"
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

	TimeoutRuntimeStartup = "runtime-startup-timeout"
	TimeoutOperation      = "operation-timeout"
	TimeoutMigration      = "migration-timeout"
)

// DeadlineExceeded identifies the KTX-owned deadline that expired.
type DeadlineExceeded struct {
	Attribution string
	Phase       string
	Elapsed     time.Duration
	err         error
}

func (e *DeadlineExceeded) Error() string { return e.Attribution }
func (e *DeadlineExceeded) Unwrap() error { return e.err }

// AttributeDeadline preserves database contention errors and attributes other
// deadline exhaustion to one KTX-owned boundary.
func AttributeDeadline(attribution, phase string, started time.Time, err error) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || IsDatabaseUnavailable(err) {
		return err
	}
	var existing *DeadlineExceeded
	if errors.As(err, &existing) {
		return err
	}
	return &DeadlineExceeded{Attribution: attribution, Phase: phase, Elapsed: time.Since(started), err: err}
}

// DeadlineDetails returns KTX-owned deadline attribution when present.
func DeadlineDetails(err error) (*DeadlineExceeded, bool) {
	var details *DeadlineExceeded
	return details, errors.As(err, &details)
}

// DatabaseUnavailable describes a bounded database availability failure.
type DatabaseUnavailable struct {
	Class                      string
	Phase                      string
	ElapsedWait                time.Duration
	RequestedLockMode          string
	OwnershipSnapshotAvailable *bool
	LockHolders                []LockHolder
	err                        error
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
	details := &DatabaseUnavailable{Class: class, Phase: phase, ElapsedWait: time.Since(started), err: err}
	var timeout *coordinationTimeoutError
	if errors.As(err, &timeout) {
		available := timeout.OwnershipSnapshotAvailable
		details.RequestedLockMode = timeout.RequestedMode.String()
		details.OwnershipSnapshotAvailable = &available
		details.LockHolders = timeout.LockHolders
	}
	return details
}

// DatabaseUnavailableDetails returns bounded attribution when present.
func DatabaseUnavailableDetails(err error) (*DatabaseUnavailable, bool) {
	var details *DatabaseUnavailable
	return details, errors.As(err, &details)
}
