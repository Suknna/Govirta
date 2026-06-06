package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/network"
	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/internal/network/networker"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/node/identity"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/link"
)

// errInvalidNetworkSpec marks a Network spec whose semantic fields cannot be
// parsed into a valid netpool definition. It is a permanent (config) error: a
// requeue cannot fix a malformed CIDR or address, so the object is reported
// failed and not re-enqueued.
var errInvalidNetworkSpec = errors.New("network controller: invalid spec")

const (
	// bridgeMTU is the standard Ethernet MTU. The Network spec carries only
	// semantic intent and exposes no MTU knob, so the controller uses the
	// kernel-standard frame size as a deterministic constant (not a business
	// default). It is a property of Ethernet, not of any one network.
	bridgeMTU = 1500
)

// NetworkEnsurer is the narrow slice of the network service the controller
// needs: register a logical network definition, reconcile its host primitives,
// and read its live aggregated status. *network.NetworkService satisfies it
// (积木式 + 可测).
type NetworkEnsurer interface {
	RegisterNetwork(def netpool.NetworkDefinition) error
	EnsureNetwork(ctx context.Context, name netpool.NetworkName) (netpool.NetworkStatus, error)
	GetNetworkStatus(ctx context.Context, name netpool.NetworkName) (netpool.NetworkStatus, error)
}

// 编译期证明真实生产类型满足窄接口。
var _ NetworkEnsurer = (*network.NetworkService)(nil)

// NetworkController reconciles Network objects. It decodes each object, parses
// the spec's semantic intent (bridge/subnet/dhcp/egress), derives the kernel
// firewall identities deterministically via identity.DeriveNetworkIdentity, and
// assembles a netpool.NetworkDefinition. It registers the definition (treating
// an already-registered network as an idempotent success), ensures the host
// primitives, reads the live aggregated status, and patches a phase up to the
// master.
//
// This is the controller that proves DESIGN decision A: the API spec never
// carries nftables table/chain/priority identities; the node controller derives
// them from the stable network name and fills them into the execution-plane
// definition.
type NetworkController struct {
	networks NetworkEnsurer
	client   StatusReporter
}

var _ controller.Controller = (*NetworkController)(nil)

// NewNetworkController wires a NetworkController against the network service and
// the master status client.
func NewNetworkController(networks NetworkEnsurer, client StatusReporter) *NetworkController {
	return &NetworkController{networks: networks, client: client}
}

// Kind is the apis kind this controller watches.
func (c *NetworkController) Kind() string {
	return string(metav1.KindNetwork)
}

// Reconcile drives one Network event toward its desired state.
//
// DELETED is a no-op in this slice (delete is a later cut). For ADDED/MODIFIED
// it decodes the object, parses the spec and derives the kernel firewall
// identity, registers the definition, ensures host primitives, reads the live
// status, and patches a phase. A spec that cannot be parsed or that the netpool
// core rejects as invalid is a permanent failure: it is reported failed and not
// requeued. A register (non-idempotent) / ensure / status-read failure is
// transient: it is reported failed and requeued.
func (c *NetworkController) Reconcile(ctx context.Context, ev controller.Event) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("network controller: context done before reconcile: %w", err)
	}

	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("network deleted; delete is a no-op in this slice")
		return false, nil
	}

	var obj networkv1.Network
	if err := json.Unmarshal(ev.Object, &obj); err != nil {
		return false, fmt.Errorf("network controller: decode object %q: %w", ev.Key, err)
	}

	def, err := buildNetworkDefinition(obj)
	if err != nil {
		// A spec that does not parse is a permanent failure: requeue cannot fix it.
		if perr := c.reportFailure(ctx, obj.Name, err); perr != nil {
			return false, fmt.Errorf("network controller: parse spec %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		logger.Error().Err(err).Str("key", ev.Key).Msg("network spec parsing failed permanently (config error); not requeuing")
		return false, nil
	}

	if err := c.networks.RegisterNetwork(def); err != nil && !errors.Is(err, networker.ErrAlreadyExists) {
		// An invalid definition rejected by the netpool core is permanent; any
		// other registration failure is transient.
		permanent := errors.Is(err, networker.ErrInvalidRequest)
		if perr := c.reportFailure(ctx, obj.Name, err); perr != nil {
			return !permanent, fmt.Errorf("network controller: register %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		if permanent {
			logger.Error().Err(err).Str("key", ev.Key).Msg("network definition rejected permanently (config error); not requeuing")
			return false, nil
		}
		return true, fmt.Errorf("network controller: register %q: %w", obj.Name, err)
	}

	name := netpool.NetworkName(obj.Name)
	if _, err := c.networks.EnsureNetwork(ctx, name); err != nil {
		if perr := c.reportFailure(ctx, obj.Name, err); perr != nil {
			return true, fmt.Errorf("network controller: ensure %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("network controller: ensure %q: %w", obj.Name, err)
	}

	// Authoritative read: report from the live aggregated status, never from the
	// definition we just registered (netpool 的“总以实况为准”约定).
	status, err := c.networks.GetNetworkStatus(ctx, name)
	if err != nil {
		if perr := c.reportFailure(ctx, obj.Name, err); perr != nil {
			return true, fmt.Errorf("network controller: read status for %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("network controller: read status for %q: %w", obj.Name, err)
	}

	phase, requeue := mapNetworkPhase(status.DHCP.State)
	apiStatus := networkv1.NetworkStatus{Phase: phase}
	if phase == networkv1.NetworkPhaseFailed {
		apiStatus.Message = fmt.Sprintf("dhcp server in unexpected state %q after ensure", status.DHCP.State)
	}
	if err := c.patchStatus(ctx, obj.Name, apiStatus); err != nil {
		return true, err
	}

	logger.Info().
		Str("key", ev.Key).
		Str("bridge", string(status.Bridge.Name)).
		Str("dhcpState", string(status.DHCP.State)).
		Str("phase", string(phase)).
		Bool("requeue", requeue).
		Msg("network reconciled")
	return requeue, nil
}

// reportFailure patches a failed status carrying cause's message.
func (c *NetworkController) reportFailure(ctx context.Context, name string, cause error) error {
	return c.patchStatus(ctx, name, networkv1.NetworkStatus{
		Phase:   networkv1.NetworkPhaseFailed,
		Message: cause.Error(),
	})
}

// patchStatus marshals status and PATCHes it to the master's /status sub-resource.
func (c *NetworkController) patchStatus(ctx context.Context, name string, status networkv1.NetworkStatus) error {
	body, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("network controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("network controller: patch status for %q: %w", name, err)
	}
	return nil
}

// buildNetworkDefinition parses a Network object's semantic spec, derives the
// kernel firewall identity from the stable network name, and assembles a
// netpool.NetworkDefinition. Every parse failure is wrapped as a permanent
// errInvalidNetworkSpec. The firewall table/chain/priority/owner fields are
// never read from the spec; they come solely from identity.DeriveNetworkIdentity
// (DESIGN 决策 A).
func buildNetworkDefinition(n networkv1.Network) (netpool.NetworkDefinition, error) {
	subnet, err := netip.ParsePrefix(n.Spec.Subnet)
	if err != nil {
		return netpool.NetworkDefinition{}, fmt.Errorf("%w: subnet %q: %v", errInvalidNetworkSpec, n.Spec.Subnet, err)
	}
	gateway, err := netip.ParsePrefix(n.Spec.GatewayCIDR)
	if err != nil {
		return netpool.NetworkDefinition{}, fmt.Errorf("%w: gatewayCIDR %q: %v", errInvalidNetworkSpec, n.Spec.GatewayCIDR, err)
	}
	poolStart, err := netip.ParseAddr(n.Spec.DHCPRangeStart)
	if err != nil {
		return netpool.NetworkDefinition{}, fmt.Errorf("%w: dhcpRangeStart %q: %v", errInvalidNetworkSpec, n.Spec.DHCPRangeStart, err)
	}
	poolEnd, err := netip.ParseAddr(n.Spec.DHCPRangeEnd)
	if err != nil {
		return netpool.NetworkDefinition{}, fmt.Errorf("%w: dhcpRangeEnd %q: %v", errInvalidNetworkSpec, n.Spec.DHCPRangeEnd, err)
	}
	router, err := buildDHCPOption(n.Spec.Router, "router")
	if err != nil {
		return netpool.NetworkDefinition{}, err
	}
	dns, err := buildDHCPOption(n.Spec.DNS, "dns")
	if err != nil {
		return netpool.NetworkDefinition{}, err
	}
	// LeaseSeconds is optional on the wire but the DHCP responder needs a positive
	// lease. The controller requires it explicitly rather than inventing a lease
	// policy (显式优于隐式: a lease duration is a business knob, not a constant).
	if n.Spec.LeaseSeconds <= 0 {
		return netpool.NetworkDefinition{}, fmt.Errorf("%w: leaseSeconds must be positive, got %d", errInvalidNetworkSpec, n.Spec.LeaseSeconds)
	}

	// 决策 A 核心：内核身份不来自 spec，由稳定的网络名确定性派生后填入定义。
	id := identity.DeriveNetworkIdentity(n.Name)

	return netpool.NetworkDefinition{
		Name:        netpool.NetworkName(n.Name),
		BridgeName:  link.Name(n.Spec.BridgeName),
		BridgeMAC:   deriveBridgeMAC(n.Name),
		BridgeMTU:   bridgeMTU,
		Subnet:      subnet,
		GatewayCIDR: gateway,
		Pool:        dhcp.AddressRange{Start: poolStart, End: poolEnd},

		EgressIface: firewall.InterfaceName(n.Spec.EgressInterface),

		// The DHCP responder instance id is derived deterministically from the
		// network name so registrations replay to the same server identity.
		DHCPServerID:  dhcp.ServerID(n.Name),
		Router:        router,
		DNS:           dns,
		LeaseDuration: time.Duration(n.Spec.LeaseSeconds) * time.Second,

		FirewallTable:      id.FirewallTable,
		MasqueradeChain:    id.MasqueradeChain,
		ForwardChain:       id.ForwardChain,
		RuleOwner:          id.RuleOwner,
		MasqueradePriority: id.MasqueradePriority,
		ForwardPriority:    id.ForwardPriority,
	}, nil
}

// buildDHCPOption parses an optional list of address strings into an explicit
// DHCP option. An empty list yields an explicitly disabled option (the
// controller never infers a router or DNS server from the gateway).
func buildDHCPOption(addrStrings []string, field string) (dhcp.DHCPOptionAddrs, error) {
	if len(addrStrings) == 0 {
		return dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionDisabled}, nil
	}
	addrs := make([]netip.Addr, 0, len(addrStrings))
	for _, s := range addrStrings {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return dhcp.DHCPOptionAddrs{}, fmt.Errorf("%w: %s %q: %v", errInvalidNetworkSpec, field, s, err)
		}
		addrs = append(addrs, a)
	}
	return dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: addrs}, nil
}

// deriveBridgeMAC derives a deterministic, locally-administered unicast MAC for
// the network's bridge from the stable network name. The Network spec carries
// no MAC, so the controller derives one: the same name always yields the same
// MAC, and the locally-administered (0x02) / unicast (clear 0x01) bits keep it
// valid and out of the IEEE-assigned space.
func deriveBridgeMAC(name string) net.HardwareAddr {
	sum := sha256.Sum256([]byte(name))
	mac := make(net.HardwareAddr, 6)
	copy(mac, sum[:6])
	mac[0] = (mac[0] | 0x02) &^ 0x01
	return mac
}

// mapNetworkPhase maps the live DHCP responder state to an apis NetworkPhase and
// reports whether the controller should requeue. A successful EnsureNetwork +
// GetNetworkStatus already proves the bridge, forwarding, and firewall rules are
// present (GetNetworkStatus errors otherwise), so the responder lifecycle is the
// remaining readiness signal. The mapping is an explicit switch over the typed
// dhcp.ServerState enum (项目铁律: 禁止裸 string 映射).
func mapNetworkPhase(state dhcp.ServerState) (networkv1.NetworkPhase, bool) {
	switch state {
	case dhcp.ServerStateReady:
		return networkv1.NetworkPhaseReady, false
	case dhcp.ServerStateStarting:
		// Still coming up: not failed, but not done — requeue to re-check.
		return networkv1.NetworkPhasePending, true
	default:
		// stopping/stopped are unexpected immediately after a successful ensure;
		// treat as a transient anomaly and requeue.
		return networkv1.NetworkPhaseFailed, true
	}
}
