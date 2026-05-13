package store

import (
	"context"

	"github.com/suknna/govirta/internal/types"
)

// Store defines the state storage boundary.
type Store interface {
	ListNodes(ctx context.Context) ([]types.Node, error)
}

// MemoryStore is a minimal in-memory store for the initial skeleton.
type MemoryStore struct {
	nodes []types.Node
}

// NewMemoryStore creates a store with no initial state.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

// ListNodes returns a copy of known nodes.
func (s *MemoryStore) ListNodes(ctx context.Context) ([]types.Node, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	nodes := make([]types.Node, len(s.nodes))
	copy(nodes, s.nodes)
	return nodes, nil
}
