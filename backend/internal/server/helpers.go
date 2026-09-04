package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/litebase/litebase/internal/api"
	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/backup"
	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/rows"
	"github.com/litebase/litebase/internal/storage"
	"github.com/litebase/litebase/internal/tables"
)

// contextWithTimeout bounds a handler's work.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// mapError converts a service-layer error into an HTTP error.
//
// Routing every handler's errors through one function is what keeps status
// codes consistent and stops an unexpected internal error being rendered
// verbatim: anything unrecognised becomes a generic 500 whose detail is logged
// rather than sent.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	// An error already shaped for HTTP passes through untouched.
	var httpErr *httpx.Error
	if errors.As(err, &httpErr) {
		return httpErr
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return httpx.Timeout("the operation took too long and was cancelled")
	case errors.Is(err, context.Canceled):
		return httpx.BadRequest("the request was cancelled")

	// Not found.
	case errors.Is(err, database.ErrNotFound),
		errors.Is(err, tables.ErrObjectNotFound),
		errors.Is(err, rows.ErrNoRow),
		errors.Is(err, api.ErrEndpointNotFound),
		errors.Is(err, backup.ErrBackupNotFound),
		errors.Is(err, backup.ErrScheduleNotFound),
		errors.Is(err, auth.ErrUserNotFound),
		errors.Is(err, auth.ErrKeyNotFound),
		errors.Is(err, storage.ErrNotFound):
		return httpx.NotFound("%s", err.Error())

	// Conflicts.
	case errors.Is(err, database.ErrAlreadyExists),
		errors.Is(err, auth.ErrUserExists),
		errors.Is(err, api.ErrRouteConflict),
		errors.Is(err, auth.ErrLastOwner):
		return httpx.Conflict("%s", err.Error())

	// Client input problems. These messages describe the caller's own request,
	// so they are safe and useful to return.
	case errors.Is(err, database.ErrInvalidName),
		errors.Is(err, database.ErrNotSQLite),
		errors.Is(err, auth.ErrWeakPassword),
		errors.Is(err, auth.ErrInvalidEmail),
		errors.Is(err, rows.ErrNoPrimaryKey),
		errors.Is(err, backup.ErrNoKey),
		errors.Is(err, backup.ErrNotEncrypted),
		errors.Is(err, backup.ErrDecryptFailed):
		return httpx.BadRequest("%s", err.Error())

	// Parameter validation reports every bad field at once.
	case isValidationError(err):
		var verr *api.ValidationError
		errors.As(err, &verr)
		return httpx.Validation("one or more parameters are invalid", verr.Fields())

	case errors.Is(err, database.ErrTooManyOpen):
		return httpx.Unavailable("too many databases are open; try again shortly")
	}

	return httpx.Internal(err)
}

func isValidationError(err error) bool {
	var verr *api.ValidationError
	return errors.As(err, &verr)
}

// fail maps and writes an error response.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	httpx.Fail(w, r, mapError(err))
}

// queryInt reads an integer query parameter, falling back to a default.
func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// queryBool reads a boolean query parameter.
func queryBool(r *http.Request, name string, def bool) bool {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
