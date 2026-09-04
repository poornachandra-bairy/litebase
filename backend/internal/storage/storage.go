// Package storage abstracts where backup artefacts are kept.
//
// The interface is deliberately small — put, get, delete, list — so that adding
// S3 or R2 later means writing one new implementation rather than touching the
// backup logic.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("stored object not found")

// Object describes a stored artefact.
type Object struct {
	// Key identifies the object within the provider.
	Key string `json:"key"`
	// RemoteID is the provider's own identifier, when it differs from the key.
	// Google Drive, for example, addresses files by an opaque id.
	RemoteID  string    `json:"remote_id"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Provider stores and retrieves backup artefacts.
//
// Implementations must be safe for concurrent use.
type Provider interface {
	// Name identifies the provider in configuration and backup records.
	Name() string

	// Put stores the contents of r under key and returns the resulting object.
	// size may be -1 when unknown; providers that need a length will buffer.
	Put(ctx context.Context, key string, r io.Reader, size int64) (*Object, error)

	// Get opens a stored object for reading. The caller closes the reader.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Delete removes an object. Deleting a missing object is not an error, so
	// that retention runs are idempotent.
	Delete(ctx context.Context, key string) error

	// Stat returns metadata for one object.
	Stat(ctx context.Context, key string) (*Object, error)

	// List returns the objects under a prefix.
	List(ctx context.Context, prefix string) ([]Object, error)
}

// Registry holds the configured providers by name.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds a provider, replacing any existing one with the same name.
func (r *Registry) Register(p Provider) {
	if p != nil {
		r.providers[p.Name()] = p
	}
}

// Get returns a provider by name.
func (r *Registry) Get(name string) (Provider, error) {
	if name == "" {
		name = "local"
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, errors.New("storage provider " + name + " is not configured")
	}
	return p, nil
}

// Names lists the configured providers.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	return out
}

// Has reports whether a provider is configured.
func (r *Registry) Has(name string) bool {
	_, ok := r.providers[name]
	return ok
}
