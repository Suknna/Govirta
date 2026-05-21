<p align="center">
  <img src="image/govirta_icon.png" alt="Govirta project icon" width="160" />
</p>

# Govirta

Govirta is a Go-based virtualization infrastructure project. It starts from the QEMU layer and builds upward toward a lightweight virtual machine orchestration platform.

The project targets ESXi / VMware-style infrastructure capabilities at the virtualization layer. Its long-term goal is to provide a lighter alternative to OpenStack for virtual machine orchestration scenarios where a full OpenStack deployment is too heavy.

## Architecture Direction

Govirta follows a Kubernetes-inspired architecture without depending on Kubernetes in the short term.

The system is split into:

- **Control plane (`govirtad`)**: owns resource modeling, API boundaries, scheduling, node coordination, and state management.
- **Compute node agent (`govirtlet`)**: runs on virtualization hosts and owns local QEMU, QMP, and Linux bridge integration.
- **CLI (`govirtctl`)**: provides an operator-facing command-line entrypoint.

The intended shape is:

```text
govirtctl -> govirtad control plane -> scheduler/store/control loops -> govirtlet -> QEMU/QMP/Linux bridge
```

## Technology Stack

- Go
- QEMU
- QMP
- Linux bridge
- zerolog for structured logging

## Current Status

Govirta is in an early, fast-iteration development phase. APIs, configuration, package boundaries, and architecture are expected to change substantially. Backward compatibility is not a goal at this stage.

## Roadmap

Govirta evolves cycle by cycle. Each cycle has a single north-star goal, a fixed scope, and a checklist of completion criteria. A cycle is only marked complete when every box is ticked. Cycle documents live under [`docs/roadmap/`](./docs/roadmap/README.md).

The virtualization stack is permanently QEMU + QMP + qemu-img + netlink. **Govirta will not integrate libvirt at any cycle.**

- [ ] **Cycle 1 — Single-node single-VM loop.** Boot and gracefully stop a cirros VM through Govirta's own code paths. *In progress.* See [`docs/roadmap/cycle-1-single-node-single-vm.md`](./docs/roadmap/cycle-1-single-node-single-vm.md).
- [ ] **Cycle 2 — Node daemon and multi-VM lifecycle.** govirtlet runs as a daemon, manages multiple VMs on one host, and recovers state across restarts. See [`docs/roadmap/cycle-2-node-daemon.md`](./docs/roadmap/cycle-2-node-daemon.md).
- [ ] **Cycle 3 — Control plane and scheduling.** govirtad coordinates multiple govirtlet nodes; govirtctl creates a VM end-to-end through the control plane. See [`docs/roadmap/cycle-3-control-plane.md`](./docs/roadmap/cycle-3-control-plane.md).
- [ ] **Cycle 4 — Production minimum.** Templates, cloud-init injection, VNC access, disk resize, basic auth and audit. See [`docs/roadmap/cycle-4-production-minimum.md`](./docs/roadmap/cycle-4-production-minimum.md).
- [ ] **Cycle 5 — Operations and resilience.** Live migration, online snapshot, backup, metrics, node drain. See [`docs/roadmap/cycle-5-operations.md`](./docs/roadmap/cycle-5-operations.md).

Update the checkbox above, the cycle document's `状态` field, and the AGENTS.md roadmap section together when a cycle's status changes.

## License

Govirta is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
