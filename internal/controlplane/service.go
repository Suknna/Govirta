package controlplane

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/apiserver"
)

// Service coordinates control plane components.
type Service struct {
	apiServer apiserver.Server
}

// NewService creates a control plane service with no-op dependencies.
func NewService() *Service {
	return &Service{
		apiServer: apiserver.NewNoopServer(),
	}
}

// Run starts the control plane service.
func (s *Service) Run(ctx context.Context) error {
	zerolog.Ctx(ctx).Info().Str("component", "controlplane").Msg("starting control plane")
	return s.apiServer.Run(ctx)
}
