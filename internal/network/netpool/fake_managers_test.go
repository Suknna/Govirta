package netpool

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/route"
)

// recorder captures the ordered sequence of primitive Manager method calls made
// by the orchestration service. A single recorder is shared by all four fakes
// in a test so the cross-manager call order is observable.
type recorder struct {
	mu    sync.Mutex
	seq   []string
	calls map[string]int
}

func newRecorder() *recorder {
	return &recorder{calls: make(map[string]int)}
}

func (r *recorder) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq = append(r.seq, name)
	r.calls[name]++
}

// sequence returns a copy of the recorded call order.
func (r *recorder) sequence() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.seq))
	copy(out, r.seq)
	return out
}

// count returns how many times the named method was recorded.
func (r *recorder) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

// reset clears all recorded calls. Useful between an Ensure phase and a Delete
// phase so the delete-phase slice is clean.
func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq = nil
	r.calls = make(map[string]int)
}

// fakeLink is a recording link.Manager. It captures the last EnsureBridge and
// EnsureTap specs, records Delete targets, and returns observed-style info.
type fakeLink struct {
	rec        *recorder
	errs       map[string]error
	bridgeSpec link.BridgeSpec
	tapSpec    link.TapSpec
	deleted    []link.Name
}

var _ link.Manager = (*fakeLink)(nil)

func (f *fakeLink) EnsureBridge(_ context.Context, spec link.BridgeSpec) (link.LinkInfo, error) {
	f.rec.record("link.EnsureBridge")
	f.bridgeSpec = spec
	if err := f.errs["EnsureBridge"]; err != nil {
		return link.LinkInfo{}, err
	}
	return link.LinkInfo{
		Name:       spec.Name,
		Kind:       link.KindBridge,
		MTU:        spec.MTU,
		MAC:        spec.MAC,
		AdminState: link.AdminStateUp,
	}, nil
}

func (f *fakeLink) EnsureTap(_ context.Context, spec link.TapSpec) (link.LinkInfo, error) {
	f.rec.record("link.EnsureTap")
	f.tapSpec = spec
	if err := f.errs["EnsureTap"]; err != nil {
		return link.LinkInfo{}, err
	}
	return link.LinkInfo{
		Name:       spec.Name,
		Kind:       link.KindTap,
		MTU:        spec.MTU,
		MAC:        spec.MAC,
		MasterName: spec.BridgeName,
		AdminState: link.AdminStateUp,
	}, nil
}

func (f *fakeLink) Delete(_ context.Context, name link.Name) error {
	f.rec.record("link.Delete")
	f.deleted = append(f.deleted, name)
	return f.errs["Delete"]
}

func (f *fakeLink) Exists(_ context.Context, _ link.Name) (bool, error) {
	f.rec.record("link.Exists")
	if err := f.errs["Exists"]; err != nil {
		return false, err
	}
	return true, nil
}

func (f *fakeLink) Get(_ context.Context, name link.Name) (link.LinkInfo, error) {
	f.rec.record("link.Get")
	if err := f.errs["Get"]; err != nil {
		return link.LinkInfo{}, err
	}
	return link.LinkInfo{Name: name, AdminState: link.AdminStateUp}, nil
}

func (f *fakeLink) List(_ context.Context, _ link.ListFilter) ([]link.LinkInfo, error) {
	f.rec.record("link.List")
	if err := f.errs["List"]; err != nil {
		return nil, err
	}
	return nil, nil
}

// fakeRoute is a recording route.Manager.
type fakeRoute struct {
	rec  *recorder
	errs map[string]error
}

var _ route.Manager = (*fakeRoute)(nil)

func (f *fakeRoute) GetIPv4Forwarding(_ context.Context) (route.IPv4ForwardingInfo, error) {
	f.rec.record("route.GetIPv4Forwarding")
	if err := f.errs["GetIPv4Forwarding"]; err != nil {
		return route.IPv4ForwardingInfo{}, err
	}
	return route.IPv4ForwardingInfo{State: route.IPv4ForwardingEnabled, Path: "/proc/sys/net/ipv4/ip_forward"}, nil
}

func (f *fakeRoute) CheckIPv4Forwarding(_ context.Context, expected route.IPv4ForwardingState) (route.IPv4ForwardingInfo, error) {
	f.rec.record("route.CheckIPv4Forwarding")
	if err := f.errs["CheckIPv4Forwarding"]; err != nil {
		return route.IPv4ForwardingInfo{}, err
	}
	return route.IPv4ForwardingInfo{State: expected, Path: "/proc/sys/net/ipv4/ip_forward"}, nil
}

func (f *fakeRoute) AddRoute(_ context.Context, _ route.RouteSpec) (route.RouteInfo, error) {
	f.rec.record("route.AddRoute")
	return route.RouteInfo{}, f.errs["AddRoute"]
}

func (f *fakeRoute) ReplaceRoute(_ context.Context, _ route.RouteSpec) (route.RouteInfo, error) {
	f.rec.record("route.ReplaceRoute")
	return route.RouteInfo{}, f.errs["ReplaceRoute"]
}

func (f *fakeRoute) DeleteRoute(_ context.Context, _ route.RouteSpec) error {
	f.rec.record("route.DeleteRoute")
	return f.errs["DeleteRoute"]
}

func (f *fakeRoute) ListRoutes(_ context.Context, _ route.RouteFilter) ([]route.RouteInfo, error) {
	f.rec.record("route.ListRoutes")
	return nil, f.errs["ListRoutes"]
}

func (f *fakeRoute) GetRoute(_ context.Context, _ route.RouteQuery) (route.RouteInfo, error) {
	f.rec.record("route.GetRoute")
	return route.RouteInfo{}, f.errs["GetRoute"]
}

// fakeFirewall is a recording firewall.Manager. Ensure* methods return rule
// info with stable handles (masquerade=1, forward-accept=2, anti-spoofing=3) so
// the orchestration layer can carry refs forward to deletes.
type fakeFirewall struct {
	rec          *recorder
	errs         map[string]error
	masqSpec     firewall.MasqueradeSpec
	forwardSpec  firewall.ForwardAcceptSpec
	endpointSpec firewall.EndpointAntiSpoofingSpec
	deletedRefs  []firewall.RuleRef
	// rules holds the observable rules an Ensure call created, so ListRules
	// can return them live the way a real firewall would. Network masquerade
	// and forward-accept rules are unique per identity; endpoint rules are keyed
	// additionally by MAC so multiple NICs on one chain stay distinct.
	rules []firewall.RuleInfo
}

// storeRule replaces any existing observable rule sharing the new rule's logical
// identity, then records the new rule. For endpoint anti-spoofing rules the
// identity also includes the observed MAC so distinct NICs coexist.
func (f *fakeFirewall) storeRule(info firewall.RuleInfo) {
	newMAC := ""
	if info.Summary.EndpointAntiSpoofing != nil {
		newMAC = info.Summary.EndpointAntiSpoofing.MAC.String()
	}
	var kept []firewall.RuleInfo
	for _, r := range f.rules {
		sameIdentity := r.Ref.Purpose == info.Ref.Purpose &&
			r.Ref.TableName == info.Ref.TableName &&
			r.Ref.ChainName == info.Ref.ChainName
		if sameIdentity && newMAC != "" {
			oldMAC := ""
			if r.Summary.EndpointAntiSpoofing != nil {
				oldMAC = r.Summary.EndpointAntiSpoofing.MAC.String()
			}
			if oldMAC == newMAC {
				continue
			}
			kept = append(kept, r)
			continue
		}
		if sameIdentity {
			continue
		}
		kept = append(kept, r)
	}
	f.rules = append(kept, info)
}

// ruleMatchesFilter reports whether a stored rule satisfies every value-mode
// dimension of filter; any-mode dimensions match unconditionally.
func ruleMatchesFilter(rule firewall.RuleInfo, filter firewall.RuleFilter) bool {
	if filter.Owner.Mode == firewall.OwnerValue && rule.Ref.Owner != filter.Owner.Value {
		return false
	}
	if filter.Purpose.Mode == firewall.PurposeValue && rule.Ref.Purpose != filter.Purpose.Value {
		return false
	}
	if filter.Family.Mode == firewall.FamilyValue && rule.Ref.Family != filter.Family.Value {
		return false
	}
	if filter.Table.Mode == firewall.TableValue && rule.Ref.TableName != filter.Table.Value {
		return false
	}
	if filter.Chain.Mode == firewall.ChainValue && rule.Ref.ChainName != filter.Chain.Value {
		return false
	}
	return true
}

var _ firewall.Manager = (*fakeFirewall)(nil)

func (f *fakeFirewall) EnsureMasquerade(_ context.Context, spec firewall.MasqueradeSpec) (firewall.RuleInfo, error) {
	f.rec.record("firewall.EnsureMasquerade")
	f.masqSpec = spec
	if err := f.errs["EnsureMasquerade"]; err != nil {
		return firewall.RuleInfo{}, err
	}
	info := firewall.RuleInfo{
		Ref: firewall.RuleRef{
			Owner:     spec.RuleOwner,
			Purpose:   firewall.RulePurposeMasquerade,
			Family:    firewall.TableFamilyIPv4,
			TableName: spec.TableName,
			ChainName: spec.ChainName,
			Handle:    1,
		},
		Summary: firewall.RuleSummary{Masquerade: &firewall.MasqueradeSummary{
			GuestCIDR:           spec.GuestCIDR,
			EgressInterfaceName: spec.EgressInterfaceName,
			Priority:            spec.Priority,
		}},
	}
	f.storeRule(info)
	return info, nil
}

func (f *fakeFirewall) DeleteMasquerade(_ context.Context, ref firewall.RuleRef) error {
	f.rec.record("firewall.DeleteMasquerade")
	f.deletedRefs = append(f.deletedRefs, ref)
	return f.errs["DeleteMasquerade"]
}

func (f *fakeFirewall) EnsureEndpointAntiSpoofing(_ context.Context, spec firewall.EndpointAntiSpoofingSpec) (firewall.RuleInfo, error) {
	f.rec.record("firewall.EnsureEndpointAntiSpoofing")
	f.endpointSpec = spec
	if err := f.errs["EnsureEndpointAntiSpoofing"]; err != nil {
		return firewall.RuleInfo{}, err
	}
	info := firewall.RuleInfo{
		Ref: firewall.RuleRef{
			Owner:     spec.RuleOwner,
			Purpose:   firewall.RulePurposeEndpointAntiSpoofing,
			Family:    firewall.TableFamilyBridge,
			TableName: spec.TableName,
			ChainName: spec.ChainName,
			Handle:    3,
		},
		Summary: firewall.RuleSummary{EndpointAntiSpoofing: &firewall.EndpointAntiSpoofingSummary{
			BridgeName: spec.BridgeName,
			TapName:    spec.TapName,
			MAC:        spec.MAC,
			IPv4:       spec.IPv4,
			Priority:   spec.Priority,
		}},
	}
	f.storeRule(info)
	return info, nil
}

func (f *fakeFirewall) DeleteEndpointAntiSpoofing(_ context.Context, ref firewall.RuleRef) error {
	f.rec.record("firewall.DeleteEndpointAntiSpoofing")
	f.deletedRefs = append(f.deletedRefs, ref)
	return f.errs["DeleteEndpointAntiSpoofing"]
}

func (f *fakeFirewall) EnsureForwardAccept(_ context.Context, spec firewall.ForwardAcceptSpec) (firewall.RuleInfo, error) {
	f.rec.record("firewall.EnsureForwardAccept")
	f.forwardSpec = spec
	if err := f.errs["EnsureForwardAccept"]; err != nil {
		return firewall.RuleInfo{}, err
	}
	info := firewall.RuleInfo{
		Ref: firewall.RuleRef{
			Owner:     spec.RuleOwner,
			Purpose:   firewall.RulePurposeForwardAccept,
			Family:    firewall.TableFamilyIPv4,
			TableName: spec.TableName,
			ChainName: spec.ChainName,
			Handle:    2,
		},
		Summary: firewall.RuleSummary{ForwardAccept: &firewall.ForwardAcceptSummary{
			GuestCIDR:           spec.GuestCIDR,
			EgressInterfaceName: spec.EgressInterfaceName,
			Priority:            spec.Priority,
		}},
	}
	f.storeRule(info)
	return info, nil
}

func (f *fakeFirewall) DeleteForwardAccept(_ context.Context, ref firewall.RuleRef) error {
	f.rec.record("firewall.DeleteForwardAccept")
	f.deletedRefs = append(f.deletedRefs, ref)
	return f.errs["DeleteForwardAccept"]
}

func (f *fakeFirewall) GetRule(_ context.Context, _ firewall.RuleQuery) (firewall.RuleInfo, error) {
	f.rec.record("firewall.GetRule")
	return firewall.RuleInfo{}, f.errs["GetRule"]
}

func (f *fakeFirewall) ListRules(_ context.Context, filter firewall.RuleFilter) ([]firewall.RuleInfo, error) {
	f.rec.record("firewall.ListRules")
	if err := f.errs["ListRules"]; err != nil {
		return nil, err
	}
	var out []firewall.RuleInfo
	for _, r := range f.rules {
		if ruleMatchesFilter(r, filter) {
			out = append(out, r)
		}
	}
	return out, nil
}

// fakeDHCP is a recording dhcp.Manager. It captures the last server spec and
// binding request, records stops, and returns ready/bound observed state.
type fakeDHCP struct {
	rec        *recorder
	errs       map[string]error
	serverSpec dhcp.ServerSpec
	bindingReq dhcp.BindingRequest
	stopped    []dhcp.ServerID
}

var _ dhcp.Manager = (*fakeDHCP)(nil)

func (f *fakeDHCP) Start(_ context.Context, spec dhcp.ServerSpec) (dhcp.ServerInfo, error) {
	f.rec.record("dhcp.Start")
	f.serverSpec = spec
	if err := f.errs["Start"]; err != nil {
		return dhcp.ServerInfo{}, err
	}
	return dhcp.ServerInfo{
		ID:            spec.ID,
		InterfaceName: spec.InterfaceName,
		Subnet:        spec.Subnet,
		Pool:          spec.Pool,
		State:         dhcp.ServerStateReady,
	}, nil
}

func (f *fakeDHCP) Stop(_ context.Context, id dhcp.ServerID) error {
	f.rec.record("dhcp.Stop")
	f.stopped = append(f.stopped, id)
	return f.errs["Stop"]
}

func (f *fakeDHCP) ApplyBinding(_ context.Context, req dhcp.BindingRequest) (dhcp.LeaseInfo, error) {
	f.rec.record("dhcp.ApplyBinding")
	f.bindingReq = req
	if err := f.errs["ApplyBinding"]; err != nil {
		return dhcp.LeaseInfo{}, err
	}
	return dhcp.LeaseInfo{
		ServerID: req.ServerID,
		MAC:      req.MAC,
		IP:       req.IP,
		Hostname: req.Hostname,
		State:    dhcp.LeaseStateBound,
	}, nil
}

func (f *fakeDHCP) RemoveBinding(_ context.Context, _ dhcp.BindingQuery) error {
	f.rec.record("dhcp.RemoveBinding")
	return f.errs["RemoveBinding"]
}

func (f *fakeDHCP) GetServer(_ context.Context, id dhcp.ServerID) (dhcp.ServerInfo, error) {
	f.rec.record("dhcp.GetServer")
	if err := f.errs["GetServer"]; err != nil {
		return dhcp.ServerInfo{}, err
	}
	return dhcp.ServerInfo{ID: id, State: dhcp.ServerStateReady}, nil
}

func (f *fakeDHCP) GetLease(_ context.Context, query dhcp.BindingQuery) (dhcp.LeaseInfo, error) {
	f.rec.record("dhcp.GetLease")
	if err := f.errs["GetLease"]; err != nil {
		return dhcp.LeaseInfo{}, err
	}
	return dhcp.LeaseInfo{ServerID: query.ServerID, MAC: query.MAC, State: dhcp.LeaseStateBound}, nil
}

func (f *fakeDHCP) ListLeases(_ context.Context, _ dhcp.LeaseFilter) ([]dhcp.LeaseInfo, error) {
	f.rec.record("dhcp.ListLeases")
	return nil, f.errs["ListLeases"]
}

// newTestService wires fresh fakes that share one recorder and returns them all
// so tests can register definitions, drive orchestration, and assert both the
// recorded call order and the captured specs.
func newTestService() (*Service, *fakeLink, *fakeRoute, *fakeFirewall, *fakeDHCP, *recorder) {
	rec := newRecorder()
	fl := &fakeLink{rec: rec, errs: make(map[string]error)}
	fr := &fakeRoute{rec: rec, errs: make(map[string]error)}
	ff := &fakeFirewall{rec: rec, errs: make(map[string]error)}
	fd := &fakeDHCP{rec: rec, errs: make(map[string]error)}
	svc := NewService(fl, fr, ff, fd)
	return svc, fl, fr, ff, fd, rec
}

// sampleNetwork returns a fully valid NetworkDefinition that passes
// validateNetworkDefinition.
func sampleNetwork() NetworkDefinition {
	return NetworkDefinition{
		Name:        "net0",
		BridgeName:  "br-net0",
		BridgeMAC:   net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
		BridgeMTU:   1500,
		Subnet:      netip.MustParsePrefix("192.168.100.0/24"),
		GatewayCIDR: netip.MustParsePrefix("192.168.100.1/24"),
		Pool: dhcp.AddressRange{
			Start: netip.MustParseAddr("192.168.100.10"),
			End:   netip.MustParseAddr("192.168.100.200"),
		},
		EgressIface:        "eth0",
		DHCPServerID:       "net0-dhcp",
		LeaseDuration:      time.Hour,
		FirewallTable:      "govirta-nat",
		MasqueradeChain:    "masquerade",
		ForwardChain:       "forward",
		RuleOwner:          "net0",
		MasqueradePriority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
		ForwardPriority:    firewall.ExplicitPriority(0, firewall.PriorityNameForwardFilter),
	}
}

// sampleNIC returns a fully valid NICDefinition (for network "net0") that passes
// validateNICDefinition and validateNICAgainstNetwork.
func sampleNIC() NICDefinition {
	return NICDefinition{
		NetworkName:       "net0",
		VMID:              "vm1",
		TapName:           "tap-vm1",
		MAC:               net.HardwareAddr{0x52, 0x54, 0x00, 0x12, 0x34, 0x56},
		IP:                netip.MustParseAddr("192.168.100.50"),
		TapMTU:            1500,
		VNetHeader:        link.VNetHeaderEnabled,
		OwnerUID:          link.ExplicitUID(0),
		OwnerGID:          link.ExplicitGID(0),
		Hostname:          dhcp.BindingHostname{Value: "vm1", Set: true},
		AntiSpoofTable:    "govirta-bridge",
		AntiSpoofChain:    "antispoof",
		AntiSpoofPriority: firewall.ExplicitPriority(-300, firewall.PriorityNameBridgeFilter),
	}
}
