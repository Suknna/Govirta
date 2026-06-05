package coredhcp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coredhcp/coredhcp/config"
	"github.com/coredhcp/coredhcp/handler"
	"github.com/coredhcp/coredhcp/plugins"

	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/dhcp/dhcperr"
	"github.com/suknna/govirta/pkg/hostnet/link"
)

var testServerSeq atomic.Uint64

type fakeStarter struct {
	started []*config.Config
	servers coreServers
	err     error
}

func (f *fakeStarter) Start(cfg *config.Config) (coreServers, error) {
	f.started = append(f.started, cfg)
	return f.servers, f.err
}

type fakeServers struct {
	closed  bool
	waited  bool
	waitErr error
}

func (f *fakeServers) Close() { f.closed = true }

func (f *fakeServers) Wait() error {
	f.waited = true
	return f.waitErr
}

type blockingStarter struct {
	entered chan struct{}
	release chan struct{}
	servers coreServers
}

func newBlockingStarter(servers coreServers) *blockingStarter {
	return &blockingStarter{entered: make(chan struct{}), release: make(chan struct{}), servers: servers}
}

func (b *blockingStarter) Start(_ *config.Config) (coreServers, error) {
	close(b.entered)
	<-b.release
	return b.servers, nil
}

type blockingServers struct {
	closed  chan struct{}
	release chan struct{}
	waited  chan struct{}

	mu        sync.Mutex
	closeOnce sync.Once
	waitOnce  sync.Once
	closes    int
	waits     int
}

func newBlockingServers() *blockingServers {
	return &blockingServers{closed: make(chan struct{}), release: make(chan struct{}), waited: make(chan struct{})}
}

func (b *blockingServers) Close() {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	b.closeOnce.Do(func() { close(b.closed) })
}

func (b *blockingServers) Wait() error {
	b.mu.Lock()
	b.waits++
	b.mu.Unlock()
	<-b.release
	b.waitOnce.Do(func() { close(b.waited) })
	return nil
}

func (b *blockingServers) WaitCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.waits
}

func (b *blockingServers) CloseCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes
}

func TestStartValidSpecReturnsReadyServerInfoAndExplicitZoneConfig(t *testing.T) {
	fake := &fakeServers{}
	starter := &fakeStarter{servers: fake}
	manager := newManager(starter)
	spec := validServerSpec()

	info, err := manager.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if info.ID != spec.ID || info.State != dhcp.ServerStateReady || info.LeaseCount != 0 {
		t.Fatalf("unexpected info: %#v", info)
	}
	if len(starter.started) != 1 {
		t.Fatalf("expected one CoreDHCP start, got %d", len(starter.started))
	}
	cfg := starter.started[0]
	if cfg.Server4 == nil || len(cfg.Server4.Addresses) != 1 {
		t.Fatalf("expected one DHCPv4 listen address, got %#v", cfg.Server4)
	}
	addr := cfg.Server4.Addresses[0]
	if !addr.IP.Equal(net.IP(spec.ListenAddr.AsSlice())) || addr.Port != int(spec.ListenPort) || addr.Zone != string(spec.InterfaceName) {
		t.Fatalf("unexpected listen address: %#v", addr)
	}
	if len(cfg.Server4.Plugins) != 1 || cfg.Server4.Plugins[0].Name != govirtaPluginName || cfg.Server4.Plugins[0].Args[0] != string(spec.ID) {
		t.Fatalf("unexpected plugins: %#v", cfg.Server4.Plugins)
	}
}

func TestStartDuplicateServerReturnsAlreadyRunning(t *testing.T) {
	manager := newManager(&fakeStarter{servers: &fakeServers{}})
	spec := validServerSpec()

	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	_, err := manager.Start(context.Background(), spec)
	if !errors.Is(err, dhcperr.ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}
}

func TestStartFailureDoesNotKeepRuntime(t *testing.T) {
	starter := &fakeStarter{err: syscallAddrInUse()}
	manager := newManager(starter)
	spec := validServerSpec()

	_, err := manager.Start(context.Background(), spec)
	if !errors.Is(err, dhcperr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	_, err = manager.GetServer(context.Background(), spec.ID)
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after failed start, got %v", err)
	}
}

func TestStartRegisterRuntimeFailureDoesNotBlockConcurrentStop(t *testing.T) {
	// Two Managers sharing one ServerID is supported: the first wins the global
	// runtime registry, so the second Manager's Start must fail at
	// registerRuntime. The regression guard: a registration failure must never
	// leave an rt that is visible to a concurrent Stop while its startDone is
	// never closed, otherwise that Stop would block forever on <-startDone.
	// Because Start now registers before publishing to m.servers, a failed rt is
	// never visible to Stop, so a Stop racing the failing Start returns promptly.
	//
	// On the pre-fix code the failing Start published rt to m.servers before
	// registerRuntime, opening a window where a concurrent Stop could flip the rt
	// to Stopping and then deadlock on <-startDone (the failure branch never
	// closed it). This test races Start and Stop and fails via timeout if that
	// window ever reopens.
	firstManager := newManager(&fakeStarter{servers: &fakeServers{}})
	secondManager := newManager(&fakeStarter{servers: &fakeServers{}})
	spec := validServerSpec()
	t.Cleanup(func() { runtimeRegistry.Delete(string(spec.ID)) })

	if _, err := firstManager.Start(context.Background(), spec); err != nil {
		t.Fatalf("first Start returned error: %v", err)
	}

	startDone := make(chan error, 1)
	stopDone := make(chan error, 1)
	go func() {
		_, err := secondManager.Start(context.Background(), spec)
		startDone <- err
	}()
	go func() {
		stopDone <- secondManager.Stop(context.Background(), spec.ID)
	}()

	select {
	case err := <-startDone:
		if !errors.Is(err, dhcperr.ErrAlreadyRunning) {
			t.Fatalf("expected ErrAlreadyRunning from second Start, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("second Start deadlocked after registerRuntime conflict")
	}
	select {
	case err := <-stopDone:
		if !errors.Is(err, dhcperr.ErrNotFound) {
			t.Fatalf("expected ErrNotFound from concurrent Stop, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("concurrent Stop deadlocked after registerRuntime failure")
	}

	// The failing Start must never publish its rt to the second Manager.
	if _, err := secondManager.GetServer(context.Background(), spec.ID); !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("failed registration must leave no runtime in second Manager, got %v", err)
	}
	// The first Manager still owns a healthy server and can stop normally.
	if err := firstManager.Stop(context.Background(), spec.ID); err != nil {
		t.Fatalf("first Stop returned error: %v", err)
	}
}

func TestStartBlockedStarterDoesNotHoldManagerLock(t *testing.T) {
	starter := newBlockingStarter(&fakeServers{})
	manager := newManager(starter)
	spec := validServerSpec()

	startDone := make(chan error, 1)
	go func() {
		_, err := manager.Start(context.Background(), spec)
		startDone <- err
	}()
	waitForClosed(t, starter.entered, "starter to block")

	info, err := manager.GetServer(context.Background(), spec.ID)
	if err != nil {
		t.Fatalf("GetServer during Start returned error: %v", err)
	}
	if info.State != dhcp.ServerStateStarting {
		t.Fatalf("expected starting state, got %#v", info)
	}

	otherDone := make(chan error, 1)
	go func() {
		_, err := manager.GetServer(context.Background(), dhcp.ServerID("other"))
		otherDone <- err
	}()
	select {
	case err := <-otherDone:
		if !errors.Is(err, dhcperr.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for other server, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("GetServer for another server blocked while starter.Start was blocked")
	}

	close(starter.release)
	if err := <-startDone; err != nil {
		t.Fatalf("Start returned error after release: %v", err)
	}
}

func TestValidateServerSpecRejectsInvalidFields(t *testing.T) {
	valid := validServerSpec()
	tests := []struct {
		name string
		mut  func(*dhcp.ServerSpec)
		want error
	}{
		{name: "missing ID", mut: func(s *dhcp.ServerSpec) { s.ID = "" }, want: dhcperr.ErrInvalidRequest},
		{name: "missing interface", mut: func(s *dhcp.ServerSpec) { s.InterfaceName = "" }, want: dhcperr.ErrInvalidRequest},
		{name: "invalid listen", mut: func(s *dhcp.ServerSpec) { s.ListenAddr = netip.Addr{} }, want: dhcperr.ErrInvalidRequest},
		{name: "ipv6 listen", mut: func(s *dhcp.ServerSpec) { s.ListenAddr = netip.MustParseAddr("2001:db8::1") }, want: dhcperr.ErrInvalidRequest},
		{name: "zero port", mut: func(s *dhcp.ServerSpec) { s.ListenPort = 0 }, want: dhcperr.ErrInvalidRequest},
		{name: "server outside subnet", mut: func(s *dhcp.ServerSpec) { s.ServerAddr = netip.MustParseAddr("192.168.101.1") }, want: dhcperr.ErrInvalidRequest},
		{name: "invalid subnet", mut: func(s *dhcp.ServerSpec) { s.Subnet = netip.Prefix{} }, want: dhcperr.ErrInvalidRequest},
		{name: "pool outside subnet", mut: func(s *dhcp.ServerSpec) { s.Pool.End = netip.MustParseAddr("192.168.101.10") }, want: dhcperr.ErrInvalidRequest},
		{name: "pool start after end", mut: func(s *dhcp.ServerSpec) { s.Pool.Start, s.Pool.End = s.Pool.End, s.Pool.Start }, want: dhcperr.ErrInvalidRequest},
		{name: "pool contains server address", mut: func(s *dhcp.ServerSpec) { s.Pool.Start = s.ServerAddr }, want: dhcperr.ErrInvalidRequest},
		{name: "zero lease", mut: func(s *dhcp.ServerSpec) { s.LeaseDuration = 0 }, want: dhcperr.ErrInvalidRequest},
		{name: "empty router mode", mut: func(s *dhcp.ServerSpec) { s.Router.Mode = "" }, want: dhcperr.ErrInvalidRequest},
		{name: "enabled router without address", mut: func(s *dhcp.ServerSpec) { s.Router = dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled} }, want: dhcperr.ErrInvalidRequest},
		{name: "disabled dns with address", mut: func(s *dhcp.ServerSpec) {
			s.DNS = dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionDisabled, Addrs: []netip.Addr{s.ServerAddr}}
		}, want: dhcperr.ErrInvalidRequest},
		{name: "empty bind mode", mut: func(s *dhcp.ServerSpec) { s.BindMode = "" }, want: dhcperr.ErrInvalidRequest},
		{name: "unsupported bind mode", mut: func(s *dhcp.ServerSpec) { s.BindMode = dhcp.BindMode("wildcard") }, want: dhcperr.ErrUnsupported},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid
			tt.mut(&spec)
			err := validateServerSpec(spec)
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestValidateContextRejectsNilAndCanceled(t *testing.T) {
	if err := checkContext(nilContext()); !errors.Is(err, dhcperr.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestStopMissingServerReturnsNotFound(t *testing.T) {
	manager := newManager(&fakeStarter{servers: &fakeServers{}})
	err := manager.Stop(context.Background(), dhcp.ServerID("missing"))
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStopExistingServerClosesWaitsAndRemovesRuntime(t *testing.T) {
	fake := &fakeServers{}
	manager := newManager(&fakeStarter{servers: fake})
	spec := validServerSpec()
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if err := manager.Stop(context.Background(), spec.ID); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if !fake.closed || !fake.waited {
		t.Fatalf("expected Close and Wait, got closed=%v waited=%v", fake.closed, fake.waited)
	}
	_, err := manager.GetServer(context.Background(), spec.ID)
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after stop, got %v", err)
	}
}

// TestRestartReplayRestoresBindings encodes the process-memory-only contract
// documented on the DHCP Manager: bindings live solely in the runtime's memory,
// so Stop discards them and a restarted server starts empty. Upper layers must
// replay Start + ApplyBinding to restore a binding; the manager never persists
// or auto-restores it. This proves both halves: the binding is gone after a
// stop/start cycle, and an explicit replay brings it back.
func TestRestartReplayRestoresBindings(t *testing.T) {
	spec := validServerSpec()
	mac := "02:00:00:00:00:01"
	ip := "192.168.100.10"

	manager := newManager(&fakeStarter{servers: &fakeServers{}})
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("first Start returned error: %v", err)
	}
	if _, err := manager.ApplyBinding(context.Background(), bindingRequest(spec.ID, mac, ip)); err != nil {
		t.Fatalf("first ApplyBinding returned error: %v", err)
	}
	query := dhcp.BindingQuery{ServerID: spec.ID, MAC: mustMAC(mac)}
	if _, err := manager.GetLease(context.Background(), query); err != nil {
		t.Fatalf("GetLease before restart returned error: %v", err)
	}

	if err := manager.Stop(context.Background(), spec.ID); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	// Re-Start the same logical server with a fresh listener (the first
	// fakeServers is closed). Bindings are process-memory only, so the
	// restarted runtime must start empty until the caller replays them.
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("second Start returned error: %v", err)
	}
	if _, err := manager.GetLease(context.Background(), query); !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("binding must not survive restart (process-memory only), GetLease error = %v, want ErrNotFound", err)
	}

	// Explicit replay restores the binding; nothing auto-restores it.
	replayed, err := manager.ApplyBinding(context.Background(), bindingRequest(spec.ID, mac, ip))
	if err != nil {
		t.Fatalf("replay ApplyBinding returned error: %v", err)
	}
	if replayed.State != dhcp.LeaseStateReserved || replayed.IP != netip.MustParseAddr(ip) {
		t.Fatalf("replayed lease = %#v, want reserved lease for %s", replayed, ip)
	}
	if _, err := manager.GetLease(context.Background(), query); err != nil {
		t.Fatalf("GetLease after replay returned error: %v", err)
	}
}

// TestStopCanceledContextStillCleansUp encodes the post-fix contract: once Stop
// is reached with a valid (non-nil) context and a running server, a canceled
// caller context must NOT abort cleanup. Stop takes ownership and runs
// Close/Wait to completion so the CoreDHCP listener is released and the runtime
// reaches stopped — never left running with state stuck in stopping. This
// replaces the prior test that asserted the buggy early-return-on-cancel
// behavior (a canceled Stop used to leak the listener).
func TestStopCanceledContextStillCleansUp(t *testing.T) {
	fake := &fakeServers{}
	manager := newManager(&fakeStarter{servers: fake})
	spec := validServerSpec()
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Stop(ctx, spec.ID); err != nil {
		t.Fatalf("Stop with canceled context returned error: %v", err)
	}
	if !fake.closed || !fake.waited {
		t.Fatalf("canceled Stop must still Close/Wait once it owns cleanup, got closed=%v waited=%v", fake.closed, fake.waited)
	}
	_, err := manager.GetServer(context.Background(), spec.ID)
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after canceled Stop completed cleanup, got %v", err)
	}
}

func TestStopNilContextReturnsInvalidRequest(t *testing.T) {
	fake := &fakeServers{}
	manager := newManager(&fakeStarter{servers: fake})
	spec := validServerSpec()
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	//nolint:staticcheck // intentionally passing a nil context to assert validation
	if err := manager.Stop(nil, spec.ID); !errors.Is(err, dhcperr.ErrInvalidRequest) {
		t.Fatalf("Stop(nil ctx) error = %v, want ErrInvalidRequest", err)
	}
	if fake.closed || fake.waited {
		t.Fatalf("nil-context Stop must reject before any cleanup, got closed=%v waited=%v", fake.closed, fake.waited)
	}
}

func TestStopKeepsRuntimeStoppingUntilWaitCompletes(t *testing.T) {
	servers := newBlockingServers()
	manager := newManager(&fakeStarter{servers: servers})
	spec := validServerSpec()
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- manager.Stop(context.Background(), spec.ID)
	}()
	waitForClosed(t, servers.closed, "server close")

	info, err := manager.GetServer(context.Background(), spec.ID)
	if err != nil {
		t.Fatalf("GetServer during Stop returned error: %v", err)
	}
	if info.State != dhcp.ServerStateStopping {
		t.Fatalf("expected stopping state, got %#v", info)
	}

	_, err = manager.Start(context.Background(), spec)
	if !errors.Is(err, dhcperr.ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning while stopping, got %v", err)
	}

	close(servers.release)
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop returned error after release: %v", err)
	}
	waitForClosed(t, servers.waited, "server wait")
	_, err = manager.GetServer(context.Background(), spec.ID)
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after stop completed, got %v", err)
	}
}

// TestApplyBindingDuringStopReturnsNotRunning proves the binding state gate: a
// concurrent Stop transitions the runtime to Stopping (blocked here on Wait via
// the release channel), and ApplyBinding must refuse with ErrNotRunning rather
// than orphan a binding on a listener that is tearing down.
func TestApplyBindingDuringStopReturnsNotRunning(t *testing.T) {
	servers := newBlockingServers()
	manager := newManager(&fakeStarter{servers: servers})
	spec := validServerSpec()
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- manager.Stop(context.Background(), spec.ID)
	}()
	waitForClosed(t, servers.closed, "server close")

	// The runtime is now Stopping (Stop is blocked on Wait until release).
	_, err := manager.ApplyBinding(context.Background(), bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.10"))
	if !errors.Is(err, dhcperr.ErrNotRunning) {
		t.Fatalf("ApplyBinding during Stop error = %v, want ErrNotRunning", err)
	}

	close(servers.release)
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop returned error after release: %v", err)
	}
}

func TestStopConcurrentSameServerHasSingleOwner(t *testing.T) {
	servers := newBlockingServers()
	manager := newManager(&fakeStarter{servers: servers})
	spec := validServerSpec()
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	firstStopDone := make(chan error, 1)
	go func() {
		firstStopDone <- manager.Stop(context.Background(), spec.ID)
	}()
	waitForClosed(t, servers.closed, "first stop close")
	waitUntil(t, "first stop wait call", func() bool { return servers.WaitCount() == 1 })

	secondErr := manager.Stop(context.Background(), spec.ID)
	if !errors.Is(secondErr, dhcperr.ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning from concurrent Stop, got %v", secondErr)
	}
	if waits := servers.WaitCount(); waits != 1 {
		t.Fatalf("expected one Wait call, got %d", waits)
	}
	if closes := servers.CloseCount(); closes != 1 {
		t.Fatalf("expected one Close call, got %d", closes)
	}

	close(servers.release)
	if err := <-firstStopDone; err != nil {
		t.Fatalf("first Stop returned error after release: %v", err)
	}
	_, err := manager.GetServer(context.Background(), spec.ID)
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after stop completed, got %v", err)
	}
}

func TestStopStartingServerWaitsForStartAndKeepsReservation(t *testing.T) {
	servers := newBlockingServers()
	starter := newBlockingStarter(servers)
	manager := newManager(starter)
	spec := validServerSpec()

	startDone := make(chan error, 1)
	go func() {
		_, err := manager.Start(context.Background(), spec)
		startDone <- err
	}()
	waitForClosed(t, starter.entered, "starter to block")

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- manager.Stop(context.Background(), spec.ID)
	}()
	waitUntil(t, "stopping state while start is blocked", func() bool {
		info, err := manager.GetServer(context.Background(), spec.ID)
		return err == nil && info.State == dhcp.ServerStateStopping
	})

	_, err := manager.Start(context.Background(), spec)
	if !errors.Is(err, dhcperr.ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning while starting server is stopping, got %v", err)
	}

	close(starter.release)
	waitForClosed(t, servers.closed, "server close after starter release")
	waitUntil(t, "server wait call", func() bool { return servers.WaitCount() == 1 })
	if closes := servers.CloseCount(); closes != 1 {
		t.Fatalf("expected one Close call, got %d", closes)
	}
	if waits := servers.WaitCount(); waits != 1 {
		t.Fatalf("expected one Wait call, got %d", waits)
	}

	startErr := <-startDone
	if !errors.Is(startErr, dhcperr.ErrNotRunning) {
		t.Fatalf("expected Start to return ErrNotRunning, got %v", startErr)
	}
	close(servers.release)
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop returned error after release: %v", err)
	}
	_, err = manager.GetServer(context.Background(), spec.ID)
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after stop completed, got %v", err)
	}
}

func TestGetServerReturnsObservedReadyState(t *testing.T) {
	manager := newManager(&fakeStarter{servers: &fakeServers{}})
	spec := validServerSpec()
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	info, err := manager.GetServer(context.Background(), spec.ID)
	if err != nil {
		t.Fatalf("GetServer returned error: %v", err)
	}
	if info.State != dhcp.ServerStateReady || info.ID != spec.ID || info.InterfaceName != spec.InterfaceName {
		t.Fatalf("unexpected info: %#v", info)
	}
}

func TestPluginRegistrationIsIdempotent(t *testing.T) {
	restoreRegisteredPlugin(t)
	setRegisteredPlugin(nil)

	if err := registerPlaceholderPlugin(); err != nil {
		t.Fatalf("first registerPlaceholderPlugin returned error: %v", err)
	}
	if err := registerPlaceholderPlugin(); err != nil {
		t.Fatalf("second registerPlaceholderPlugin returned error: %v", err)
	}
	if plugins.RegisteredPlugins[govirtaPluginName] != govirtaPlugin {
		t.Fatalf("expected registered Govirta plugin pointer")
	}
}

func TestPluginRegistrationConflictReturnsErrConflict(t *testing.T) {
	restoreRegisteredPlugin(t)
	setRegisteredPlugin(&plugins.Plugin{Name: govirtaPluginName, Setup4: placeholderSetup4})

	err := registerPlaceholderPlugin()
	if !errors.Is(err, dhcperr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func validServerSpec() dhcp.ServerSpec {
	serverAddr := netip.MustParseAddr("192.168.100.1")
	seq := testServerSeq.Add(1)
	return dhcp.ServerSpec{
		ID:            dhcp.ServerID("gvbr0-192.168.100.0-24-" + strconv.FormatUint(seq, 10)),
		InterfaceName: link.Name("gvbr0"),
		ListenAddr:    netip.MustParseAddr("0.0.0.0"),
		ListenPort:    dhcp.Port(67),
		ServerAddr:    serverAddr,
		Subnet:        netip.MustParsePrefix("192.168.100.0/24"),
		Pool: dhcp.AddressRange{
			Start: netip.MustParseAddr("192.168.100.10"),
			End:   netip.MustParseAddr("192.168.100.20"),
		},
		LeaseDuration: time.Hour,
		Router:        dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{serverAddr}},
		DNS:           dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionDisabled},
		BindMode:      dhcp.BindModeInterfaceZone,
	}
}

func nilContext() context.Context { return nil }

func syscallAddrInUse() error {
	return &net.OpError{Err: syscall.EADDRINUSE}
}

func waitForClosed(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitUntil(t *testing.T, name string, condition func() bool) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", name)
		case <-tick.C:
		}
	}
}

func restoreRegisteredPlugin(t *testing.T) {
	t.Helper()
	pluginMu.Lock()
	original, existed := plugins.RegisteredPlugins[govirtaPluginName]
	pluginMu.Unlock()
	t.Cleanup(func() {
		pluginMu.Lock()
		defer pluginMu.Unlock()
		if existed {
			plugins.RegisteredPlugins[govirtaPluginName] = original
		} else {
			delete(plugins.RegisteredPlugins, govirtaPluginName)
		}
	})
}

func setRegisteredPlugin(plugin *plugins.Plugin) {
	pluginMu.Lock()
	defer pluginMu.Unlock()
	if plugin == nil {
		delete(plugins.RegisteredPlugins, govirtaPluginName)
		return
	}
	plugins.RegisteredPlugins[govirtaPluginName] = plugin
}

// placeholderSetup4 is a test-only CoreDHCP Setup4 that forwards to the real
// runtime-backed handler builder. It exists so plugin-registration tests can
// install a Setup4 without the production manager carrying a test-only shim.
func placeholderSetup4(args ...string) (handler.Handler4, error) {
	return setupHandler4(args...)
}
