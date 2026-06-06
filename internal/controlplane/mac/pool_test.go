package mac

import (
	"errors"
	"net"
	"testing"
)

// localPrefix is a valid locally-administered unicast OUI: first octet 0x02
// has the U/L bit set (bit 1) and the I/G bit clear (bit 0).
var localPrefix = net.HardwareAddr{0x02, 0x00, 0x00}

func TestPoolNewPoolValidation(t *testing.T) {
	tests := []struct {
		name    string
		prefix  net.HardwareAddr
		start   uint32
		end     uint32
		wantErr error // nil means construction must succeed
	}{
		{
			name:   "valid locally-administered unicast",
			prefix: net.HardwareAddr{0x02, 0xAB, 0xCD},
			start:  0,
			end:    0xFF,
		},
		{
			name:    "prefix too short",
			prefix:  net.HardwareAddr{0x02, 0x00},
			start:   0,
			end:     1,
			wantErr: ErrPrefixLength,
		},
		{
			name:    "prefix too long",
			prefix:  net.HardwareAddr{0x02, 0x00, 0x00, 0x00},
			start:   0,
			end:     1,
			wantErr: ErrPrefixLength,
		},
		{
			name:    "multicast prefix rejected (I/G bit set)",
			prefix:  net.HardwareAddr{0x03, 0x00, 0x00}, // bit0=1 multicast, bit1=1 local
			start:   0,
			end:     1,
			wantErr: ErrPrefixMulticast,
		},
		{
			name:    "global prefix rejected (U/L bit clear)",
			prefix:  net.HardwareAddr{0x00, 0x00, 0x00}, // bit1=0 global
			start:   0,
			end:     1,
			wantErr: ErrPrefixNotLocal,
		},
		{
			name:    "start greater than end rejected",
			prefix:  localPrefix,
			start:   10,
			end:     5,
			wantErr: ErrSuffixRange,
		},
		{
			name:    "start over 0xFFFFFF rejected",
			prefix:  localPrefix,
			start:   0x1000000,
			end:     0x1000000,
			wantErr: ErrSuffixOverflow,
		},
		{
			name:    "end over 0xFFFFFF rejected",
			prefix:  localPrefix,
			start:   0,
			end:     0x1000000,
			wantErr: ErrSuffixOverflow,
		},
		{
			name:   "max suffix boundary accepted",
			prefix: localPrefix,
			start:  0xFFFFFF,
			end:    0xFFFFFF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPool(tt.prefix, tt.start, tt.end)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewPool error = %v, want errors.Is %v", err, tt.wantErr)
				}
				if p != nil {
					t.Fatalf("NewPool returned non-nil pool on error: %+v", p)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPool unexpected error: %v", err)
			}
			if p == nil {
				t.Fatalf("NewPool returned nil pool without error")
			}
		})
	}
}

func TestPoolEnumerationOrderDeterministic(t *testing.T) {
	p, err := NewPool(net.HardwareAddr{0x02, 0xAB, 0xCD}, 0x10, 0x14)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	want := []string{
		"02:ab:cd:00:00:10",
		"02:ab:cd:00:00:11",
		"02:ab:cd:00:00:12",
		"02:ab:cd:00:00:13",
		"02:ab:cd:00:00:14",
	}

	collect := func() []string {
		var got []string
		for mac := range p.Candidates() {
			got = append(got, mac.String())
		}
		return got
	}

	first := collect()
	if len(first) != len(want) {
		t.Fatalf("candidate count = %d, want %d (%v)", len(first), len(want), first)
	}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("candidate[%d] = %s, want %s", i, first[i], want[i])
		}
	}

	// Determinism: a second enumeration of the same pool must be identical.
	second := collect()
	if len(second) != len(first) {
		t.Fatalf("second enumeration count = %d, want %d", len(second), len(first))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("non-deterministic at [%d]: %s vs %s", i, first[i], second[i])
		}
	}
}

func TestPoolCandidatesAreLocallyAdministeredUnicast(t *testing.T) {
	p, err := NewPool(localPrefix, 0, 0xFF)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	count := 0
	for mac := range p.Candidates() {
		count++
		if len(mac) != 6 {
			t.Fatalf("candidate %s has length %d, want 6", mac, len(mac))
		}
		// Unicast: I/G bit (bit 0) must be clear.
		if mac[0]&0x01 != 0 {
			t.Fatalf("candidate %s is multicast (bit0 set)", mac)
		}
		// Locally administered: U/L bit (bit 1) must be set.
		if mac[0]&0x02 != 0x02 {
			t.Fatalf("candidate %s is not locally administered (bit1 clear)", mac)
		}
	}
	if count != 0x100 {
		t.Fatalf("candidate count = %d, want 256", count)
	}
}

func TestPoolCandidatesIndependentSlices(t *testing.T) {
	p, err := NewPool(localPrefix, 0, 2)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	var macs []net.HardwareAddr
	for mac := range p.Candidates() {
		macs = append(macs, mac)
	}
	// Mutating one retained candidate must not corrupt another: each yield is a
	// freshly allocated slice.
	macs[0][5] = 0xFF
	if macs[1][5] != 0x01 {
		t.Fatalf("candidates alias backing array: macs[1] = %s", macs[1])
	}
}

func TestPoolCandidatesMaxSuffixNoOverflow(t *testing.T) {
	// end == 0xFFFFFF exercises the unsigned-overflow guard: suffix++ past the
	// max would wrap to 0 and loop forever without the explicit stop.
	p, err := NewPool(localPrefix, 0xFFFFFE, 0xFFFFFF)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	var got []string
	for mac := range p.Candidates() {
		got = append(got, mac.String())
	}
	want := []string{"02:00:00:ff:ff:fe", "02:00:00:ff:ff:ff"}
	if len(got) != len(want) {
		t.Fatalf("candidate count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestPoolCandidatesEarlyStop(t *testing.T) {
	// Breaking out of the range must terminate the iterator cleanly.
	p, err := NewPool(localPrefix, 0, 0xFFFF)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	count := 0
	for range p.Candidates() {
		count++
		if count == 3 {
			break
		}
	}
	if count != 3 {
		t.Fatalf("early stop yielded %d candidates, want 3", count)
	}
}

func TestPoolContains(t *testing.T) {
	p, err := NewPool(net.HardwareAddr{0x02, 0xAB, 0xCD}, 0x10, 0x20)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	tests := []struct {
		name string
		mac  net.HardwareAddr
		want bool
	}{
		{
			name: "in range lower bound",
			mac:  net.HardwareAddr{0x02, 0xAB, 0xCD, 0x00, 0x00, 0x10},
			want: true,
		},
		{
			name: "in range upper bound",
			mac:  net.HardwareAddr{0x02, 0xAB, 0xCD, 0x00, 0x00, 0x20},
			want: true,
		},
		{
			name: "in range middle",
			mac:  net.HardwareAddr{0x02, 0xAB, 0xCD, 0x00, 0x00, 0x18},
			want: true,
		},
		{
			name: "suffix below start",
			mac:  net.HardwareAddr{0x02, 0xAB, 0xCD, 0x00, 0x00, 0x0F},
			want: false,
		},
		{
			name: "suffix above end",
			mac:  net.HardwareAddr{0x02, 0xAB, 0xCD, 0x00, 0x00, 0x21},
			want: false,
		},
		{
			name: "wrong prefix",
			mac:  net.HardwareAddr{0x02, 0xAB, 0xCE, 0x00, 0x00, 0x10},
			want: false,
		},
		{
			name: "wrong length",
			mac:  net.HardwareAddr{0x02, 0xAB, 0xCD, 0x00, 0x10},
			want: false,
		},
		{
			name: "nil mac",
			mac:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Contains(tt.mac); got != tt.want {
				t.Fatalf("Contains(%s) = %v, want %v", tt.mac, got, tt.want)
			}
		})
	}
}

func TestPoolContainsMatchesCandidates(t *testing.T) {
	// Every emitted candidate must be reported as contained: Candidates and
	// Contains must agree on the pool's membership.
	p, err := NewPool(localPrefix, 0x100, 0x110)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	for mac := range p.Candidates() {
		if !p.Contains(mac) {
			t.Fatalf("Contains(%s) = false for emitted candidate", mac)
		}
	}
}
