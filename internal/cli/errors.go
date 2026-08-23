package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// Exit codes are stable process-level outcomes:
//
//	0 = ok
//	1 = usage
//	2 = not-found
//	3 = conflict
//	4 = internal
//	5 = schema-incompatible
const (
	ExitOK                 = 0
	ExitUsage              = 1
	ExitNotFound           = 2
	ExitConflict           = 3
	ExitInternal           = 4
	ExitSchemaIncompatible = 5
)

// ErrorCode is the machine-readable failure class carried in the error
// envelope. Multiple schema failures share the stable schema exit status.
type ErrorCode string

const (
	CodeUsage               ErrorCode = "usage"
	CodeNotFound            ErrorCode = "not-found"
	CodeConflict            ErrorCode = "conflict"
	CodeInternal            ErrorCode = "internal"
	CodeDatabaseUnavailable ErrorCode = "database-unavailable"
	CodeSchemaIncompatible  ErrorCode = "schema-incompatible"
)

// ExitCode returns the process exit code for the error class.
func (c ErrorCode) ExitCode() int {
	switch c {
	case CodeUsage:
		return ExitUsage
	case CodeNotFound:
		return ExitNotFound
	case CodeConflict:
		return ExitConflict
	case CodeInternal, CodeDatabaseUnavailable:
		return ExitInternal
	case CodeSchemaIncompatible:
		return ExitSchemaIncompatible
	default:
		return ExitInternal
	}
}

// Error is the ktx error envelope. It renders as JSON on stderr for agents
// and as a one-line message for humans.
type Error struct {
	Code    ErrorCode
	Message string
	// Extra carries optional fields merged into the JSON error envelope
	// (e.g. valid_examples for self-correcting usage errors). Nil is
	// the common case — most errors carry only code + message.
	Extra map[string]any
	cause error
}

// commandExit carries a process status for commands that already rendered any
// necessary diagnostics or are forwarding a child process exit code.
type commandExit struct {
	code int
}

func (e *commandExit) Error() string { return fmt.Sprintf("command exited with status %d", e.code) }

func exitWith(code int) error { return &commandExit{code: code} }

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.cause }

// newError constructs a typed error.
func newError(code ErrorCode, format string, args ...any) *Error {
	result := &Error{Code: code, Message: fmt.Sprintf(format, args...)}
	for _, arg := range args {
		if err, ok := arg.(error); ok {
			result.cause = err
			break
		}
	}
	return result
}

// Constructors for each error class.
func usageError(format string, args ...any) *Error {
	return newError(CodeUsage, format, args...)
}

func notFoundError(format string, args ...any) *Error {
	return newError(CodeNotFound, format, args...)
}

func conflictError(format string, args ...any) *Error {
	return newError(CodeConflict, format, args...)
}

func internalError(format string, args ...any) *Error {
	return newError(CodeInternal, format, args...)
}

// printError writes err to w in the requested format. If err is not a
// *Error, it is wrapped as an internal error so the envelope is always
// machine-parseable.
func printError(w io.Writer, err error, format string) {
	ktxErr := toKtxError(err)
	if format == FormatText {
		fmt.Fprintf(w, "ktx: %s: %s\n", ktxErr.Code, ktxErr.Message)
		return
	}
	// Default: JSON envelope on stderr for agents.
	errObj := map[string]any{
		"code":    string(ktxErr.Code),
		"message": ktxErr.Message,
	}
	for k, v := range ktxErr.Extra {
		errObj[k] = v
	}
	envelope := map[string]any{
		"error": errObj,
	}
	_ = json.NewEncoder(w).Encode(envelope)
}

func toKtxError(err error) *Error {
	var ktxErr *Error
	if errors.As(err, &ktxErr) {
		return ktxErr
	}
	// dbpkg.ErrNotFound is the canonical "absent" sentinel: it carries no
	// context, so we wrap it with the standard not-found message rather
	// than dumping the bare sentinel into the envelope. errors.Is is used
	// (not ==) so wrapped errors (errors.Join, fmt.Errorf("%w", ...)) map
	// the same way.
	if errors.Is(err, dbpkg.ErrNotFound) {
		return notFoundError("%v", err)
	}
	return internalError("%v", err)
}

// exitCodeOf returns the exit code for err, defaulting to internal.
func exitCodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	return toKtxError(err).Code.ExitCode()
}
