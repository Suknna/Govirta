package storage

import (
	"github.com/suknna/govirta/internal/storage/pool"
	"github.com/suknna/govirta/internal/storage/volume"
)

var (
	// ErrInvalidRequest marks caller input that cannot form a valid storage operation.
	ErrInvalidRequest = volume.ErrInvalidRequest
	// ErrUnsupported marks a request outside the backend capabilities for the current phase.
	ErrUnsupported = volume.ErrUnsupported

	// ErrPoolRequired marks requests that do not identify a storage pool.
	ErrPoolRequired = pool.ErrPoolRequired
	// ErrPoolNotFound marks lookup requests for an unknown storage pool.
	ErrPoolNotFound = pool.ErrPoolNotFound
	// ErrPoolAlreadyExists marks registration requests that would replace an existing pool.
	ErrPoolAlreadyExists = pool.ErrPoolAlreadyExists
	// ErrPoolCapacityExceeded marks volume allocation requests beyond pool overcommit capacity.
	ErrPoolCapacityExceeded = pool.ErrPoolCapacityExceeded

	// ErrVolumeNotFound marks lookup or mutation requests for an unknown volume.
	ErrVolumeNotFound = volume.ErrVolumeNotFound
	// ErrVolumeAlreadyExists marks create requests that would duplicate an indexed volume.
	ErrVolumeAlreadyExists = volume.ErrVolumeAlreadyExists
	// ErrVolumeConflict marks requests that violate current volume metadata or state.
	ErrVolumeConflict = volume.ErrVolumeConflict
	// ErrVolumeInUse marks operations that require an unpublished or detached volume.
	ErrVolumeInUse = volume.ErrVolumeInUse
	// ErrVolumeNotPublished marks attachment-dependent operations before publish succeeds.
	ErrVolumeNotPublished = volume.ErrVolumeNotPublished
)
