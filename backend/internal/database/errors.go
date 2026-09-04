package database

import "errors"

// Sentinel errors used across the application. Handlers map these onto HTTP
// status codes so that internal error text never needs to reach a client.
var (
	ErrInvalidName   = errors.New("invalid name")
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrClosed        = errors.New("database is closed")
	ErrReadOnly      = errors.New("database is read-only")
	ErrNotSQLite     = errors.New("file is not a valid SQLite database")
	ErrTooManyOpen   = errors.New("too many open databases")
)
