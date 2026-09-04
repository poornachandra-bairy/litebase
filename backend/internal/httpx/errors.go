// Package httpx holds the HTTP conventions shared by the middleware and the
// handlers: JSON encoding, the error envelope, and request identifiers.
package httpx

import (
	"errors"
	"fmt"
	"net/http"
)

// Error is an API error that is safe to show a client.
//
// Internal failures are never returned directly. Handlers wrap them with
// Internal(), which keeps the underlying cause for the log while sending the
// caller a generic message, so database paths, SQL text and driver internals
// cannot leak.
type Error struct {
	Status  int               `json:"-"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`

	// cause is logged server-side and never serialised.
	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.cause)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// Cause returns the wrapped internal error, for logging.
func (e *Error) Cause() error { return e.cause }

func newError(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// BadRequest reports malformed or unacceptable input.
func BadRequest(format string, args ...any) *Error {
	return newError(http.StatusBadRequest, "bad_request", fmt.Sprintf(format, args...))
}

// Validation reports per-field input problems.
func Validation(msg string, fields map[string]string) *Error {
	e := newError(http.StatusUnprocessableEntity, "validation_failed", msg)
	e.Fields = fields
	return e
}

// Unauthorized reports missing or invalid credentials.
func Unauthorized(msg string) *Error {
	if msg == "" {
		msg = "authentication required"
	}
	return newError(http.StatusUnauthorized, "unauthorized", msg)
}

// Forbidden reports valid credentials without sufficient permission.
func Forbidden(msg string) *Error {
	if msg == "" {
		msg = "you do not have permission to perform this action"
	}
	return newError(http.StatusForbidden, "forbidden", msg)
}

// NotFound reports a missing resource.
func NotFound(format string, args ...any) *Error {
	return newError(http.StatusNotFound, "not_found", fmt.Sprintf(format, args...))
}

// Conflict reports a state clash, such as a duplicate name.
func Conflict(format string, args ...any) *Error {
	return newError(http.StatusConflict, "conflict", fmt.Sprintf(format, args...))
}

// TooLarge reports a request body over the configured limit.
func TooLarge(msg string) *Error {
	return newError(http.StatusRequestEntityTooLarge, "payload_too_large", msg)
}

// RateLimited reports that the caller has exceeded their quota.
func RateLimited(msg string) *Error {
	if msg == "" {
		msg = "rate limit exceeded"
	}
	return newError(http.StatusTooManyRequests, "rate_limited", msg)
}

// Timeout reports that an operation exceeded its deadline.
func Timeout(msg string) *Error {
	return newError(http.StatusGatewayTimeout, "timeout", msg)
}

// Unavailable reports a dependency that is temporarily unusable.
func Unavailable(msg string) *Error {
	return newError(http.StatusServiceUnavailable, "unavailable", msg)
}

// Internal wraps an unexpected failure. The cause is logged; the client only
// ever sees the generic message.
func Internal(cause error) *Error {
	e := newError(http.StatusInternalServerError, "internal_error",
		"an internal error occurred")
	e.cause = cause
	return e
}

// UserError attaches a client-safe message to an internal cause, for failures
// where the reason genuinely helps the caller (an invalid SQL statement, say)
// without exposing server internals.
func UserError(status int, code, msg string, cause error) *Error {
	e := newError(status, code, msg)
	e.cause = cause
	return e
}

// AsError converts any error into an *Error, defaulting to a 500 so that an
// unexpected error type can never be rendered verbatim to a client.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}
