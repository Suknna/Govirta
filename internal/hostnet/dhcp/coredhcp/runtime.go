package coredhcp

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/coredhcp/coredhcp/config"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/dhcp/dhcperr"
)

var runtimeRegistry sync.Map

type coreServerStarter interface {
	Start(*config.Config) (coreServers, error)
}

type coreServers interface {
	Close()
	Wait() error
}

type leaseRecord struct {
	serverID  dhcp.ServerID
	mac       net.HardwareAddr
	ip        netip.Addr
	hostname  dhcp.BindingHostname
	state     dhcp.LeaseState
	expiresAt time.Time
}

type serverRuntime struct {
	mu sync.RWMutex

	spec  dhcp.ServerSpec
	state dhcp.ServerState
	now   func() time.Time

	startDone chan struct{}

	bindingsByMAC map[string]*leaseRecord
	bindingsByIP  map[netip.Addr]*leaseRecord

	coreServers coreServers
}

func newServerRuntime(spec dhcp.ServerSpec) *serverRuntime {
	return &serverRuntime{
		spec:          spec,
		state:         dhcp.ServerStateStarting,
		now:           time.Now,
		startDone:     make(chan struct{}),
		bindingsByMAC: make(map[string]*leaseRecord),
		bindingsByIP:  make(map[netip.Addr]*leaseRecord),
	}
}

func macKey(mac net.HardwareAddr) string {
	return strings.ToLower(mac.String())
}

func registerRuntime(rt *serverRuntime) error {
	if rt == nil {
		return fmt.Errorf("%w: runtime is nil", dhcperr.ErrInvalidObservedState)
	}
	rt.mu.RLock()
	id := rt.spec.ID
	rt.mu.RUnlock()
	if id == "" {
		return fmt.Errorf("%w: runtime server ID is empty", dhcperr.ErrInvalidObservedState)
	}
	actual, loaded := runtimeRegistry.LoadOrStore(string(id), rt)
	if !loaded || actual == rt {
		return nil
	}
	return fmt.Errorf("%w: CoreDHCP runtime %q", dhcperr.ErrAlreadyRunning, id)
}

func unregisterRuntime(id dhcp.ServerID, rt *serverRuntime) {
	if id == "" || rt == nil {
		return
	}
	runtimeRegistry.CompareAndDelete(string(id), rt)
}

func lookupRuntime(id dhcp.ServerID) (*serverRuntime, error) {
	value, ok := runtimeRegistry.Load(string(id))
	if !ok {
		return nil, fmt.Errorf("%w: CoreDHCP runtime %q", dhcperr.ErrNotFound, id)
	}
	rt, ok := value.(*serverRuntime)
	if !ok {
		return nil, fmt.Errorf("%w: CoreDHCP runtime %q has type %T", dhcperr.ErrInvalidObservedState, id, value)
	}
	return rt, nil
}

func (rt *serverRuntime) applyBinding(req dhcp.BindingRequest) (dhcp.LeaseInfo, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	// Gate binding mutation on runtime state: a concurrent Stop transitions the
	// runtime to stopping/stopped, after which a new binding would be orphaned
	// (the listener is tearing down and upper layers must replay on restart).
	// Mirror bindLease, which already refuses to bind a lease once stopping.
	if rt.state == dhcp.ServerStateStopping || rt.state == dhcp.ServerStateStopped {
		return dhcp.LeaseInfo{}, fmt.Errorf("%w: DHCP server %s is %s", dhcperr.ErrNotRunning, req.ServerID, rt.state)
	}

	key := macKey(req.MAC)
	if existing := rt.bindingsByMAC[key]; existing != nil {
		if existing.ip != req.IP {
			return dhcp.LeaseInfo{}, fmt.Errorf("%w: MAC %s already bound to %s", dhcperr.ErrConflict, req.MAC, existing.ip)
		}
		// Reconcile the mutable hostname toward the requested state so a
		// re-ApplyBinding with the same MAC+IP but a changed hostname is not
		// silently dropped (observed-state-as-truth). Immutable identity (MAC,
		// IP) is unchanged; only the hostname is updated.
		existing.hostname = req.Hostname
		return leaseInfo(existing), nil
	}
	if existing := rt.bindingsByIP[req.IP]; existing != nil {
		return dhcp.LeaseInfo{}, fmt.Errorf("%w: IP %s already bound to %s", dhcperr.ErrConflict, req.IP, existing.mac)
	}

	record := &leaseRecord{
		serverID:  req.ServerID,
		mac:       append(net.HardwareAddr(nil), req.MAC...),
		ip:        req.IP,
		hostname:  req.Hostname,
		state:     dhcp.LeaseStateReserved,
		expiresAt: time.Time{},
	}
	rt.bindingsByMAC[key] = record
	rt.bindingsByIP[req.IP] = record
	return leaseInfo(record), nil
}

func (rt *serverRuntime) removeBinding(query dhcp.BindingQuery) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	// Gate on runtime state, symmetric with applyBinding: once a concurrent Stop
	// has moved the runtime to stopping/stopped the binding table is being torn
	// down, so a mutation returns ErrNotRunning rather than racing teardown.
	if rt.state == dhcp.ServerStateStopping || rt.state == dhcp.ServerStateStopped {
		return fmt.Errorf("%w: DHCP server %s is %s", dhcperr.ErrNotRunning, query.ServerID, rt.state)
	}

	key := macKey(query.MAC)
	record := rt.bindingsByMAC[key]
	if record == nil {
		return fmt.Errorf("%w: binding for MAC %s", dhcperr.ErrNotFound, query.MAC)
	}
	delete(rt.bindingsByMAC, key)
	delete(rt.bindingsByIP, record.ip)
	return nil
}

func (rt *serverRuntime) getLease(query dhcp.BindingQuery) (dhcp.LeaseInfo, error) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	record := rt.bindingsByMAC[macKey(query.MAC)]
	if record == nil {
		return dhcp.LeaseInfo{}, fmt.Errorf("%w: binding for MAC %s", dhcperr.ErrNotFound, query.MAC)
	}
	return leaseInfo(record), nil
}

func (rt *serverRuntime) bindingForMAC(mac net.HardwareAddr) (dhcp.ServerSpec, dhcp.ServerState, leaseRecord, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	record := rt.bindingsByMAC[macKey(mac)]
	if record == nil {
		return rt.spec, rt.state, leaseRecord{}, false
	}
	return rt.spec, rt.state, *record, true
}

func (rt *serverRuntime) bindLease(mac net.HardwareAddr) (dhcp.ServerSpec, leaseRecord, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	record := rt.bindingsByMAC[macKey(mac)]
	if record == nil || rt.state == dhcp.ServerStateStopping || rt.state == dhcp.ServerStateStopped {
		return rt.spec, leaseRecord{}, false
	}
	record.state = dhcp.LeaseStateBound
	record.expiresAt = rt.now().Add(rt.spec.LeaseDuration)
	return rt.spec, *record, true
}

func (rt *serverRuntime) listLeases() []dhcp.LeaseInfo {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	records := make([]*leaseRecord, 0, len(rt.bindingsByMAC))
	for _, record := range rt.bindingsByMAC {
		records = append(records, record)
	}
	return sortedLeaseInfos(records)
}
