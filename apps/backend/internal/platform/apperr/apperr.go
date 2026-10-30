// Package apperr defines transport-agnostic application errors.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Error codes rendered in API responses.
const (
	CodeNotFound        = "not_found"
	CodeInvalid         = "invalid"
	CodeUnauthorized    = "unauthorized"
	CodeForbidden       = "forbidden"
	CodeConflict        = "conflict"
	CodeTooManyRequests = "too_many_requests"
	CodeUnavailable     = "unavailable"
	CodeInternal        = "internal"
)

// Error is an application error with a stable code, a client-safe message and an HTTP status.
type Error struct {
	Code    string
	Message string
	Status  int
	Fields  map[string]string // per-field messages (validation)
	Err     error             // cause; never shown to clients
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Wrap returns a copy of e with err as its cause.
func (e *Error) Wrap(err error) *Error {
	c := *e
	c.Err = err
	return &c
}

// WithFields returns a copy of e with the given per-field messages.
func (e *Error) WithFields(fields map[string]string) *Error {
	c := *e
	c.Fields = fields
	return &c
}

// New builds an Error with an arbitrary status and code.
func New(status int, code, msg string) *Error {
	return &Error{Code: code, Message: msg, Status: status}
}

func NotFound(msg string) *Error { return New(http.StatusNotFound, CodeNotFound, msg) }

func Invalid(msg string, fields map[string]string) *Error {
	return &Error{Code: CodeInvalid, Message: msg, Status: http.StatusBadRequest, Fields: fields}
}

func Unauthorized(msg string) *Error { return New(http.StatusUnauthorized, CodeUnauthorized, msg) }
func Forbidden(msg string) *Error    { return New(http.StatusForbidden, CodeForbidden, msg) }
func Conflict(msg string) *Error     { return New(http.StatusConflict, CodeConflict, msg) }
func TooManyRequests(msg string) *Error {
	return New(http.StatusTooManyRequests, CodeTooManyRequests, msg)
}
func Unavailable(msg string) *Error { return New(http.StatusServiceUnavailable, CodeUnavailable, msg) }

// Internal wraps an unexpected error; its cause is logged but never exposed.
func Internal(err error) *Error {
	return &Error{Code: CodeInternal, Message: "internal error", Status: http.StatusInternalServerError, Err: err}
}

// As returns the first *Error in err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// IsCode reports whether err's chain contains an *Error with the given code.
func IsCode(err error, code string) bool {
	e, ok := As(err)
	return ok && e.Code == code
}
