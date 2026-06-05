package coredhcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/coredhcp/coredhcp/config"
	"github.com/coredhcp/coredhcp/plugins"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/dhcp/dhcperr"
)

const govirtaPluginName = "govirta_static"

var (
	pluginMu      sync.Mutex
	govirtaPlugin = &plugins.Plugin{
		Name:   govirtaPluginName,
		Setup4: setupHandler4,
	}
)

// Manager wraps CoreDHCP behind the Govirta DHCP contract.
type Manager struct {
	mu      sync.RWMutex
	starter coreServerStarter
	servers map[dhcp.ServerID]*serverRuntime
}

var _ dhcp.Manager = (*Manager)(nil)

// NewManager returns a CoreDHCP-backed DHCP manager.
func NewManager() *Manager {
	return newManager(realStarter{})
}

func newManager(starter coreServerStarter) *Manager {
	return &Manager{starter: starter, servers: make(map[dhcp.ServerID]*serverRuntime)}
}

func (m *Manager) Start(ctx context.Context, spec dhcp.ServerSpec) (dhcp.ServerInfo, error) {
	if err := checkContext(ctx); err != nil {
		return dhcp.ServerInfo{}, err
	}
	if err := validateServerSpec(spec); err != nil {
		return dhcp.ServerInfo{}, err
	}
	if err := registerPlaceholderPlugin(); err != nil {
		return dhcp.ServerInfo{}, err
	}

	rt := newServerRuntime(spec)
	cfg := coreConfig(spec)

	// Register the runtime in the global registry before publishing it to
	// m.servers. A failed registration (e.g. another Manager already owns this
	// ServerID) must never leave an rt that is visible to a concurrent Stop but
	// whose startDone is never closed, otherwise Stop would block forever on
	// <-startDone. Registering first keeps the failed rt invisible to Stop.
	if err := registerRuntime(rt); err != nil {
		return dhcp.ServerInfo{}, err
	}

	m.mu.Lock()
	if _, exists := m.servers[spec.ID]; exists {
		m.mu.Unlock()
		// Roll back the registration we just made. unregisterRuntime uses
		// CompareAndDelete, so it only removes the rt we registered above.
		unregisterRuntime(spec.ID, rt)
		return dhcp.ServerInfo{}, fmt.Errorf("%w: server %q", dhcperr.ErrAlreadyRunning, spec.ID)
	}
	m.servers[spec.ID] = rt
	m.mu.Unlock()

	servers, err := m.starter.Start(cfg)
	if err != nil {
		var shouldCleanup bool
		rt.mu.Lock()
		if rt.state == dhcp.ServerStateStopping {
			rt.coreServers = servers
			rt.state = dhcp.ServerStateStopped
		} else {
			rt.state = dhcp.ServerStateStopped
			shouldCleanup = true
		}
		close(rt.startDone)
		rt.mu.Unlock()

		var cleanupErr error
		if shouldCleanup {
			cleanupErr = cleanupStartedServer(servers)
		}

		m.mu.Lock()
		if m.servers[spec.ID] == rt {
			delete(m.servers, spec.ID)
		}
		m.mu.Unlock()
		unregisterRuntime(spec.ID, rt)
		return dhcp.ServerInfo{}, errors.Join(classifyStartError(err), cleanupErr)
	}

	var stoppedBeforeReady bool
	rt.mu.Lock()
	rt.coreServers = servers
	if rt.state == dhcp.ServerStateStopping {
		stoppedBeforeReady = true
	} else {
		rt.state = dhcp.ServerStateReady
	}
	close(rt.startDone)
	rt.mu.Unlock()
	if stoppedBeforeReady {
		return dhcp.ServerInfo{}, fmt.Errorf("%w: server %q stopped before start completed", dhcperr.ErrNotRunning, spec.ID)
	}

	return serverInfo(rt), nil
}

func (m *Manager) Stop(ctx context.Context, id dhcp.ServerID) error {
	// Stop validates only that ctx is non-nil and does NOT short-circuit on a
	// canceled ctx. Once Stop takes cleanup ownership below (state -> Stopping)
	// it must run Close/Wait to completion even if the caller's ctx is already
	// canceled — returning ctx.Err() here would leave the CoreDHCP server
	// running with Govirta state stuck, leaking the listener. This is symmetric
	// with the existing "owns cleanup, finishes even if ctx canceled" guard
	// below.
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", dhcperr.ErrInvalidRequest)
	}
	if err := validateServerID(id); err != nil {
		return err
	}

	m.mu.Lock()
	rt, exists := m.servers[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("%w: server %q", dhcperr.ErrNotFound, id)
	}
	rt.mu.Lock()
	state := rt.state
	if state == dhcp.ServerStateStopping || state == dhcp.ServerStateStopped {
		rt.mu.Unlock()
		m.mu.Unlock()
		return fmt.Errorf("%w: DHCP server %s is %s", dhcperr.ErrNotRunning, id, state)
	}
	rt.state = dhcp.ServerStateStopping
	servers := rt.coreServers
	startDone := rt.startDone
	rt.mu.Unlock()
	m.mu.Unlock()
	// Once this call owns cleanup, it must finish Close/Wait even if ctx is
	// canceled later; returning early would leave the CoreDHCP server running
	// with Govirta state stuck in Stopping.
	if state == dhcp.ServerStateStarting {
		<-startDone
		rt.mu.RLock()
		servers = rt.coreServers
		rt.mu.RUnlock()
	}

	if servers != nil {
		servers.Close()
	}
	waitErr := waitForServer(servers)

	m.mu.Lock()
	rt.mu.Lock()
	rt.state = dhcp.ServerStateStopped
	rt.mu.Unlock()
	if m.servers[id] == rt {
		delete(m.servers, id)
	}
	m.mu.Unlock()
	unregisterRuntime(id, rt)

	return waitErr
}

func (m *Manager) GetServer(ctx context.Context, id dhcp.ServerID) (dhcp.ServerInfo, error) {
	if err := checkContext(ctx); err != nil {
		return dhcp.ServerInfo{}, err
	}
	if err := validateServerID(id); err != nil {
		return dhcp.ServerInfo{}, err
	}

	m.mu.RLock()
	rt, exists := m.servers[id]
	m.mu.RUnlock()
	if !exists {
		return dhcp.ServerInfo{}, fmt.Errorf("%w: server %q", dhcperr.ErrNotFound, id)
	}
	return serverInfo(rt), nil
}

func (m *Manager) ApplyBinding(ctx context.Context, req dhcp.BindingRequest) (dhcp.LeaseInfo, error) {
	if err := checkContext(ctx); err != nil {
		return dhcp.LeaseInfo{}, err
	}
	if err := validateServerID(req.ServerID); err != nil {
		return dhcp.LeaseInfo{}, err
	}

	rt, err := m.serverRuntime(req.ServerID)
	if err != nil {
		return dhcp.LeaseInfo{}, err
	}
	if err := validateMAC(req.MAC); err != nil {
		return dhcp.LeaseInfo{}, err
	}
	rt.mu.RLock()
	spec := rt.spec
	rt.mu.RUnlock()
	if err := validateBindingIPInPool(spec, req.IP); err != nil {
		return dhcp.LeaseInfo{}, err
	}
	if err := validateHostname(req.Hostname); err != nil {
		return dhcp.LeaseInfo{}, err
	}
	return rt.applyBinding(req)
}

func (m *Manager) RemoveBinding(ctx context.Context, query dhcp.BindingQuery) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := validateServerID(query.ServerID); err != nil {
		return err
	}
	rt, err := m.serverRuntime(query.ServerID)
	if err != nil {
		return err
	}
	if err := validateMAC(query.MAC); err != nil {
		return err
	}
	return rt.removeBinding(query)
}

func (m *Manager) GetLease(ctx context.Context, query dhcp.BindingQuery) (dhcp.LeaseInfo, error) {
	if err := checkContext(ctx); err != nil {
		return dhcp.LeaseInfo{}, err
	}
	if err := validateServerID(query.ServerID); err != nil {
		return dhcp.LeaseInfo{}, err
	}
	rt, err := m.serverRuntime(query.ServerID)
	if err != nil {
		return dhcp.LeaseInfo{}, err
	}
	if err := validateMAC(query.MAC); err != nil {
		return dhcp.LeaseInfo{}, err
	}
	return rt.getLease(query)
}

func (m *Manager) ListLeases(ctx context.Context, filter dhcp.LeaseFilter) ([]dhcp.LeaseInfo, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validateLeaseFilter(filter); err != nil {
		return nil, err
	}
	rt, err := m.serverRuntime(filter.ServerID)
	if err != nil {
		return nil, err
	}
	return rt.listLeases(), nil
}

func (m *Manager) serverRuntime(id dhcp.ServerID) (*serverRuntime, error) {
	m.mu.RLock()
	rt, exists := m.servers[id]
	m.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("%w: server %q", dhcperr.ErrNotFound, id)
	}
	return rt, nil
}

func coreConfig(spec dhcp.ServerSpec) *config.Config {
	addr := net.UDPAddr{
		IP:   net.IP(spec.ListenAddr.AsSlice()),
		Port: int(spec.ListenPort),
		Zone: string(spec.InterfaceName),
	}
	return &config.Config{
		Server4: &config.ServerConfig{
			Addresses: []net.UDPAddr{addr},
			Plugins: []config.PluginConfig{{
				Name: govirtaPluginName,
				Args: []string{string(spec.ID)},
			}},
		},
	}
}

func registerPlaceholderPlugin() error {
	pluginMu.Lock()
	defer pluginMu.Unlock()

	if registered := plugins.RegisteredPlugins[govirtaPluginName]; registered != nil {
		if registered == govirtaPlugin {
			return nil
		}
		return fmt.Errorf("%w: CoreDHCP plugin %q is already registered", dhcperr.ErrConflict, govirtaPluginName)
	}

	var (
		panicValue any
		err        error
	)
	func() {
		defer func() {
			panicValue = recover()
		}()
		err = plugins.RegisterPlugin(govirtaPlugin)
	}()
	if panicValue != nil {
		return fmt.Errorf("%w: CoreDHCP plugin %q registration panicked: %v", dhcperr.ErrConflict, govirtaPluginName, panicValue)
	}
	if err != nil {
		return err
	}
	return nil
}

func cleanupStartedServer(servers coreServers) error {
	if servers == nil {
		return nil
	}
	servers.Close()
	return waitForServer(servers)
}

func waitForServer(servers coreServers) error {
	if servers == nil {
		return nil
	}
	return servers.Wait()
}
