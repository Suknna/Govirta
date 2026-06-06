package mac

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/suknna/govirta/internal/controlplane/store"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
)

// nicKeyPrefix is the store key prefix under which every NIC object lives. The
// allocator lists this prefix to derive the set of MACs already occupied by
// existing NIC objects (NIC objects are the single source of truth for MAC
// occupancy; there is no separate ledger).
const nicKeyPrefix = "/govirta/NIC/"

// ErrMACPoolExhausted is returned when every MAC in the pool is already
// occupied by an existing NIC object.
var ErrMACPoolExhausted = errors.New("mac: pool exhausted")

// MACAllocator allocates a unique MAC from the platform pool. The first-slice
// implementation relies on a single-process apiserver (process-mutex
// serialization); multi-replica deployments must migrate allocation to a
// dedicated control-plane controller. This interface is the seam for that migration.
type MACAllocator interface {
	// WithAllocation picks a MAC not occupied by any existing NIC object and
	// invokes commit(mac) while holding the allocation lock, so the caller's NIC
	// Put is atomic with selection. commit's error aborts allocation.
	WithAllocation(ctx context.Context, commit func(mac net.HardwareAddr) error) error
}

// etcdAllocator implements MACAllocator over an injected store.Store. The name
// reflects the production backing (etcd); the store interface lets unit tests
// substitute the in-memory fake. A single process-wide mutex serializes the
// whole "list NIC -> derive occupancy -> pick free -> commit" sequence so that
// selection and the caller's NIC Put are atomic with respect to one another.
type etcdAllocator struct {
	pool  *Pool
	store store.Store
	mu    sync.Mutex
}

// NewAllocator constructs an etcdAllocator over pool and st. The returned value
// satisfies MACAllocator.
func NewAllocator(pool *Pool, st store.Store) *etcdAllocator {
	return &etcdAllocator{
		pool:  pool,
		store: st,
	}
}

// Compile-time assertion that *etcdAllocator satisfies MACAllocator.
var _ MACAllocator = (*etcdAllocator)(nil)

// WithAllocation holds the allocation lock for the entire selection+commit
// sequence so that listing existing NICs, choosing a free MAC, and the caller's
// NIC Put (performed inside commit) are atomic. It lists every NIC object,
// builds the occupied-MAC set from non-empty Spec.MAC values, walks the pool
// candidates in order, and invokes commit with the first free MAC. If commit
// returns an error the allocation is aborted and the error is propagated. When
// no candidate is free it returns ErrMACPoolExhausted.
func (a *etcdAllocator) WithAllocation(ctx context.Context, commit func(mac net.HardwareAddr) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	raws, err := a.store.List(ctx, nicKeyPrefix)
	if err != nil {
		return fmt.Errorf("mac: list NIC objects: %w", err)
	}

	occupied := make(map[string]struct{}, len(raws))
	for _, raw := range raws {
		var nic nicv1.NIC
		if err := json.Unmarshal(raw.Value, &nic); err != nil {
			return fmt.Errorf("mac: decode NIC %q: %w", raw.Key, err)
		}
		if nic.Spec.MAC == "" {
			continue
		}
		hw, err := net.ParseMAC(nic.Spec.MAC)
		if err != nil {
			return fmt.Errorf("mac: parse occupied MAC %q on NIC %q: %w", nic.Spec.MAC, raw.Key, err)
		}
		// Normalize via ParseMAC().String() so case and separator differences
		// in stored MACs cannot cause a free candidate to be wrongly treated as
		// available (or vice versa).
		occupied[hw.String()] = struct{}{}
	}

	for candidate := range a.pool.Candidates() {
		if _, taken := occupied[candidate.String()]; taken {
			continue
		}
		if err := commit(candidate); err != nil {
			return fmt.Errorf("mac: commit allocation %q: %w", candidate, err)
		}
		return nil
	}

	return ErrMACPoolExhausted
}
