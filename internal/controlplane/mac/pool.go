// Package mac provides deterministic candidate enumeration over a configured
// range of locally-administered unicast MAC addresses. It is a pure-function
// package: it has no etcd, apiserver, or other external dependencies, so the
// same configured pool always yields the same reproducible candidate sequence.
//
// A Pool pins a 3-byte OUI prefix and walks a suffix interval [start, end]
// across the low 3 bytes (24 bits) of the address. The allocator layer consumes
// the candidate sequence to find a free address and uses Contains to validate
// or reclaim addresses; the policy of which candidate is actually claimed lives
// outside this package.
package mac

import (
	"errors"
	"fmt"
	"iter"
	"net"
)

// Stable sentinel errors so callers classify construction failures with
// errors.Is. Each is wrapped with %w at the call site to add context.
var (
	// ErrPrefixLength is returned when the prefix is not exactly a 3-byte OUI.
	// The OUI plus the 3-byte suffix must form a complete 6-byte MAC.
	ErrPrefixLength = errors.New("mac: prefix must be a 3-byte OUI")
	// ErrPrefixNotLocal is returned when the prefix is not locally administered,
	// i.e. bit 1 (the U/L bit) of the first octet is not set. Platform-assigned
	// MACs must come from the locally-administered space to avoid colliding with
	// globally-unique vendor addresses.
	ErrPrefixNotLocal = errors.New("mac: prefix is not locally administered")
	// ErrPrefixMulticast is returned when the prefix is a multicast address,
	// i.e. bit 0 (the I/G bit) of the first octet is set. Assigned addresses
	// must be unicast.
	ErrPrefixMulticast = errors.New("mac: prefix is multicast, must be unicast")
	// ErrSuffixRange is returned when start > end; the interval is empty/invalid.
	ErrSuffixRange = errors.New("mac: suffix start must be <= end")
	// ErrSuffixOverflow is returned when start or end exceeds 0xFFFFFF, the
	// largest value representable in the 3-byte (24-bit) suffix.
	ErrSuffixOverflow = errors.New("mac: suffix exceeds 24-bit range (0xFFFFFF)")
)

// maxSuffix is the largest value the 3-byte suffix can hold (2^24 - 1).
const maxSuffix uint32 = 0xFFFFFF

// Pool represents a configured range of locally-administered unicast MAC
// addresses as a deterministic candidate sequence. It is immutable after
// construction and safe to enumerate repeatedly.
type Pool struct {
	// prefix is the validated 3-byte OUI. Stored as a private copy so callers
	// cannot mutate the pool's backing array after construction.
	prefix [3]byte
	// start and end bound the inclusive suffix interval, both <= maxSuffix.
	start uint32
	end   uint32
}

// NewPool constructs a Pool from a 3-byte OUI prefix and an inclusive suffix
// interval [start, end] over the low 24 bits of the address.
//
// It rejects invalid pools explicitly rather than silently correcting them:
//   - prefix must be exactly 3 bytes (ErrPrefixLength);
//   - prefix must be locally administered, first octet bit 1 set (ErrPrefixNotLocal);
//   - prefix must be unicast, first octet bit 0 clear (ErrPrefixMulticast);
//   - start and end must each be <= 0xFFFFFF (ErrSuffixOverflow);
//   - start must be <= end (ErrSuffixRange).
//
// On success it returns a Pool whose Candidates sequence is deterministic and
// reproducible.
func NewPool(prefix net.HardwareAddr, start, end uint32) (*Pool, error) {
	if len(prefix) != 3 {
		return nil, fmt.Errorf("mac: got %d-byte prefix: %w", len(prefix), ErrPrefixLength)
	}
	// The first octet carries both the I/G bit (bit 0) and the U/L bit (bit 1).
	// We require unicast (I/G == 0) and locally administered (U/L == 1).
	first := prefix[0]
	if first&0x01 != 0 {
		return nil, fmt.Errorf("mac: first octet 0x%02x: %w", first, ErrPrefixMulticast)
	}
	if first&0x02 != 0x02 {
		return nil, fmt.Errorf("mac: first octet 0x%02x: %w", first, ErrPrefixNotLocal)
	}
	if start > maxSuffix {
		return nil, fmt.Errorf("mac: start 0x%x: %w", start, ErrSuffixOverflow)
	}
	if end > maxSuffix {
		return nil, fmt.Errorf("mac: end 0x%x: %w", end, ErrSuffixOverflow)
	}
	if start > end {
		return nil, fmt.Errorf("mac: start 0x%x > end 0x%x: %w", start, end, ErrSuffixRange)
	}

	p := &Pool{start: start, end: end}
	copy(p.prefix[:], prefix)
	return p, nil
}

// Candidates returns a deterministic iterator over every MAC in the pool, in
// ascending suffix order from start to end inclusive. Each yielded
// net.HardwareAddr is a freshly allocated 6-byte slice so the caller may retain
// it without aliasing the pool's internal state. Two iterations of the same
// pool yield identical sequences.
func (p *Pool) Candidates() iter.Seq[net.HardwareAddr] {
	return func(yield func(net.HardwareAddr) bool) {
		for suffix := p.start; suffix <= p.end; suffix++ {
			if !yield(p.macFor(suffix)) {
				return
			}
			// Guard against unsigned overflow when end == maxSuffix: suffix++
			// would wrap to 0 and loop forever. Stop once we have yielded end.
			if suffix == p.end {
				return
			}
		}
	}
}

// Contains reports whether mac falls within this pool: a 6-byte address whose
// first 3 bytes match the prefix and whose 3-byte suffix lies in [start, end].
// The allocator uses this to validate or reclaim addresses.
func (p *Pool) Contains(mac net.HardwareAddr) bool {
	if len(mac) != 6 {
		return false
	}
	if mac[0] != p.prefix[0] || mac[1] != p.prefix[1] || mac[2] != p.prefix[2] {
		return false
	}
	suffix := uint32(mac[3])<<16 | uint32(mac[4])<<8 | uint32(mac[5])
	return suffix >= p.start && suffix <= p.end
}

// macFor builds the 6-byte address for a given 24-bit suffix. Callers must
// ensure suffix <= maxSuffix; Candidates guarantees this by construction.
func (p *Pool) macFor(suffix uint32) net.HardwareAddr {
	return net.HardwareAddr{
		p.prefix[0], p.prefix[1], p.prefix[2],
		byte(suffix >> 16),
		byte(suffix >> 8),
		byte(suffix),
	}
}
