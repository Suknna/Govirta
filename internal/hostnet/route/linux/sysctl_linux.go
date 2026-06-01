//go:build linux

package linux

import (
	"context"
	"os"
)

const ipv4ForwardingPath = "/proc/sys/net/ipv4/ip_forward"

type forwardingReader interface {
	ReadIPv4Forwarding(ctx context.Context) (string, error)
}

type procForwardingReader struct{}

func (procForwardingReader) ReadIPv4Forwarding(ctx context.Context) (string, error) {
	if err := checkContext(ctx); err != nil {
		return "", err
	}

	data, err := os.ReadFile(ipv4ForwardingPath)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
