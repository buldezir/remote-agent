package orchestrator

import (
	"errors"
	"fmt"
)

// Error carries a machine-readable code that the API forwards to clients.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

const (
	CodeInvalid     = "invalid"
	CodeNotFound    = "not_found"
	CodeConflict    = "conflict"
	CodeUnavailable = "unavailable"
)

func errorf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "internal"
}
