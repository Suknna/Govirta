# Govirta

Govirta is a Go-based virtualization infrastructure project that builds from QEMU upward.

The project targets ESXi and VMware-style infrastructure operations, with the long-term goal of becoming a lightweight alternative to OpenStack for environments that need practical virtualization control without adopting a large cloud platform.

## Architecture Direction

Govirta's architecture direction is inspired by Kubernetes control-plane patterns, but the project does not depend on Kubernetes in the short term. The initial focus is a small, explicit control plane and compute-node model that can iterate quickly while keeping room for future scheduling, reconciliation, and distributed state design.

Core components:

- `govirtad`: control plane daemon.
- `govirtlet`: compute node agent.
- `govirtctl`: command-line client.

Process model:

```text
govirtctl -> govirtad control plane -> scheduler/store/control loops -> govirtlet -> QEMU/QMP/Linux bridge
```

## Technology Stack

- Go
- QEMU
- QMP
- Linux bridge
- zerolog

## Status

Govirta is in a fast iteration phase. The repository is intentionally starting with a small foundation so core architecture, package boundaries, and runtime contracts can be shaped as the implementation grows.

## License

Govirta is licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.
