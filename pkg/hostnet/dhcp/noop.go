package dhcp

import (
	"context"
	"fmt"

	"github.com/suknna/govirta/pkg/hostnet/dhcp/dhcperr"
)

// NoopManager is an unsupported DHCP manager for composition tests.
type NoopManager struct{}

var _ Manager = NoopManager{}

// NewNoopManager returns a DHCP manager that validates context and rejects work.
func NewNoopManager() NoopManager { return NoopManager{} }

func checkNoopContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", dhcperr.ErrInvalidRequest)
	}
	return ctx.Err()
}

func (NoopManager) Start(ctx context.Context, _ ServerSpec) (ServerInfo, error) {
	if err := checkNoopContext(ctx); err != nil {
		return ServerInfo{}, err
	}
	return ServerInfo{}, dhcperr.ErrUnsupported
}

func (NoopManager) Stop(ctx context.Context, _ ServerID) error {
	if err := checkNoopContext(ctx); err != nil {
		return err
	}
	return dhcperr.ErrUnsupported
}

func (NoopManager) ApplyBinding(ctx context.Context, _ BindingRequest) (LeaseInfo, error) {
	if err := checkNoopContext(ctx); err != nil {
		return LeaseInfo{}, err
	}
	return LeaseInfo{}, dhcperr.ErrUnsupported
}

func (NoopManager) RemoveBinding(ctx context.Context, _ BindingQuery) error {
	if err := checkNoopContext(ctx); err != nil {
		return err
	}
	return dhcperr.ErrUnsupported
}

func (NoopManager) GetServer(ctx context.Context, _ ServerID) (ServerInfo, error) {
	if err := checkNoopContext(ctx); err != nil {
		return ServerInfo{}, err
	}
	return ServerInfo{}, dhcperr.ErrUnsupported
}

func (NoopManager) GetLease(ctx context.Context, _ BindingQuery) (LeaseInfo, error) {
	if err := checkNoopContext(ctx); err != nil {
		return LeaseInfo{}, err
	}
	return LeaseInfo{}, dhcperr.ErrUnsupported
}

func (NoopManager) ListLeases(ctx context.Context, _ LeaseFilter) ([]LeaseInfo, error) {
	if err := checkNoopContext(ctx); err != nil {
		return nil, err
	}
	return nil, dhcperr.ErrUnsupported
}
