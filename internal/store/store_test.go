package store

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/types"
)

func TestMemoryStoreListNodesEmpty(t *testing.T) {
	store := NewMemoryStore()

	nodes, err := store.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes() error = %v, want nil", err)
	}

	if len(nodes) != 0 {
		t.Fatalf("ListNodes() len = %d, want 0", len(nodes))
	}
}

func TestMemoryStoreListNodesCanceledContext(t *testing.T) {
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.ListNodes(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListNodes() error = %v, want %v", err, context.Canceled)
	}
}

func TestMemoryStoreListNodesReturnsCopy(t *testing.T) {
	store := &MemoryStore{nodes: []types.Node{{Name: "n1"}}}

	nodes, err := store.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes() error = %v, want nil", err)
	}

	nodes[0].Name = "mutated"

	again, err := store.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes() second error = %v, want nil", err)
	}

	if again[0].Name != "n1" {
		t.Fatalf("ListNodes() copied node = %q, want %q", again[0].Name, "n1")
	}
}
