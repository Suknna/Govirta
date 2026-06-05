# internal/virt/qemu Knowledge Base

<!--
Verified-against:
  base_commit: 3edfafd
  files:
    - cmd/qemucli/main.go
    - internal/virt/qemu/vm.go
    - internal/virt/qemu/vm_test.go
    - internal/virt/qemu/machine/machine.go
    - internal/virt/qemu/blockdev/blockdev.go
    - internal/virt/qemu/chardev/chardev.go
    - internal/virt/qemu/cpu/cpu.go
    - internal/virt/qemu/device/device.go
    - internal/virt/qemu/display/display.go
    - internal/virt/qemu/firmware/firmware.go
    - internal/virt/qemu/monitor/monitor.go
    - internal/virt/qemu/netdev/netdev.go
    - internal/virt/qemu/qflag/qflag.go
    - internal/virt/qemu/serial/serial.go
  flows:
    - anchor: flow-argv-build
      sources:
        - cmd/qemucli/main.go
        - internal/virt/qemu/vm.go
        - internal/virt/qemu/machine/machine.go
        - internal/virt/qemu/blockdev/blockdev.go
        - internal/virt/qemu/device/device.go
        - internal/virt/qemu/netdev/netdev.go
        - internal/virt/qemu/chardev/chardev.go
        - internal/virt/qemu/monitor/monitor.go
        - internal/virt/qemu/serial/serial.go
-->

## OVERVIEW

Typed QEMU argv builder. Composes a `Builder` from machine profiles and typed device structs, then renders a deterministic `[]string` argv via `Build()` → `VM.Argv()`. No process execution; argv consumers (qemucli or future runner) handle exec.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Builder API + argv flatten | `vm.go` | `NewVM`, `Builder` fluent setters, `Build`, `VM.Argv` |
| Argv ordering contract | `vm_test.go:22` | `TestVMArgvBuildsRequiredQEMUCommand` golden test asserts full argv slice for x86_64 prod VM |
| Reject matrix | `vm_test.go:157` | Table-driven `TestBuildRejectsInvalidConfig` covers 27 cases |
| Explicit no-NIC | `vm.go:293`, `vm_test.go:94` | `Builder.NoNIC()` renders `-nic none`; `TestVMArgvRendersExplicitNoNIC` + reject test at `:116` |
| Machine profile whitelist | `machine/machine.go:8` | `ProfileX86_64Q35KVM`, `ProfileAArch64VirtKVM` |
| qcow2 backing | `blockdev/blockdev.go:22` | `Qcow2{NodeName, File, Cache, AIO}` + `Arg()` (`:46`) renders nested `file.driver=file` + `file.filename` |
| virtio-blk / virtio-net | `device/device.go:13` | `VirtioBlkPCI`, `VirtioNetPCI` with optional fields |
| Firmware BIOS | `firmware/firmware.go:8` | `BIOS{Path}` renders the value for typed `Builder.BIOS` |
| TAP netdev | `netdev/netdev.go:10` | `Tap{ID, IfName, Script, DownScript, Vhost}` |
| QMP/serial chardev | `chardev/chardev.go:7` | `Socket{ID, Path, Server, Wait}` |
| Optional flag plumbing | `qflag/qflag.go:4` | `OnOff` enum + `OptionalInt` distinguishing 0 vs unset |
| QEMU option rendering | `qopt/qopt.go` | Shared typed option renderer; rejects comma/control-character injection in option values |

## CONVENTIONS

- Machine config is profile-only. `Build()` rejects generic `-machine` and `-M` arguments.
- Generic escape hatch is `AddArgument(Arg(flag, value))` / `AddArgument(Flag(flag))` / exported `TypedArg(flag, renderer)`. It is allowlist-only and arity-checked: `-enable-kvm` accepts only `Flag("-enable-kvm")`; `-rtc` accepts only value forms (`Arg` or external `TypedArg`). Flags with dedicated typed builders (`-machine`, `-M`, `-name`, `-cpu`, `-smp`, `-m`, `-bios`, `-blockdev`, `-device`, `-netdev`, `-chardev`, `-mon`, `-serial`, `-display`, `-msg`, `-pidfile`, `-no-reboot`, `-no-shutdown`) are rejected to prevent bypassing typed validation. Package-internal builders use an unexported internal typed argument path and are not generic escape hatches.
- Typed entries validate and render during `Build()` through `qopt`; required fields, supported enum values, and QEMU option delimiters must be rejected before `VM.Argv()` is exposed.
- Optional `OnOff` fields default to empty string (unset → omitted); never compare to `"on"`/`"off"` directly, use `qflag.On`/`qflag.Off`.
- Optional integers (e.g. `VirtioBlkPCI.BootIndex`) use `qflag.OptionalInt` so 0 is distinguishable from unset.
- Subpackages depend only on `qflag`; never cross-import sibling subpackages outside `device` (which legitimately references `blockdev` + `netdev` ref types).
- Machine network defaults are not implicit: a VM either declares network devices (`AddNetdev`/virtio-net `AddDevice`) or calls `NoNIC()` to render an explicit `-nic none`. `Build()` rejects combining the two (`vm.go:335`).
- Adding a new device: implement `Arg() (string, error)` matching the `Device` interface (`vm.go:154`), validate with `qopt`, and pass it via `Builder.AddDevice`. No core switch changes required (`vm_test.go:475` covers this contract).

## ANTI-PATTERNS

- Do not add `machine.WithAccel`, `machine.WithKernelIRQChip`, `TypeQ35`, `TypeVirt`, or other free-composition APIs. Profiles only.
- Do not bypass `Build()` validation by appending raw strings to `Builder.ordered` from outside the package; it is unexported.
- Do not start QEMU processes from this package; argv is the deliverable. Process spawn lives above this typed boundary.
- Do not reference TAP names that are not pre-created on the host. This package consumes TAP names; `internal/hostnet/link` owns host TAP/bridge lifecycle and `internal/network` orchestrates it.
- Do not silently widen the machine profile whitelist; `Profile.IsSupported()` (`machine.go:20`) is the single source of truth.
- Do not let unit tests require a real `qemu-system-*` binary; tests assert argv strings only.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: argv build {#flow-argv-build}

承接根 flow `qemucli argv rendering` 在 `internal/virt/qemu` 内的展开。也是任何未来 runtime VM spawn 的输入路径。

- Entry from root flow: `cmd/qemucli/main.go:35 (buildDefaultArgv)` — 来自 `cmd/qemucli/main.go:24 (main)` 的根 flow `#flow-qemucli-argv`
- Local chain:
  1. `internal/virt/qemu/vm.go:185 (NewVM)` — `binaryForArch(arch)` 选 `qemu-system-x86_64` / `qemu-system-aarch64` → 返回 `*Builder{binary}`
  2. `internal/virt/qemu/vm.go:215 (Builder.Name)` / `:228 (Builder.Machine)` / `:234 (Builder.CPU)` / `:236 (Builder.SMP)` / `:238 (Builder.Memory)` — 写入命名字段
  3. `internal/virt/qemu/vm.go:240 (Builder.AddBlockdev)` / `:245 (Builder.AddDevice)` / `:258 (Builder.AddNetdev)` / `:264 (Builder.AddChardev)` / `:269 (Builder.BIOS)` / `:274 (Builder.Monitor)` / `:279 (Builder.Serial)` — 保留 package-internal typed renderer；`AddNetdev` 与 virtio-net `AddDevice` 置 `b.network=true`
  4. `internal/virt/qemu/vm.go:245 (Builder.AddDevice)` — 反射 nil 检查 `isNilDevice`，防止 typed-nil 接口在 `Argv()` 阶段 nil-deref
  5. `internal/virt/qemu/vm.go:293 (Builder.NoNIC)` — 置 `b.noNIC=true`，要求显式 `-nic none`，与 `b.network` 互斥
  6. `internal/virt/qemu/vm.go:299 (Builder.Build)` — 校验 binary/name/SMP/CPU/memory/display/msg/profile + NoNIC/network 互斥(`:335`) + typed renderer + external generic arguments allowlist；任一失败 wrap `ErrInvalidVM` 返回
  7. `internal/virt/qemu/vm.go:349 (Builder.Build)` — 拷贝 Builder 与 ordered slice 进入 `VM` 值，避免 Argv() 期间外部 mutate
  8. `internal/virt/qemu/vm.go:354 (VM.Argv)` — 固定段顺序 flatten：binary → -name → -machine → -cpu → -smp → -m → ordered 切片 → -nic none(若 NoNIC) → -display → -no-reboot → -no-shutdown → -msg → -pidfile
- Data (within module): `Arch` → `*Builder` (字段化配置) → `VM` (不可变快照) → `[]string` (argv)
- Side effects (within module): 无；纯值变换。错误经 `errors.Is(err, ErrInvalidVM)` 可命中
- Exit / next hop: `cmd/qemucli/main.go:29 (main)` — `strings.Join(argv, " ")` 写 stdout（当前唯一消费者；future runtime 会传给 `os/exec.CommandContext`）

引用契约要点：
- 头部段（`-name`/`-machine`/`-cpu`/`-smp`/`-m`/`-display`/`-no-reboot`/`-no-shutdown`/`-msg`/`-pidfile`）的渲染顺序由 `Argv()` 内 if 链固定，调用方 setter 顺序无影响。
- 中段段（`-blockdev`/`-device`/`-netdev`/`-chardev`/`-mon`/`-serial`/`AddArgument`）严格按调用顺序输出，由 `Builder.ordered` 切片保序。这是 `vm_test.go:22` 黄金测试中第二个 `-chardev (serial)` 出现在 `-mon` 之后、`-serial` 之前的来源。

`[已验证]` 数据流证据来源：直接读取 `vm.go` 与黄金测试断言（`reflect.DeepEqual` 比对 19 项命令行），无需 LSP 调用图反查。

## NOTES

- aarch64 有完整 argv 黄金测试，覆盖 `/usr/libexec/qemu-kvm`、`virt`、`cortex-a57` 与 Rocky 验收固件 `Builder.BIOS(firmware.BIOS{Path: "/usr/share/edk2/aarch64/QEMU_EFI.fd"})`。
- `cmd/qemucli/main_test.go` 与 `vm_test.go:21` 的 expected argv 必须同步更新；前者是 CLI 输出契约，后者是构建器契约。
- 黄金测试在 x86_64 全栈生产 VM 场景断言：`prod-vm` + Q35 KVM + host CPU + 4 vCPU + 8 GiB + qcow2 根盘 + virtio-blk + tap + virtio-net + QMP socket + 串口 socket + `-display none` + `-no-reboot/no-shutdown` + `-msg timestamp=on,guest-name=on` + `-pidfile`。
- 当前 `ArchAArch64` 默认 binary 选 `qemu-system-aarch64`；Rocky 8.10 验收主机的 `/usr/libexec/qemu-kvm` 走 `Builder.Binary()` 显式覆盖（`vm_test.go` 已示范）。
- 远程 acceptance 的固件必须通过 `Builder.BIOS(firmware.BIOS{Path: "/usr/share/edk2/aarch64/QEMU_EFI.fd"})` 渲染；generic `AddArgument(Arg("-bios", "..."))` 与 external `TypedArg("-bios", ...)` 会被 `Build()` 拒绝。
- `AddArgument` 是 allowlist-only 且校验参数形态。新增允许的 generic flag 时必须同时确认它没有已有 typed builder、不会绕过枚举/字段校验、不会用错误 arity 注入额外 argv，并补充 allowlist、denylist 与 arity 回归测试。
- 单元测试纯逻辑，不依赖 QEMU 二进制；集成验收路径见根 AGENTS.md 的远程跨编译指引。
