package netpool

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/dhcp/dhcperr"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
	"github.com/suknna/govirta/internal/network/networker"
)

// NetworkStatus is observed network state aggregated live from the primitives.
// It is never read from the in-memory definition index.
type NetworkStatus struct {
	Name       NetworkName
	Bridge     link.LinkInfo
	Forwarding route.IPv4ForwardingInfo
	Masquerade firewall.RuleInfo
	Forward    firewall.RuleInfo
	DHCP       dhcp.ServerInfo
}

// NICStatus is observed NIC state aggregated live from the primitives.
type NICStatus struct {
	NetworkName  NetworkName
	VMID         VMID
	Tap          link.LinkInfo
	Lease        dhcp.LeaseInfo
	AntiSpoofing firewall.RuleInfo
}

// EnsureNetwork reconciles host primitives to match the registered network
// definition: bridge, IPv4 forwarding readiness, masquerade, forward-accept,
// then DHCP. It is idempotent and never tears down already-created resources on
// partial failure; the caller decides whether to retry or DeleteNetwork.
func (s *Service) EnsureNetwork(ctx context.Context, name NetworkName) (NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return NetworkStatus{}, err
	}
	record, err := s.getRecord(name)
	if err != nil {
		return NetworkStatus{}, err
	}
	def := cloneNetworkDefinition(record.def)

	if _, err := s.link.EnsureBridge(ctx, link.BridgeSpec{
		Name:        def.BridgeName,
		GatewayCIDR: def.GatewayCIDR.String(),
		MTU:         def.BridgeMTU,
		MAC:         def.BridgeMAC,
	}); err != nil {
		return NetworkStatus{}, fmt.Errorf("ensure network %q bridge: %w", name, err)
	}

	if _, err := s.route.CheckIPv4Forwarding(ctx, route.IPv4ForwardingEnabled); err != nil {
		return NetworkStatus{}, fmt.Errorf("ensure network %q forwarding: %w", name, classifyNotReady(err))
	}

	if _, err := s.firewall.EnsureMasquerade(ctx, firewall.MasqueradeSpec{
		TableName:           def.FirewallTable,
		ChainName:           def.MasqueradeChain,
		RuleOwner:           def.RuleOwner,
		GuestCIDR:           def.Subnet,
		EgressInterfaceName: def.EgressIface,
		Priority:            def.MasqueradePriority,
	}); err != nil {
		return NetworkStatus{}, fmt.Errorf("ensure network %q masquerade: %w", name, err)
	}

	if _, err := s.firewall.EnsureForwardAccept(ctx, firewall.ForwardAcceptSpec{
		TableName:           def.FirewallTable,
		ChainName:           def.ForwardChain,
		RuleOwner:           def.RuleOwner,
		GuestCIDR:           def.Subnet,
		EgressInterfaceName: def.EgressIface,
		Priority:            def.ForwardPriority,
	}); err != nil {
		return NetworkStatus{}, fmt.Errorf("ensure network %q forward-accept: %w", name, err)
	}

	if _, err := s.dhcp.Start(ctx, dhcp.ServerSpec{
		ID:            def.DHCPServerID,
		InterfaceName: def.BridgeName,
		ListenAddr:    netipUnspecified4(),
		ListenPort:    dhcpServerPort(),
		ServerAddr:    def.GatewayCIDR.Addr(),
		Subnet:        def.Subnet,
		Pool:          def.Pool,
		LeaseDuration: def.LeaseDuration,
		Router:        def.Router,
		DNS:           def.DNS,
		BindMode:      dhcp.BindModeInterfaceZone,
	}); err != nil {
		if !errors.Is(err, dhcperr.ErrAlreadyRunning) {
			return NetworkStatus{}, fmt.Errorf("ensure network %q dhcp: %w", name, err)
		}
	}

	return s.GetNetworkStatus(ctx, name)
}

// EnsureNIC reconciles host primitives for one VM NIC: TAP enslaved to the
// network bridge, the static DHCP binding, and the endpoint anti-spoofing
// guard. Idempotent; never tears down on partial failure.
func (s *Service) EnsureNIC(ctx context.Context, networkName NetworkName, vmID VMID) (NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return NICStatus{}, err
	}
	record, err := s.getRecord(networkName)
	if err != nil {
		return NICStatus{}, err
	}
	s.mu.RLock()
	nic, exists := record.nics[vmID]
	def := record.def
	s.mu.RUnlock()
	if !exists {
		return NICStatus{}, networker.ErrNotFound
	}
	nic = cloneNICDefinition(nic)
	def = cloneNetworkDefinition(def)

	if _, err := s.link.EnsureTap(ctx, link.TapSpec{
		Name:       nic.TapName,
		BridgeName: def.BridgeName,
		OwnerUID:   nic.OwnerUID,
		OwnerGID:   nic.OwnerGID,
		MTU:        nic.TapMTU,
		MAC:        nic.MAC,
		VNetHeader: nic.VNetHeader,
	}); err != nil {
		return NICStatus{}, fmt.Errorf("ensure nic %q/%q tap: %w", networkName, vmID, err)
	}

	if _, err := s.dhcp.ApplyBinding(ctx, dhcp.BindingRequest{
		ServerID: def.DHCPServerID,
		MAC:      nic.MAC,
		IP:       nic.IP,
		Hostname: nic.Hostname,
	}); err != nil {
		return NICStatus{}, fmt.Errorf("ensure nic %q/%q binding: %w", networkName, vmID, err)
	}

	if _, err := s.firewall.EnsureEndpointAntiSpoofing(ctx, firewall.EndpointAntiSpoofingSpec{
		TableName:  nic.AntiSpoofTable,
		ChainName:  nic.AntiSpoofChain,
		RuleOwner:  def.RuleOwner,
		BridgeName: firewall.InterfaceName(def.BridgeName),
		TapName:    firewall.InterfaceName(nic.TapName),
		MAC:        nic.MAC,
		IPv4:       nic.IP,
		Priority:   nic.AntiSpoofPriority,
	}); err != nil {
		return NICStatus{}, fmt.Errorf("ensure nic %q/%q anti-spoofing: %w", networkName, vmID, err)
	}

	return s.GetNICStatus(ctx, networkName, vmID)
}

// DeleteNIC tears down one NIC's host resources in reverse order, preserving
// every error via errors.Join. The logical definition stays registered; callers
// remove it explicitly if desired (out of scope for this method).
func (s *Service) DeleteNIC(ctx context.Context, networkName NetworkName, vmID VMID, antiSpoofRef firewall.RuleRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := s.getRecord(networkName)
	if err != nil {
		return err
	}
	s.mu.RLock()
	nic, exists := record.nics[vmID]
	def := record.def
	s.mu.RUnlock()
	if !exists {
		return networker.ErrNotFound
	}

	var errs []error
	if err := s.firewall.DeleteEndpointAntiSpoofing(ctx, antiSpoofRef); err != nil {
		errs = append(errs, fmt.Errorf("delete anti-spoofing: %w", err))
	}
	if err := s.dhcp.RemoveBinding(ctx, dhcp.BindingQuery{ServerID: def.DHCPServerID, MAC: nic.MAC}); err != nil {
		errs = append(errs, fmt.Errorf("remove binding: %w", err))
	}
	if err := s.link.Delete(ctx, nic.TapName); err != nil {
		errs = append(errs, fmt.Errorf("delete tap: %w", err))
	}
	return errors.Join(errs...)
}

// DeleteNetwork tears down a network's shared host resources in reverse order.
// It refuses to delete a network that still has registered NICs.
func (s *Service) DeleteNetwork(ctx context.Context, name NetworkName, masqueradeRef firewall.RuleRef, forwardRef firewall.RuleRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := s.getRecord(name)
	if err != nil {
		return err
	}
	s.mu.RLock()
	nicCount := len(record.nics)
	def := cloneNetworkDefinition(record.def)
	s.mu.RUnlock()
	if nicCount > 0 {
		return networker.ErrConflict
	}

	var errs []error
	if err := s.dhcp.Stop(ctx, def.DHCPServerID); err != nil {
		errs = append(errs, fmt.Errorf("stop dhcp: %w", err))
	}
	if err := s.firewall.DeleteForwardAccept(ctx, forwardRef); err != nil {
		errs = append(errs, fmt.Errorf("delete forward-accept: %w", err))
	}
	if err := s.firewall.DeleteMasquerade(ctx, masqueradeRef); err != nil {
		errs = append(errs, fmt.Errorf("delete masquerade: %w", err))
	}
	if err := s.link.Delete(ctx, def.BridgeName); err != nil {
		errs = append(errs, fmt.Errorf("delete bridge: %w", err))
	}
	return errors.Join(errs...)
}

// GetNetworkStatus aggregates observed network state live from the primitives.
// It never returns the in-memory definition as if it were observed truth.
func (s *Service) GetNetworkStatus(ctx context.Context, name NetworkName) (NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return NetworkStatus{}, err
	}
	record, err := s.getRecord(name)
	if err != nil {
		return NetworkStatus{}, err
	}
	def := cloneNetworkDefinition(record.def)

	bridge, err := s.link.Get(ctx, def.BridgeName)
	if err != nil {
		return NetworkStatus{}, fmt.Errorf("get network %q bridge: %w", name, err)
	}
	forwarding, err := s.route.GetIPv4Forwarding(ctx)
	if err != nil {
		return NetworkStatus{}, fmt.Errorf("get network %q forwarding: %w", name, err)
	}
	masquerade, err := s.firewallRule(ctx, firewall.RuleFilter{
		Owner:   firewall.FilterOwner(def.RuleOwner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeMasquerade),
		Family:  firewall.FilterFamily(firewall.TableFamilyIPv4),
		Table:   firewall.FilterTable(def.FirewallTable),
		Chain:   firewall.FilterChain(def.MasqueradeChain),
	})
	if err != nil {
		return NetworkStatus{}, fmt.Errorf("get network %q masquerade: %w", name, err)
	}
	forward, err := s.firewallRule(ctx, firewall.RuleFilter{
		Owner:   firewall.FilterOwner(def.RuleOwner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeForwardAccept),
		Family:  firewall.FilterFamily(firewall.TableFamilyIPv4),
		Table:   firewall.FilterTable(def.FirewallTable),
		Chain:   firewall.FilterChain(def.ForwardChain),
	})
	if err != nil {
		return NetworkStatus{}, fmt.Errorf("get network %q forward-accept: %w", name, err)
	}
	server, err := s.dhcp.GetServer(ctx, def.DHCPServerID)
	if err != nil {
		return NetworkStatus{}, fmt.Errorf("get network %q dhcp: %w", name, err)
	}
	return NetworkStatus{
		Name:       name,
		Bridge:     bridge,
		Forwarding: forwarding,
		Masquerade: masquerade,
		Forward:    forward,
		DHCP:       server,
	}, nil
}

// GetNICStatus aggregates observed NIC state live from the primitives.
func (s *Service) GetNICStatus(ctx context.Context, networkName NetworkName, vmID VMID) (NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return NICStatus{}, err
	}
	record, err := s.getRecord(networkName)
	if err != nil {
		return NICStatus{}, err
	}
	s.mu.RLock()
	nic, exists := record.nics[vmID]
	def := record.def
	s.mu.RUnlock()
	if !exists {
		return NICStatus{}, networker.ErrNotFound
	}

	tap, err := s.link.Get(ctx, nic.TapName)
	if err != nil {
		return NICStatus{}, fmt.Errorf("get nic %q/%q tap: %w", networkName, vmID, err)
	}
	lease, err := s.dhcp.GetLease(ctx, dhcp.BindingQuery{ServerID: def.DHCPServerID, MAC: nic.MAC})
	if err != nil {
		return NICStatus{}, fmt.Errorf("get nic %q/%q lease: %w", networkName, vmID, err)
	}
	antiSpoofing, err := s.nicAntiSpoofingRule(ctx, def.RuleOwner, nic)
	if err != nil {
		return NICStatus{}, fmt.Errorf("get nic %q/%q anti-spoofing: %w", networkName, vmID, err)
	}
	return NICStatus{
		NetworkName:  networkName,
		VMID:         vmID,
		Tap:          tap,
		Lease:        lease,
		AntiSpoofing: antiSpoofing,
	}, nil
}

func classifyNotReady(err error) error {
	if errors.Is(err, routeerr.ErrNotReady) {
		return fmt.Errorf("%w: %w", networker.ErrNotReady, err)
	}
	return err
}

// firewallRule returns the single observed firewall rule matching filter.
// Network masquerade and forward-accept rules are unique per network identity,
// so observing zero rules is ErrNotFound and observing more than one is
// ErrConflict rather than returning an ambiguous zero-value RuleInfo.
func (s *Service) firewallRule(ctx context.Context, filter firewall.RuleFilter) (firewall.RuleInfo, error) {
	rules, err := s.firewall.ListRules(ctx, filter)
	if err != nil {
		return firewall.RuleInfo{}, err
	}
	switch len(rules) {
	case 1:
		return rules[0], nil
	case 0:
		return firewall.RuleInfo{}, fmt.Errorf("%w: no matching firewall rule observed", networker.ErrNotFound)
	default:
		return firewall.RuleInfo{}, fmt.Errorf("%w: %d matching firewall rules observed, want one", networker.ErrConflict, len(rules))
	}
}

// nicAntiSpoofingRule returns the observed anti-spoofing rule for one NIC.
// Multiple NICs share one owner, table, and chain, and ListRules cannot filter
// by MAC, so the unique match is completed Go-side against the observed
// EndpointAntiSpoofingSummary MAC.
func (s *Service) nicAntiSpoofingRule(ctx context.Context, owner firewall.RuleOwner, nic NICDefinition) (firewall.RuleInfo, error) {
	rules, err := s.firewall.ListRules(ctx, firewall.RuleFilter{
		Owner:   firewall.FilterOwner(owner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeEndpointAntiSpoofing),
		Family:  firewall.FilterFamily(firewall.TableFamilyBridge),
		Table:   firewall.FilterTable(nic.AntiSpoofTable),
		Chain:   firewall.FilterChain(nic.AntiSpoofChain),
	})
	if err != nil {
		return firewall.RuleInfo{}, err
	}
	for _, rule := range rules {
		summary := rule.Summary.EndpointAntiSpoofing
		if summary != nil && summary.MAC.String() == nic.MAC.String() {
			return rule, nil
		}
	}
	return firewall.RuleInfo{}, fmt.Errorf("%w: no anti-spoofing rule observed for MAC %s", networker.ErrNotFound, nic.MAC)
}

func netipUnspecified4() netip.Addr { return netip.AddrFrom4([4]byte{0, 0, 0, 0}) }
func dhcpServerPort() dhcp.Port     { return dhcp.Port(67) }
