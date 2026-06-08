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
	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
)

// errUnsupportedArch marks a VM spec whose arch cannot be mapped to a supported
// QEMU machine profile. It is a permanent (config) error: a requeue cannot fix
// an unknown arch, so the object is reported failed and not re-enqueued.
var errUnsupportedArch = errors.New("vm controller: unsupported arch")

// VMRunner is the narrow slice of the VM process manager the controller needs:
// create a daemonized QEMU from a configured builder, start or gracefully stop
// it, read its live phase, and on teardown forcibly kill a running guest then
// delete its persisted state. Deletion is a destroy intent (ESXi-aligned), so teardown
// uses forced termination (QMP quit + SIGKILL fallback), not graceful ACPI
// powerdown — a minimal guest (e.g. CirrOS) ignores ACPI and would otherwise
// never converge. Graceful guest shutdown is a separate powerState=Shutdown concern.
// *vmm.VMMService satisfies it (积木式 + 可测).
type VMRunner interface {
	Create(ctx context.Context, req vmm.CreateRequest) (vmm.VM, error)
	Start(ctx context.Context, uuid string) (vmm.VM, error)
	Stop(ctx context.Context, uuid string) error
	Status(ctx context.Context, uuid string) (vmm.VM, error)
	Kill(ctx context.Context, uuid string) error
	Delete(ctx context.Context, uuid string) error
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
// DELETED is a no-op because the apiserver sends deletion intent as a normal
// object carrying deletionTimestamp/finalizers. For ADDED/MODIFIED it decodes the
// event key, refreshes the current VM object from the master before any side
// effect, gates on Volume/NIC readiness, and reconciles the process against the
// latest spec.powerState and live vmm phase.
//
// requeue semantics:
//   - dependency not ready / not found → delayed requeue, no failure patch (waiting)
//   - dependency read transport error  → delayed requeue, no failure patch (transient)
//   - unsupported arch / bad spec      → permanent failure, no requeue
//   - vmm Start/Stop/Kill/Status error → structured failure/progress patch + delayed requeue
func (c *VMController) Reconcile(ctx context.Context, ev controller.Event) (controller.ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return controller.Done(), fmt.Errorf("vm controller: context done before reconcile: %w", err)
	}

	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("vm deleted; delete is a no-op in this slice")
		return controller.Done(), nil
	}

	var obj vmv1.VM
	if err := json.Unmarshal(ev.Object, &obj); err != nil {
		return controller.Done(), fmt.Errorf("vm controller: decode object %q: %w", ev.Key, err)
	}
	current, exists, err := c.currentVM(ctx, obj.Name)
	if err != nil {
		return controller.RequeueAfter(vmPowerRequeueDelay), err
	}
	if !exists {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("vm no longer exists; stale event ignored")
		return controller.Done(), nil
	}
	obj = current

	// Teardown branch: a deletion-stamped object means apiserver wants this guest
	// gone. Unlike the other controllers, VM teardown is multi-step (kill-then-
	// delete): vmm.Delete refuses a running VM (ErrConflict), so a live guest must
	// first be forcibly terminated and the reconcile requeued until the
	// process exits. teardown returns done=true only once the guest is fully gone;
	// done=false means "teardown in progress" → requeue WITHOUT dropping the
	// finalizer so the object stays "deleting" until the next pass observes the
	// terminal state.
	if isDeleting(obj.ObjectMeta) {
		done, err := c.teardown(ctx, obj)
		if err != nil {
			return controller.Requeue(), fmt.Errorf("vm controller: teardown %q: %w", obj.Name, err)
		}
		if !done {
			return controller.Requeue(), nil
		}
		if err := removeTeardownFinalizer(ctx, c.client, c.Kind(), obj.Name); err != nil {
			return controller.Requeue(), fmt.Errorf("vm controller: remove finalizer %q: %w", obj.Name, err)
		}
		return controller.Done(), nil
	}

	live, err := c.vmm.Status(ctx, obj.UID)
	if err == nil {
		return c.reconcileExistingVM(ctx, obj, live)
	}
	if !errors.Is(err, vmm.ErrNotFound) {
		// A real status error (not "not yet created") is transient.
		if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
			return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: status %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: status %q: %w", obj.Name, err)
	}

	return c.reconcileMissingVM(ctx, ev.Key, obj)
}

func (c *VMController) reconcileMissingVM(ctx context.Context, key string, obj vmv1.VM) (controller.ReconcileResult, error) {
	if obj.Spec.PowerState == vmv1.PowerStateShutdown {
		err := fmt.Errorf("%w: powerState Shutdown is only valid for an existing VM", vmv1.ErrInvalidSpec)
		desired := vmv1.VMStatus{
			Phase:              vmv1.VMPhaseFailed,
			ObservedPowerState: vmv1.ObservedPowerStateOff,
			PowerTransition:    vmv1.PowerTransitionShutdownRequested,
			Message:            err.Error(),
		}
		if perr := c.patchVMStatusIfChanged(ctx, obj.Name, desired); perr != nil {
			return controller.Done(), fmt.Errorf("vm controller: report invalid shutdown create %q: %w", obj.Name, perr)
		}
		return controller.Done(), nil
	}

	// Gate on dependencies and collect the disk paths + tap names they expose.
	diskPaths, tapNames, ready, err := c.gatherDependencies(ctx, obj)
	if err != nil {
		// Dependency read transport error: transient, wait and retry.
		return controller.RequeueAfter(vmDependencyRequeueDelay), fmt.Errorf("vm controller: gate dependencies for %q: %w", obj.Name, err)
	}
	if !ready {
		logger := zerolog.Ctx(ctx)
		logger.Info().Str("key", key).Msg("vm dependencies not ready; delayed requeue")
		return controller.RequeueAfter(vmDependencyRequeueDelay), nil
	}

	builder, err := c.buildVM(obj, diskPaths, tapNames)
	if err != nil {
		// Bad spec (e.g. unsupported arch) is permanent: requeue cannot fix it.
		if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
			return controller.Done(), fmt.Errorf("vm controller: build %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		logger := zerolog.Ctx(ctx)
		logger.Error().Err(err).Str("key", key).Msg("vm spec rejected permanently (config error); not requeuing")
		return controller.Done(), nil
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
	created, err := c.vmm.Create(ctx, create)
	if err != nil && !errors.Is(err, vmm.ErrAlreadyExists) {
		if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
			return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: create %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: create %q: %w", obj.Name, err)
	}
	if errors.Is(err, vmm.ErrAlreadyExists) {
		created = vmm.VM{Phase: vmm.PhaseDefined}
	}

	if obj.Spec.PowerState == vmv1.PowerStateOff {
		desired, known := vmPowerStatus(obj.Spec.PowerState, created.Phase, "")
		c.logUnknownPhase(ctx, key, obj.UID, created.Phase, known)
		if err := c.patchVMStatusIfChanged(ctx, obj.Name, desired); err != nil {
			return controller.RequeueAfter(vmPowerRequeueDelay), err
		}
		return controller.Done(), nil
	}

	started, err := c.vmm.Start(ctx, obj.UID)
	if err != nil {
		desired := vmv1.VMStatus{
			Phase:              vmv1.VMPhaseFailed,
			ObservedPowerState: vmv1.ObservedPowerStateOff,
			PowerTransition:    vmv1.PowerTransitionStarting,
			Message:            err.Error(),
		}
		if perr := c.patchVMStatusIfChanged(ctx, obj.Name, desired); perr != nil {
			return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: start %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: start %q: %w", obj.Name, err)
	}

	desired, known := vmPowerStatus(obj.Spec.PowerState, started.Phase, "")
	c.logUnknownPhase(ctx, key, obj.UID, started.Phase, known)
	if err := c.patchVMStatusIfChanged(ctx, obj.Name, desired); err != nil {
		return controller.RequeueAfter(vmPowerRequeueDelay), err
	}
	if !known {
		return controller.Done(), nil
	}

	obs := observePower(started.Phase, obj.Spec.PowerState)
	requeue := powerNeedsDelayedRequeue(obs) || isTransientPhase(started.Phase)
	logger := zerolog.Ctx(ctx)
	logger.Info().
		Str("key", key).
		Str("uuid", obj.UID).
		Str("phase", string(started.Phase)).
		Bool("requeue", requeue).
		Dur("requeue_after", vmPowerRequeueDelay).
		Msg("vm reconciled")
	if requeue {
		return controller.RequeueAfter(vmPowerRequeueDelay), nil
	}
	return controller.Done(), nil
}

func (c *VMController) reconcileExistingVM(ctx context.Context, obj vmv1.VM, live vmm.VM) (controller.ReconcileResult, error) {
	switch obj.Spec.PowerState {
	case vmv1.PowerStateOn:
		return c.reconcileExistingVMOn(ctx, obj, live)
	case vmv1.PowerStateShutdown:
		return c.reconcileExistingVMShutdown(ctx, obj, live)
	case vmv1.PowerStateOff:
		return c.reconcileExistingVMOff(ctx, obj, live)
	default:
		err := fmt.Errorf("%w: powerState %q must be one of On, Shutdown, Off", vmv1.ErrInvalidSpec, obj.Spec.PowerState)
		if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
			return controller.Done(), fmt.Errorf("vm controller: report invalid powerState %q: %w", obj.Name, errors.Join(err, perr))
		}
		return controller.Done(), nil
	}
}

func (c *VMController) reconcileExistingVMOn(ctx context.Context, obj vmv1.VM, live vmm.VM) (controller.ReconcileResult, error) {
	if live.Phase == vmm.PhaseDefined || live.Phase == vmm.PhaseStopped || live.Phase == vmm.PhaseFailed {
		started, err := c.vmm.Start(ctx, obj.UID)
		if err != nil {
			desired := vmv1.VMStatus{
				Phase:              vmv1.VMPhaseFailed,
				ObservedPowerState: vmv1.ObservedPowerStateOff,
				PowerTransition:    vmv1.PowerTransitionStarting,
				Message:            err.Error(),
			}
			if perr := c.patchVMStatusIfChanged(ctx, obj.Name, desired); perr != nil {
				return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: start %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
			}
			return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: start %q: %w", obj.Name, err)
		}
		return c.patchLivePowerStatus(ctx, obj, started)
	}
	return c.patchLivePowerStatus(ctx, obj, live)
}

func (c *VMController) reconcileExistingVMShutdown(ctx context.Context, obj vmv1.VM, live vmm.VM) (controller.ReconcileResult, error) {
	obs := observePower(live.Phase, obj.Spec.PowerState)
	if obs.Observed == vmv1.ObservedPowerStateOn {
		desired := vmStatus(obs, "shutdown requested via ACPI; waiting for guest to power off")
		if err := c.vmm.Stop(ctx, obj.UID); err != nil {
			desired.Message = "shutdown request failed: " + err.Error()
			if perr := c.patchVMStatusIfChanged(ctx, obj.Name, desired); perr != nil {
				return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: stop %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
			}
			return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: stop %q: %w", obj.Name, err)
		}
		if err := c.patchVMStatusIfChanged(ctx, obj.Name, desired); err != nil {
			return controller.RequeueAfter(vmPowerRequeueDelay), err
		}
		return controller.RequeueAfter(vmPowerRequeueDelay), nil
	}
	return c.patchLivePowerStatus(ctx, obj, live)
}

func (c *VMController) reconcileExistingVMOff(ctx context.Context, obj vmv1.VM, live vmm.VM) (controller.ReconcileResult, error) {
	obs := observePower(live.Phase, obj.Spec.PowerState)
	if obs.Observed == vmv1.ObservedPowerStateOn {
		desired := vmStatus(obs, "force power off requested")
		if err := c.vmm.Kill(ctx, obj.UID); err != nil {
			desired.Message = "force power off failed: " + err.Error()
			if perr := c.patchVMStatusIfChanged(ctx, obj.Name, desired); perr != nil {
				return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: kill %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
			}
			return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: kill %q: %w", obj.Name, err)
		}
		if err := c.patchVMStatusIfChanged(ctx, obj.Name, desired); err != nil {
			return controller.RequeueAfter(vmPowerRequeueDelay), err
		}
		return controller.RequeueAfter(vmPowerRequeueDelay), nil
	}
	return c.patchLivePowerStatus(ctx, obj, live)
}

func (c *VMController) patchLivePowerStatus(ctx context.Context, obj vmv1.VM, live vmm.VM) (controller.ReconcileResult, error) {
	desired, known := vmPowerStatus(obj.Spec.PowerState, live.Phase, "")
	c.logUnknownPhase(ctx, obj.Name, obj.UID, live.Phase, known)
	if err := c.patchVMStatusIfChanged(ctx, obj.Name, desired); err != nil {
		return controller.RequeueAfter(vmPowerRequeueDelay), err
	}
	if !known {
		return controller.Done(), nil
	}
	obs := observePower(live.Phase, obj.Spec.PowerState)
	if powerNeedsDelayedRequeue(obs) || isTransientPhase(live.Phase) {
		return controller.RequeueAfter(vmTransientRequeueDelay), nil
	}
	return controller.Done(), nil
}

func (c *VMController) logUnknownPhase(ctx context.Context, key, uuid string, phase vmm.Phase, known bool) {
	if known {
		return
	}
	logger := zerolog.Ctx(ctx)
	logger.Warn().
		Str("key", key).
		Str("uuid", uuid).
		Str("vmm_phase", string(phase)).
		Msg("unknown vmm phase mapped to Failed; vmm.Phase enum may have drifted")
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
			// Explicitly disable the PXE/network-boot option ROM (romfile=).
			// This project does not support PXE boot: a cold-boot guest boots
			// from its disk, and leaving the default would require the host
			// QEMU install to ship efi-virtio.rom, making spawn fail wherever
			// that file is absent. The decision is made here in the controller
			// rather than defaulted in the builder (显式优于隐式).
			RomFile: qflag.String(""),
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

// teardown drives the guest process toward gone, returning done=true only once
// the process and its persisted state no longer exist. Because vmm.Delete refuses
// a running VM (ErrConflict), teardown is a phase-driven state machine reading the
// live vmm phase:
//
//   - vmm.ErrNotFound from Status → the state file is already gone: torn down
//     (done=true). A re-driven teardown lands here and drops the finalizer.
//   - PhaseRunning / PhaseStarting → still alive: forcibly kill the guest
//     (vmm.Kill = QMP quit + SIGKILL fallback) and report done=false so the
//     reconcile requeues to await exit. Forced termination does not depend on
//     guest cooperation, so it converges even for a guest that ignores ACPI
//     powerdown (e.g. CirrOS); a graceful ACPI stop would loop forever here.
//     The finalizer is kept until the process actually leaves. Kill is
//     idempotent, so a requeue that re-issues it on a still-dying guest is safe.
//   - PhaseStopping → termination already in flight: do nothing, done=false,
//     requeue to await the terminal state.
//   - PhaseStopped / PhaseFailed / PhaseDefined → the process is dead: vmm.Delete
//     removes the persisted runtime state. Deleting an already-gone VM
//     (vmm.ErrNotFound) is an idempotent success. done=true on a clean delete so
//     the finalizer is dropped.
//
// Any unexpected error (Status / Kill / Delete) is returned so the reconcile
// requeues with the finalizer kept; teardown never drops a finalizer on a guest
// it could not confirm gone.
func (c *VMController) teardown(ctx context.Context, obj vmv1.VM) (bool, error) {
	v, err := c.vmm.Status(ctx, obj.UID)
	if err != nil {
		if errors.Is(err, vmm.ErrNotFound) {
			// Process/state already gone: torn down, drop the finalizer.
			return true, nil
		}
		return false, fmt.Errorf("vm controller: status %q for teardown: %w", obj.Name, err)
	}

	switch v.Phase {
	case vmm.PhaseRunning, vmm.PhaseStarting:
		// Alive: forcibly kill the guest, then requeue to await process exit.
		// Kill (QMP quit + SIGKILL fallback) does not depend on guest ACPI
		// cooperation, so teardown converges even when the guest ignores a
		// graceful powerdown. The finalizer is kept (done=false) until a later
		// pass observes a terminal phase and reaches Delete.
		if err := c.vmm.Kill(ctx, obj.UID); err != nil {
			return false, fmt.Errorf("vm controller: kill %q for teardown: %w", obj.Name, err)
		}
		return false, nil
	case vmm.PhaseStopping:
		// Termination already in flight: requeue to await the terminal state.
		return false, nil
	default:
		// PhaseStopped / PhaseFailed / PhaseDefined: process is dead, remove the
		// persisted state. An already-gone VM is an idempotent success.
		if err := c.vmm.Delete(ctx, obj.UID); err != nil && !errors.Is(err, vmm.ErrNotFound) {
			return false, fmt.Errorf("vm controller: delete %q for teardown: %w", obj.Name, err)
		}
		return true, nil
	}
}

// reportFailure patches a failed status carrying cause's message and complete
// structured power fields, skipping the PATCH when the fresh master-side VM
// status already matches.
func (c *VMController) reportFailure(ctx context.Context, name string, desiredPower vmv1.PowerState, cause error) error {
	desired := vmv1.VMStatus{
		Phase:              vmv1.VMPhaseFailed,
		ObservedPowerState: vmv1.ObservedPowerStateOff,
		PowerTransition:    vmv1.PowerTransitionNone,
		Message:            cause.Error(),
	}
	if desiredPower == vmv1.PowerStateOn {
		desired.PowerTransition = vmv1.PowerTransitionStarting
	}
	return c.patchVMStatusIfChanged(ctx, name, desired)
}

// patchVMStatusIfChanged reads the current VM object before comparing status.
// Delayed self-requeues reuse the old watch Event.Object; comparing against that
// stale status would repeatedly PATCH an already-current status and recreate the
// feedback loop Knife 1 fixed.
func (c *VMController) patchVMStatusIfChanged(ctx context.Context, name string, desired vmv1.VMStatus) error {
	current, exists, err := c.currentVM(ctx, name)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("vm controller: get fresh VM %q before status patch: %w", name, client.ErrNotFound)
	}
	if current.Status == desired {
		return nil
	}
	return c.patchStatus(ctx, name, desired)
}

func (c *VMController) currentVM(ctx context.Context, name string) (vmv1.VM, bool, error) {
	raw, err := c.client.Get(ctx, c.Kind(), name)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return vmv1.VM{}, false, nil
		}
		return vmv1.VM{}, false, fmt.Errorf("vm controller: get current VM %q: %w", name, err)
	}
	var current vmv1.VM
	if err := json.Unmarshal(raw, &current); err != nil {
		return vmv1.VM{}, false, fmt.Errorf("vm controller: decode current VM %q: %w", name, err)
	}
	return current, true, nil
}

// patchStatus marshals desired and PATCHes it to the master's /status
// sub-resource. Callers must use patchVMStatusIfChanged for normal VM status
// writes so the no-op guard compares against a fresh master object.
func (c *VMController) patchStatus(ctx context.Context, name string, desired vmv1.VMStatus) error {
	body, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("vm controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("vm controller: patch status for %q: %w", name, err)
	}
	return nil
}
