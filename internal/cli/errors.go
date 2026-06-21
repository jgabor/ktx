package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// Exit codes are the truth (Decision 4):
//
//	0 = ok
//	1 = usage
//	2 = not-found
//	3 = conflict
//	4 = internal
const (
	ExitOK       = 0
	ExitUsage    = 1
	ExitNotFound = 2
	ExitConflict = 3
	ExitInternal = 4
)

// ErrorCode is the machine-readable failure class carried in the error
// envelope. It maps 1:1 to an exit code.
type ErrorCode string

const (
	CodeUsage    ErrorCode = "usage"
	CodeNotFound ErrorCode = "not-found"
	CodeConflict ErrorCode = "conflict"
	CodeInternal ErrorCode = "internal"
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
	case CodeInternal:
		return ExitInternal
	default:
		return ExitInternal
	}
}

// Error is the ktx error envelope. It renders as JSON on stderr for agents
// and as a one-line message for humans (Decision 4).
type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string { return e.Message }

// newError constructs a typed error.
func newError(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
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
	envelope := map[string]any{
		"error": map[string]any{
			"code":    string(ktxErr.Code),
			"message": ktxErr.Message,
		},
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
