# Govirta Architecture

Govirta is a lightweight virtual machine orchestration platform that starts from QEMU and builds upward.

## Architectural Inspiration

Govirta borrows architectural ideas from Kubernetes:

- separate control plane and node agent responsibilities;
- model infrastructure resources explicitly;
- reconcile desired state through future control loops;
- schedule workloads onto nodes through a scheduler boundary.

Govirta does not depend on Kubernetes or CRDs in the short term.

## Process Model

```text
govirtctl -> govirtad -> scheduler/store/control loops -> govirtlet -> QEMU/QMP/Linux bridge
```

## Components

- `govirtad`: control plane process.
- `govirtlet`: compute node process.
- `govirtctl`: CLI process.
- `internal/apiserver`: API boundary.
- `internal/controlplane`: control plane composition.
- `internal/scheduler`: placement boundary.
- `internal/store`: state boundary.
- `internal/node`: node agent composition.
- `internal/virt/qemu`: QEMU process abstraction.
- `internal/virt/qmp`: QMP protocol abstraction.
- `internal/network/bridge`: Linux bridge abstraction.

## Fast Iteration Policy

Govirta is currently in a fast-iteration phase. Backward compatibility is not a goal. Incorrect abstractions should be replaced instead of preserved behind compatibility shims.
