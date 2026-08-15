// Package logging provides the optional, best-effort structured file sink.
package logging

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	maxRecordBytes   = 16 << 10
	maxFileBytes     = 5 << 20
	lockWaitTarget   = 25 * time.Millisecond
	lockPollInterval = time.Millisecond
)

// Level is an inclusive logging threshold and event severity.
type Level uint8

const (
	Debug Level = iota
	Info
	Warn
	Error
	Off
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "debug"
	case Info:
		return "info"
	case Warn:
		return "warn"
	case Error:
		return "error"
	default:
		return "off"
	}
}

// ParseLevel parses a validated logging level.
func ParseLevel(value string) (Level, bool) {
	switch value {
	case "debug":
		return Debug, true
	case "info":
		return Info, true
	case "warn":
		return Warn, true
	case "error":
		return Error, true
	case "off":
		return Off, true
	default:
		return Off, false
	}
}

// Event is implemented only by closed event schemas in this package.
type Event interface {
	logRecord(time.Time) any
	logLevel() Level
}

// DatabaseUnavailable is the closed schema for a database availability error.
// Executable and database paths and the executable hash are sensitive retained
// data; they are identifiers, not anonymous telemetry.
type DatabaseUnavailable struct {
	RuntimeSurface         string
	ExecutablePath         string
	ExecutableSHA256       string
	DatabasePath           string
	FailureClass           string
	OperationPhase         string
	ElapsedWaitNanoseconds int64
	Correction             string
}

type databaseUnavailableRecord struct {
	Timestamp              string `json:"timestamp"`
	Level                  string `json:"level"`
	Event                  string `json:"event"`
	RuntimeSurface         string `json:"runtime_surface"`
	ExecutablePath         string `json:"executable_path"`
	ExecutableSHA256       string `json:"executable_sha256"`
	DatabasePath           string `json:"database_path"`
	FailureClass           string `json:"failure_class"`
	OperationPhase         string `json:"operation_phase"`
	ElapsedWaitNanoseconds int64  `json:"elapsed_wait_nanoseconds"`
	Correction             string `json:"correction"`
}

func (DatabaseUnavailable) logLevel() Level { return Error }
func (e DatabaseUnavailable) logRecord(now time.Time) any {
	return databaseUnavailableRecord{
		Timestamp:              now.Format(time.RFC3339Nano),
		Level:                  Error.String(),
		Event:                  "database-unavailable",
		RuntimeSurface:         e.RuntimeSurface,
		ExecutablePath:         e.ExecutablePath,
		ExecutableSHA256:       e.ExecutableSHA256,
		DatabasePath:           e.DatabasePath,
		FailureClass:           e.FailureClass,
		OperationPhase:         e.OperationPhase,
		ElapsedWaitNanoseconds: e.ElapsedWaitNanoseconds,
		Correction:             e.Correction,
	}
}

// Sink appends accepted events to one bounded JSON Lines destination.
type Sink struct {
	level        Level
	path         string
	databasePath string
	disabled     bool
}

// New resolves a configured destination without creating files. An off level
// returns before destination resolution. Resolution failures disable the sink.
func New(level Level, configuredPath, databasePath string) *Sink {
	s := &Sink{level: level, databasePath: databasePath, disabled: level == Off}
	if s.disabled {
		return s
	}
	path, err := resolvePath(configuredPath)
	if err != nil {
		s.disabled = true
		return s
	}
	s.path = path
	return s
}

// Write appends an accepted event. Sink failures are deliberately ignored.
func (s *Sink) Write(event Event) {
	_, _ = s.write(event)
}

func (s *Sink) write(event Event) (time.Duration, bool) {
	if s == nil || s.disabled || event == nil || event.logLevel() < s.level || event.logLevel() >= Off {
		return 0, false
	}
	record, err := json.Marshal(event.logRecord(time.Now().UTC()))
	if err != nil || len(record)+1 > maxRecordBytes {
		return 0, false
	}
	record = append(record, '\n')
	wait, err := s.append(record)
	return wait, err == nil
}

func resolvePath(configured string) (string, error) {
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured), nil
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	stateHome, err := filepath.Abs(stateHome)
	if err != nil {
		return "", err
	}
	if configured == "" {
		configured = filepath.Join("ktx", "ktx.log")
	}
	resolved := filepath.Join(stateHome, configured)
	relative, err := filepath.Rel(stateHome, resolved)
	if err != nil || relative == ".." || len(relative) > 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", errors.New("relative log path escapes state directory")
	}
	return resolved, nil
}

func (s *Sink) append(record []byte) (time.Duration, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return 0, err
	}
	lock, err := openRegular(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return 0, err
	}
	wait, err := acquire(lock, lockWaitTarget)
	if err != nil {
		return wait, err
	}
	defer func() { _ = release(lock) }()

	if err := rejectUnsafeTarget(s.path, s.databasePath); err != nil {
		return wait, err
	}
	info, err := os.Stat(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return wait, err
	}
	if err == nil && info.Size()+int64(len(record)) > maxFileBytes {
		previous := s.path + ".1"
		if err := os.Remove(previous); err != nil && !errors.Is(err, os.ErrNotExist) {
			return wait, err
		}
		if info.Size() <= maxFileBytes {
			if err := os.Rename(s.path, previous); err != nil {
				return wait, err
			}
		} else if err := os.Remove(s.path); err != nil {
			return wait, err
		}
	}

	file, err := openRegular(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return wait, err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return wait, err
	}
	if aliasesFile(file, s.databasePath) {
		return wait, errors.New("log destination aliases database")
	}
	n, err := file.Write(record)
	if err != nil {
		return wait, err
	}
	if n != len(record) {
		return wait, errors.New("short log write")
	}
	return wait, nil
}

func rejectUnsafeTarget(path, databasePath string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("log destination is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if databasePath == "" {
		return nil
	}
	logAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dbAbs, err := filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	if filepath.Clean(logAbs) == filepath.Clean(dbAbs) {
		return errors.New("log destination aliases database")
	}
	logInfo, logErr := os.Stat(path)
	dbInfo, dbErr := os.Stat(databasePath)
	if logErr == nil && dbErr == nil && os.SameFile(logInfo, dbInfo) {
		return errors.New("log destination aliases database")
	}
	return nil
}

func aliasesFile(file *os.File, databasePath string) bool {
	if databasePath == "" {
		return false
	}
	logInfo, logErr := file.Stat()
	dbInfo, dbErr := os.Stat(databasePath)
	return logErr == nil && dbErr == nil && os.SameFile(logInfo, dbInfo)
}
