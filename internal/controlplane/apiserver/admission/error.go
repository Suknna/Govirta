package admission

import "fmt"

// ErrorReason classifies why admission rejected a request.
type ErrorReason string

const (
	ReasonBadRequest ErrorReason = "BadRequest"
	ReasonConflict   ErrorReason = "Conflict"
	ReasonInternal   ErrorReason = "Internal"
)

// Error is a structured admission rejection that preserves the validator cause.
type Error struct {
	Validator string
	Reason    ErrorReason
	Err       error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("admission %s rejected request: %v", e.Validator, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Reject creates a structured admission rejection for a validator.
func Reject(validator string, reason ErrorReason, err error) *Error {
	return &Error{Validator: validator, Reason: reason, Err: err}
}
