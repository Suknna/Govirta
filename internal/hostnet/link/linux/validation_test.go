//go:build linux

package linux

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/link/linkerr"
)

func TestValidationRejectsInvalidRequests(t *testing.T) {
	validBridge := link.BridgeSpec{
		Name:        "br0",
		GatewayCIDR: "192.0.2.1/24",
		MTU:         1500,
		MAC:         net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
	}
	validTap := link.TapSpec{
		Name:       "tap0",
		BridgeName: "br0",
		OwnerUID:   link.ExplicitUID(0),
		OwnerGID:   link.ExplicitGID(0),
		MTU:        1500,
		MAC:        net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02},
		VNetHeader: link.VNetHeaderEnabled,
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Nanosecond))
	defer deadlineCancel()

	tests := []struct {
		name string
		call func() error
		want error
	}{
		{
			name: "nil context",
			call: func() error { return validateBridgeSpec(nil, validBridge) },
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "canceled context",
			call: func() error { return validateBridgeSpec(canceledCtx, validBridge) },
			want: context.Canceled,
		},
		{
			name: "deadline context",
			call: func() error { return validateBridgeSpec(deadlineCtx, validBridge) },
			want: context.DeadlineExceeded,
		},
		{
			name: "empty name",
			call: func() error {
				spec := validBridge
				spec.Name = ""
				return validateBridgeSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "sixteen byte name",
			call: func() error {
				spec := validBridge
				spec.Name = "abcdefghijklmnop"
				return validateBridgeSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "invalid gateway CIDR",
			call: func() error {
				spec := validBridge
				spec.GatewayCIDR = "not-cidr"
				return validateBridgeSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "zero MTU",
			call: func() error {
				spec := validBridge
				spec.MTU = 0
				return validateBridgeSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "nil MAC",
			call: func() error {
				spec := validBridge
				spec.MAC = nil
				return validateBridgeSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "multicast MAC",
			call: func() error {
				spec := validBridge
				spec.MAC = net.HardwareAddr{0x03, 0x00, 0x00, 0x00, 0x00, 0x01}
				return validateBridgeSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "globally administered MAC",
			call: func() error {
				spec := validBridge
				spec.MAC = net.HardwareAddr{0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
				return validateBridgeSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "unset owner UID",
			call: func() error {
				spec := validTap
				spec.OwnerUID = link.UID{}
				return validateTapSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "unset owner GID",
			call: func() error {
				spec := validTap
				spec.OwnerGID = link.GID{}
				return validateTapSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "empty VNetHeader",
			call: func() error {
				spec := validTap
				spec.VNetHeader = ""
				return validateTapSpec(context.Background(), spec)
			},
			want: linkerr.ErrInvalidRequest,
		},
		{
			name: "empty ListFilter kind",
			call: func() error { return validateListFilter(context.Background(), link.ListFilter{}) },
			want: linkerr.ErrInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}
