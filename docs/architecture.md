# Govirta Architecture

Govirta is a distributed virtual machine cluster platform that starts from QEMU and builds upward into cluster-wide orchestration. From the ground up it is a Kubernetes-inspired master/node cluster: each `govirtlet` compute node opens a long-lived, node-initiated connection to the `govirtad` control plane, registers, and executes VM tasks dispatched over that channel.

## Architectural Inspiration

Govirta is a Kubernetes-inspired cluster:

- separate control plane and node agent responsibilities;
- model infrastructure resources explicitly;
- reconcile desired state through future control loops;
- schedule workloads onto nodes through a scheduler boundary;
- connect nodes to the master over a long-lived, node-initiated task channel.

Govirta is k8s-inspired, not k8s-integrated: it does not depend on, run on, or define Kubernetes or CRDs. This exclusion is permanent, not a short-term deferral.

## Process Model

```text
govirtctl ──▶ govirtad ◀══════════════════╗  (1) govirtlet dials the master:
              (master:                     ║      node-initiated, long-lived
               scheduler / store /         ║      connection
               control loops)              ║
                 ║                         ║
                 ╚══ (2) dispatch tasks ══▶ govirtlet ──▶ QEMU / QMP / Linux bridge
                        over same channel   (node)
```

The connection is always initiated by the node. `govirtlet` dials `govirtad` and keeps the channel open; the master then dispatches VM tasks back to the node over that same long-lived connection. The master never opens connections to nodes.

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
- `internal/network`: VM-facing network orchestration layer over `internal/hostnet/*` host primitives (link/route/firewall/dhcp).

## Fast Iteration Policy

Govirta is currently in a fast-iteration phase. Backward compatibility is not a goal. Incorrect abstractions should be replaced instead of preserved behind compatibility shims.
