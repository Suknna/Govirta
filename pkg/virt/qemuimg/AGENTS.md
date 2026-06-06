# pkg/virt/qemuimg Knowledge Base

<!--
Verified-against:
  base_commit: 3804ad0
  files:
    - pkg/virt/qemuimg/client.go
    - pkg/virt/qemuimg/client_test.go
    - pkg/virt/qemuimg/check/check.go
    - pkg/virt/qemuimg/info/info.go
    - pkg/virt/qemuimg/convert/convert.go
    - pkg/virt/qemuimg/create/create.go
    - pkg/virt/qemuimg/remove/remove.go
    - pkg/virt/qemuimg/resize/resize.go
    - pkg/virt/qemuimg/snapshot/snapshot.go
    - pkg/virt/qemuimg/internal/exec/exec.go
  flows:
    - anchor: flow-qcow2-do
      sources:
        - pkg/virt/qemuimg/client.go
        - pkg/virt/qemuimg/check/check.go
        - pkg/virt/qemuimg/info/info.go
        - pkg/virt/qemuimg/convert/convert.go
        - pkg/virt/qemuimg/create/create.go
        - pkg/virt/qemuimg/snapshot/snapshot.go
        - pkg/virt/qemuimg/remove/remove.go
        - pkg/virt/qemuimg/resize/resize.go
        - pkg/virt/qemuimg/internal/exec/exec.go
-->

## OVERVIEW

Offline qemu-img wrapper. `Client.QCOW2()` exposes per-subcommand fluent builders (`Create/Info/Convert/Resize/Snapshot/Check/Remove`). Most `Do(ctx)` methods validate fields, render argv, and dispatch via an injectable `Runner` that defaults to `os/exec.CommandContext`; `Remove().Do(ctx)` is the exception and performs local filesystem deletion for trusted Govirta-owned image paths.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Client construction | `client.go:81` | `NewClient(Config)`; defaults `binary=qemu-img`, `runner=OSRunner{}` |
| QCOW2 builder dispatch | `client.go:95` | `ExecClient.QCOW2()` → `QCOW2Client` value |
| Subcommand builders | `check/`, `info/`, `convert/`, `create/`, `resize/`, `snapshot/`, `remove/` | One subpackage per subcommand; identical shape: `New(binary, runner)` + setters + `Do(ctx)` |
| Runner boundary | `internal/exec/exec.go:18` | `Runner` interface; `OSRunner.Run` at line 47 |
| Error model | `internal/exec/exec.go:11` | `ErrInvalidRequest` sentinel; `*CommandError` wraps process result + cause; `*DecodeError` wraps JSON decode result + cause |
| Public error aliases | `client.go:13` | `qemuimg.ErrInvalidRequest = imgexec.ErrInvalidRequest`; `qemuimg.CommandError = imgexec.CommandError`; `qemuimg.DecodeError = imgexec.DecodeError` |
| Path operand validation | `internal/argv/argv.go` | Rejects blank and leading-dash path operands before invoking qemu-img |
| End-to-end argv assertions | `client_test.go` | `TestQCOW2*UsesConfiguredRunner` covers all runner-backed subcommands; remove has filesystem behavior tests |

## CONVENTIONS

- Argv is `[]string`; never assemble shell command strings. `Runner.Run(ctx, binary, args)` is the only execution surface.
- File path operands must pass through `internal/argv.PathOperand` so qemu-img cannot parse caller-controlled paths beginning with `-` as options.
- Subcommand builders share an identical shape: `New(binary, runner)` → fluent setters → `Do(ctx)` returning `error` or `(Result, error)`. Match this shape when adding new subcommands.
- Validation failures return `imgexec.InvalidRequest("...")` so callers can `errors.Is(err, ErrInvalidRequest)`.
- QCOW2 input commands pass `-f qcow2` when qemu-img reads an existing image (`info`, `check`, `convert`) so image format probing is not implicit.
- Process failures route through `imgexec.WrapError(result, err)` → `*CommandError`, preserving stdout/stderr; callers outside the internal package use public alias `qemuimg.CommandError`.
- JSON decode failures from `info`/`check` route through `imgexec.WrapDecodeError(result, err)` → `*DecodeError`, preserving stdout/stderr without classifying parser failures as process-level `CommandError`.
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

This is the canonical execution path for every subcommand exposed via `QCOW2Client`. There is no current direct `cmd/*` caller; `internal/storage/local` calls it today for qcow2 volume create/convert/resize/info/remove, and future node-agent runtime code will also use it.

- Entry from root flow: `pkg/virt/qemuimg/client.go:81 (NewClient)` / `:105 (QCOW2Client.Create)` / `:115 (Convert)` / `:120 (Resize)` — invoked by storage local driver and future node runtime callers
- Local chain:
  1. `client.go:81 (NewClient)` — fill defaults: `binary` falls back to `"qemu-img"`, `runner` falls back to `imgexec.OSRunner{}`
  2. `client.go:95 (ExecClient.QCOW2)` — return value-typed `QCOW2Client{binary, runner}`; each call returns a fresh copy but shares the runner interface
  3. `client.go:104-136 (QCOW2Client.{Create,Info,Convert,Resize,Snapshot,Check,Remove})` — delegate to `<sub>.New(binary, runner)`; returns `*Builder`
  4. `<sub>.Builder.<Setter>(...)` — fluent setters mutate builder fields (path, target, base, size, name)
  5. `<sub>.Builder.Do(ctx)` — validate non-empty / positive fields with `imgexec.InvalidRequest` (returns `%w ErrInvalidRequest`); path operands additionally reject leading `-`; on pass, assemble argv slice
  6. `<sub>.Builder.Do → b.runner.Run(ctx, b.binary, args)` — runner invocation:
     - `internal/exec/exec.go:47 (OSRunner.Run)` — `os_exec.CommandContext(ctx, binary, args...).Run()`; captures stdout/stderr into `Result` regardless of success
     - Process / failure path: caller wraps via `imgexec.WrapError(result, err)` → `*CommandError` with `Unwrap()` exposing original error; external callers classify it through `qemuimg.CommandError` (success case returns `nil` because `WrapError(_, nil) == nil`)
  7. For `info`/`check`: `json.Unmarshal(result.Stdout, &Result)` after success; parse error wraps via `imgexec.WrapDecodeError(result, err)` → `*DecodeError` — `info/info.go:49`, `check/check.go:50`
- Data (within module): `Config` → `Builder` fields (typed: `path`, `target`, `base`, `size int64`, `name`) → `[]string` argv (subcommand-specific) → `imgexec.Result{Stdout, Stderr}` → typed `Result` (info/check) or `error`
- Side effects (within module): spawns `qemu-img` subprocess via `OSRunner` (default); `remove.Builder.Do` is the **exception** — it calls `os.Remove` directly without invoking the runner for trusted Govirta-owned image paths, enforcing a case-insensitive `.qcow2` extension, checking `ctx.Err()` before file inspection and before deletion, rejecting directories/symlinks/non-regular files, and using `os.Lstat` for file-type guardrails. These guardrails do not make an untrusted parent directory safe against pathname replacement between `Lstat` and `Remove`.
- Exit / next hop:
  - Filesystem: qcow2 file at `b.target` (create/convert), snapshot inside qcow2 (snapshot), file deletion (remove)
  - Process: external `qemu-img` exit code, captured stderr in `*CommandError.Result.Stderr`
  - JSON: structured info/check `Result` returned to caller

Argv catalog (verified by `client_test.go`):

| Subcommand | argv | Output |
| --- | --- | --- |
| create | `["create", "-f", "qcow2", "-F", "qcow2", "-b", base, target, strconv.FormatInt(size,10)]` (`create/create.go:52`) | `error` |
| info | `["info", "-f", "qcow2", "--output=json", path]` (`info/info.go:43`) | `(Result, error)` |
| convert | `["convert", "-f", "qcow2", "-O", "qcow2", source, target]` (`convert/convert.go:42`) | `error` |
| resize | `["resize", "-f", "qcow2", path, strconv.FormatInt(size,10)]` (`resize/resize.go:49`) | `error` |
| snapshot | `["snapshot", "-c", name, path]` (`snapshot/snapshot.go:42`) | `error` |
| check | `["check", "-f", "qcow2", "--output=json", path]` (`check/check.go:44`) | `(Result, error)` with `RawOutput` |
| remove | (no argv; calls `os.Remove(path)` after case-insensitive `.qcow2` suffix and file-type guardrails; caller must supply a trusted storage path) | `error` |

`[已验证]` 数据流证据来源：直接读取 7 个 builder 的 `Do(ctx)` 实现 + `client_test.go` 端到端 argv/删除断言 + `internal/exec/exec_test.go` 的 helper-process 子进程测试。

## NOTES

- `remove.Builder` keeps `binary` + `runner` fields purely for constructor parity with siblings; they are unused. Remove is local deletion for Govirta trusted storage paths, not a qemu-img command. Its suffix check accepts `.qcow2` case-insensitively, and its `Lstat` checks prevent accidental directory/symlink/non-regular deletion but do not prove safety when untrusted users can write the parent directory. If qemu-img-native delete arrives, choose between two paths explicitly rather than silently switching.
- `info.Result` discards stdout (no `RawOutput` field); `check.Result` exposes it via `RawOutput string \`json:"-"\`` (`check/check.go:18`). Mirror this asymmetry consciously when adding similar JSON-emitting subcommands.
- Test runner `recordingRunner` is package-private to `qemuimg` (`client_test.go:167`); do not promote it to a shared test helper unless you are willing to update all six call sites.
- The remote acceptance host has `qemu-img` at `/usr/bin/qemu-img` (Rocky 8.10 aarch64). Pass it via `Config.Binary` for integration tests; default `"qemu-img"` works on macOS dev hosts when qemu is installed via Homebrew.
