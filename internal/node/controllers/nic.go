package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/network"
	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/internal/network/networker"
	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/node/identity"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/link"
)

// errInvalidNICSpec marks a NIC spec whose semantic fields cannot be turned into
// a valid netpool definition. It is a permanent (config) error: a requeue cannot
// fix an empty/malformed MAC or an unparsable IP, so the object is reported
// failed and not re-enqueued.
var errInvalidNICSpec = errors.New("nic controller: invalid spec")

const (
	// tapMTU is the standard Ethernet MTU for the NIC's host TAP. The NIC spec
	// carries only semantic intent and exposes no MTU knob, so the controller
	// uses the kernel-standard frame size as a deterministic constant (a property
	// of Ethernet, not a per-NIC business default).
	tapMTU = 1500

	// nicIndex is the index of the NIC within its VM. This slice models one NIC
	// per VM, so the index is a fixed 0; the derivation accepts it explicitly
	// rather than inferring it.
	nicIndex = 0
)

// NICEnsurer is the narrow slice of the NIC service the controller needs:
// register a logical NIC definition, reconcile its host primitives, and read its
// live aggregated status. *network.NICService satisfies it (积木式 + 可测).
type NICEnsurer interface {
	RegisterNIC(def netpool.NICDefinition) error
	EnsureNIC(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) (netpool.NICStatus, error)
	GetNICStatus(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) (netpool.NICStatus, error)
}

// 编译期证明真实生产类型满足窄接口。DependencyReader 复用 volume.go 的既有定义。
var (
	_ NICEnsurer            = (*network.NICService)(nil)
	_ controller.Controller = (*NICController)(nil)
)

// NICController reconciles NIC objects. It gates on the referenced Network being
// live Ready, then derives the kernel TAP/anti-spoofing identity deterministically
// via identity.DeriveNICIdentity, assembles a netpool.NICDefinition threading the
// apiserver-allocated MAC through unchanged, registers the definition (treating an
// already-registered NIC as an idempotent success), ensures the host primitives,
// reads the live aggregated status, and patches a phase up to the master.
//
// MAC 铁律 (#698 + 项目决策): the controller never generates a MAC. The MAC is
// allocated by the apiserver and carried in NICSpec.MAC; it is threaded into the
// definition verbatim. An empty MAC is a permanent config error, never a signal
// to invent one.
type NICController struct {
	nics     NICEnsurer
	client   DependencyReader
	ownerUID link.UID
	ownerGID link.GID
}

// NewNICController wires a NICController against the NIC service, the master
// dependency/status client, and the explicit TAP owner identity (the OS user
// that runs QEMU, injected from configuration — never derived).
func NewNICController(nics NICEnsurer, client DependencyReader, ownerUID link.UID, ownerGID link.GID) *NICController {
	return &NICController{nics: nics, client: client, ownerUID: ownerUID, ownerGID: ownerGID}
}

// Kind is the apis kind this controller watches.
func (c *NICController) Kind() string {
	return string(metav1.KindNIC)
}

// Reconcile drives one NIC event toward its desired state.
//
// DELETED is a no-op in this slice (delete is a later cut). For ADDED/MODIFIED it
// decodes the object and gates on the referenced Network being live Ready. requeue
// semantics:
//   - the referenced Network is missing (ErrNotFound) or not yet Ready → requeue,
//     no status patch (just wait — the NIC itself has not failed);
//   - a Network read fails for any other reason → transient: requeue with a
//     wrapped error, no status patch (readiness could not be assessed);
//   - the spec cannot be parsed into a definition (empty/malformed MAC, unparsable
//     IP) → permanent config failure: patch failed and do NOT requeue;
//   - RegisterNIC (non-idempotent) / EnsureNIC / GetNICStatus fails → transient:
//     patch failed and requeue with a wrapped error.
func (c *NICController) Reconcile(ctx context.Context, ev controller.Event) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("nic controller: context done before reconcile: %w", err)
	}

	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("nic deleted; delete is a no-op in this slice")
		return false, nil
	}

	var obj nicv1.NIC
	if err := json.Unmarshal(ev.Object, &obj); err != nil {
		return false, fmt.Errorf("nic controller: decode object %q: %w", ev.Key, err)
	}

	// Gate: the referenced Network must be live Ready before any NIC work. A
	// missing or not-yet-ready Network is a wait, not a failure: requeue without
	// patching failed.
	ready, err := c.networkReady(ctx, obj.Spec.NetworkRef)
	if err != nil {
		return true, err
	}
	if !ready {
		logger.Info().
			Str("key", ev.Key).
			Str("networkRef", obj.Spec.NetworkRef).
			Msg("nic referenced network not ready; requeueing")
		return true, nil
	}

	def, err := c.buildNICDefinition(obj)
	if err != nil {
		// A spec that does not parse is a permanent failure: requeue cannot fix it.
		if perr := c.reportFailure(ctx, obj.Name, err); perr != nil {
			return false, fmt.Errorf("nic controller: parse spec %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		logger.Error().Err(err).Str("key", ev.Key).Msg("nic spec parsing failed permanently (config error); not requeuing")
		return false, nil
	}

	if err := c.nics.RegisterNIC(def); err != nil && !errors.Is(err, networker.ErrAlreadyExists) {
		if perr := c.reportFailure(ctx, obj.Name, err); perr != nil {
			return true, fmt.Errorf("nic controller: register %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("nic controller: register %q: %w", obj.Name, err)
	}

	networkName := netpool.NetworkName(obj.Spec.NetworkRef)
	vmID := netpool.VMID(obj.Spec.VMRef)
	if _, err := c.nics.EnsureNIC(ctx, networkName, vmID); err != nil {
		if perr := c.reportFailure(ctx, obj.Name, err); perr != nil {
			return true, fmt.Errorf("nic controller: ensure %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("nic controller: ensure %q: %w", obj.Name, err)
	}

	// Authoritative read: report from the live aggregated status, never from the
	// definition we just registered (netpool 的“总以实况为准”约定).
	if _, err := c.nics.GetNICStatus(ctx, networkName, vmID); err != nil {
		if perr := c.reportFailure(ctx, obj.Name, err); perr != nil {
			return true, fmt.Errorf("nic controller: read status for %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("nic controller: read status for %q: %w", obj.Name, err)
	}

	// The derived TapName is the authoritative host TAP name reported up. A
	// successful EnsureNIC + GetNICStatus proves the TAP, DHCP binding, and
	// anti-spoofing rule are present, so the NIC is ready.
	status := nicv1.NICStatus{
		Phase:   nicv1.NICPhaseReady,
		TapName: string(def.TapName),
	}
	if err := c.patchStatus(ctx, obj.Name, status); err != nil {
		return true, err
	}

	logger.Info().
		Str("key", ev.Key).
		Str("networkRef", obj.Spec.NetworkRef).
		Str("vmRef", obj.Spec.VMRef).
		Str("tapName", string(def.TapName)).
		Msg("nic ready")
	return false, nil
}

// networkReady reads the referenced Network and reports whether its observed
// phase is Ready. A missing object (ErrNotFound) is not-ready with a nil error so
// the caller waits; any other read/decode failure is transient.
func (c *NICController) networkReady(ctx context.Context, name string) (bool, error) {
	raw, err := c.client.Get(ctx, string(metav1.KindNetwork), name)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("nic controller: get Network %q: %w", name, err)
	}
	var n networkv1.Network
	if err := json.Unmarshal(raw, &n); err != nil {
		return false, fmt.Errorf("nic controller: decode Network %q: %w", name, err)
	}
	return n.Status.Phase == networkv1.NetworkPhaseReady, nil
}

// buildNICDefinition parses a NIC object's semantic spec, derives the kernel
// TAP/anti-spoofing identity from the stable VM ref, and assembles a
// netpool.NICDefinition. The MAC is threaded through unchanged from the spec
// (apiserver-allocated); an empty or malformed MAC is a permanent config error
// (MAC 铁律 #698: the controller never generates one). The IP is parsed via
// netip.ParseAddr; a parse failure is permanent. The TAP/vnet/anti-spoof
// identities are never read from the spec; they come solely from
// identity.DeriveNICIdentity.
func (c *NICController) buildNICDefinition(n nicv1.NIC) (netpool.NICDefinition, error) {
	// MAC 铁律: 原样透传 spec.MAC，绝不生成。空 MAC 是永久配置错误。
	if n.Spec.MAC == "" {
		return netpool.NICDefinition{}, fmt.Errorf("%w: mac is empty (apiserver must allocate; controller never generates)", errInvalidNICSpec)
	}
	mac, err := net.ParseMAC(n.Spec.MAC)
	if err != nil {
		return netpool.NICDefinition{}, fmt.Errorf("%w: mac %q: %v", errInvalidNICSpec, n.Spec.MAC, err)
	}

	ip, err := netip.ParseAddr(n.Spec.IP)
	if err != nil {
		return netpool.NICDefinition{}, fmt.Errorf("%w: ip %q: %v", errInvalidNICSpec, n.Spec.IP, err)
	}

	// 内核身份不来自 spec，由稳定的 VM ref 确定性派生后填入定义。
	id := identity.DeriveNICIdentity(n.Spec.VMRef, nicIndex)

	hostname := dhcp.BindingHostname{}
	if n.Spec.Hostname != "" {
		hostname = dhcp.BindingHostname{Value: n.Spec.Hostname, Set: true}
	}

	return netpool.NICDefinition{
		NetworkName: netpool.NetworkName(n.Spec.NetworkRef),
		VMID:        netpool.VMID(n.Spec.VMRef),
		TapName:     id.TapName,
		MAC:         mac,
		IP:          ip,
		TapMTU:      tapMTU,
		VNetHeader:  id.VNetHeader,
		OwnerUID:    c.ownerUID,
		OwnerGID:    c.ownerGID,
		Hostname:    hostname,

		AntiSpoofTable:    id.AntiSpoofTable,
		AntiSpoofChain:    id.AntiSpoofChain,
		AntiSpoofPriority: id.AntiSpoofPriority,
	}, nil
}

// reportFailure patches a failed status carrying cause's message.
func (c *NICController) reportFailure(ctx context.Context, name string, cause error) error {
	return c.patchStatus(ctx, name, nicv1.NICStatus{
		Phase:   nicv1.NICPhaseFailed,
		Message: cause.Error(),
	})
}

// patchStatus marshals status and PATCHes it to the master's /status sub-resource.
func (c *NICController) patchStatus(ctx context.Context, name string, status nicv1.NICStatus) error {
	body, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("nic controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("nic controller: patch status for %q: %w", name, err)
	}
	return nil
}
