//go:build linux

package coredhcp

import (
	"github.com/coredhcp/coredhcp/config"
	"github.com/coredhcp/coredhcp/server"
)

type realStarter struct{}

func (realStarter) Start(cfg *config.Config) (coreServers, error) {
	return server.Start(cfg)
}
