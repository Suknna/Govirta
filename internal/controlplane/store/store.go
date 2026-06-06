// Package store defines the control-plane persistence boundary. It is a raw
// key/value + watch contract that is intentionally unaware of the six concrete
// resource types (积木式): the etcd implementation and the in-memory fake are
// interchangeable, and kind/nodeName filtering happens in the apiserver layer.
package store

import (
	"context"
	"errors"
)

// Stable sentinel errors so callers classify with errors.Is.
var (
	// ErrNotFound is returned when a key has no stored object.
	ErrNotFound = errors.New("store: object not found")
	// ErrRevisionConflict is returned when a compare-and-swap precondition fails.
	ErrRevisionConflict = errors.New("store: resource version conflict")
	// ErrClosed is returned when the store has been closed.
	ErrClosed = errors.New("store: closed")
)

// EventType is the kind of change observed on a watched key. State-machine-like
// discriminator, so it is a dedicated type with named constants (项目铁律).
type EventType string

const (
	// EventAdded means the object was created.
	EventAdded EventType = "ADDED"
	// EventModified means the object was updated.
	EventModified EventType = "MODIFIED"
	// EventDeleted means the object was removed.
	EventDeleted EventType = "DELETED"
)

// RawObject is a stored object's bytes plus its store-assigned revision. The
// bytes are the resource object's JSON; the store never parses them.
type RawObject struct {
	// Key is the full store key, /govirta/<kind>/<name>.
	Key string
	// Value is the resource object JSON.
	Value []byte
	// ResourceVersion carries the etcd ModRevision as a string.
	ResourceVersion string
}

// WatchEvent is a single change delivered on a Watch channel.
type WatchEvent struct {
	// Type is ADDED/MODIFIED/DELETED.
	Type EventType
	// Object is the post-change object (for DELETED, Key is set and Value may be empty).
	Object RawObject
}

// Store is the raw persistence + watch boundary.
type Store interface {
	// Put stores value at key. When expectedVersion is non-empty, the write is a
	// compare-and-swap that returns ErrRevisionConflict if the current version
	// differs. Returns the new RawObject (with assigned ResourceVersion).
	Put(ctx context.Context, key string, value []byte, expectedVersion string) (RawObject, error)
	// Get returns the object at key or ErrNotFound.
	Get(ctx context.Context, key string) (RawObject, error)
	// List returns all objects whose key starts with prefix, sorted by key.
	List(ctx context.Context, prefix string) ([]RawObject, error)
	// Delete removes key. Deleting a missing key is not an error (idempotent).
	Delete(ctx context.Context, key string) error
	// Watch streams events for keys under prefix starting after startRevision
	// ("" = current). The channel closes when ctx is done or the store closes.
	Watch(ctx context.Context, prefix string, startRevision string) (<-chan WatchEvent, error)
	// Close releases store resources.
	Close() error
}
