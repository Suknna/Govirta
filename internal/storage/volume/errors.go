package volume

import "errors"

var (
	// ErrInvalidRequest marks caller input that cannot form a valid storage operation.
	ErrInvalidRequest = errors.New("invalid storage request")
	// ErrUnsupported marks a request outside the backend capabilities for the current phase.
	ErrUnsupported = errors.New("storage operation unsupported")
	// ErrVolumeNotFound marks lookup or mutation requests for an unknown volume.
	ErrVolumeNotFound = errors.New("volume not found")
	// ErrVolumeAlreadyExists marks create requests that would duplicate an indexed volume.
	ErrVolumeAlreadyExists = errors.New("volume already exists")
	// ErrVolumeConflict marks requests that violate current volume metadata or state.
	ErrVolumeConflict = errors.New("volume conflict")
	// ErrVolumeInUse marks operations that require an unpublished or detached volume.
	ErrVolumeInUse = errors.New("volume in use")
	// ErrVolumeNotPublished marks attachment-dependent operations before publish succeeds.
	ErrVolumeNotPublished = errors.New("volume not published")
)
