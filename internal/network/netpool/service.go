package netpool

import (
	"context"
	"net/netip"
	"sort"
	"sync"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/network/networker"
)

// Service registers logical network/NIC definitions and orchestrates the host
// primitives that realize them. The injected managers are the only source of
// observed truth; Service stores declarative intent only.
type Service struct {
	mu       sync.RWMutex
	networks map[NetworkName]*networkRecord

	link     link.Manager
	route    route.Manager
	firewall firewall.Manager
	dhcp     dhcp.Manager
}

// NewService creates a network orchestration service backed by explicit host
// primitive managers. All four managers are required; a nil manager is a
// programming error the caller must not make.
func NewService(linkMgr link.Manager, routeMgr route.Manager, firewallMgr firewall.Manager, dhcpMgr dhcp.Manager) *Service {
	return &Service{
		networks: make(map[NetworkName]*networkRecord),
		link:     linkMgr,
		route:    routeMgr,
		firewall: firewallMgr,
		dhcp:     dhcpMgr,
	}
}

// RegisterNetwork validates and stores a logical network definition. It does
// not touch the kernel; EnsureNetwork performs host reconciliation. The stored
// record is a service-owned deep copy so external pointers cannot mutate the
// index.
func (s *Service) RegisterNetwork(def NetworkDefinition) error {
	if err := validateNetworkDefinition(def); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.networks[def.Name]; exists {
		return networker.ErrAlreadyExists
	}
	s.networks[def.Name] = &networkRecord{
		def:  cloneNetworkDefinition(def),
		nics: make(map[VMID]NICDefinition),
	}
	return nil
}

// RegisterNIC validates and stores a NIC definition under an already-registered
// network. The IP must fall inside the network's DHCP pool and must not collide
// with another NIC's IP on the same network.
func (s *Service) RegisterNIC(def NICDefinition) error {
	if err := validateNICDefinition(def); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.networks[def.NetworkName]
	if !exists {
		return networker.ErrNotFound
	}
	if err := validateNICAgainstNetwork(def, record.def); err != nil {
		return err
	}
	if _, exists := record.nics[def.VMID]; exists {
		return networker.ErrAlreadyExists
	}
	for vmID, existing := range record.nics {
		if vmID == def.VMID {
			continue
		}
		if existing.IP == def.IP {
			return networker.ErrConflict
		}
		if existing.TapName == def.TapName {
			return networker.ErrConflict
		}
		if existing.MAC.String() == def.MAC.String() {
			return networker.ErrConflict
		}
	}
	record.nics[def.VMID] = cloneNICDefinition(def)
	return nil
}

// GetNetwork returns a deep copy of a registered network definition.
func (s *Service) GetNetwork(name NetworkName) (NetworkDefinition, error) {
	record, err := s.getRecord(name)
	if err != nil {
		return NetworkDefinition{}, err
	}
	return cloneNetworkDefinition(record.def), nil
}

// ListNetworks returns deep copies of all registered network definitions sorted
// by name.
func (s *Service) ListNetworks(ctx context.Context) ([]NetworkDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]NetworkName, 0, len(s.networks))
	for name := range s.networks {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	defs := make([]NetworkDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, cloneNetworkDefinition(s.networks[name].def))
	}
	return defs, nil
}

// GetNIC returns a deep copy of a registered NIC definition.
func (s *Service) GetNIC(networkName NetworkName, vmID VMID) (NICDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, exists := s.networks[networkName]
	if !exists {
		return NICDefinition{}, networker.ErrNotFound
	}
	nic, exists := record.nics[vmID]
	if !exists {
		return NICDefinition{}, networker.ErrNotFound
	}
	return cloneNICDefinition(nic), nil
}

// getRecord returns the live internal record under read lock for internal
// mutation/orchestration paths. Callers must not leak the returned pointer.
func (s *Service) getRecord(name NetworkName) (*networkRecord, error) {
	if name == "" {
		return nil, networker.ErrInvalidRequest
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, exists := s.networks[name]
	if !exists {
		return nil, networker.ErrNotFound
	}
	return record, nil
}

func validateNetworkDefinition(def NetworkDefinition) error {
	if def.Name == "" || def.BridgeName == "" || def.DHCPServerID == "" {
		return networker.ErrInvalidRequest
	}
	if len(def.BridgeMAC) != 6 || def.BridgeMAC[0]&1 != 0 {
		return networker.ErrInvalidRequest
	}
	if def.BridgeMTU <= 0 {
		return networker.ErrInvalidRequest
	}
	if !def.Subnet.IsValid() || !def.Subnet.Addr().Is4() {
		return networker.ErrInvalidRequest
	}
	if !def.GatewayCIDR.IsValid() || !def.GatewayCIDR.Addr().Is4() {
		return networker.ErrInvalidRequest
	}
	if def.Subnet.Masked() != def.Subnet {
		return networker.ErrInvalidRequest
	}
	if !def.Subnet.Contains(def.GatewayCIDR.Addr()) {
		return networker.ErrInvalidRequest
	}
	if !def.Pool.Start.IsValid() || !def.Pool.End.IsValid() {
		return networker.ErrInvalidRequest
	}
	if !def.Subnet.Contains(def.Pool.Start) || !def.Subnet.Contains(def.Pool.End) {
		return networker.ErrInvalidRequest
	}
	if def.Pool.Start.Compare(def.Pool.End) > 0 {
		return networker.ErrInvalidRequest
	}
	if def.EgressIface == "" || def.FirewallTable == "" || def.MasqueradeChain == "" || def.ForwardChain == "" || def.RuleOwner == "" {
		return networker.ErrInvalidRequest
	}
	if def.LeaseDuration <= 0 {
		return networker.ErrInvalidRequest
	}
	return nil
}

func validateNICDefinition(def NICDefinition) error {
	if def.NetworkName == "" || def.VMID == "" || def.TapName == "" {
		return networker.ErrInvalidRequest
	}
	if len(def.MAC) != 6 || def.MAC[0]&1 != 0 {
		return networker.ErrInvalidRequest
	}
	if !def.IP.IsValid() || !def.IP.Is4() {
		return networker.ErrInvalidRequest
	}
	if def.TapMTU <= 0 {
		return networker.ErrInvalidRequest
	}
	if def.VNetHeader != link.VNetHeaderEnabled && def.VNetHeader != link.VNetHeaderDisabled {
		return networker.ErrInvalidRequest
	}
	if !def.OwnerUID.Set || !def.OwnerGID.Set {
		return networker.ErrInvalidRequest
	}
	if def.AntiSpoofTable == "" || def.AntiSpoofChain == "" {
		return networker.ErrInvalidRequest
	}
	return nil
}

func validateNICAgainstNetwork(def NICDefinition, network NetworkDefinition) error {
	if !network.Subnet.Contains(def.IP) {
		return networker.ErrInvalidRequest
	}
	if !ipInRange(def.IP, network.Pool.Start, network.Pool.End) {
		return networker.ErrInvalidRequest
	}
	return nil
}

func ipInRange(ip, start, end netip.Addr) bool {
	return ip.Compare(start) >= 0 && ip.Compare(end) <= 0
}
