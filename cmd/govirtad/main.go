package main

import (
	"context"
	"os"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("process", "govirtad").Logger()
	ctx := logger.WithContext(context.Background())

	if err := controlplane.NewService().Run(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("control plane exited with error")
		os.Exit(1)
	}
}
