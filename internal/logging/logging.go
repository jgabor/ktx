// Package logging provides the optional, best-effort structured file sink.
package logging

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Level is an inclusive logging threshold.
type Level uint8

const (
	Debug Level = iota
	Info
	Warn
	Error
	Off
)

func (l Level) String() string {
	return [...]string{"debug", "info", "warn", "error", "off"}[l]
}

// ParseLevel parses a logging level.
func ParseLevel(value string) (Level, bool) {
	for level, name := range []string{"debug", "info", "warn", "error", "off"} {
		if value == name {
			return Level(level), true
		}
	}
	return Off, false
}

// Config is a validated logging configuration.
type Config struct {
	Level Level
	Path  string
}

// DatabaseUnavailable is the closed database-unavailable event schema.
// Database paths are sensitive retained identifiers.
type DatabaseUnavailable struct {
	Command                      string
	ExecutablePath               string
	DatabasePath                 string
	FailureClass                 string
	OperationPhase               string
	ElapsedWaitNanoseconds       int64
	CoordinatorRequestedMode     string
	CoordinatorSnapshotAvailable *bool
	CoordinatorBlockers          []LockBlocker
}

// LockBlocker identifies one observed process holding a conflicting flock.
type LockBlocker struct {
	PID     int    `json:"pid"`
	Mode    string `json:"mode"`
	Command string `json:"command"`
}

type databaseUnavailableRecord struct {
	Timestamp                    string        `json:"timestamp"`
	Level                        string        `json:"level"`
	Event                        string        `json:"event"`
	Command                      string        `json:"ktx_command"`
	ExecutablePath               string        `json:"executable_path"`
	DatabasePath                 string        `json:"database_path"`
	FailureClass                 string        `json:"failure_class"`
	OperationPhase               string        `json:"operation_phase"`
	ElapsedWaitNanoseconds       int64         `json:"elapsed_wait_nanoseconds"`
	CoordinatorRequestedMode     string        `json:"coordinator_requested_mode,omitempty"`
	CoordinatorSnapshotAvailable *bool         `json:"coordinator_snapshot_available,omitempty"`
	CoordinatorBlockers          []LockBlocker `json:"coordinator_blockers,omitempty"`
}

// AppendDatabaseUnavailable makes one best-effort append attempt.
func AppendDatabaseUnavailable(cfg Config, databasePath string, event DatabaseUnavailable) {
	if cfg.Level > Error {
		return
	}
	record, err := json.Marshal(databaseUnavailableRecord{
		Timestamp:                    time.Now().UTC().Format(time.RFC3339Nano),
		Level:                        Error.String(),
		Event:                        "database-unavailable",
		Command:                      event.Command,
		ExecutablePath:               event.ExecutablePath,
		DatabasePath:                 event.DatabasePath,
		FailureClass:                 event.FailureClass,
		OperationPhase:               event.OperationPhase,
		ElapsedWaitNanoseconds:       event.ElapsedWaitNanoseconds,
		CoordinatorRequestedMode:     event.CoordinatorRequestedMode,
		CoordinatorSnapshotAvailable: event.CoordinatorSnapshotAvailable,
		CoordinatorBlockers:          event.CoordinatorBlockers,
	})
	if err != nil {
		return
	}
	record = append(record, '\n')
	_ = appendRecord(cfg.Path, databasePath, record)
}

// DefaultPath resolves the default destination without creating it.
func DefaultPath() (string, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Abs(filepath.Join(stateHome, "ktx", "ktx.log"))
}

func appendRecord(path, databasePath string, record []byte) error {
	if samePath(path, databasePath) {
		return errors.New("log destination aliases database")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := openRegular(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := rejectPublicPermissions(file); err != nil {
		return err
	}
	if aliasesDatabase(path, file, databasePath) {
		return errors.New("log destination aliases database")
	}
	_, err = file.Write(record)
	return err
}

func aliasesDatabase(path string, file *os.File, databasePath string) bool {
	if databasePath == "" {
		return false
	}
	if samePath(path, databasePath) {
		return true
	}
	logInfo, logErr := file.Stat()
	dbInfo, dbErr := os.Stat(databasePath)
	return logErr == nil && dbErr == nil && os.SameFile(logInfo, dbInfo)
}

func samePath(path, databasePath string) bool {
	if databasePath == "" {
		return false
	}
	logAbs, logErr := filepath.Abs(path)
	dbAbs, dbErr := filepath.Abs(databasePath)
	return logErr == nil && dbErr == nil && filepath.Clean(logAbs) == filepath.Clean(dbAbs)
}

func privateLogDACL(userSID string) string {
	return "D:P(A;;FA;;;" + userSID + ")"
}
