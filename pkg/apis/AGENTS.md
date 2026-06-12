# pkg/apis Knowledge Base

<!--
Verified-against:
  base_commit: dfad16b
  files:
    - pkg/apis/meta/v1alpha1/types.go
    - pkg/apis/meta/v1alpha1/types_test.go
    - pkg/apis/storagepool/v1alpha1/types.go
    - pkg/apis/storagepool/v1alpha1/types_test.go
    - pkg/apis/image/v1alpha1/types.go
    - pkg/apis/image/v1alpha1/types_test.go
    - pkg/apis/volume/v1alpha1/types.go
    - pkg/apis/volume/v1alpha1/types_test.go
    - pkg/apis/network/v1alpha1/types.go
    - pkg/apis/network/v1alpha1/types_test.go
    - pkg/apis/nic/v1alpha1/types.go
    - pkg/apis/nic/v1alpha1/types_test.go
    - pkg/apis/vm/v1alpha1/types.go
    - pkg/apis/vm/v1alpha1/types_test.go
    - pkg/apis/snapshot/v1alpha1/types.go
    - pkg/apis/snapshot/v1alpha1/types_test.go
    - pkg/apis/roundtrip_test.go
  flows: []
-->

## OVERVIEW

API contract layer: typed Go structs for 7 resource kinds (StoragePool, Image, Volume, Network, NIC, VM, Snapshot) plus shared metadata envelope. Every package depends only on stdlib and the shared `meta/v1alpha1` envelope; no `internal/` or `pkg/hostnet` imports. Types mirror internal domain values by string but are defined independently.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Shared metadata | `meta/v1alpha1/types.go` | `TypeMeta` (apiVersion+kind), `ObjectMeta` (name+uid+rv+nodeName+labels) |
| StoragePool types | `storagepool/v1alpha1/types.go` | `BackendType`, `PoolType`, `PoolPhase`, `StoragePoolSpec`, `StoragePoolStatus` |
| Image types | `image/v1alpha1/types.go` | `ImageFormat`, `ImageSourceType`, `ImagePhase`, `ImageSpec`, `ImageStatus` |
| Volume types | `volume/v1alpha1/types.go` | `VolumeRole`, `VolumePhase`, `VolumeSpec`, `VolumeStatus` |
| Network types | `network/v1alpha1/types.go` | `NetworkPhase`, `NetworkSpec` (subnet/gateway/DHCP/egress/DNS/router/lease) |
| NIC types | `nic/v1alpha1/types.go` | `NICPhase`, `NICSpec` (networkRef/vmRef/MAC/IP/hostname) |
| VM types | `vm/v1alpha1/types.go` | `VMPhase` (6 phases), `VMSpec` (arch/vcpus/memory/volumeRefs/nicRefs) |
| Snapshot types | `snapshot/v1alpha1/types.go` | whole-VM cold snapshot: `SnapshotSpec{VMRef}`, `SnapshotPhase`, per-disk results |
| Round-trip test | `roundtrip_test.go` | proves flat `apiVersion/kind/metadata/spec/status` JSON envelope |

## CONVENTIONS

- All objects use `metav1.TypeMeta` (inline) + `metav1.ObjectMeta` (`json:"metadata"`) + `Spec` (`json:"spec"`) + `Status` (`json:"status"`).
- Enum-like discriminators use dedicated `string` types with named constants + `Valid() bool` method.
- Every `XxxSpec` has `Validate() error` returning `ErrInvalidSpec` wrapped with context.
- Optional fields use `omitempty`; addresses are strings on the wire with `Validate()` parsing via `net/netip`.
- Cross-references between domains are opaque strings (name or UID), never typed imports.
- `NICSpec.MAC` is `omitempty` — the apiserver allocator fills it at admission.
- The round-trip test at the package root proves all 7 objects survive marshal-unmarshal without loss.

## ANTI-PATTERNS

- Do not import `internal/` or `pkg/hostnet` from any `pkg/apis/*` package.
- Do not add parsed `netip.Addr` or `net.HardwareAddr` fields to API structs; wire format is string, validation at boundary.
- Do not use bare `string`/`int`/`bool` for discriminator values; use dedicated typed constants.

## NOTES

- `APIGroupVersion = "govirta.io/v1alpha1"` is the single version string shared by all objects.
- Root volumes require `ImageRef` + `ImageFilePoolRef`; data volumes must not carry them (enforced by `VolumeSpec.Validate()`).
- VM has the richest lifecycle: `defined` → `starting` → `running` → `stopping` → `stopped` + `failed`.
- `Kind` constants: `StoragePool`, `Image`, `Volume`, `Network`, `NIC`, `VM`, `Snapshot`.
- `Snapshot.spec.vmRef` is a VM name, not UID. The apiserver resolves the target VM's `metadata.nodeName` and stores it on the Snapshot for node watch routing.
