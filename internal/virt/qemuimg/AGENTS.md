# internal/virt/qemuimg Knowledge Base

<!--
Verified-against:
  base_commit: 1f893ee
  files:
    - internal/virt/qemuimg/client.go
    - internal/virt/qemuimg/client_test.go
    - internal/virt/qemuimg/check/check.go
    - internal/virt/qemuimg/info/info.go
    - internal/virt/qemuimg/convert/convert.go
    - internal/virt/qemuimg/create/create.go
    - internal/virt/qemuimg/remove/remove.go
    - internal/virt/qemuimg/snapshot/snapshot.go
    - internal/virt/qemuimg/internal/exec/exec.go
  flows:
    - anchor: flow-qcow2-do
      sources:
        - internal/virt/qemuimg/client.go
        - internal/virt/qemuimg/check/check.go
        - internal/virt/qemuimg/info/info.go
        - internal/virt/qemuimg/convert/convert.go
        - internal/virt/qemuimg/create/create.go
        - internal/virt/qemuimg/snapshot/snapshot.go
        - internal/virt/qemuimg/remove/remove.go
        - internal/virt/qemuimg/internal/exec/exec.go
-->

## OVERVIEW

Offline qemu-img wrapper. `Client.QCOW2()` exposes per-subcommand fluent builders (`Create/Info/Convert/Snapshot/Check/Remove`); each `Do(ctx)` validates fields, renders argv, and dispatches via an injectable `Runner` that defaults to `os/exec.CommandContext`.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Client construction | `client.go:34` | `NewClient(Config)`; defaults `binary=qemu-img`, `runner=OSRunner{}` |
| QCOW2 builder dispatch | `client.go:46` | `ExecClient.QCOW2()` → `QCOW2Client` value |
| Subcommand builders | `check/`, `info/`, `convert/`, `create/`, `snapshot/`, `remove/` | One subpackage per subcommand; identical shape: `New(binary, runner)` + setters + `Do(ctx)` |
| Runner boundary | `internal/exec/exec.go:18` | `Runner` interface; `OSRunner.Run` at line 47 |
| Error model | `internal/exec/exec.go:11` | `ErrInvalidRequest` sentinel; `*CommandError` wraps result + cause |
| Public error aliases | `client.go:13` | `qemuimg.ErrInvalidRequest = imgexec.ErrInvalidRequest`; `qemuimg.CommandError = imgexec.CommandError` |
| Path operand validation | `internal/argv/argv.go` | Rejects blank and leading-dash path operands before invoking qemu-img |
| End-to-end argv assertions | `client_test.go:61` | `TestQCOW2*UsesConfiguredRunner` covers all 6 subcommands |

## CONVENTIONS

- Argv is `[]string`; never assemble shell command strings. `Runner.Run(ctx, binary, args)` is the only execution surface.
- File path operands must pass through `internal/argv.PathOperand` so qemu-img cannot parse caller-controlled paths beginning with `-` as options.
- Subcommand builders share an identical shape: `New(binary, runner)` → fluent setters → `Do(ctx)` returning `error` or `(Result, error)`. Match this shape when adding new subcommands.
- Validation failures return `imgexec.InvalidRequest("...")` so callers can `errors.Is(err, ErrInvalidRequest)`.
- Process failures route through `imgexec.WrapError(result, err)` → `*CommandError`, preserving stdout/stderr; callers outside the internal package use public alias `qemuimg.CommandError`. JSON parse failures (info/check) bypass `WrapError` and return the raw `json` error directly — by design.
- Test fakes implement `imgexec.Runner` and capture last `(binary, args)` for assertion. The shared style is `recordingRunner` (`client_test.go:167`); reuse it across new subcommand tests rather than introducing parallel naming.
- Unit tests must not require a real `qemu-img` binary. The OS runner is tested via the helper-process pattern in `internal/exec/exec_test.go:74`.

## ANTI-PATTERNS

- Do not add a subcommand that decides whether the target image is attached to a running VM. That safety check belongs to the caller / future storage layer.
- Do not call `os/exec` directly from subcommand builders. Always go through `runner.Run`.
- Do not echo the QCOW2 binary name in subcommand builders; it comes from `Client.binary` and is propagated via `New`.
- Do not surface stderr by string-matching its content. Use `errors.Is(err, ErrInvalidRequest)` for input errors and `errors.As(err, &*CommandError)` for process errors.
- Do not invent a parallel runner-fake naming convention; reuse `recordingRunner` from `client_test.go`.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: qcow2 subcommand Do {#flow-qcow2-do}

This is the canonical execution path for every subcommand exposed via `QCOW2Client`. There is no current production caller in `cmd/*`; future node-agent storage code will be the entry point. Captured here so the runtime path is documented before it grows.

- Entry from root flow: `internal/virt/qemuimg/client.go:34 (NewClient)` — invoked by future caller from root flow `#flow-qcow2-cli` (placeholder, no current production caller; verified against tests in `client_test.go:61`)
- Local chain:
  1. `client.go:34 (NewClient)` — fill defaults: `binary` falls back to `"qemu-img"`, `runner` falls back to `imgexec.OSRunner{}`
  2. `client.go:46 (ExecClient.QCOW2)` — return value-typed `QCOW2Client{binary, runner}`; each call returns a fresh copy but shares the runner interface
  3. `client.go:54-74 (QCOW2Client.{Create,Info,Convert,Snapshot,Check,Remove})` — delegate to `<sub>.New(binary, runner)`; returns `*Builder`
  4. `<sub>.Builder.<Setter>(...)` — fluent setters mutate builder fields (path, target, base, size, name)
  5. `<sub>.Builder.Do(ctx)` — validate non-empty / positive fields with `imgexec.InvalidRequest` (returns `%w ErrInvalidRequest`); path operands additionally reject leading `-`; on pass, assemble argv slice
  6. `<sub>.Builder.Do → b.runner.Run(ctx, b.binary, args)` — runner invocation:
     - `internal/exec/exec.go:47 (OSRunner.Run)` — `os_exec.CommandContext(ctx, binary, args...).Run()`; captures stdout/stderr into `Result` regardless of success
     - Process / failure path: caller wraps via `imgexec.WrapError(result, err)` → `*CommandError` with `Unwrap()` exposing original error; external callers classify it through `qemuimg.CommandError` (success case returns `nil` because `WrapError(_, nil) == nil`)
  7. For `info`/`check`: `json.Unmarshal(result.Stdout, &Result)` after success; parse error returns raw `json` error (NOT re-wrapped) — `info/info.go:49`, `check/check.go:50`
- Data (within module): `Config` → `Builder` fields (typed: `path`, `target`, `base`, `size int64`, `name`) → `[]string` argv (subcommand-specific) → `imgexec.Result{Stdout, Stderr}` → typed `Result` (info/check) or `error`
- Side effects (within module): spawns `qemu-img` subprocess via `OSRunner` (default); `remove.Builder.Do` is the **exception** — it calls `os.Remove` directly without invoking the runner, enforcing `.qcow2` extension, checking `ctx.Err()` before file inspection and before deletion, rejecting directories/symlinks/non-regular files, and using `os.Lstat` for file-type checks.
- Exit / next hop:
  - Filesystem: qcow2 file at `b.target` (create/convert), snapshot inside qcow2 (snapshot), file deletion (remove)
  - Process: external `qemu-img` exit code, captured stderr in `*CommandError.Result.Stderr`
  - JSON: structured info/check `Result` returned to caller

Argv catalog (verified by `client_test.go`):

| Subcommand | argv | Output |
| --- | --- | --- |
| create | `["create", "-f", "qcow2", "-F", "qcow2", "-b", base, target, strconv.FormatInt(size,10)]` (`create/create.go:52`) | `error` |
| info | `["info", "--output=json", path]` (`info/info.go:43`) | `(Result, error)` |
| convert | `["convert", "-O", "qcow2", source, target]` (`convert/convert.go:42`) | `error` |
| snapshot | `["snapshot", "-c", name, path]` (`snapshot/snapshot.go:42`) | `error` |
| check | `["check", "--output=json", path]` (`check/check.go:44`) | `(Result, error)` with `RawOutput` |
| remove | (no argv; calls `os.Remove(path)` after `.qcow2` and directory checks) | `error` |

`[已验证]` 数据流证据来源：直接读取 6 个 builder 的 `Do(ctx)` 实现 + `client_test.go` 端到端 argv 断言 + `internal/exec/exec_test.go` 的 helper-process 子进程测试。

## NOTES

- `remove.Builder` keeps `binary` + `runner` fields purely for constructor parity with siblings; they are unused. If qemu-img-native delete arrives, choose between two paths explicitly rather than silently switching.
- `info.Result` discards stdout (no `RawOutput` field); `check.Result` exposes it via `RawOutput string \`json:"-"\`` (`check/check.go:18`). Mirror this asymmetry consciously when adding similar JSON-emitting subcommands.
- Test runner `recordingRunner` is package-private to `qemuimg` (`client_test.go:167`); do not promote it to a shared test helper unless you are willing to update all six call sites.
- The remote acceptance host has `qemu-img` at `/usr/bin/qemu-img` (Rocky 8.10 aarch64). Pass it via `Config.Binary` for integration tests; default `"qemu-img"` works on macOS dev hosts when qemu is installed via Homebrew.
