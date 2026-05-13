# Govirta Agent Rules

## Project Context

Govirta is a Go-based virtualization infrastructure project that builds from QEMU upward. It targets ESXi and VMware-style operations and is intended to become a lightweight alternative to OpenStack over time.

The architecture is inspired by Kubernetes control-plane patterns without depending on Kubernetes in the short term. The planned process model is:

```text
govirtctl -> govirtad control plane -> scheduler/store/control loops -> govirtlet -> QEMU/QMP/Linux bridge
```

## Fast Iteration Phase

- Keep changes small, direct, and easy to review.
- Prefer clear contracts over premature abstraction.
- Do not implement future tasks unless explicitly requested.
- Avoid large refactors while the project skeleton is still forming.

## Technology Choices

- Primary language: Go.
- Virtualization layer: QEMU.
- QEMU control protocol: QMP.
- Networking foundation: Linux bridge.
- Logging: `github.com/rs/zerolog` structured logging.

## Architecture Boundaries

- Keep CLI behavior in `govirtctl` boundaries.
- Keep control-plane orchestration in `govirtad` boundaries.
- Keep compute-node execution in `govirtlet` boundaries.
- Keep scheduler, store, and control-loop responsibilities explicit.
- Do not make internal packages depend on CLI concerns.
- Do not hide QEMU/QMP or Linux bridge side effects behind unrelated packages.

## Go Context Rules

- The root `ctx` is created in `main`.
- Do not create orphan `context.Background()` or `context.TODO()` values inside internal packages.
- Pass `ctx` through call chains that perform I/O, blocking work, subprocess management, QMP communication, store access, or control-loop work.
- Use derived contexts only when ownership, cancellation, and timeout scope are clear.

## Goroutine Rules

- Every goroutine must have a shutdown path.
- Goroutines must observe `ctx.Done()` when they perform loops, blocking work, waits, or long-running operations.
- Infinite loops require either `ctx.Done()` handling or another explicit exit condition.
- Do not leak goroutines after command shutdown, daemon shutdown, or test completion.
- Do not use `goto` as normal control flow.

## Panic and Recover Rules

- Do not use `panic` for business errors, expected runtime failures, validation failures, QMP failures, process failures, or user input errors.
- Boundaries that launch long-running goroutines or serve external requests should recover, log, and convert panics into controlled failures where appropriate.
- Recovered panics must be logged with structured context.
- Do not silently recover and continue without reporting the failure.

## Error Handling Rules

- Return errors instead of logging and swallowing them.
- Wrap errors with `%w` when adding context.
- Use `errors.Is` and `errors.As` for sentinel or typed error inspection.
- Preserve enough context for operators and tests to identify the failing component and operation.
- Do not compare wrapped errors by string.

## Logging Rules

- Use `zerolog` structured logs.
- Prefer fields over formatted message text for machine-readable values.
- Include component, operation, and relevant identifiers when available.
- Do not log secrets, tokens, credentials, or private key material.
- Avoid duplicate logging at every layer; log where the error is handled or crosses a boundary.

## Testing Rules

- Add or update tests for behavior changes.
- Prefer deterministic tests over sleeps and timing assumptions.
- Tests that start goroutines must verify shutdown behavior when feasible.
- Tests for context-aware code should cover cancellation when practical.
- Do not weaken or delete tests to make a change pass.

## Change Reporting Rules

- Every handoff must include files changed, verification commands, and results.
- Every handoff includes call relationships affected.
- If no code call relationships changed, state that explicitly.
- Report assumptions, risks, and skipped verification clearly.

## Git Commit Rules

- Use Conventional Commits.
- Keep commits focused on the requested task.
- Do not include `.pomelo_mem` or local session state.
- Do not rewrite history unless explicitly requested.

Examples:

```text
docs(project): describe govirta architecture and agent rules
chore(project): initialize repository foundation
feat(govirtctl): add root command
fix(govirtlet): stop qmp watcher on context cancellation
test(control): cover scheduler retry backoff
```
