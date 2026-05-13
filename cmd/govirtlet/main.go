package main

import (
	"context"
	"os"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/node"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("process", "govirtlet").Logger()
	ctx := logger.WithContext(context.Background())

	if err := node.NewAgent().Run(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("node agent exited with error")
		os.Exit(1)
	}
}
