package pool

import "errors"

var (
	// ErrPoolRequired marks requests that do not identify a storage pool.
	ErrPoolRequired = errors.New("pool name is required")
	// ErrPoolNotFound marks lookup requests for an unknown storage pool.
	ErrPoolNotFound = errors.New("pool not found")
	// ErrPoolAlreadyExists marks registration requests that would replace an existing pool.
	ErrPoolAlreadyExists = errors.New("pool already exists")
	// ErrPoolCapacityExceeded marks volume allocation requests beyond pool overcommit capacity.
	ErrPoolCapacityExceeded = errors.New("pool capacity exceeded")
	// ErrPoolNotEmpty marks unregister requests for a pool that still holds volumes or images.
	ErrPoolNotEmpty = errors.New("pool not empty")
)
