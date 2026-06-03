//go:build !linux

package coredhcp

import (
	"fmt"

	"github.com/coredhcp/coredhcp/config"

	"github.com/suknna/govirta/internal/hostnet/dhcp/dhcperr"
)

type realStarter struct{}

func (realStarter) Start(_ *config.Config) (coreServers, error) {
	return nil, fmt.Errorf("%w: CoreDHCP server starter is only supported on Linux", dhcperr.ErrUnsupported)
}
