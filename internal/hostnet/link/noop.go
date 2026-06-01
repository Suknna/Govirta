package link

import (
	"context"

	"github.com/suknna/govirta/internal/hostnet/link/linkerr"
)

type NoopManager struct{}

func NewNoopManager() NoopManager { return NoopManager{} }

func (NoopManager) EnsureBridge(ctx context.Context, spec BridgeSpec) (LinkInfo, error) {
	if err := noopOperationError(ctx); err != nil {
		return LinkInfo{}, err
	}

	return LinkInfo{}, linkerr.ErrUnsupported
}

func (NoopManager) EnsureTap(ctx context.Context, spec TapSpec) (LinkInfo, error) {
	if err := noopOperationError(ctx); err != nil {
		return LinkInfo{}, err
	}

	return LinkInfo{}, linkerr.ErrUnsupported
}

func (NoopManager) Delete(ctx context.Context, name Name) error {
	if err := noopOperationError(ctx); err != nil {
		return err
	}

	return linkerr.ErrUnsupported
}

func (NoopManager) Exists(ctx context.Context, name Name) (bool, error) {
	if err := noopOperationError(ctx); err != nil {
		return false, err
	}

	return false, linkerr.ErrUnsupported
}

func (NoopManager) Get(ctx context.Context, name Name) (LinkInfo, error) {
	if err := noopOperationError(ctx); err != nil {
		return LinkInfo{}, err
	}

	return LinkInfo{}, linkerr.ErrUnsupported
}

func (NoopManager) List(ctx context.Context, filter ListFilter) ([]LinkInfo, error) {
	if err := noopOperationError(ctx); err != nil {
		return nil, err
	}

	return nil, linkerr.ErrUnsupported
}

func noopOperationError(ctx context.Context) error {
	if ctx == nil {
		return linkerr.ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	return nil
}
