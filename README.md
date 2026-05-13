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

## License

Govirta is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
