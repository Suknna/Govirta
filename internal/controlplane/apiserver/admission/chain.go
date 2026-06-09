package admission

import (
	"context"
	"errors"
	"fmt"
)

// Validator validates a single admission request.
type Validator interface {
	Name() string
	Validate(ctx context.Context, req Request) error
}

// Chain executes validators in order and stops on the first rejection.
type Chain struct {
	validators []Validator
}

// NewChain creates an ordered validator chain.
func NewChain(validators ...Validator) Chain {
	return Chain{validators: append([]Validator(nil), validators...)}
}

// Validate runs validators in order. Non-admission validator errors are treated
// as internal admission failures so callers receive a consistent error shape.
func (c Chain) Validate(ctx context.Context, req Request) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("admission: context done: %w", err)
	}
	for _, v := range c.validators {
		if err := v.Validate(ctx, req); err != nil {
			var admissionErr *Error
			if errors.As(err, &admissionErr) {
				return err
			}
			return Reject(v.Name(), ReasonInternal, err)
		}
	}
	return nil
}
