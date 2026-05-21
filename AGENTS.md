# Govirta Agent Guide

## Project Context

Govirta is a Go-based virtualization infrastructure platform. It targets ESXi / VMware-style infrastructure capabilities and is expected to become a lightweight virtual machine orchestration alternative to OpenStack for smaller or simpler environments.

Govirta starts from QEMU and builds upward. The compute node wraps QEMU, QMP, and Linux bridge capabilities. The control plane owns resource modeling, scheduling, node coordination, and state management.

The architecture is inspired by Kubernetes control plane / node separation, scheduling, and control-loop ideas, but the project does not target Kubernetes or CRD integration in the short term.

## Project Icon and Theme Colors

- Project icon: `image/govirta_icon.png`.
- The icon should be used as the canonical Govirta visual mark in repository documentation and project-facing materials.
- Brand colors are derived from the non-white areas of the icon; white is treated as background, not as a brand color:
  - Primary violet-blue: `#2000C0` for infrastructure, control-plane, and virtualization identity.
  - Secondary teal: `#00B0B0` for compute-node, runtime, and operational status accents.
- Keep future visual assets consistent with these colors unless the project explicitly adopts a new design direction.

## Current Development Phase

Govirta is in a fast-iteration phase.

- Do not preserve backward compatibility for its own sake.
- Do not keep technical debt only to maintain compatibility with earlier internal APIs, configs, or layouts.
- If an abstraction is wrong, replace or remove it directly.
- Keep changes focused on the current task, but prefer clean replacement over compatibility shims.

## Roadmap

Govirta evolves cycle by cycle. Cycle documents live under [`docs/roadmap/`](./docs/roadmap/README.md). Each cycle has a single north-star goal and a checklist; a cycle is only complete when every box is ticked.

- [ ] **Cycle 1 — Single-node single-VM loop.** *In progress.* `docs/roadmap/cycle-1-single-node-single-vm.md`.
- [ ] **Cycle 2 — Node daemon and multi-VM lifecycle.** `docs/roadmap/cycle-2-node-daemon.md`.
- [ ] **Cycle 3 — Control plane and scheduling.** `docs/roadmap/cycle-3-control-plane.md`.
- [ ] **Cycle 4 — Production minimum.** `docs/roadmap/cycle-4-production-minimum.md`.
- [ ] **Cycle 5 — Operations and resilience.** `docs/roadmap/cycle-5-operations.md`.

Rules for agents working in Govirta:

- The active cycle is the only one tagged *In progress*. Do not start work that belongs to a later cycle without first marking the current one complete.
- Items listed under a cycle's *范围外* section are explicitly deferred. Do not implement them in the current cycle.
- When a cycle finishes, update three places together: the cycle document's `状态` field, this section's checkbox, and the README roadmap checkbox.

## Permanent Architecture Constraints

- **No libvirt, ever.** Govirta's virtualization stack is permanently QEMU + QMP + qemu-img + netlink. Do not introduce `libvirt.org/go/libvirt`, `digitalocean/go-libvirt`, libvirtd, or any libvirt-derived abstraction at any cycle. Do not write design notes evaluating libvirt re-entry.

## Technology Choices

- Language: Go
- Virtualization layer: QEMU
- QEMU control protocol: QMP
- Host networking boundary: Linux bridge
- Logging: `github.com/rs/zerolog`
- License: Apache-2.0

## Architecture Boundaries

- `govirtad`: control plane process.
- `govirtlet`: compute node agent process.
- `govirtctl`: command-line client.
- `internal/apiserver`: API server boundary.
- `internal/controlplane`: control plane orchestration.
- `internal/scheduler`: VM placement boundary.
- `internal/store`: state storage abstraction.
- `internal/node`: node agent boundary.
- `internal/virt/qemu`: QEMU process abstraction.
- `internal/virt/qmp`: QMP client abstraction.
- `internal/network/bridge`: Linux bridge abstraction.
- `internal/types`: shared domain types.

## Go Context Rules

- The root `context.Context` must be created in `main`.
- Every child context must derive from the root context passed down from `main`.
- Do not create orphan contexts inside internal packages with `context.Background()` or `context.TODO()`.
- Internal packages must accept `ctx context.Context` from their caller for I/O, long-running work, cross-package operations, and goroutines.
- If a timeout or cancellation scope is needed, use `context.WithCancel`, `context.WithTimeout`, or `context.WithDeadline` on the caller-provided context.

## Goroutine Rules

- Every goroutine must have an owner and a shutdown path.
- Every long-running goroutine must select on `ctx.Done()`.
- Do not start fire-and-forget goroutines without error reporting or observability.
- Prefer small runner/worker abstractions over scattered anonymous `go func()` blocks.

## Panic and Recover Rules

- Do not use `panic` for expected business errors.
- Process and goroutine boundaries must recover from panic when a panic could otherwise be lost or crash without context.
- Recover paths must log structured details and convert the panic into an error or shutdown signal.
- Infinite loops are forbidden unless they include `ctx.Done()` or another explicit exit condition.
- Do not use `goto` as normal control flow.

## Error Handling Rules

- Return errors to the caller unless the current layer is explicitly responsible for final handling.
- Wrap errors with `%w` when adding context.
- Use `errors.Is` and `errors.As` for classification.
- Do not match errors by string.
- Do not swallow errors silently.

## Logging Rules

- Use zerolog structured logging.
- Initialize the base logger at the process entrypoint.
- Prefer passing logger context through `context.Context` using zerolog context integration.
- Library packages must not use `fmt.Println` for runtime logs.
- Logs must use stable field names.
- Do not log secrets, tokens, private keys, or sensitive host paths.

## Testing Rules

- Unit tests live next to the package under test.
- Prefer table-driven tests with `t.Run`.
- Test helpers must call `t.Helper()`.
- Use `go test ./...` as the baseline verification command.
- Use `go test -race ./...` for concurrency-sensitive changes.

## Change Reporting Rules

Every implementation handoff must include the key function call relationships affected by the change.

Example:

```text
cmd/govirtlet/main.go -> internal/node.Agent.Run -> internal/virt/qemu.Driver
```

Before changing core logic, inspect and report the affected call chain.

## Git Commit Rules

Use Conventional Commits:

```text
<type>(<scope>): <summary>
```

Examples:

```text
feat(node): add qemu runtime boundary
fix(controlplane): propagate run context cancellation
docs(project): document architecture direction
test(version): cover version string formatting
chore(project): initialize repository foundation
```
