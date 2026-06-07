package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/vmm"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qemu/device"
	"github.com/suknna/govirta/pkg/virt/qemu/machine"
	"github.com/suknna/govirta/pkg/virt/qemu/netdev"
)

// errUnsupportedArch marks a VM spec whose arch cannot be mapped to a supported
// QEMU machine profile. It is a permanent (config) error: a requeue cannot fix
// an unknown arch, so the object is reported failed and not re-enqueued.
var errUnsupportedArch = errors.New("vm controller: unsupported arch")

// VMRunner is the narrow slice of the VM process manager the controller needs:
// create a daemonized QEMU from a configured builder, start it, and read its
// live phase. *vmm.VMMService satisfies it (积木式 + 可测).
type VMRunner interface {
	Create(ctx context.Context, req vmm.CreateRequest) (vmm.VM, error)
	Start(ctx context.Context, uuid string) (vmm.VM, error)
	Status(ctx context.Context, uuid string) (vmm.VM, error)
}

// 编译期证明真实生产类型满足窄接口。
var (
	_ VMRunner              = (*vmm.VMMService)(nil)
	_ controller.Controller = (*VMController)(nil)
)

// VMController reconciles VM objects. It gates on every referenced Volume and
// NIC being live Ready, reads the root disk host path from the Volume status and
// the host TAP name from the NIC status, assembles a typed qemu.Builder, and
// drives the VM process through vmm.Create + vmm.Start. It then reads the live
// vmm phase and patches it up to the master.
//
// The controller threads the live phase on every reconcile — including watch
// MODIFIED events and periodic resyncs — so the master always reflects the
// running guest's real state (上下一致: the running QEMU + QMP is the single
// source of truth, never an upper-layer cache).
//
// vmm process lifecycle is decoupled from the orchestrator: an already-running
// VM is detected via vmm.Status and only re-reported, never re-created, so a
// controller restart reattaches rather than disturbing the live guest.
type VMController struct {
	vmm    VMRunner
	client DependencyReader
	cpu    cpu.Model
}

// NewVMController wires a VMController against the VM process manager, the master
// dependency/status client, and the guest CPU model the node runs guests with.
func NewVMController(runner VMRunner, client DependencyReader, cpuModel cpu.Model) *VMController {
	return &VMController{vmm: runner, client: client, cpu: cpuModel}
}

// Kind is the apis kind this controller watches.
func (c *VMController) Kind() string {
	return string(metav1.KindVM)
}

// Reconcile drives one VM event toward its desired state.
//
// DELETED is a no-op in this slice. For ADDED/MODIFIED it decodes the object,
// gates on its Volume and NIC dependencies being live Ready (a missing or
// not-ready dependency requeues without reporting failure — it is a wait, not an
// error), then ensures the guest process exists and reports its live phase.
//
// requeue semantics:
//   - dependency not ready / not found → requeue=true, no failure patch (waiting)
//   - dependency read transport error  → requeue=true, no failure patch (transient)
//   - unsupported arch / bad spec       → permanent failure, no requeue
//   - vmm.Create / Start / Status error → failure patch + requeue (transient)
func (c *VMController) Reconcile(ctx context.Context, ev controller.Event) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("vm controller: context done before reconcile: %w", err)
	}

	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("vm deleted; delete is a no-op in this slice")
		return false, nil
	}

	var obj vmv1.VM
	if err := json.Unmarshal(ev.Object, &obj); err != nil {
		return false, fmt.Errorf("vm controller: decode object %q: %w", ev.Key, err)
	}

	// Idempotency / liveness loop: if the guest already exists, never re-create.
	// Re-report its live phase so the master tracks the running guest (存活循环).
	if existing, err := c.vmm.Status(ctx, obj.UID); err == nil {
		phase, known := mapVMPhase(existing.Phase)
		if !known {
			logger.Warn().
				Str("key", ev.Key).
				Str("uuid", obj.UID).
				Str("vmm_phase", string(existing.Phase)).
				Msg("unknown vmm phase mapped to Failed; vmm.Phase enum may have drifted")
		}
		if err := c.patchStatus(ctx, obj.Name, obj.Status, vmv1.VMStatus{Phase: phase}); err != nil {
			return true, err
		}
		// Transient phases (Starting/Stopping) settle on their own; requeue so
		// the master tracks the guest to a terminal phase instead of freezing at
		// the in-flight value until an unrelated watch event arrives (M-1).
		requeue := isTransientPhase(existing.Phase)
		logger.Info().
			Str("key", ev.Key).
			Str("uuid", obj.UID).
			Str("phase", string(existing.Phase)).
			Bool("requeue", requeue).
			Msg("vm already exists; re-reported live phase")
		return requeue, nil
	} else if !errors.Is(err, vmm.ErrNotFound) {
		// A real status error (not "not yet created") is transient.
		if perr := c.reportFailure(ctx, obj.Name, obj.Status, err); perr != nil {
			return true, fmt.Errorf("vm controller: status %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("vm controller: status %q: %w", obj.Name, err)
	}

	// Gate on dependencies and collect the disk paths + tap names they expose.
	diskPaths, tapNames, ready, err := c.gatherDependencies(ctx, obj)
	if err != nil {
		// Dependency read transport error: transient, wait and retry.
		return true, fmt.Errorf("vm controller: gate dependencies for %q: %w", obj.Name, err)
	}
	if !ready {
		logger.Info().Str("key", ev.Key).Msg("vm dependencies not ready; requeuing")
		return true, nil
	}

	builder, err := c.buildVM(obj, diskPaths, tapNames)
	if err != nil {
		// Bad spec (e.g. unsupported arch) is permanent: requeue cannot fix it.
		if perr := c.reportFailure(ctx, obj.Name, obj.Status, err); perr != nil {
			return false, fmt.Errorf("vm controller: build %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		logger.Error().Err(err).Str("key", ev.Key).Msg("vm spec rejected permanently (config error); not requeuing")
		return false, nil
	}

	create := vmm.CreateRequest{
		UUID:    obj.UID,
		Builder: builder,
		Spec: vmm.SpecSummary{
			Arch:      obj.Spec.Arch,
			VCPUs:     obj.Spec.VCPUs,
			MemoryMiB: obj.Spec.MemoryMiB,
			DiskPaths: diskPaths,
			TapNames:  tapNames,
		},
	}
	if _, err := c.vmm.Create(ctx, create); err != nil && !errors.Is(err, vmm.ErrAlreadyExists) {
		if perr := c.reportFailure(ctx, obj.Name, obj.Status, err); perr != nil {
			return true, fmt.Errorf("vm controller: create %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("vm controller: create %q: %w", obj.Name, err)
	}

	started, err := c.vmm.Start(ctx, obj.UID)
	if err != nil {
		if perr := c.reportFailure(ctx, obj.Name, obj.Status, err); perr != nil {
			return true, fmt.Errorf("vm controller: start %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("vm controller: start %q: %w", obj.Name, err)
	}

	startedPhase, known := mapVMPhase(started.Phase)
	if !known {
		logger.Warn().
			Str("key", ev.Key).
			Str("uuid", obj.UID).
			Str("vmm_phase", string(started.Phase)).
			Msg("unknown vmm phase mapped to Failed; vmm.Phase enum may have drifted")
	}
	if err := c.patchStatus(ctx, obj.Name, obj.Status, vmv1.VMStatus{Phase: startedPhase}); err != nil {
		return true, err
	}

	// A freshly started guest is typically Starting/Running; if it is still in a
	// transient phase, requeue to track it to terminal (same liveness rule as the
	// already-exists path above, M-1).
	requeue := isTransientPhase(started.Phase)
	logger.Info().
		Str("key", ev.Key).
		Str("uuid", obj.UID).
		Str("phase", string(started.Phase)).
		Bool("requeue", requeue).
		Msg("vm reconciled")
	return requeue, nil
}

// gatherDependencies reads every referenced Volume and NIC object's live status.
// It returns the ordered root disk host paths (from Volume status.VolumePath) and
// host tap names (from NIC status.TapName). ready is false when any dependency is
// missing or not yet Ready (a wait, not an error). A non-nil error is a transport
// failure reading a dependency, which the caller treats as transient.
func (c *VMController) gatherDependencies(ctx context.Context, obj vmv1.VM) (diskPaths, tapNames []string, ready bool, err error) {
	for _, ref := range obj.Spec.VolumeRefs {
		raw, gerr := c.client.Get(ctx, string(metav1.KindVolume), ref)
		if gerr != nil {
			if errors.Is(gerr, client.ErrNotFound) {
				return nil, nil, false, nil
			}
			return nil, nil, false, fmt.Errorf("read Volume %q: %w", ref, gerr)
		}
		var vol volumev1.Volume
		if uerr := json.Unmarshal(raw, &vol); uerr != nil {
			return nil, nil, false, fmt.Errorf("decode Volume %q: %w", ref, uerr)
		}
		if vol.Status.Phase != volumev1.VolumePhaseReady || vol.Status.VolumePath == "" {
			return nil, nil, false, nil
		}
		diskPaths = append(diskPaths, vol.Status.VolumePath)
	}

	for _, ref := range obj.Spec.NICRefs {
		raw, gerr := c.client.Get(ctx, string(metav1.KindNIC), ref)
		if gerr != nil {
			if errors.Is(gerr, client.ErrNotFound) {
				return nil, nil, false, nil
			}
			return nil, nil, false, fmt.Errorf("read NIC %q: %w", ref, gerr)
		}
		var nic nicv1.NIC
		if uerr := json.Unmarshal(raw, &nic); uerr != nil {
			return nil, nil, false, fmt.Errorf("decode NIC %q: %w", ref, uerr)
		}
		if nic.Status.Phase != nicv1.NICPhaseReady || nic.Status.TapName == "" {
			return nil, nil, false, nil
		}
		tapNames = append(tapNames, nic.Status.TapName)
	}

	return diskPaths, tapNames, true, nil
}

// buildVM assembles a configured-but-not-built qemu.Builder from the VM spec and
// the resolved dependency host resources. Per the vmm.CreateRequest contract the
// controller sets only domain config (arch/cpu/smp/memory/machine/disk/tap);
// vmm injects the facility flags (QMP, pidfile, daemonize) and calls Build.
func (c *VMController) buildVM(obj vmv1.VM, diskPaths, tapNames []string) (*qemu.Builder, error) {
	arch, profile, err := mapArch(obj.Spec.Arch)
	if err != nil {
		return nil, err
	}

	b := qemu.NewVM(arch).
		Name(obj.Name).
		Machine(profile).
		CPU(c.cpu).
		SMP(qemu.SMP{CPUs: obj.Spec.VCPUs, Cores: obj.Spec.VCPUs, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(obj.Spec.MemoryMiB))

	for i, path := range diskPaths {
		node := fmt.Sprintf("disk%d", i)
		b = b.AddBlockdev(blockdev.Qcow2{
			NodeName: node,
			File:     blockdev.FileProtocol{Filename: path},
			Cache:    blockdev.Cache{Direct: qemu.Off},
			AIO:      blockdev.AIOThreads,
		}).AddDevice(device.VirtioBlkPCI{
			ID:    fmt.Sprintf("blk%d", i),
			Drive: blockdev.Ref(node),
		})
	}

	for i, tap := range tapNames {
		netID := fmt.Sprintf("net%d", i)
		b = b.AddNetdev(netdev.Tap{
			ID:         netID,
			IfName:     tap,
			Script:     netdev.ScriptNo,
			DownScript: netdev.ScriptNo,
			Vhost:      qemu.On,
		}).AddDevice(device.VirtioNetPCI{
			ID:     fmt.Sprintf("nic%d", i),
			Netdev: netdev.Ref(netID),
		})
	}

	return b, nil
}

// mapArch maps an apis VM spec arch string to a typed qemu.Arch and the supported
// KVM machine profile for it. An unknown arch is a permanent config error. It is
// an explicit switch over the supported set (项目铁律: 禁止裸 string 推断).
func mapArch(arch string) (qemu.Arch, machine.Profile, error) {
	switch arch {
	case "x86_64":
		return qemu.ArchX86_64, machine.ProfileX86_64Q35KVM, nil
	case "aarch64":
		return qemu.ArchAArch64, machine.ProfileAArch64VirtKVM, nil
	default:
		return "", "", fmt.Errorf("%w: %q", errUnsupportedArch, arch)
	}
}

// mapVMPhase maps the live vmm.Phase to the apis VMPhase. Both enums mirror each
// other by value but are defined independently (契约层不依赖 internal); the
// mapping is an explicit switch (项目铁律: 禁止裸 string 转换). The second return
// is false when p is outside the known enum: callers log it as enum drift rather
// than silently reporting a bogus Failed (M-3). A false still maps to Failed so a
// drifted node never reports a guest as healthy by accident.
func mapVMPhase(p vmm.Phase) (vmv1.VMPhase, bool) {
	switch p {
	case vmm.PhaseDefined:
		return vmv1.VMPhaseDefined, true
	case vmm.PhaseStarting:
		return vmv1.VMPhaseStarting, true
	case vmm.PhaseRunning:
		return vmv1.VMPhaseRunning, true
	case vmm.PhaseStopping:
		return vmv1.VMPhaseStopping, true
	case vmm.PhaseStopped:
		return vmv1.VMPhaseStopped, true
	case vmm.PhaseFailed:
		return vmv1.VMPhaseFailed, true
	default:
		return vmv1.VMPhaseFailed, false
	}
}

// isTransientPhase reports whether p is an in-flight phase that will settle on
// its own (Starting → Running, Stopping → Stopped). The liveness path requeues
// these so the master tracks the guest to a terminal phase instead of freezing
// at the in-flight value until an unrelated watch event arrives (M-1).
//
// Caveat: the framework's queue has no backoff (最薄实现), so a requeue here is a
// tight poll of vmm.Status until the phase settles — bounded by the transient
// phase's own duration (typically sub-second to a few seconds), not infinite. A
// rate-limited requeue belongs to a later framework iteration; until then a
// bounded tight poll is preferred over a guest frozen at Starting in the master.
func isTransientPhase(p vmm.Phase) bool {
	return p == vmm.PhaseStarting || p == vmm.PhaseStopping
}

// reportFailure patches a failed status carrying cause's message, skipping the
// PATCH when the observed status already matches (no-op guard).
func (c *VMController) reportFailure(ctx context.Context, name string, observed vmv1.VMStatus, cause error) error {
	return c.patchStatus(ctx, name, observed, vmv1.VMStatus{
		Phase:   vmv1.VMPhaseFailed,
		Message: cause.Error(),
	})
}

// patchStatus marshals desired and PATCHes it to the master's /status
// sub-resource, but only when it differs from observed (the status carried by the
// watched object). Skipping an identical PATCH breaks the status→MODIFIED→watch→
// reconcile→PATCH feedback loop that would otherwise spin every reconcile (level-
// triggered idempotence). The Status structs are comparable (scalar fields only),
// so == is a sound equality test.
func (c *VMController) patchStatus(ctx context.Context, name string, observed, desired vmv1.VMStatus) error {
	if observed == desired {
		return nil
	}
	body, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("vm controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("vm controller: patch status for %q: %w", name, err)
	}
	return nil
}
