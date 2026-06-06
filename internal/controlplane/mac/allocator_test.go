package mac

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
)

// testPool builds a small locally-administered unicast pool with the given
// inclusive suffix range, failing the test on construction error.
func testPool(t *testing.T, start, end uint32) *Pool {
	t.Helper()
	// 0x02 first octet: U/L bit set (locally administered), I/G bit clear (unicast).
	prefix := net.HardwareAddr{0x02, 0x00, 0x00}
	p, err := NewPool(prefix, start, end)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

// firstCandidate returns the first MAC the pool would yield.
func firstCandidate(t *testing.T, p *Pool) net.HardwareAddr {
	t.Helper()
	for c := range p.Candidates() {
		return c
	}
	t.Fatal("pool yielded no candidates")
	return nil
}

// nthCandidate returns the n-th MAC (0-based) the pool would yield.
func nthCandidate(t *testing.T, p *Pool, n int) net.HardwareAddr {
	t.Helper()
	i := 0
	for c := range p.Candidates() {
		if i == n {
			return c
		}
		i++
	}
	t.Fatalf("pool has fewer than %d candidates", n+1)
	return nil
}

// putNIC marshals a real nicv1.NIC carrying mac into the store under the
// conventional NIC key, so the allocator's List+decode path sees a
// contract-faithful object rather than a hand-written JSON blob.
func putNIC(t *testing.T, st store.Store, name, mac string) {
	t.Helper()
	nic := nicv1.NIC{
		TypeMeta: metav1.TypeMeta{
			APIVersion: metav1.APIGroupVersion,
			Kind:       metav1.KindNIC,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  "uid-" + name,
		},
		Spec: nicv1.NICSpec{
			NetworkRef: "net-a",
			VMRef:      "vm-a",
			MAC:        mac,
			IP:         "10.0.0.1",
		},
	}
	value, err := json.Marshal(nic)
	if err != nil {
		t.Fatalf("marshal NIC %q: %v", name, err)
	}
	if _, err := st.Put(context.Background(), nicKeyPrefix+name, value, ""); err != nil {
		t.Fatalf("put NIC %q: %v", name, err)
	}
}

// TestAllocatorEmptyOccupancyPicksFirstCandidate verifies that with no existing
// NIC objects the allocator commits the pool's first candidate.
func TestAllocatorEmptyOccupancyPicksFirstCandidate(t *testing.T) {
	st := fake.New()
	defer st.Close()
	pool := testPool(t, 0, 10)
	alloc := NewAllocator(pool, st)

	want := firstCandidate(t, pool)

	var got net.HardwareAddr
	err := alloc.WithAllocation(context.Background(), func(mac net.HardwareAddr) error {
		got = mac
		return nil
	})
	if err != nil {
		t.Fatalf("WithAllocation: %v", err)
	}
	if got.String() != want.String() {
		t.Fatalf("allocated %s, want first candidate %s", got, want)
	}
}

// TestAllocatorSkipsOccupiedCandidate verifies that when an existing NIC object
// occupies the first candidate, the allocator gives the second candidate.
func TestAllocatorSkipsOccupiedCandidate(t *testing.T) {
	st := fake.New()
	defer st.Close()
	pool := testPool(t, 0, 10)
	alloc := NewAllocator(pool, st)

	occupiedMAC := firstCandidate(t, pool)
	putNIC(t, st, "nic-occupied", occupiedMAC.String())

	want := nthCandidate(t, pool, 1)

	var got net.HardwareAddr
	err := alloc.WithAllocation(context.Background(), func(mac net.HardwareAddr) error {
		got = mac
		return nil
	})
	if err != nil {
		t.Fatalf("WithAllocation: %v", err)
	}
	if got.String() == occupiedMAC.String() {
		t.Fatalf("allocated occupied MAC %s, expected to skip it", got)
	}
	if got.String() != want.String() {
		t.Fatalf("allocated %s, want second candidate %s", got, want)
	}
}

// TestAllocatorSkipsOccupiedCandidateCaseInsensitive verifies that MAC
// normalization makes occupancy matching robust to case differences in the
// stored MAC string.
func TestAllocatorSkipsOccupiedCandidateCaseInsensitive(t *testing.T) {
	st := fake.New()
	defer st.Close()
	// Use a prefix with nonzero high bytes so the MAC has hex letters to vary case.
	prefix := net.HardwareAddr{0x02, 0xAB, 0xCD}
	pool, err := NewPool(prefix, 0xEF, 0xEF+5)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	alloc := NewAllocator(pool, st)

	first := firstCandidate(t, pool)
	// Store the occupied MAC uppercased; net.ParseMAC emits lowercase, so this
	// only matches the candidate if the allocator normalizes both sides.
	upper := net.HardwareAddr(append([]byte(nil), first...)).String()
	// Force uppercase representation to exercise normalization.
	putNIC(t, st, "nic-upper", upperHex(upper))

	want := nthCandidate(t, pool, 1)

	var got net.HardwareAddr
	if err := alloc.WithAllocation(context.Background(), func(mac net.HardwareAddr) error {
		got = mac
		return nil
	}); err != nil {
		t.Fatalf("WithAllocation: %v", err)
	}
	if got.String() != want.String() {
		t.Fatalf("allocated %s, want second candidate %s (occupancy normalization failed)", got, want)
	}
}

// upperHex uppercases an ASCII hex/colon MAC string for the normalization test.
func upperHex(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

// TestAllocatorConcurrentUniqueness is the core three-layer-uniqueness proof:
// N goroutines each call WithAllocation and, inside their commit closure,
// actually Put a NIC object carrying the allocated MAC into the store. Because
// the allocator serializes the whole list->pick->commit sequence under its
// mutex, every goroutine's List observes the NICs committed by predecessors, so
// no MAC is ever handed out twice. We assert the set of allocated MACs has size
// exactly N.
func TestAllocatorConcurrentUniqueness(t *testing.T) {
	const n = 50
	st := fake.New()
	defer st.Close()
	pool := testPool(t, 0, n*2) // plenty of room so exhaustion is not the limiter
	alloc := NewAllocator(pool, st)

	var (
		mu   sync.Mutex
		macs = make(map[string]struct{}, n)
		wg   sync.WaitGroup
	)
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := alloc.WithAllocation(context.Background(), func(mac net.HardwareAddr) error {
				// Persist a NIC carrying this MAC so the next goroutine to take
				// the lock sees it as occupied. This is what proves the
				// list->pick->commit sequence is serialized as one atomic unit.
				name := fmt.Sprintf("nic-%d", i)
				putNIC(t, st, name, mac.String())
				mu.Lock()
				macs[mac.String()] = struct{}{}
				mu.Unlock()
				return nil
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("WithAllocation: %v", err)
	}

	if len(macs) != n {
		t.Fatalf("allocated %d distinct MACs, want %d (duplicates handed out)", len(macs), n)
	}
}

// TestAllocatorPoolExhausted verifies that when every candidate is occupied the
// allocator returns ErrMACPoolExhausted.
func TestAllocatorPoolExhausted(t *testing.T) {
	st := fake.New()
	defer st.Close()
	// A pool with exactly two candidates.
	pool := testPool(t, 0, 1)
	alloc := NewAllocator(pool, st)

	// Occupy both candidates with existing NIC objects.
	putNIC(t, st, "nic-0", nthCandidate(t, pool, 0).String())
	putNIC(t, st, "nic-1", nthCandidate(t, pool, 1).String())

	called := false
	err := alloc.WithAllocation(context.Background(), func(mac net.HardwareAddr) error {
		called = true
		return nil
	})
	if called {
		t.Fatalf("commit was invoked despite an exhausted pool")
	}
	if !errors.Is(err, ErrMACPoolExhausted) {
		t.Fatalf("got error %v, want ErrMACPoolExhausted", err)
	}
}

// TestAllocatorCommitErrorPropagates verifies a commit error aborts allocation
// and is propagated to the caller (errors.Is recoverable).
func TestAllocatorCommitErrorPropagates(t *testing.T) {
	st := fake.New()
	defer st.Close()
	pool := testPool(t, 0, 10)
	alloc := NewAllocator(pool, st)

	sentinel := errors.New("commit boom")
	err := alloc.WithAllocation(context.Background(), func(mac net.HardwareAddr) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got error %v, want it to wrap sentinel", err)
	}
}
