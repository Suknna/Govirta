package link

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/pkg/hostnet/link/linkerr"
)

func TestExplicitUIDAllowsZeroValue(t *testing.T) {
	uid := ExplicitUID(0)
	if !uid.Set {
		t.Fatalf("expected explicit UID marker to be set")
	}
	if uid.Value != 0 {
		t.Fatalf("expected UID value 0, got %d", uid.Value)
	}
}

func TestNoopManagerRejectsNilContext(t *testing.T) {
	manager := NewNoopManager()
	tests := noopManagerOperations()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(manager, nil)
			if !errors.Is(err, linkerr.ErrInvalidRequest) {
				t.Fatalf("expected ErrInvalidRequest, got %v", err)
			}
		})
	}
}

func TestNoopManagerReturnsCanceledContext(t *testing.T) {
	manager := NewNoopManager()
	tests := noopManagerOperations()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(manager, ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got %v", err)
			}
		})
	}
}

func TestNoopManagerReturnsUnsupportedForLiveOperation(t *testing.T) {
	manager := NewNoopManager()
	tests := noopManagerOperations()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(manager, context.Background())
			if !errors.Is(err, linkerr.ErrUnsupported) {
				t.Fatalf("expected ErrUnsupported, got %v", err)
			}
		})
	}
}

type noopManagerOperation struct {
	name string
	call func(manager NoopManager, ctx context.Context) error
}

func noopManagerOperations() []noopManagerOperation {
	return []noopManagerOperation{
		{
			name: "EnsureBridge",
			call: func(manager NoopManager, ctx context.Context) error {
				_, err := manager.EnsureBridge(ctx, BridgeSpec{})
				return err
			},
		},
		{
			name: "EnsureTap",
			call: func(manager NoopManager, ctx context.Context) error {
				_, err := manager.EnsureTap(ctx, TapSpec{})
				return err
			},
		},
		{
			name: "Delete",
			call: func(manager NoopManager, ctx context.Context) error {
				return manager.Delete(ctx, Name("tap0"))
			},
		},
		{
			name: "Exists",
			call: func(manager NoopManager, ctx context.Context) error {
				_, err := manager.Exists(ctx, Name("tap0"))
				return err
			},
		},
		{
			name: "Get",
			call: func(manager NoopManager, ctx context.Context) error {
				_, err := manager.Get(ctx, Name("tap0"))
				return err
			},
		},
		{
			name: "List",
			call: func(manager NoopManager, ctx context.Context) error {
				_, err := manager.List(ctx, ListFilter{Kind: KindAny})
				return err
			},
		},
	}
}
