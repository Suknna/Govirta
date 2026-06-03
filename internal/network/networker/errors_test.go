package networker

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelsSupportWrapping(t *testing.T) {
	for _, sentinel := range []error{ErrInvalidRequest, ErrNotFound, ErrAlreadyExists, ErrConflict, ErrNotReady} {
		wrapped := fmt.Errorf("context: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Fatalf("wrapped %v does not match sentinel", sentinel)
		}
	}
}
