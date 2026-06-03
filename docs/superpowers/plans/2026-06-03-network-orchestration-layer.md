# 网络编排层实现计划(仿存储结构 + guest 出外网闭环)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有 `internal/hostnet/*` 主机网络原语之上,新增仿 `internal/storage` 结构的 VM-facing 网络编排层(`internal/network/`),并新增一个 firewall FORWARD-accept 原语,打通 guest 出外网完整链路,经 Lima 真实验收。

**Architecture:** 三段式 `NetworkService`/`NICService`(VM-facing)→ `netpool.Service`(逻辑网络定义注册核心,不缓存可漂移真实状态)→ `link`/`route`/`firewall`/`dhcp` 四原语(driver 层)。`netpool` 只存声明式逻辑意图,真实状态永远通过原语 `Get`/`List` 现读现聚合。新增 firewall FORWARD-accept 是一个双向规则组原语(出向 accept + 回向 conntrack established/related accept),走 anti-spoofing 同款的规则组路径。

**Tech Stack:** Go 1.26,`github.com/google/nftables`(+`expr`),`github.com/rs/zerolog`,既有 hostnet 原语,Lima + QEMU + CirrOS aarch64 验收。

**Spec:** `docs/superpowers/specs/2026-06-03-network-orchestration-layer-design.md`

**Module path:** `github.com/suknna/govirta`(import 前缀 `github.com/suknna/govirta/internal/...`)

---

## 文件结构(实现前锁定的拆分决策)

按 AGENTS.md 第十八章在计划阶段预判文件体量并设计拆分边界:

**Phase A — firewall FORWARD-accept 原语(`internal/hostnet/firewall/`)**
- Modify `constants.go`:新增 `RulePurposeForwardAccept`、`PriorityNameForwardFilter` 常量。
- Modify `firewall.go`:接口加 `EnsureForwardAccept`/`DeleteForwardAccept`;新增 `ForwardAcceptSpec`、`ForwardAcceptSummary`、`RuleSummary.ForwardAccept` 字段。
- Modify `noop.go`:新增两个 no-op 方法。
- Modify `linux/manager_linux.go`:新增 `EnsureForwardAccept`/`DeleteForwardAccept` 方法(薄封装,委托给 forward 组路径)。
- Modify `linux/rules_linux.go`:仅在既有 `switch desired.purpose` 中追加 forward-accept 分支(`chainForDesired`、`validateExistingChain`、`ruleExprs`、`ruleUserDataForDesired`、`ruleSummaryMatchesDesired`)。
- Modify `linux/info_linux.go`:`compactObservedRules` 增加 forward-accept 分组分支。
- Modify `linux/expr_linux.go`:`observedRuleDetailFor`、`validateObservedRuleUserData` 两个 switch 追加 forward-accept 分支。
- Modify `linux/validate_linux.go`:新增 `validateForwardAcceptSpec`;`validateRuleQuery`/`validateFamilyForPurpose` 追加 forward-accept 分支。
- **Create** `linux/forward_linux.go`:forward-accept 专属规则组路径(`desiredForwardAcceptRules`、`ensureDesiredForwardGroup`、`deleteObservedForwardGroup`、`matchingForwardDetails`、`forwardGuardMatchesDesired`、`validateDesiredForwardGroup`、`logicalForwardInfo`、`forwardGroupKey`)。**拆分理由**:`rules_linux.go` 已 506 行,组路径约 +110 行会越过软上限;forward 组路径是独立内聚职责,单独成文件。
- **Create** `linux/forward_expr_linux.go`:forward-accept nft 表达式构造与解析(`forwardEgressAcceptExprs`、`forwardReturnAcceptExprs`、`parseForwardAccept`)。**拆分理由**:`expr_linux.go` 已 451 行,+约 100 行越过软上限;forward expr 构造是独立内聚职责。
- Test:`linux/forward_test.go`(新建,fake handle 驱动)、`noop_test.go`(追加)。

**Phase B — 网络编排层(`internal/network/`)**
- **Create** `networker/errors.go`:稳定错误哨兵。
- **Create** `netpool/network.go`:`NetworkDefinition`、`NICDefinition`、`networkRecord`、克隆函数、`NetworkName`/`VMID` 类型。
- **Create** `netpool/service.go`:`netpool.Service` 注册核心(RegisterNetwork/RegisterNIC/Get/List + 两级锁 + 克隆),依赖注入四原语 Manager。
- **Create** `netpool/orchestrate.go`:`EnsureNetwork`/`EnsureNIC`/`DeleteNetwork`/`DeleteNIC`/状态聚合(编排四原语)。**拆分理由**:注册与编排是两类职责,分文件避免 service.go 越过软上限。
- **Create** `service.go`:`NetworkService`(VM-facing,网段)。
- **Create** `nic_service.go`:`NICService`(VM-facing,网卡)。
- Test:`netpool/service_test.go`、`netpool/orchestrate_test.go`(fake 原语 Manager)、`service_test.go`、`nic_service_test.go`。

**Phase C — 死包移除 + node 重接**
- **Delete** `internal/network/bridge/`(整个包)。
- Modify `internal/node/agent.go`:移除 bridge,改组合 `*network.NetworkService`/`*network.NICService`。

**Phase D — Lima 验收**
- **Create** `test/acceptance/network_egress_test.go`。
- Modify `test/acceptance/doc.go`:登记新测试 scope。

**Phase E — 知识库**
- Modify `AGENTS.md`、`internal/storage` 无关;新增 `internal/network/AGENTS.md`、`internal/hostnet/firewall` 知识更新。

---

## Phase A — firewall FORWARD-accept 原语

Phase A 在 `internal/hostnet/firewall` 新增一个双向规则组原语,严格复用 anti-spoofing 的规则组路径与 user-data/guard 机制。完成后 `firewall.Manager` 多出 `EnsureForwardAccept`/`DeleteForwardAccept`,Linux 实现可在 filter forward 链 ensure「出向 accept + 回向 conntrack established/related accept」两条 Govirta-owned 规则并现读回观测状态。

### Task A1: firewall 契约 + 常量 + noop(根包先编译)

**Files:**
- Modify: `internal/hostnet/firewall/constants.go`
- Modify: `internal/hostnet/firewall/firewall.go`
- Modify: `internal/hostnet/firewall/noop.go`
- Test: `internal/hostnet/firewall/noop_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `firewall` 根包新增 forward-accept 契约类型与接口方法,`NoopManager` 实现新接口,根包编译通过。
Acceptance evidence:
- `go build ./internal/hostnet/firewall/` 通过
- `go test ./internal/hostnet/firewall/ -run TestNoop -v` 通过(noop 新方法在 nil/canceled context 与正常 context 下的行为)

- [ ] **Step 2: 在 `constants.go` 追加常量**

在 `RulePurpose` 常量组(现有 `RulePurposeMasquerade`/`RulePurposeEndpointAntiSpoofing` 所在 `const (...)` 块,constants.go:49-74)末尾追加:

```go
	RulePurposeForwardAccept RulePurpose = "forward-accept"
```

在 `PriorityName` 常量组(现有 `PriorityNameSrcNAT`/`PriorityNameBridgeFilter`)追加:

```go
	PriorityNameForwardFilter PriorityName = "forward-filter"
```

- [ ] **Step 3: 在 `firewall.go` 追加接口方法与类型**

在 `Manager` 接口中 `DeleteEndpointAntiSpoofing` 之后、`GetRule` 之前插入:

```go
	// EnsureForwardAccept creates or reconciles the explicit filter-forward
	// accept rule group that allows guest CIDR egress traffic and its conntrack
	// established/related return traffic across the egress interface.
	//
	// Implementations return observed firewall state after success rather than a
	// blind echo of spec.
	EnsureForwardAccept(ctx context.Context, spec ForwardAcceptSpec) (RuleInfo, error)

	// DeleteForwardAccept removes the forward-accept rule group selected by ref.
	DeleteForwardAccept(ctx context.Context, ref RuleRef) error
```

在 `MasqueradeSpec` 定义之后追加:

```go
// ForwardAcceptSpec describes the complete desired filter-forward accept state.
//
// TableName, ChainName, RuleOwner, GuestCIDR, EgressInterfaceName, and Priority
// are all behavior-affecting fields and must be explicitly supplied by callers.
// The implementation creates a two-rule group (egress accept plus a conntrack
// established/related return accept) under one logical RuleInfo; callers must
// not pass a bridge name because forward-accept matches by guest CIDR and egress
// interface only, symmetric with MasqueradeSpec.
type ForwardAcceptSpec struct {
	TableName           TableName
	ChainName           ChainName
	RuleOwner           RuleOwner
	GuestCIDR           netip.Prefix
	EgressInterfaceName InterfaceName
	Priority            Priority
}
```

在 `MasqueradeSummary` 定义之后追加:

```go
// ForwardAcceptSummary reports observed forward-accept rule group details.
//
// The two underlying rules (egress accept and conntrack return accept) are
// compacted into one logical summary.
type ForwardAcceptSummary struct {
	GuestCIDR           netip.Prefix
	EgressInterfaceName InterfaceName
	Priority            Priority
}
```

修改 `RuleSummary` 结构体,新增 `ForwardAccept` 指针(三选一变四选一):

```go
// RuleSummary contains purpose-specific observed firewall rule details.
//
// Exactly one summary pointer must be populated. The populated summary must
// match RuleInfo.Ref.Purpose.
type RuleSummary struct {
	Masquerade           *MasqueradeSummary
	EndpointAntiSpoofing *EndpointAntiSpoofingSummary
	ForwardAccept        *ForwardAcceptSummary
}
```

- [ ] **Step 4: 在 `noop.go` 追加两个 no-op 方法**

在 `DeleteEndpointAntiSpoofing` 方法之后插入:

```go
// EnsureForwardAccept validates nil or canceled context and otherwise returns ErrUnsupported.
func (NoopManager) EnsureForwardAccept(ctx context.Context, _ ForwardAcceptSpec) (RuleInfo, error) {
	if err := noopFirewallOperationError(ctx); err != nil {
		return RuleInfo{}, err
	}

	return RuleInfo{}, firewallerr.ErrUnsupported
}

// DeleteForwardAccept validates nil or canceled context and otherwise returns ErrUnsupported.
func (NoopManager) DeleteForwardAccept(ctx context.Context, _ RuleRef) error {
	if err := noopFirewallOperationError(ctx); err != nil {
		return err
	}

	return firewallerr.ErrUnsupported
}
```

- [ ] **Step 5: 在 `noop_test.go` 追加测试**

沿用文件中既有 noop 测试风格(canceled context 返回 `context.Canceled`,nil context 返回 `firewallerr.ErrInvalidRequest`,正常 context 返回 `firewallerr.ErrUnsupported`),追加:

```go
func TestNoopManagerEnsureForwardAccept(t *testing.T) {
	mgr := NewNoopManager()

	if _, err := mgr.EnsureForwardAccept(context.Background(), ForwardAcceptSpec{}); !errors.Is(err, firewallerr.ErrUnsupported) {
		t.Fatalf("EnsureForwardAccept on background context = %v, want ErrUnsupported", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mgr.EnsureForwardAccept(canceled, ForwardAcceptSpec{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureForwardAccept on canceled context = %v, want context.Canceled", err)
	}

	//nolint:staticcheck // explicitly testing nil-context rejection
	if _, err := mgr.EnsureForwardAccept(nil, ForwardAcceptSpec{}); !errors.Is(err, firewallerr.ErrInvalidRequest) {
		t.Fatalf("EnsureForwardAccept on nil context = %v, want ErrInvalidRequest", err)
	}
}

func TestNoopManagerDeleteForwardAccept(t *testing.T) {
	mgr := NewNoopManager()

	if err := mgr.DeleteForwardAccept(context.Background(), RuleRef{}); !errors.Is(err, firewallerr.ErrUnsupported) {
		t.Fatalf("DeleteForwardAccept on background context = %v, want ErrUnsupported", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.DeleteForwardAccept(canceled, RuleRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteForwardAccept on canceled context = %v, want context.Canceled", err)
	}

	//nolint:staticcheck // explicitly testing nil-context rejection
	if err := mgr.DeleteForwardAccept(nil, RuleRef{}); !errors.Is(err, firewallerr.ErrInvalidRequest) {
		t.Fatalf("DeleteForwardAccept on nil context = %v, want ErrInvalidRequest", err)
	}
}
```

确认 `noop_test.go` 顶部已 import `context`、`errors`、`testing`、`firewallerr`;若缺则补。

- [ ] **Step 6: 运行验证**

Run: `go build ./internal/hostnet/firewall/ && go test ./internal/hostnet/firewall/ -run TestNoop -v`
Expected: PASS（linux 子包此时尚未实现新接口方法,但根包与 noop 独立编译;linux 包构建在 Task A2 完成。)

- [ ] **Step 7: 若验证失败,修实现或过期测试**

接口方法签名与 noop 实现签名不一致 → 对齐签名。`RuleSummary` 新字段破坏既有穷举 → 既有代码用具名字段构造,不受影响,无需改。

- [ ] **Step 8: Commit**

```bash
git add internal/hostnet/firewall/constants.go internal/hostnet/firewall/firewall.go internal/hostnet/firewall/noop.go internal/hostnet/firewall/noop_test.go
git commit -m "feat(hostnet/firewall): add forward-accept contract, constants, noop"
```

### Task A2: forward-accept nft 表达式构造与解析

**Files:**
- Create: `internal/hostnet/firewall/linux/forward_expr_linux.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 提供 forward-accept 出向/回向规则的 nft expr 构造函数与观测解析函数,与 masquerade expr 风格一致,回向用 `expr.Ct` 匹配 conntrack established/related。
Acceptance evidence:
- 本任务只新增构造/解析函数,编译验证在 Task A4 整组路径接好后统一进行(`go build ./internal/hostnet/firewall/linux/`)。
- 函数被 Task A4 的 `ruleExprs` 分支与 `observedRuleDetailFor` 分支调用。

- [ ] **Step 2: 创建 `forward_expr_linux.go`**

```go
//go:build linux

package linux

import (
	"bytes"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/suknna/govirta/internal/hostnet/firewall"
)

// forwardEgressAcceptExprs builds "ip saddr {GuestCIDR} oifname {egress} accept".
//
// It mirrors masqueradeExprs source-CIDR and output-interface matching, then
// accepts instead of masquerading.
func forwardEgressAcceptExprs(summary firewall.ForwardAcceptSummary) []expr.Any {
	masked := summary.GuestCIDR.Masked()
	addr := masked.Addr().As4()
	mask := prefixMask(summary.GuestCIDR.Bits(), 4)
	return []expr.Any{
		&expr.Payload{OperationType: expr.PayloadLoad, DestRegister: regMatch, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Bitwise{SourceRegister: regMatch, DestRegister: regMask, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMask, Data: addr[:]},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: regMatch},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMatch, Data: interfaceNameData(summary.EgressInterfaceName)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// forwardReturnAcceptExprs builds
// "ip daddr {GuestCIDR} iifname {egress} ct state established,related accept".
//
// The conntrack state is loaded into a register, bitwise-masked with the
// established|related bits, then compared not-equal to zero, matching the
// canonical nftables stateful-return pattern.
func forwardReturnAcceptExprs(summary firewall.ForwardAcceptSummary) []expr.Any {
	masked := summary.GuestCIDR.Masked()
	addr := masked.Addr().As4()
	mask := prefixMask(summary.GuestCIDR.Bits(), 4)
	stateMask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	return []expr.Any{
		&expr.Payload{OperationType: expr.PayloadLoad, DestRegister: regMatch, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Bitwise{SourceRegister: regMatch, DestRegister: regMask, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMask, Data: addr[:]},
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: regMatch},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMatch, Data: interfaceNameData(summary.EgressInterfaceName)},
		&expr.Ct{Register: regProtocol, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: regProtocol, DestRegister: regProtocol, Len: 4, Mask: stateMask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: regProtocol, Data: []byte{0, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// parseForwardAccept reconstructs a ForwardAcceptSummary from one observed
// forward-accept rule (either guard direction). GuestCIDR and egress are present
// in both directions, so a single rule is sufficient to recover the summary;
// the group path validates that both guards exist.
func parseForwardAccept(guard endpointGuardKind, exprs []expr.Any, chain *nftables.Chain) (*firewall.ForwardAcceptSummary, error) {
	switch guard {
	case guardForwardEgress:
		return parseForwardEgress(exprs, chain)
	case guardForwardReturn:
		return parseForwardReturn(exprs, chain)
	default:
		return nil, invalidObservedState("unsupported forward-accept guard %q", guard)
	}
}

func parseForwardEgress(exprs []expr.Any, chain *nftables.Chain) (*firewall.ForwardAcceptSummary, error) {
	if len(exprs) != 6 {
		return nil, invalidObservedState("forward egress expression count is %d", len(exprs))
	}
	payload, ok := exprs[0].(*expr.Payload)
	if !ok || payload.OperationType != expr.PayloadLoad || payload.DestRegister != regMatch || payload.Base != expr.PayloadBaseNetworkHeader || payload.Offset != 12 || payload.Len != 4 {
		return nil, invalidObservedState("forward egress source payload expression is invalid")
	}
	bitwise, ok := exprs[1].(*expr.Bitwise)
	if !ok || bitwise.SourceRegister != regMatch || bitwise.DestRegister != regMask || bitwise.Len != 4 || len(bitwise.Mask) != 4 || !bytes.Equal(bitwise.Xor, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("forward egress mask expression is invalid")
	}
	cmpCIDR, ok := exprs[2].(*expr.Cmp)
	if !ok || cmpCIDR.Op != expr.CmpOpEq || cmpCIDR.Register != regMask || len(cmpCIDR.Data) != 4 {
		return nil, invalidObservedState("forward egress CIDR comparison is invalid")
	}
	meta, ok := exprs[3].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyOIFNAME || meta.Register != regMatch || meta.SourceRegister {
		return nil, invalidObservedState("forward egress output interface expression is invalid")
	}
	cmpIface, ok := exprs[4].(*expr.Cmp)
	if !ok || cmpIface.Op != expr.CmpOpEq || cmpIface.Register != regMatch {
		return nil, invalidObservedState("forward egress output interface comparison is invalid")
	}
	verdict, ok := exprs[5].(*expr.Verdict)
	if !ok || verdict.Kind != expr.VerdictAccept || verdict.Chain != "" {
		return nil, invalidObservedState("forward egress accept verdict is invalid")
	}
	return forwardSummaryFromMatch(cmpCIDR.Data, bitwise.Mask, cmpIface.Data, chain)
}

func parseForwardReturn(exprs []expr.Any, chain *nftables.Chain) (*firewall.ForwardAcceptSummary, error) {
	if len(exprs) != 9 {
		return nil, invalidObservedState("forward return expression count is %d", len(exprs))
	}
	payload, ok := exprs[0].(*expr.Payload)
	if !ok || payload.OperationType != expr.PayloadLoad || payload.DestRegister != regMatch || payload.Base != expr.PayloadBaseNetworkHeader || payload.Offset != 16 || payload.Len != 4 {
		return nil, invalidObservedState("forward return destination payload expression is invalid")
	}
	bitwise, ok := exprs[1].(*expr.Bitwise)
	if !ok || bitwise.SourceRegister != regMatch || bitwise.DestRegister != regMask || bitwise.Len != 4 || len(bitwise.Mask) != 4 || !bytes.Equal(bitwise.Xor, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("forward return mask expression is invalid")
	}
	cmpCIDR, ok := exprs[2].(*expr.Cmp)
	if !ok || cmpCIDR.Op != expr.CmpOpEq || cmpCIDR.Register != regMask || len(cmpCIDR.Data) != 4 {
		return nil, invalidObservedState("forward return CIDR comparison is invalid")
	}
	meta, ok := exprs[3].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyIIFNAME || meta.Register != regMatch || meta.SourceRegister {
		return nil, invalidObservedState("forward return input interface expression is invalid")
	}
	cmpIface, ok := exprs[4].(*expr.Cmp)
	if !ok || cmpIface.Op != expr.CmpOpEq || cmpIface.Register != regMatch {
		return nil, invalidObservedState("forward return input interface comparison is invalid")
	}
	ct, ok := exprs[5].(*expr.Ct)
	if !ok || ct.Key != expr.CtKeySTATE || ct.Register != regProtocol || ct.SourceRegister {
		return nil, invalidObservedState("forward return conntrack state expression is invalid")
	}
	ctMask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	ctBitwise, ok := exprs[6].(*expr.Bitwise)
	if !ok || ctBitwise.SourceRegister != regProtocol || ctBitwise.DestRegister != regProtocol || ctBitwise.Len != 4 || !bytes.Equal(ctBitwise.Mask, ctMask) || !bytes.Equal(ctBitwise.Xor, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("forward return conntrack mask expression is invalid")
	}
	ctCmp, ok := exprs[7].(*expr.Cmp)
	if !ok || ctCmp.Op != expr.CmpOpNeq || ctCmp.Register != regProtocol || !bytes.Equal(ctCmp.Data, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("forward return conntrack comparison is invalid")
	}
	verdict, ok := exprs[8].(*expr.Verdict)
	if !ok || verdict.Kind != expr.VerdictAccept || verdict.Chain != "" {
		return nil, invalidObservedState("forward return accept verdict is invalid")
	}
	return forwardSummaryFromMatch(cmpCIDR.Data, bitwise.Mask, cmpIface.Data, chain)
}

func forwardSummaryFromMatch(cidrData []byte, mask []byte, ifaceData []byte, chain *nftables.Chain) (*firewall.ForwardAcceptSummary, error) {
	bits, ok := maskBits(mask)
	if !ok {
		return nil, invalidObservedState("forward-accept CIDR mask is not contiguous")
	}
	addr := netipAddrFrom4(cidrData)
	return &firewall.ForwardAcceptSummary{
		GuestCIDR:           netipPrefixFrom(addr, bits),
		EgressInterfaceName: firewall.InterfaceName(interfaceNameFromData(ifaceData)),
		Priority:            priorityFromChain(chain, firewall.PriorityNameForwardFilter),
	}, nil
}
```

注:`netipAddrFrom4`/`netipPrefixFrom` 是对 `netip.AddrFrom4(bytesTo4(...))` 与 `netip.PrefixFrom(addr, bits).Masked()` 的小封装,避免本文件直接 import `net/netip` 时与既有 helper 重复;若偏好直接调用,Step 改为 `netip.AddrFrom4(bytesTo4(cidrData))` 与 `netip.PrefixFrom(addr, bits).Masked()` 并 import `net/netip`。实现时二选一并保持一致。

- [ ] **Step 3: 运行验证(随 A4 一并编译)**

本任务不单独构建(linux 包此刻不完整);Task A4 接好 switch 分支后统一 `go build ./internal/hostnet/firewall/linux/`。

- [ ] **Step 4: Commit(与 A4 合并提交)**

forward_expr 与 forward 组路径同属一个逻辑单元,合并到 A4 的提交。


### Task A3: forward-accept 规则组路径

**Files:**
- Create: `internal/hostnet/firewall/linux/forward_linux.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 提供 forward-accept 双向规则组的 desired 构造、ensure 组路径、delete 组路径、观测分组聚合与组校验,严格复用 anti-spoofing 组路径的结构与不变量。
Acceptance evidence:
- 本任务新增组路径函数,统一在 Task A4 接好 switch 分支与 manager 方法后 `go build ./internal/hostnet/firewall/linux/` 通过。
- Task A5 的 fake-handle 测试覆盖 ensure 幂等、组冲突、delete 组、summary 聚合。

- [ ] **Step 2: 创建 `forward_linux.go`**

复用既有 `desiredRule` 结构体(`rules_linux.go:16-25`)、`ensureTable`/`ensureChain`、`listObservedRuleDetails`、`observedRuleDetail`、`translateError`、`endpointGroupLowestHandle` 等既有 helper。新增两个 guard 常量与组路径:

```go
//go:build linux

package linux

import (
	"fmt"
	"net/netip"

	"github.com/google/nftables"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

// forward-accept rule-group guards. Each guard maps to exactly one rule in the
// two-rule forward-accept group, mirroring the anti-spoofing guard mechanism.
const (
	guardForwardEgress endpointGuardKind = "forward-egress"
	guardForwardReturn endpointGuardKind = "forward-return"
)

// desiredForwardAcceptRules expands a ForwardAcceptSpec into the two-rule group
// (egress accept + conntrack established/related return accept).
func desiredForwardAcceptRules(spec firewall.ForwardAcceptSpec) []desiredRule {
	summary := firewall.RuleSummary{ForwardAccept: &firewall.ForwardAcceptSummary{
		GuestCIDR:           spec.GuestCIDR.Masked(),
		EgressInterfaceName: spec.EgressInterfaceName,
		Priority:            spec.Priority,
	}}
	guards := []endpointGuardKind{guardForwardEgress, guardForwardReturn}
	desired := make([]desiredRule, 0, len(guards))
	for _, guard := range guards {
		desired = append(desired, desiredRule{
			family:    firewall.TableFamilyIPv4,
			tableName: spec.TableName,
			chainName: spec.ChainName,
			purpose:   firewall.RulePurposeForwardAccept,
			owner:     spec.RuleOwner,
			guard:     guard,
			priority:  spec.Priority,
			summary:   summary,
		})
	}
	return desired
}

// ensureDesiredForwardGroup reconciles the forward-accept two-rule group. It
// mirrors ensureDesiredRuleGroup but groups by guest CIDR + egress rather than
// by TAP name.
func ensureDesiredForwardGroup(ctx context.Context, h handle, operation string, desired []desiredRule) (firewall.RuleInfo, error) {
	if err := checkContext(ctx); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	if len(desired) == 0 {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: desired forward-accept group must be non-empty", firewallerr.ErrInvalidRequest))
	}
	base := desired[0]
	if err := validateDesiredForwardGroup(desired); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	details, err := listObservedRuleDetails(h, desiredRuleFilter(base))
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	matching, err := matchingForwardDetails(details, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	existingByGuard := map[endpointGuardKind]observedRuleDetail{}
	for _, detail := range matching {
		if _, exists := existingByGuard[detail.guard]; exists {
			return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: duplicate forward-accept guard %q exists for guest CIDR %s", firewallerr.ErrConflict, detail.guard, base.summary.ForwardAccept.GuestCIDR))
		}
		if !forwardGuardMatchesDesired(detail, base) {
			return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: existing forward-accept guard differs from requested state", firewallerr.ErrConflict))
		}
		existingByGuard[detail.guard] = detail
	}
	if len(existingByGuard) == len(desired) {
		info, err := logicalForwardInfo(matching)
		return info, translateError(operation, err)
	}

	table, err := ensureTable(h, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	chain, err := ensureChain(h, table, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	for _, rule := range desired {
		if _, exists := existingByGuard[rule.guard]; exists {
			continue
		}
		exprs, err := ruleExprs(rule)
		if err != nil {
			return firewall.RuleInfo{}, translateError(operation, err)
		}
		userData, err := ruleUserDataForDesired(rule)
		if err != nil {
			return firewall.RuleInfo{}, translateError(operation, err)
		}
		h.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs, UserData: userData})
	}
	if err := h.Flush(); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	details, err = listObservedRuleDetails(h, desiredRuleFilter(base))
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	matching, err = matchingForwardDetails(details, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	info, err := logicalForwardInfo(matching)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	if !ruleSummaryMatchesDesired(info.Summary, base.summary) {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: ensured forward-accept group differs from requested state", firewallerr.ErrInvalidObservedState))
	}
	return info, nil
}

// deleteObservedForwardGroup removes the forward-accept group whose lowest rule
// handle equals ref.Handle, mirroring deleteObservedEndpointGroup.
func deleteObservedForwardGroup(h handle, ref firewall.RuleRef) error {
	filter := firewall.RuleFilter{
		Owner:   firewall.FilterOwner(ref.Owner),
		Purpose: firewall.FilterPurpose(ref.Purpose),
		Family:  firewall.FilterFamily(ref.Family),
		Table:   firewall.FilterTable(ref.TableName),
		Chain:   firewall.FilterChain(ref.ChainName),
	}
	details, err := listObservedRuleDetails(h, filter)
	if err != nil {
		return err
	}
	groups := map[netip.Prefix][]observedRuleDetail{}
	for _, detail := range details {
		if detail.info.Summary.ForwardAccept == nil {
			continue
		}
		key := detail.info.Summary.ForwardAccept.GuestCIDR
		groups[key] = append(groups[key], detail)
	}

	var selected []observedRuleDetail
	for _, group := range groups {
		if endpointGroupLowestHandle(group) == ref.Handle {
			selected = group
			break
		}
	}
	if len(selected) == 0 {
		return nil
	}

	table := &nftables.Table{Family: nftFamily(ref.Family), Name: string(ref.TableName)}
	chain := &nftables.Chain{Table: table, Name: string(ref.ChainName)}
	for _, detail := range selected {
		if err := h.DelRule(&nftables.Rule{Table: table, Chain: chain, Handle: uint64(detail.info.Ref.Handle)}); err != nil {
			return err
		}
	}
	if err := h.Flush(); err != nil {
		return err
	}

	details, err = listObservedRuleDetails(h, filter)
	if err != nil {
		return err
	}
	for _, detail := range details {
		for _, deleted := range selected {
			if detail.info.Ref.Handle == deleted.info.Ref.Handle {
				return fmt.Errorf("%w: deleted forward-accept guard still observed", firewallerr.ErrConflict)
			}
		}
	}
	return nil
}

// matchingForwardDetails selects observed forward-accept rules whose guest CIDR
// matches the desired group, rejecting egress/priority conflicts under the same
// guest CIDR.
func matchingForwardDetails(details []observedRuleDetail, desired desiredRule) ([]observedRuleDetail, error) {
	var matching []observedRuleDetail
	want := desired.summary.ForwardAccept
	for _, detail := range details {
		if detail.info.Ref.Purpose != firewall.RulePurposeForwardAccept || detail.info.Summary.ForwardAccept == nil {
			continue
		}
		observed := detail.info.Summary.ForwardAccept
		if observed.GuestCIDR != want.GuestCIDR {
			continue
		}
		if observed.EgressInterfaceName != want.EgressInterfaceName || observed.Priority != want.Priority {
			return nil, fmt.Errorf("%w: existing forward-accept guard has different egress or priority", firewallerr.ErrConflict)
		}
		matching = append(matching, detail)
	}
	return matching, nil
}

func forwardGuardMatchesDesired(detail observedRuleDetail, desired desiredRule) bool {
	observed := detail.info.Summary.ForwardAccept
	want := desired.summary.ForwardAccept
	if observed == nil || want == nil {
		return false
	}
	return observed.GuestCIDR == want.GuestCIDR &&
		observed.EgressInterfaceName == want.EgressInterfaceName &&
		observed.Priority == want.Priority
}

func validateDesiredForwardGroup(desired []desiredRule) error {
	base := desired[0]
	if base.purpose != firewall.RulePurposeForwardAccept || base.summary.ForwardAccept == nil {
		return fmt.Errorf("%w: desired group must contain forward-accept rules", firewallerr.ErrInvalidRequest)
	}
	seen := map[endpointGuardKind]bool{}
	for _, rule := range desired {
		if rule.family != base.family || rule.tableName != base.tableName || rule.chainName != base.chainName || rule.purpose != base.purpose || rule.owner != base.owner || rule.priority != base.priority || !ruleSummaryMatchesDesired(rule.summary, base.summary) {
			return fmt.Errorf("%w: desired forward-accept group contains mixed identities", firewallerr.ErrInvalidRequest)
		}
		switch rule.guard {
		case guardForwardEgress, guardForwardReturn:
			if seen[rule.guard] {
				return fmt.Errorf("%w: desired forward-accept group contains duplicate guard %q", firewallerr.ErrInvalidRequest, rule.guard)
			}
			seen[rule.guard] = true
		default:
			return fmt.Errorf("%w: unsupported forward-accept guard %q", firewallerr.ErrUnsupported, rule.guard)
		}
	}
	if len(seen) != 2 {
		return fmt.Errorf("%w: desired forward-accept group must contain two guards", firewallerr.ErrInvalidRequest)
	}
	return nil
}

// logicalForwardInfo compacts the two observed forward-accept guard rules into a
// single logical RuleInfo whose Ref.Handle is the lowest guard handle (stable
// identity for delete), mirroring logicalEndpointInfo.
func logicalForwardInfo(details []observedRuleDetail) (firewall.RuleInfo, error) {
	if len(details) == 0 {
		return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept group is empty", firewallerr.ErrInvalidObservedState)
	}
	base := details[0].info
	summary := base.Summary.ForwardAccept
	if summary == nil {
		return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept rule has no summary", firewallerr.ErrInvalidObservedState)
	}
	lowest := details[0].info.Ref.Handle
	for _, detail := range details {
		if detail.info.Summary.ForwardAccept == nil {
			return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept rule has no summary", firewallerr.ErrInvalidObservedState)
		}
		if detail.info.Ref.Handle < lowest {
			lowest = detail.info.Ref.Handle
		}
	}
	ref := base.Ref
	ref.Handle = lowest
	return firewall.RuleInfo{Ref: ref, Summary: firewall.RuleSummary{ForwardAccept: summary}}, nil
}
```

注:`endpointGroupLowestHandle` 已存在于 `manager_linux.go`(被 `deleteObservedEndpointGroup` 使用),forward 组复用它,无需新增。若编译报 `endpointGroupLowestHandle` 不可见,确认其在同 `package linux` 内(同包,无需导出)。

- [ ] **Step 3: 验证(随 A4 一并)**

本任务函数被 A4 的 switch 分支与 manager 方法调用;统一在 A4 后构建。

- [ ] **Step 4: Commit(与 A4 合并)**

forward 组路径、forward expr、switch 分支、manager 方法同属一个可编译逻辑单元,合并为 A4 的单次提交。

### Task A4: switch 分支接线 + manager 方法(linux 包编译闭合)

**Files:**
- Modify: `internal/hostnet/firewall/linux/rules_linux.go`（`ruleExprs`、`ruleUserDataForDesired`、`ruleSummaryMatchesDesired`、`chainForDesired`、`validateExistingChain` 各加 forward-accept 分支）
- Modify: `internal/hostnet/firewall/linux/expr_linux.go`（`observedRuleDetailFor`、`validateObservedRuleUserData` 各加分支）
- Modify: `internal/hostnet/firewall/linux/info_linux.go`（`compactObservedRules` 加 forward 分组分支）
- Modify: `internal/hostnet/firewall/linux/validate_linux.go`（新增 `validateForwardAcceptSpec`；`validateRuleQuery`、`validateFamilyForPurpose` 加分支）
- Modify: `internal/hostnet/firewall/linux/manager_linux.go`（新增 `EnsureForwardAccept`/`DeleteForwardAccept`）

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 把 forward-accept 接入所有既有 purpose-switch 与 manager,`internal/hostnet/firewall/linux/` 编译通过,`*Manager` 满足扩展后的 `firewall.Manager` 接口。
Acceptance evidence:
- `go build ./internal/hostnet/firewall/...` 通过
- `go vet ./internal/hostnet/firewall/...` 通过

- [ ] **Step 2: `rules_linux.go` — `ruleExprs` 加分支**

在 `ruleExprs`（rules_linux.go:326-353）的 `switch desired.purpose` 中,`RulePurposeEndpointAntiSpoofing` case 之后、`default` 之前插入:

```go
	case firewall.RulePurposeForwardAccept:
		if desired.summary.ForwardAccept == nil {
			return nil, fmt.Errorf("%w: invalid forward-accept desired rule", firewallerr.ErrInvalidRequest)
		}
		summary := *desired.summary.ForwardAccept
		switch desired.guard {
		case guardForwardEgress:
			return forwardEgressAcceptExprs(summary), nil
		case guardForwardReturn:
			return forwardReturnAcceptExprs(summary), nil
		default:
			return nil, fmt.Errorf("%w: unsupported forward-accept guard %q", firewallerr.ErrUnsupported, desired.guard)
		}
```

- [ ] **Step 3: `rules_linux.go` — `ruleUserDataForDesired` 加分支**

在 `ruleUserDataForDesired`（rules_linux.go:355-372）的 `switch` 中插入:

```go
	case firewall.RulePurposeForwardAccept:
		switch desired.guard {
		case guardForwardEgress, guardForwardReturn:
			return userDataForRule(desired.owner, desired.purpose, desired.guard), nil
		default:
			return nil, fmt.Errorf("%w: unsupported forward-accept guard %q", firewallerr.ErrUnsupported, desired.guard)
		}
```

- [ ] **Step 4: `rules_linux.go` — `ruleSummaryMatchesDesired` 加分支**

在 `ruleSummaryMatchesDesired`（rules_linux.go:225-241）的 `EndpointAntiSpoofing` 判断块之后、`return false` 之前插入:

```go
	if desired.ForwardAccept != nil {
		return observed.ForwardAccept != nil &&
			observed.ForwardAccept.GuestCIDR == desired.ForwardAccept.GuestCIDR &&
			observed.ForwardAccept.EgressInterfaceName == desired.ForwardAccept.EgressInterfaceName &&
			observed.ForwardAccept.Priority == desired.ForwardAccept.Priority
	}
```

- [ ] **Step 5: `rules_linux.go` — `chainForDesired` 与 `validateExistingChain` 加分支**

`chainForDesired`（rules_linux.go:309-324）在 `RulePurposeEndpointAntiSpoofing` case 后插入(IPv4 filter forward 链):

```go
	case firewall.RulePurposeForwardAccept:
		chain.Type = nftables.ChainTypeFilter
		chain.Hooknum = nftables.ChainHookForward
		return chain, nil
```

`validateExistingChain`（rules_linux.go:280-307）在 `RulePurposeEndpointAntiSpoofing` case 后插入:

```go
	case firewall.RulePurposeForwardAccept:
		if chain.Type != nftables.ChainTypeFilter {
			return fmt.Errorf("%w: existing chain %q is not a filter chain", firewallerr.ErrConflict, chain.Name)
		}
		if chain.Hooknum == nil || *chain.Hooknum != *nftables.ChainHookForward {
			return fmt.Errorf("%w: existing chain %q is not hooked at forward", firewallerr.ErrConflict, chain.Name)
		}
		if chain.Priority == nil || int(*chain.Priority) != desired.priority.Value {
			return fmt.Errorf("%w: existing chain %q priority does not match requested forward filter priority", firewallerr.ErrConflict, chain.Name)
		}
		return nil
```

- [ ] **Step 6: `expr_linux.go` — `observedRuleDetailFor` 加分支**

在 `observedRuleDetailFor`（expr_linux.go:139-157）的 `switch metadata.purpose` 中,`RulePurposeEndpointAntiSpoofing` case 之后插入:

```go
	case firewall.RulePurposeForwardAccept:
		summary, err := parseForwardAccept(metadata.guard, rule.Exprs, chain)
		if err != nil {
			return observedRuleDetail{}, true, err
		}
		info.Summary.ForwardAccept = summary
```

- [ ] **Step 7: `expr_linux.go` — `validateObservedRuleUserData` 加分支**

在 `validateObservedRuleUserData`（expr_linux.go:201-222）的 `switch metadata.purpose` 中追加:

```go
	case firewall.RulePurposeForwardAccept:
		switch metadata.guard {
		case guardForwardEgress, guardForwardReturn:
			return nil
		default:
			return invalidObservedState("forward-accept rule has guard %q", metadata.guard)
		}
```

- [ ] **Step 8: `info_linux.go` — `compactObservedRules` 加 forward 分组**

`compactObservedRules`（info_linux.go:78-107）当前把非 anti-spoofing 规则直接 append,会把 forward-accept 的两条规则当成两个独立 RuleInfo。改为:forward-accept 也走分组聚合。在该函数中,把 forward-accept 也纳入分组逻辑——将顶部的 `if detail.info.Ref.Purpose != firewall.RulePurposeEndpointAntiSpoofing { infos = append(...); continue }` 改为同时排除 forward-accept,并用 guest CIDR 分组:

```go
func compactObservedRules(details []observedRuleDetail) ([]firewall.RuleInfo, error) {
	var infos []firewall.RuleInfo
	endpointGroups := map[endpointGroupKey][]observedRuleDetail{}
	forwardGroups := map[forwardGroupKey][]observedRuleDetail{}
	for _, detail := range details {
		switch detail.info.Ref.Purpose {
		case firewall.RulePurposeEndpointAntiSpoofing:
			summary := detail.info.Summary.EndpointAntiSpoofing
			if summary == nil {
				return nil, fmt.Errorf("%w: endpoint anti-spoofing rule has no endpoint summary", firewallerr.ErrInvalidObservedState)
			}
			key := endpointGroupKey{
				owner:     detail.info.Ref.Owner,
				family:    detail.info.Ref.Family,
				tableName: detail.info.Ref.TableName,
				chainName: detail.info.Ref.ChainName,
				tapName:   summary.TapName,
			}
			endpointGroups[key] = append(endpointGroups[key], detail)
		case firewall.RulePurposeForwardAccept:
			summary := detail.info.Summary.ForwardAccept
			if summary == nil {
				return nil, fmt.Errorf("%w: forward-accept rule has no summary", firewallerr.ErrInvalidObservedState)
			}
			key := forwardGroupKey{
				owner:     detail.info.Ref.Owner,
				family:    detail.info.Ref.Family,
				tableName: detail.info.Ref.TableName,
				chainName: detail.info.Ref.ChainName,
				guestCIDR: summary.GuestCIDR,
			}
			forwardGroups[key] = append(forwardGroups[key], detail)
		default:
			infos = append(infos, detail.info)
		}
	}
	for _, group := range endpointGroups {
		info, err := logicalEndpointInfo(group)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	for _, group := range forwardGroups {
		info, err := logicalForwardInfo(group)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}
```

并在 `info_linux.go` 的 `endpointGroupKey` 定义之后新增 `forwardGroupKey`:

```go
type forwardGroupKey struct {
	owner     firewall.RuleOwner
	family    firewall.TableFamily
	tableName firewall.TableName
	chainName firewall.ChainName
	guestCIDR netip.Prefix
}
```

确认 `info_linux.go` 已 import `net/netip`(若未则补)。

- [ ] **Step 9: `validate_linux.go` — 新增 `validateForwardAcceptSpec` + 两处分支**

新增校验函数(仿 `validateMasqueradeSpec`,validate_linux.go:21-41):

```go
func validateForwardAcceptSpec(ctx context.Context, spec firewall.ForwardAcceptSpec) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := validateSafeName("table", string(spec.TableName)); err != nil {
		return err
	}
	if err := validateSafeName("chain", string(spec.ChainName)); err != nil {
		return err
	}
	if err := validateSafeName("owner", string(spec.RuleOwner)); err != nil {
		return err
	}
	if !spec.GuestCIDR.IsValid() || !spec.GuestCIDR.Addr().Is4() || spec.GuestCIDR.Bits() == 0 {
		return invalidRequest("guest CIDR must be a non-zero IPv4 prefix")
	}
	if err := validateInterfaceName("egress interface", spec.EgressInterfaceName); err != nil {
		return err
	}
	return validatePriority(spec.Priority, firewall.PriorityNameForwardFilter)
}
```

`validateRuleQuery`（validate_linux.go:96-106）的 `case` 列表加入 forward-accept:

```go
	case firewall.RulePurposeMasquerade, firewall.RulePurposeEndpointAntiSpoofing, firewall.RulePurposeForwardAccept:
		return validateRuleRef(ctx, query.Ref, query.Ref.Purpose)
```

`validateFamilyForPurpose`（在 validate_linux.go 内,被 `validateRuleRef` 调用——读取该函数确认其 switch 结构后,为 `RulePurposeForwardAccept` 加 `FamilyIPv4`/`TableFamilyIPv4` 合法分支,与 masquerade 同族)。具体:masquerade 分支要求 `TableFamilyIPv4`,forward-accept 同样要求 `TableFamilyIPv4`,把 forward-accept 并入 masquerade 所在的 IPv4-family case。

- [ ] **Step 10: `manager_linux.go` — 新增两个 manager 方法**

在 `DeleteEndpointAntiSpoofing`（manager_linux.go:58-63）之后插入:

```go
func (m *Manager) EnsureForwardAccept(ctx context.Context, spec firewall.ForwardAcceptSpec) (firewall.RuleInfo, error) {
	if err := validateForwardAcceptSpec(ctx, spec); err != nil {
		return firewall.RuleInfo{}, translateError("ensure forward-accept", err)
	}
	return ensureDesiredForwardGroup(ctx, m.firewallHandle(), "ensure forward-accept", desiredForwardAcceptRules(spec))
}

func (m *Manager) DeleteForwardAccept(ctx context.Context, ref firewall.RuleRef) error {
	if err := validateRuleRef(ctx, ref, firewall.RulePurposeForwardAccept); err != nil {
		return translateError("delete forward-accept", err)
	}
	return translateError("delete forward-accept", deleteObservedForwardGroup(m.firewallHandle(), ref))
}
```

- [ ] **Step 11: 运行验证**

Run: `go build ./internal/hostnet/firewall/... && go vet ./internal/hostnet/firewall/...`
Expected: PASS

- [ ] **Step 12: 若验证失败,修实现**

常见:`validateFamilyForPurpose` 的真实 switch 结构与假设不符 → 读该函数实际代码,把 forward-accept 并入要求 `TableFamilyIPv4` 的分支。`compactObservedRules` 改写后既有 anti-spoofing 测试回归 → 确认 endpoint 分组逻辑与原实现等价(只是把 if/continue 改成 switch)。

- [ ] **Step 13: Commit（A2+A3+A4 合并为一个可编译逻辑单元）**

```bash
git add internal/hostnet/firewall/linux/forward_expr_linux.go internal/hostnet/firewall/linux/forward_linux.go internal/hostnet/firewall/linux/rules_linux.go internal/hostnet/firewall/linux/expr_linux.go internal/hostnet/firewall/linux/info_linux.go internal/hostnet/firewall/linux/validate_linux.go internal/hostnet/firewall/linux/manager_linux.go
git commit -m "feat(hostnet/firewall): implement forward-accept rule group on Linux"
```

### Task A5: forward-accept fake-handle 单元测试

**Files:**
- Create: `internal/hostnet/firewall/linux/forward_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 用既有 fake handle（`fake_handle_test.go`）驱动验证 forward-accept 的 ensure 幂等、组冲突、delete、summary 聚合、校验拒绝。
Acceptance evidence:
- `go test ./internal/hostnet/firewall/linux/ -run TestForward -v` 通过

- [ ] **Step 2: 读 `fake_handle_test.go` 与 `masquerade_test.go` 确认 fake handle 构造方式**

读 `internal/hostnet/firewall/linux/fake_handle_test.go`（确认 fake handle 类型名与构造函数）和 `internal/hostnet/firewall/linux/masquerade_test.go`（确认 `NewManagerWithHandle` 用法、spec 构造、断言风格）。**沿用其确切构造方式**(本计划不假设 fake handle 的具体类型名,实现者按既有测试范式套用)。

- [ ] **Step 3: 创建 `forward_test.go`**

按 masquerade 测试范式编写以下用例(spec 用 `ExplicitPriority(0, firewall.PriorityNameForwardFilter)`、`netip.MustParsePrefix("192.168.100.0/24")`、egress `"eth0"`、owner `"net-test"`、table `"govirta-filter"`、chain `"govirta-forward"`):

```go
//go:build linux

package linux

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func forwardAcceptTestSpec() firewall.ForwardAcceptSpec {
	return firewall.ForwardAcceptSpec{
		TableName:           "govirta-filter",
		ChainName:           "govirta-forward",
		RuleOwner:           "net-test",
		GuestCIDR:           netip.MustParsePrefix("192.168.100.0/24"),
		EgressInterfaceName: "eth0",
		Priority:            firewall.ExplicitPriority(0, firewall.PriorityNameForwardFilter),
	}
}

func TestForwardAcceptEnsureCreatesGroup(t *testing.T) {
	// Use the same fake-handle constructor masquerade_test.go uses.
	h := newFakeHandle() // adjust to the real constructor name found in Step 2
	mgr := NewManagerWithHandle(h)
	spec := forwardAcceptTestSpec()

	info, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureForwardAccept: %v", err)
	}
	if info.Summary.ForwardAccept == nil {
		t.Fatalf("expected forward-accept summary, got %+v", info.Summary)
	}
	if info.Summary.ForwardAccept.GuestCIDR != spec.GuestCIDR.Masked() {
		t.Fatalf("summary GuestCIDR = %s, want %s", info.Summary.ForwardAccept.GuestCIDR, spec.GuestCIDR.Masked())
	}
	if info.Summary.ForwardAccept.EgressInterfaceName != spec.EgressInterfaceName {
		t.Fatalf("summary egress = %q, want %q", info.Summary.ForwardAccept.EgressInterfaceName, spec.EgressInterfaceName)
	}
	if info.Ref.Purpose != firewall.RulePurposeForwardAccept {
		t.Fatalf("ref purpose = %q, want forward-accept", info.Ref.Purpose)
	}

	rules, err := mgr.ListRules(context.Background(), firewall.RuleFilter{
		Owner:   firewall.FilterOwner(spec.RuleOwner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeForwardAccept),
		Family:  firewall.AnyFamily(),
		Table:   firewall.AnyTable(),
		Chain:   firewall.AnyChain(),
	})
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 logical forward-accept rule, got %d", len(rules))
	}
}

func TestForwardAcceptEnsureIsIdempotent(t *testing.T) {
	h := newFakeHandle()
	mgr := NewManagerWithHandle(h)
	spec := forwardAcceptTestSpec()

	first, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	second, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if first.Ref.Handle != second.Ref.Handle {
		t.Fatalf("idempotent ensure changed handle: %d -> %d", first.Ref.Handle, second.Ref.Handle)
	}
}

func TestForwardAcceptEnsureConflictOnDifferentEgress(t *testing.T) {
	h := newFakeHandle()
	mgr := NewManagerWithHandle(h)
	spec := forwardAcceptTestSpec()
	if _, err := mgr.EnsureForwardAccept(context.Background(), spec); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	conflicting := spec
	conflicting.EgressInterfaceName = "eth1"
	_, err := mgr.EnsureForwardAccept(context.Background(), conflicting)
	if !errors.Is(err, firewallerr.ErrConflict) {
		t.Fatalf("conflicting egress ensure = %v, want ErrConflict", err)
	}
}

func TestForwardAcceptDeleteRemovesGroup(t *testing.T) {
	h := newFakeHandle()
	mgr := NewManagerWithHandle(h)
	spec := forwardAcceptTestSpec()
	info, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := mgr.DeleteForwardAccept(context.Background(), info.Ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rules, err := mgr.ListRules(context.Background(), firewall.RuleFilter{
		Owner:   firewall.FilterOwner(spec.RuleOwner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeForwardAccept),
		Family:  firewall.AnyFamily(),
		Table:   firewall.AnyTable(),
		Chain:   firewall.AnyChain(),
	})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules after delete, got %d", len(rules))
	}
}

func TestForwardAcceptValidationRejectsZeroCIDR(t *testing.T) {
	h := newFakeHandle()
	mgr := NewManagerWithHandle(h)
	spec := forwardAcceptTestSpec()
	spec.GuestCIDR = netip.Prefix{}
	_, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if !errors.Is(err, firewallerr.ErrInvalidRequest) {
		t.Fatalf("invalid CIDR ensure = %v, want ErrInvalidRequest", err)
	}
}
```

- [ ] **Step 4: 运行验证**

Run: `go test ./internal/hostnet/firewall/linux/ -run TestForward -v`
Expected: PASS

- [ ] **Step 5: 若验证失败,修实现或测试**

fake handle 构造名不符 → 用 Step 2 查到的真实构造。组聚合返回多于 1 条 → 检查 `compactObservedRules` 的 forward 分组 key 与 `logicalForwardInfo`。delete 后仍有规则 → 检查 `deleteObservedForwardGroup` 的 lowest-handle 选择。

- [ ] **Step 6: 运行 firewall 全包验证**

Run: `go test -race -count=1 ./internal/hostnet/firewall/...`
Expected: PASS（确认未回归 masquerade / anti-spoofing 既有测试)

- [ ] **Step 7: Commit**

```bash
git add internal/hostnet/firewall/linux/forward_test.go
git commit -m "test(hostnet/firewall): cover forward-accept rule group lifecycle"
```

---

## Phase B — 网络编排层（`internal/network/`）

Phase B 在 `internal/network/` 新增仿存储三段式编排层。`netpool.Service` 是注册核心（对应 `pool.Service`），只存声明式逻辑定义、不缓存可漂移真实状态；`NetworkService`/`NICService` 是 VM-facing 门面（对应 `VolumeService`/`ImageService`）。四原语 Manager 作为接口注入 `netpool.Service`。

### Task B1: networker 错误哨兵

**Files:**
- Create: `internal/network/networker/errors.go`
- Test: `internal/network/networker/errors_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 提供网络编排层稳定错误哨兵，支持 `%w` 包装与 `errors.Is`。
Acceptance evidence:
- `go test ./internal/network/networker/ -v` 通过（含 wrapping 测试）

- [ ] **Step 2: 创建 `errors.go`**

```go
// Package networker defines stable error sentinels for the VM-facing network
// orchestration layer. Lower-layer primitive errors (linkerr, routeerr,
// firewallerr, dhcperr) are wrapped with %w and classified into these classes
// so callers can branch with errors.Is regardless of the failing primitive.
package networker

import "errors"

var (
	// ErrInvalidRequest marks caller input that cannot form a valid network operation.
	ErrInvalidRequest = errors.New("invalid network request")
	// ErrNotFound marks lookup or mutation requests for an unknown network or NIC.
	ErrNotFound = errors.New("network resource not found")
	// ErrAlreadyExists marks registration requests that would replace an existing definition.
	ErrAlreadyExists = errors.New("network resource already exists")
	// ErrConflict marks requests that violate current network or NIC definition state.
	ErrConflict = errors.New("network resource conflict")
	// ErrNotReady marks orchestration blocked by an unmet host prerequisite (e.g. IPv4 forwarding).
	ErrNotReady = errors.New("network prerequisite not ready")
)
```

- [ ] **Step 3: 创建 `errors_test.go`**

```go
package networker

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelsSupportWrapping(t *testing.T) {
	for _, sentinel := range []error{ErrInvalidRequest, ErrNotFound, ErrAlreadyExists, ErrConflict, ErrNotReady} {
		wrapped := fmt.Errorf("context: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Fatalf("wrapped %v does not match sentinel", sentinel)
		}
	}
}
```

- [ ] **Step 4: 运行验证**

Run: `go test ./internal/network/networker/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/network/networker/
git commit -m "feat(network/networker): add stable network error sentinels"
```

### Task B2: netpool 逻辑定义类型 + 克隆

**Files:**
- Create: `internal/network/netpool/network.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 定义 `NetworkDefinition`/`NICDefinition`/`networkRecord` 与深拷贝函数；所有行为相关字段显式。
Acceptance evidence:
- 本任务类型被 Task B3/B4 使用，编译在 B3 后统一进行。

- [ ] **Step 2: 创建 `network.go`**

```go
// Package netpool registers declarative logical network definitions and
// orchestrates the host networking primitives (link, route, firewall, dhcp)
// that realize them. It never caches drift-prone observed resource state: the
// authoritative state is always the live host resource, read on demand through
// the injected primitive managers. The in-memory index holds only declarative
// intent that the control plane re-registers (replays) after restart.
package netpool

import (
	"net"
	"net/netip"
	"time"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/link"
)

// NetworkName is the explicit, caller-provided identifier for a logical network.
type NetworkName string

// VMID is the explicit, caller-provided identifier for a VM owning a NIC.
type VMID string

// NetworkDefinition is the declarative logical intent for one shared network
// segment. It describes only what the network should look like; it holds no
// observed resource state. Every behavior-affecting field is explicit and
// caller-provided: the orchestration layer never generates, infers, or defaults
// names, addresses, MACs, or firewall identities.
type NetworkDefinition struct {
	Name        NetworkName
	BridgeName  link.Name
	BridgeMAC   net.HardwareAddr
	BridgeMTU   int
	Subnet      netip.Prefix
	GatewayCIDR netip.Prefix
	Pool        dhcp.AddressRange

	EgressIface firewall.InterfaceName

	DHCPServerID  dhcp.ServerID
	Router        dhcp.DHCPOptionAddrs
	DNS           dhcp.DHCPOptionAddrs
	LeaseDuration time.Duration

	// Govirta-owned firewall identities for this network's IPv4 rules.
	FirewallTable      firewall.TableName
	MasqueradeChain    firewall.ChainName
	ForwardChain       firewall.ChainName
	RuleOwner          firewall.RuleOwner
	MasqueradePriority firewall.Priority
	ForwardPriority    firewall.Priority
}

// NICDefinition is the declarative logical intent for one VM NIC. MAC is the
// guest NIC MAC, supplied by the control plane and threaded unchanged to the
// TAP, the DHCP binding, and the anti-spoofing guard. The orchestration layer
// never generates a MAC.
type NICDefinition struct {
	NetworkName NetworkName
	VMID        VMID
	TapName     link.Name
	MAC         net.HardwareAddr
	IP          netip.Addr
	TapMTU      int
	VNetHeader  link.VNetHeaderMode
	OwnerUID    link.UID
	OwnerGID    link.GID
	Hostname    dhcp.BindingHostname

	// Govirta-owned bridge-family anti-spoofing identities for this NIC.
	AntiSpoofTable    firewall.TableName
	AntiSpoofChain    firewall.ChainName
	AntiSpoofPriority firewall.Priority
}

// networkRecord is the service-owned stored form of a registered network plus
// its registered NIC definitions, keyed by VMID (one NIC per VM per network in
// this phase).
type networkRecord struct {
	def  NetworkDefinition
	nics map[VMID]NICDefinition
}

func (r *networkRecord) clone() *networkRecord {
	cloned := &networkRecord{
		def:  cloneNetworkDefinition(r.def),
		nics: make(map[VMID]NICDefinition, len(r.nics)),
	}
	for id, nic := range r.nics {
		cloned.nics[id] = cloneNICDefinition(nic)
	}
	return cloned
}

func cloneNetworkDefinition(def NetworkDefinition) NetworkDefinition {
	def.BridgeMAC = cloneHardwareAddr(def.BridgeMAC)
	def.Router = cloneDHCPOptionAddrs(def.Router)
	def.DNS = cloneDHCPOptionAddrs(def.DNS)
	return def
}

func cloneNICDefinition(nic NICDefinition) NICDefinition {
	nic.MAC = cloneHardwareAddr(nic.MAC)
	return nic
}

func cloneHardwareAddr(addr net.HardwareAddr) net.HardwareAddr {
	if addr == nil {
		return nil
	}
	cloned := make(net.HardwareAddr, len(addr))
	copy(cloned, addr)
	return cloned
}

func cloneDHCPOptionAddrs(opt dhcp.DHCPOptionAddrs) dhcp.DHCPOptionAddrs {
	if opt.Addrs == nil {
		return opt
	}
	cloned := make([]netip.Addr, len(opt.Addrs))
	copy(cloned, opt.Addrs)
	opt.Addrs = cloned
	return opt
}
```

- [ ] **Step 3: Commit（与 B3 合并）**

类型与注册核心同属一个可编译单元，合并到 B3 提交。

### Task B3: netpool.Service 注册核心

**Files:**
- Create: `internal/network/netpool/service.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `netpool.Service` 提供 `RegisterNetwork`/`RegisterNIC`/`GetNetwork`/`ListNetworks`/`GetNIC`，两级锁 + 克隆，注入四原语 Manager；注册只校验逻辑意图与索引，不碰内核。
Acceptance evidence:
- `go build ./internal/network/netpool/` 通过（B4 接入编排后；B3 单独定义注册部分）
- Task B5 的注册校验测试通过。

- [ ] **Step 2: 创建 `service.go`**

```go
package netpool

import (
	"context"
	"net/netip"
	"sort"
	"sync"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/network/networker"
)

// Service registers logical network/NIC definitions and orchestrates the host
// primitives that realize them. The injected managers are the only source of
// observed truth; Service stores declarative intent only.
type Service struct {
	mu       sync.RWMutex
	networks map[NetworkName]*networkRecord

	link     link.Manager
	route    route.Manager
	firewall firewall.Manager
	dhcp     dhcp.Manager
}

// NewService creates a network orchestration service backed by explicit host
// primitive managers. All four managers are required; a nil manager is a
// programming error the caller must not make.
func NewService(linkMgr link.Manager, routeMgr route.Manager, firewallMgr firewall.Manager, dhcpMgr dhcp.Manager) *Service {
	return &Service{
		networks: make(map[NetworkName]*networkRecord),
		link:     linkMgr,
		route:    routeMgr,
		firewall: firewallMgr,
		dhcp:     dhcpMgr,
	}
}

// RegisterNetwork validates and stores a logical network definition. It does
// not touch the kernel; EnsureNetwork performs host reconciliation. The stored
// record is a service-owned deep copy so external pointers cannot mutate the
// index.
func (s *Service) RegisterNetwork(def NetworkDefinition) error {
	if err := validateNetworkDefinition(def); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.networks[def.Name]; exists {
		return networker.ErrAlreadyExists
	}
	s.networks[def.Name] = &networkRecord{
		def:  cloneNetworkDefinition(def),
		nics: make(map[VMID]NICDefinition),
	}
	return nil
}

// RegisterNIC validates and stores a NIC definition under an already-registered
// network. The IP must fall inside the network's DHCP pool and must not collide
// with another NIC's IP on the same network.
func (s *Service) RegisterNIC(def NICDefinition) error {
	if err := validateNICDefinition(def); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.networks[def.NetworkName]
	if !exists {
		return networker.ErrNotFound
	}
	if err := validateNICAgainstNetwork(def, record.def); err != nil {
		return err
	}
	if _, exists := record.nics[def.VMID]; exists {
		return networker.ErrAlreadyExists
	}
	for vmID, existing := range record.nics {
		if vmID == def.VMID {
			continue
		}
		if existing.IP == def.IP {
			return networker.ErrConflict
		}
		if existing.TapName == def.TapName {
			return networker.ErrConflict
		}
		if existing.MAC.String() == def.MAC.String() {
			return networker.ErrConflict
		}
	}
	record.nics[def.VMID] = cloneNICDefinition(def)
	return nil
}

// GetNetwork returns a deep copy of a registered network definition.
func (s *Service) GetNetwork(name NetworkName) (NetworkDefinition, error) {
	record, err := s.getRecord(name)
	if err != nil {
		return NetworkDefinition{}, err
	}
	return cloneNetworkDefinition(record.def), nil
}

// ListNetworks returns deep copies of all registered network definitions sorted
// by name.
func (s *Service) ListNetworks(ctx context.Context) ([]NetworkDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]NetworkName, 0, len(s.networks))
	for name := range s.networks {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	defs := make([]NetworkDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, cloneNetworkDefinition(s.networks[name].def))
	}
	return defs, nil
}

// GetNIC returns a deep copy of a registered NIC definition.
func (s *Service) GetNIC(networkName NetworkName, vmID VMID) (NICDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, exists := s.networks[networkName]
	if !exists {
		return NICDefinition{}, networker.ErrNotFound
	}
	nic, exists := record.nics[vmID]
	if !exists {
		return NICDefinition{}, networker.ErrNotFound
	}
	return cloneNICDefinition(nic), nil
}

// getRecord returns the live internal record under read lock for internal
// mutation/orchestration paths. Callers must not leak the returned pointer.
func (s *Service) getRecord(name NetworkName) (*networkRecord, error) {
	if name == "" {
		return nil, networker.ErrInvalidRequest
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, exists := s.networks[name]
	if !exists {
		return nil, networker.ErrNotFound
	}
	return record, nil
}

func validateNetworkDefinition(def NetworkDefinition) error {
	if def.Name == "" || def.BridgeName == "" || def.DHCPServerID == "" {
		return networker.ErrInvalidRequest
	}
	if len(def.BridgeMAC) != 6 || def.BridgeMAC[0]&1 != 0 {
		return networker.ErrInvalidRequest
	}
	if def.BridgeMTU <= 0 {
		return networker.ErrInvalidRequest
	}
	if !def.Subnet.IsValid() || !def.Subnet.Addr().Is4() {
		return networker.ErrInvalidRequest
	}
	if !def.GatewayCIDR.IsValid() || !def.GatewayCIDR.Addr().Is4() {
		return networker.ErrInvalidRequest
	}
	if def.Subnet.Masked() != def.Subnet {
		return networker.ErrInvalidRequest
	}
	if !def.Subnet.Contains(def.GatewayCIDR.Addr()) {
		return networker.ErrInvalidRequest
	}
	if !def.Pool.Start.IsValid() || !def.Pool.End.IsValid() {
		return networker.ErrInvalidRequest
	}
	if !def.Subnet.Contains(def.Pool.Start) || !def.Subnet.Contains(def.Pool.End) {
		return networker.ErrInvalidRequest
	}
	if def.Pool.Start.Compare(def.Pool.End) > 0 {
		return networker.ErrInvalidRequest
	}
	if def.EgressIface == "" || def.FirewallTable == "" || def.MasqueradeChain == "" || def.ForwardChain == "" || def.RuleOwner == "" {
		return networker.ErrInvalidRequest
	}
	if def.LeaseDuration <= 0 {
		return networker.ErrInvalidRequest
	}
	return nil
}

func validateNICDefinition(def NICDefinition) error {
	if def.NetworkName == "" || def.VMID == "" || def.TapName == "" {
		return networker.ErrInvalidRequest
	}
	if len(def.MAC) != 6 || def.MAC[0]&1 != 0 {
		return networker.ErrInvalidRequest
	}
	if !def.IP.IsValid() || !def.IP.Is4() {
		return networker.ErrInvalidRequest
	}
	if def.TapMTU <= 0 {
		return networker.ErrInvalidRequest
	}
	if def.VNetHeader != link.VNetHeaderEnabled && def.VNetHeader != link.VNetHeaderDisabled {
		return networker.ErrInvalidRequest
	}
	if !def.OwnerUID.Set || !def.OwnerGID.Set {
		return networker.ErrInvalidRequest
	}
	if def.AntiSpoofTable == "" || def.AntiSpoofChain == "" {
		return networker.ErrInvalidRequest
	}
	return nil
}

func validateNICAgainstNetwork(def NICDefinition, network NetworkDefinition) error {
	if !network.Subnet.Contains(def.IP) {
		return networker.ErrInvalidRequest
	}
	if !ipInRange(def.IP, network.Pool.Start, network.Pool.End) {
		return networker.ErrInvalidRequest
	}
	return nil
}

func ipInRange(ip, start, end netip.Addr) bool {
	return ip.Compare(start) >= 0 && ip.Compare(end) <= 0
}
```

- [ ] **Step 3: 运行验证**

Run: `go build ./internal/network/netpool/`
Expected: 编译通过（此时 orchestrate.go 未建，但 service.go + network.go 自洽编译；若因未用字段告警则在 B4 接入后消除——Go 不对未用包级字段报错，本步应直接通过）。

- [ ] **Step 4: Commit（B2+B3 合并）**

```bash
git add internal/network/netpool/network.go internal/network/netpool/service.go
git commit -m "feat(network/netpool): add logical definitions and registration core"
```

### Task B4: netpool 编排（Ensure/Delete/状态聚合）

**Files:**
- Create: `internal/network/netpool/orchestrate.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `EnsureNetwork`/`EnsureNIC` 按依赖顺序编排四原语；`DeleteNetwork`/`DeleteNIC` 逆序拆除并 `errors.Join`；`GetNetworkStatus`/`GetNICStatus` 现读原语聚合。Ensure 永不主动拆资源。
Acceptance evidence:
- `go build ./internal/network/netpool/` 通过
- Task B5 编排测试（fake 原语）通过。

- [ ] **Step 2: 创建 `orchestrate.go`**

```go
package netpool

import (
	"context"
	"errors"
	"fmt"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/network/networker"
)

// NetworkStatus is observed network state aggregated live from the primitives.
// It is never read from the in-memory definition index.
type NetworkStatus struct {
	Name       NetworkName
	Bridge     link.LinkInfo
	Forwarding route.IPv4ForwardingInfo
	Masquerade firewall.RuleInfo
	Forward    firewall.RuleInfo
	DHCP       dhcp.ServerInfo
}

// NICStatus is observed NIC state aggregated live from the primitives.
type NICStatus struct {
	NetworkName  NetworkName
	VMID         VMID
	Tap          link.LinkInfo
	Lease        dhcp.LeaseInfo
	AntiSpoofing firewall.RuleInfo
}

// EnsureNetwork reconciles host primitives to match the registered network
// definition: bridge, IPv4 forwarding readiness, masquerade, forward-accept,
// then DHCP. It is idempotent and never tears down already-created resources on
// partial failure; the caller decides whether to retry or DeleteNetwork.
func (s *Service) EnsureNetwork(ctx context.Context, name NetworkName) (NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return NetworkStatus{}, err
	}
	record, err := s.getRecord(name)
	if err != nil {
		return NetworkStatus{}, err
	}
	def := cloneNetworkDefinition(record.def)

	if _, err := s.link.EnsureBridge(ctx, link.BridgeSpec{
		Name:        def.BridgeName,
		GatewayCIDR: def.GatewayCIDR.String(),
		MTU:         def.BridgeMTU,
		MAC:         def.BridgeMAC,
	}); err != nil {
		return NetworkStatus{}, fmt.Errorf("ensure network %q bridge: %w", name, err)
	}

	if _, err := s.route.CheckIPv4Forwarding(ctx, route.IPv4ForwardingEnabled); err != nil {
		return NetworkStatus{}, fmt.Errorf("ensure network %q forwarding: %w", name, classifyNotReady(err))
	}

	if _, err := s.firewall.EnsureMasquerade(ctx, firewall.MasqueradeSpec{
		TableName:           def.FirewallTable,
		ChainName:           def.MasqueradeChain,
		RuleOwner:           def.RuleOwner,
		GuestCIDR:           def.Subnet,
		EgressInterfaceName: def.EgressIface,
		Priority:            def.MasqueradePriority,
	}); err != nil {
		return NetworkStatus{}, fmt.Errorf("ensure network %q masquerade: %w", name, err)
	}

	if _, err := s.firewall.EnsureForwardAccept(ctx, firewall.ForwardAcceptSpec{
		TableName:           def.FirewallTable,
		ChainName:           def.ForwardChain,
		RuleOwner:           def.RuleOwner,
		GuestCIDR:           def.Subnet,
		EgressInterfaceName: def.EgressIface,
		Priority:            def.ForwardPriority,
	}); err != nil {
		return NetworkStatus{}, fmt.Errorf("ensure network %q forward-accept: %w", name, err)
	}

	if _, err := s.dhcp.Start(ctx, dhcp.ServerSpec{
		ID:            def.DHCPServerID,
		InterfaceName: def.BridgeName,
		ListenAddr:    netipUnspecified4(),
		ListenPort:    dhcpServerPort(),
		ServerAddr:    def.GatewayCIDR.Addr(),
		Subnet:        def.Subnet,
		Pool:          def.Pool,
		LeaseDuration: def.LeaseDuration,
		Router:        def.Router,
		DNS:           def.DNS,
		BindMode:      dhcp.BindModeInterfaceZone,
	}); err != nil {
		if !errors.Is(err, dhcperr.ErrAlreadyRunning) {
			return NetworkStatus{}, fmt.Errorf("ensure network %q dhcp: %w", name, err)
		}
	}

	return s.GetNetworkStatus(ctx, name)
}
```

注：`dhcp.Start` 对已运行 server 返回 `dhcperr.ErrAlreadyRunning`，幂等 Ensure 需视其为成功，故 import 块需包含 `"github.com/suknna/govirta/internal/hostnet/dhcp/dhcperr"`。`netipUnspecified4()`/`dhcpServerPort()` helper 见 Step 4 定义。

- [ ] **Step 3: 追加 `EnsureNIC` + Delete + 状态聚合到 `orchestrate.go`**

```go
// EnsureNIC reconciles host primitives for one VM NIC: TAP enslaved to the
// network bridge, the static DHCP binding, and the endpoint anti-spoofing
// guard. Idempotent; never tears down on partial failure.
func (s *Service) EnsureNIC(ctx context.Context, networkName NetworkName, vmID VMID) (NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return NICStatus{}, err
	}
	record, err := s.getRecord(networkName)
	if err != nil {
		return NICStatus{}, err
	}
	s.mu.RLock()
	nic, exists := record.nics[vmID]
	def := record.def
	s.mu.RUnlock()
	if !exists {
		return NICStatus{}, networker.ErrNotFound
	}
	nic = cloneNICDefinition(nic)
	def = cloneNetworkDefinition(def)

	if _, err := s.link.EnsureTap(ctx, link.TapSpec{
		Name:       nic.TapName,
		BridgeName: def.BridgeName,
		OwnerUID:   nic.OwnerUID,
		OwnerGID:   nic.OwnerGID,
		MTU:        nic.TapMTU,
		MAC:        nic.MAC,
		VNetHeader: nic.VNetHeader,
	}); err != nil {
		return NICStatus{}, fmt.Errorf("ensure nic %q/%q tap: %w", networkName, vmID, err)
	}

	if _, err := s.dhcp.ApplyBinding(ctx, dhcp.BindingRequest{
		ServerID: def.DHCPServerID,
		MAC:      nic.MAC,
		IP:       nic.IP,
		Hostname: nic.Hostname,
	}); err != nil {
		return NICStatus{}, fmt.Errorf("ensure nic %q/%q binding: %w", networkName, vmID, err)
	}

	if _, err := s.firewall.EnsureEndpointAntiSpoofing(ctx, firewall.EndpointAntiSpoofingSpec{
		TableName:  nic.AntiSpoofTable,
		ChainName:  nic.AntiSpoofChain,
		RuleOwner:  def.RuleOwner,
		BridgeName: firewall.InterfaceName(def.BridgeName),
		TapName:    firewall.InterfaceName(nic.TapName),
		MAC:        nic.MAC,
		IPv4:       nic.IP,
		Priority:   nic.AntiSpoofPriority,
	}); err != nil {
		return NICStatus{}, fmt.Errorf("ensure nic %q/%q anti-spoofing: %w", networkName, vmID, err)
	}

	return s.GetNICStatus(ctx, networkName, vmID)
}

// DeleteNIC tears down one NIC's host resources in reverse order, preserving
// every error via errors.Join. The logical definition stays registered; callers
// remove it explicitly if desired (out of scope for this method).
func (s *Service) DeleteNIC(ctx context.Context, networkName NetworkName, vmID VMID, antiSpoofRef firewall.RuleRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := s.getRecord(networkName)
	if err != nil {
		return err
	}
	s.mu.RLock()
	nic, exists := record.nics[vmID]
	def := record.def
	s.mu.RUnlock()
	if !exists {
		return networker.ErrNotFound
	}

	var errs []error
	if err := s.firewall.DeleteEndpointAntiSpoofing(ctx, antiSpoofRef); err != nil {
		errs = append(errs, fmt.Errorf("delete anti-spoofing: %w", err))
	}
	if err := s.dhcp.RemoveBinding(ctx, dhcp.BindingQuery{ServerID: def.DHCPServerID, MAC: nic.MAC}); err != nil {
		errs = append(errs, fmt.Errorf("remove binding: %w", err))
	}
	if err := s.link.Delete(ctx, nic.TapName); err != nil {
		errs = append(errs, fmt.Errorf("delete tap: %w", err))
	}
	return errors.Join(errs...)
}

// DeleteNetwork tears down a network's shared host resources in reverse order.
// It refuses to delete a network that still has registered NICs.
func (s *Service) DeleteNetwork(ctx context.Context, name NetworkName, masqueradeRef firewall.RuleRef, forwardRef firewall.RuleRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := s.getRecord(name)
	if err != nil {
		return err
	}
	s.mu.RLock()
	nicCount := len(record.nics)
	def := cloneNetworkDefinition(record.def)
	s.mu.RUnlock()
	if nicCount > 0 {
		return networker.ErrConflict
	}

	var errs []error
	if err := s.dhcp.Stop(ctx, def.DHCPServerID); err != nil {
		errs = append(errs, fmt.Errorf("stop dhcp: %w", err))
	}
	if err := s.firewall.DeleteForwardAccept(ctx, forwardRef); err != nil {
		errs = append(errs, fmt.Errorf("delete forward-accept: %w", err))
	}
	if err := s.firewall.DeleteMasquerade(ctx, masqueradeRef); err != nil {
		errs = append(errs, fmt.Errorf("delete masquerade: %w", err))
	}
	if err := s.link.Delete(ctx, def.BridgeName); err != nil {
		errs = append(errs, fmt.Errorf("delete bridge: %w", err))
	}
	return errors.Join(errs...)
}

// GetNetworkStatus aggregates observed network state live from the primitives.
// It never returns the in-memory definition as if it were observed truth.
func (s *Service) GetNetworkStatus(ctx context.Context, name NetworkName) (NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return NetworkStatus{}, err
	}
	record, err := s.getRecord(name)
	if err != nil {
		return NetworkStatus{}, err
	}
	def := cloneNetworkDefinition(record.def)

	bridge, err := s.link.Get(ctx, def.BridgeName)
	if err != nil {
		return NetworkStatus{}, fmt.Errorf("get network %q bridge: %w", name, err)
	}
	forwarding, err := s.route.GetIPv4Forwarding(ctx)
	if err != nil {
		return NetworkStatus{}, fmt.Errorf("get network %q forwarding: %w", name, err)
	}
	server, err := s.dhcp.GetServer(ctx, def.DHCPServerID)
	if err != nil {
		return NetworkStatus{}, fmt.Errorf("get network %q dhcp: %w", name, err)
	}
	return NetworkStatus{
		Name:       name,
		Bridge:     bridge,
		Forwarding: forwarding,
		DHCP:       server,
	}, nil
}

// GetNICStatus aggregates observed NIC state live from the primitives.
func (s *Service) GetNICStatus(ctx context.Context, networkName NetworkName, vmID VMID) (NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return NICStatus{}, err
	}
	record, err := s.getRecord(networkName)
	if err != nil {
		return NICStatus{}, err
	}
	s.mu.RLock()
	nic, exists := record.nics[vmID]
	def := record.def
	s.mu.RUnlock()
	if !exists {
		return NICStatus{}, networker.ErrNotFound
	}

	tap, err := s.link.Get(ctx, nic.TapName)
	if err != nil {
		return NICStatus{}, fmt.Errorf("get nic %q/%q tap: %w", networkName, vmID, err)
	}
	lease, err := s.dhcp.GetLease(ctx, dhcp.BindingQuery{ServerID: def.DHCPServerID, MAC: nic.MAC})
	if err != nil {
		return NICStatus{}, fmt.Errorf("get nic %q/%q lease: %w", networkName, vmID, err)
	}
	return NICStatus{
		NetworkName: networkName,
		VMID:        vmID,
		Tap:         tap,
		Lease:       lease,
	}, nil
}
```

- [ ] **Step 4: 追加 helper 到 `orchestrate.go`**

`classifyNotReady` 把 `route.CheckIPv4Forwarding` 返回的 `routeerr.ErrNotReady` 归类为编排层的 `networker.ErrNotReady`，保留底层链。`orchestrate.go` 顶部 import 增加 `"github.com/suknna/govirta/internal/hostnet/route/routeerr"`：

```go
func classifyNotReady(err error) error {
	if errors.Is(err, routeerr.ErrNotReady) {
		return fmt.Errorf("%w: %w", networker.ErrNotReady, err)
	}
	return err
}
```

`netipUnspecified4()` 返回 `netip.AddrFrom4([4]byte{0,0,0,0})`（监听 `0.0.0.0`，与既有 DHCP 验收的 `netip.MustParseAddr("0.0.0.0")` 一致）；`dhcpServerPort()` 返回 `dhcp.Port(67)`（DHCP server 端口，与既有验收 `dhcpv4.ServerPort` 一致）。这两个 helper 直接内联定义在 `orchestrate.go`：

```go
import "net/netip"

func netipUnspecified4() netip.Addr { return netip.AddrFrom4([4]byte{0, 0, 0, 0}) }
func dhcpServerPort() dhcp.Port     { return dhcp.Port(67) }
```

- [ ] **Step 5: 运行验证**

Run: `go build ./internal/network/netpool/ && go vet ./internal/network/netpool/`
Expected: PASS

- [ ] **Step 6: 若验证失败，修实现**

`dhcperr.ErrAlreadyRunning`/`routeerr.ErrNotReady` 的确切常量名以 545 报告为准（`dhcperr.ErrAlreadyRunning`、`routeerr.ErrNotReady` 已确认存在）。未用 import → 移除。

- [ ] **Step 7: Commit**

```bash
git add internal/network/netpool/orchestrate.go
git commit -m "feat(network/netpool): orchestrate ensure/delete and live status aggregation"
```

---

## Phase B(续）— netpool 测试与 VM-facing 服务

### Task B5: fake 原语 Manager + netpool 注册/编排测试

**Files:**
- Create: `internal/network/netpool/fake_managers_test.go`
- Create: `internal/network/netpool/service_test.go`
- Create: `internal/network/netpool/orchestrate_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 用可记录调用的 fake `link`/`route`/`firewall`/`dhcp` Manager 驱动 netpool，验证注册校验、克隆隔离、编排顺序与参数透传、MAC 透传、Ensure 失败不主动拆资源、Delete 逆序、状态现读聚合。
Acceptance evidence:
- `go test ./internal/network/netpool/ -v` 全部通过
- `go test -race ./internal/network/netpool/` 通过

- [ ] **Step 2: 创建 `fake_managers_test.go`**

fake 记录每次调用的 spec，并允许按方法名注入错误。`call` 记录方法名供顺序断言。

```go
package netpool

import (
	"context"
	"net/netip"
	"sync"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
)

// recorder collects the ordered sequence of primitive calls across all fakes
// so tests can assert orchestration order and per-call payloads.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
}

func (r *recorder) sequence() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

type fakeLink struct {
	rec        *recorder
	bridgeSpec link.BridgeSpec
	tapSpec    link.TapSpec
	deleted    []link.Name
	errs       map[string]error
	info       link.LinkInfo
}

func (f *fakeLink) EnsureBridge(ctx context.Context, spec link.BridgeSpec) (link.LinkInfo, error) {
	f.rec.record("link.EnsureBridge")
	f.bridgeSpec = spec
	if err := f.errs["EnsureBridge"]; err != nil {
		return link.LinkInfo{}, err
	}
	return f.info, nil
}

func (f *fakeLink) EnsureTap(ctx context.Context, spec link.TapSpec) (link.LinkInfo, error) {
	f.rec.record("link.EnsureTap")
	f.tapSpec = spec
	if err := f.errs["EnsureTap"]; err != nil {
		return link.LinkInfo{}, err
	}
	return f.info, nil
}

func (f *fakeLink) Delete(ctx context.Context, name link.Name) error {
	f.rec.record("link.Delete")
	f.deleted = append(f.deleted, name)
	return f.errs["Delete"]
}

func (f *fakeLink) Exists(ctx context.Context, name link.Name) (bool, error) {
	f.rec.record("link.Exists")
	return false, f.errs["Exists"]
}

func (f *fakeLink) Get(ctx context.Context, name link.Name) (link.LinkInfo, error) {
	f.rec.record("link.Get")
	if err := f.errs["Get"]; err != nil {
		return link.LinkInfo{}, err
	}
	info := f.info
	info.Name = name
	return info, nil
}

func (f *fakeLink) List(ctx context.Context, filter link.ListFilter) ([]link.LinkInfo, error) {
	f.rec.record("link.List")
	return nil, f.errs["List"]
}

type fakeRoute struct {
	rec      *recorder
	expected route.IPv4ForwardingState
	errs     map[string]error
}

func (f *fakeRoute) GetIPv4Forwarding(ctx context.Context) (route.IPv4ForwardingInfo, error) {
	f.rec.record("route.GetIPv4Forwarding")
	return route.IPv4ForwardingInfo{State: route.IPv4ForwardingEnabled}, f.errs["GetIPv4Forwarding"]
}

func (f *fakeRoute) CheckIPv4Forwarding(ctx context.Context, expected route.IPv4ForwardingState) (route.IPv4ForwardingInfo, error) {
	f.rec.record("route.CheckIPv4Forwarding")
	f.expected = expected
	if err := f.errs["CheckIPv4Forwarding"]; err != nil {
		return route.IPv4ForwardingInfo{}, err
	}
	return route.IPv4ForwardingInfo{State: route.IPv4ForwardingEnabled}, nil
}

func (f *fakeRoute) AddRoute(ctx context.Context, spec route.RouteSpec) (route.RouteInfo, error) {
	f.rec.record("route.AddRoute")
	return route.RouteInfo{}, f.errs["AddRoute"]
}

func (f *fakeRoute) ReplaceRoute(ctx context.Context, spec route.RouteSpec) (route.RouteInfo, error) {
	f.rec.record("route.ReplaceRoute")
	return route.RouteInfo{}, f.errs["ReplaceRoute"]
}

func (f *fakeRoute) DeleteRoute(ctx context.Context, spec route.RouteSpec) error {
	f.rec.record("route.DeleteRoute")
	return f.errs["DeleteRoute"]
}

func (f *fakeRoute) ListRoutes(ctx context.Context, filter route.RouteFilter) ([]route.RouteInfo, error) {
	f.rec.record("route.ListRoutes")
	return nil, f.errs["ListRoutes"]
}

func (f *fakeRoute) GetRoute(ctx context.Context, query route.RouteQuery) (route.RouteInfo, error) {
	f.rec.record("route.GetRoute")
	return route.RouteInfo{}, f.errs["GetRoute"]
}

type fakeFirewall struct {
	rec          *recorder
	masqSpec     firewall.MasqueradeSpec
	forwardSpec  firewall.ForwardAcceptSpec
	endpointSpec firewall.EndpointAntiSpoofingSpec
	errs         map[string]error
}

func (f *fakeFirewall) EnsureMasquerade(ctx context.Context, spec firewall.MasqueradeSpec) (firewall.RuleInfo, error) {
	f.rec.record("firewall.EnsureMasquerade")
	f.masqSpec = spec
	if err := f.errs["EnsureMasquerade"]; err != nil {
		return firewall.RuleInfo{}, err
	}
	return firewall.RuleInfo{Ref: firewall.RuleRef{Handle: 1}}, nil
}

func (f *fakeFirewall) DeleteMasquerade(ctx context.Context, ref firewall.RuleRef) error {
	f.rec.record("firewall.DeleteMasquerade")
	return f.errs["DeleteMasquerade"]
}

func (f *fakeFirewall) EnsureForwardAccept(ctx context.Context, spec firewall.ForwardAcceptSpec) (firewall.RuleInfo, error) {
	f.rec.record("firewall.EnsureForwardAccept")
	f.forwardSpec = spec
	if err := f.errs["EnsureForwardAccept"]; err != nil {
		return firewall.RuleInfo{}, err
	}
	return firewall.RuleInfo{Ref: firewall.RuleRef{Handle: 2}}, nil
}

func (f *fakeFirewall) DeleteForwardAccept(ctx context.Context, ref firewall.RuleRef) error {
	f.rec.record("firewall.DeleteForwardAccept")
	return f.errs["DeleteForwardAccept"]
}

func (f *fakeFirewall) EnsureEndpointAntiSpoofing(ctx context.Context, spec firewall.EndpointAntiSpoofingSpec) (firewall.RuleInfo, error) {
	f.rec.record("firewall.EnsureEndpointAntiSpoofing")
	f.endpointSpec = spec
	if err := f.errs["EnsureEndpointAntiSpoofing"]; err != nil {
		return firewall.RuleInfo{}, err
	}
	return firewall.RuleInfo{Ref: firewall.RuleRef{Handle: 3}}, nil
}

func (f *fakeFirewall) DeleteEndpointAntiSpoofing(ctx context.Context, ref firewall.RuleRef) error {
	f.rec.record("firewall.DeleteEndpointAntiSpoofing")
	return f.errs["DeleteEndpointAntiSpoofing"]
}

func (f *fakeFirewall) GetRule(ctx context.Context, query firewall.RuleQuery) (firewall.RuleInfo, error) {
	f.rec.record("firewall.GetRule")
	return firewall.RuleInfo{}, f.errs["GetRule"]
}

func (f *fakeFirewall) ListRules(ctx context.Context, filter firewall.RuleFilter) ([]firewall.RuleInfo, error) {
	f.rec.record("firewall.ListRules")
	return nil, f.errs["ListRules"]
}

type fakeDHCP struct {
	rec         *recorder
	serverSpec  dhcp.ServerSpec
	bindingReq  dhcp.BindingRequest
	stopped     []dhcp.ServerID
	removed     []dhcp.BindingQuery
	errs        map[string]error
}

func (f *fakeDHCP) Start(ctx context.Context, spec dhcp.ServerSpec) (dhcp.ServerInfo, error) {
	f.rec.record("dhcp.Start")
	f.serverSpec = spec
	if err := f.errs["Start"]; err != nil {
		return dhcp.ServerInfo{}, err
	}
	return dhcp.ServerInfo{ID: spec.ID, State: dhcp.ServerStateReady}, nil
}

func (f *fakeDHCP) Stop(ctx context.Context, id dhcp.ServerID) error {
	f.rec.record("dhcp.Stop")
	f.stopped = append(f.stopped, id)
	return f.errs["Stop"]
}

func (f *fakeDHCP) ApplyBinding(ctx context.Context, req dhcp.BindingRequest) (dhcp.LeaseInfo, error) {
	f.rec.record("dhcp.ApplyBinding")
	f.bindingReq = req
	if err := f.errs["ApplyBinding"]; err != nil {
		return dhcp.LeaseInfo{}, err
	}
	return dhcp.LeaseInfo{ServerID: req.ServerID, MAC: req.MAC, IP: req.IP, State: dhcp.LeaseStateReserved}, nil
}

func (f *fakeDHCP) RemoveBinding(ctx context.Context, query dhcp.BindingQuery) error {
	f.rec.record("dhcp.RemoveBinding")
	f.removed = append(f.removed, query)
	return f.errs["RemoveBinding"]
}

func (f *fakeDHCP) GetServer(ctx context.Context, id dhcp.ServerID) (dhcp.ServerInfo, error) {
	f.rec.record("dhcp.GetServer")
	if err := f.errs["GetServer"]; err != nil {
		return dhcp.ServerInfo{}, err
	}
	return dhcp.ServerInfo{ID: id, State: dhcp.ServerStateReady}, nil
}

func (f *fakeDHCP) GetLease(ctx context.Context, query dhcp.BindingQuery) (dhcp.LeaseInfo, error) {
	f.rec.record("dhcp.GetLease")
	if err := f.errs["GetLease"]; err != nil {
		return dhcp.LeaseInfo{}, err
	}
	return dhcp.LeaseInfo{ServerID: query.ServerID, MAC: query.MAC, State: dhcp.LeaseStateBound}, nil
}

func (f *fakeDHCP) ListLeases(ctx context.Context, filter dhcp.LeaseFilter) ([]dhcp.LeaseInfo, error) {
	f.rec.record("dhcp.ListLeases")
	return nil, f.errs["ListLeases"]
}

// newTestService wires a Service with fresh fakes sharing one recorder.
func newTestService() (*Service, *recorder, *fakeLink, *fakeRoute, *fakeFirewall, *fakeDHCP) {
	rec := &recorder{}
	fl := &fakeLink{rec: rec, errs: map[string]error{}}
	fr := &fakeRoute{rec: rec, errs: map[string]error{}}
	ff := &fakeFirewall{rec: rec, errs: map[string]error{}}
	fd := &fakeDHCP{rec: rec, errs: map[string]error{}}
	svc := NewService(fl, fr, ff, fd)
	return svc, rec, fl, fr, ff, fd
}

// sampleNetwork returns a valid NetworkDefinition for tests.
func sampleNetwork() NetworkDefinition {
	return NetworkDefinition{
		Name:                "net0",
		BridgeName:          "gvbr0",
		BridgeMAC:           []byte{0x02, 0x00, 0x00, 0x00, 0x01, 0x01},
		BridgeMTU:           1500,
		Subnet:              netip.MustParsePrefix("192.168.100.0/24"),
		GatewayCIDR:         netip.MustParsePrefix("192.168.100.1/24"),
		PoolStart:           netip.MustParseAddr("192.168.100.10"),
		PoolEnd:             netip.MustParseAddr("192.168.100.200"),
		EgressIface:         "eth0",
		DHCPServerID:        "net0-dhcp",
		Router:              dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{netip.MustParseAddr("192.168.100.1")}},
		DNS:                 dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{netip.MustParseAddr("1.1.1.1")}},
		LeaseDuration:       time.Hour,
		FirewallTable:       "govirta",
		MasqueradeChain:     "postrouting",
		ForwardChain:        "forward",
		AntiSpoofTable:      "govirta-bridge",
		AntiSpoofChain:      "antispoof",
		FirewallOwner:       "net0",
		MasqueradePriority:  firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
		ForwardPriority:     firewall.ExplicitPriority(0, firewall.PriorityNameForwardFilter),
		AntiSpoofPriority:   firewall.ExplicitPriority(-300, firewall.PriorityNameBridgeFilter),
	}
}

// sampleNIC returns a valid NICDefinition bound to sampleNetwork.
func sampleNIC() NICDefinition {
	return NICDefinition{
		NetworkName: "net0",
		VMID:        "vm1",
		TapName:     "gvtap0",
		MAC:         []byte{0x02, 0x00, 0x00, 0x00, 0x01, 0x02},
		IP:          netip.MustParseAddr("192.168.100.10"),
		TapMTU:      1500,
		VNetHeader:  link.VNetHeaderEnabled,
		OwnerUID:    0,
		OwnerGID:    0,
	}
}
```

注：本测试文件 import 还需 `time`、`errors`、`reflect`、`testing`；上方片段省略了仅测试用到的 import，实现时按编译器提示补齐。`sampleNetwork` 的字段集必须与 B1 最终定稿的 `NetworkDefinition` 字段一一对应——若 B1 字段命名有出入，以 B1 实现为准同步本 helper。

- [ ] **Step 3: 创建 `service_test.go`(注册校验 + 克隆隔离)**

```go
package netpool

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/suknna/govirta/internal/network/networker"
)

func TestRegisterNetworkRejectsEmptyName(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	def := sampleNetwork()
	def.Name = ""
	if err := svc.RegisterNetwork(def); !errors.Is(err, networker.ErrInvalidRequest) {
		t.Fatalf("RegisterNetwork empty name = %v, want ErrInvalidRequest", err)
	}
}

func TestRegisterNetworkRejectsGatewayOutsideSubnet(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	def := sampleNetwork()
	def.GatewayCIDR = netip.MustParsePrefix("10.0.0.1/24")
	if err := svc.RegisterNetwork(def); !errors.Is(err, networker.ErrInvalidRequest) {
		t.Fatalf("RegisterNetwork gateway outside subnet = %v, want ErrInvalidRequest", err)
	}
}

func TestRegisterNetworkRejectsDuplicate(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	def := sampleNetwork()
	if err := svc.RegisterNetwork(def); err != nil {
		t.Fatalf("first RegisterNetwork: %v", err)
	}
	if err := svc.RegisterNetwork(def); !errors.Is(err, networker.ErrAlreadyExists) {
		t.Fatalf("duplicate RegisterNetwork = %v, want ErrAlreadyExists", err)
	}
}

func TestRegisterNICRejectsUnknownNetwork(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNIC(sampleNIC()); !errors.Is(err, networker.ErrNotFound) {
		t.Fatalf("RegisterNIC unknown network = %v, want ErrNotFound", err)
	}
}

func TestRegisterNICRejectsIPOutsidePool(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	nic := sampleNIC()
	nic.IP = netip.MustParseAddr("192.168.100.250") // above PoolEnd
	if err := svc.RegisterNIC(nic); !errors.Is(err, networker.ErrInvalidRequest) {
		t.Fatalf("RegisterNIC IP outside pool = %v, want ErrInvalidRequest", err)
	}
}

func TestRegisterNICRejectsDuplicateIP(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("first RegisterNIC: %v", err)
	}
	other := sampleNIC()
	other.VMID = "vm2"
	other.TapName = "gvtap1"
	other.MAC = []byte{0x02, 0x00, 0x00, 0x00, 0x01, 0x03}
	// same IP as vm1
	if err := svc.RegisterNIC(other); !errors.Is(err, networker.ErrConflict) {
		t.Fatalf("RegisterNIC duplicate IP = %v, want ErrConflict", err)
	}
}

func TestGetNetworkReturnsClone(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	got, err := svc.GetNetwork(context.Background(), "net0")
	if err != nil {
		t.Fatalf("GetNetwork: %v", err)
	}
	// Mutating the returned clone's MAC must not affect the stored definition.
	if len(got.BridgeMAC) > 0 {
		got.BridgeMAC[0] = 0xff
	}
	again, err := svc.GetNetwork(context.Background(), "net0")
	if err != nil {
		t.Fatalf("GetNetwork again: %v", err)
	}
	if again.BridgeMAC[0] == 0xff {
		t.Fatalf("mutating returned clone leaked into stored definition")
	}
}

func TestDeleteNetworkRejectsWhenNICsPresent(t *testing.T) {
	svc, _, _, _, _, fd := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC: %v", err)
	}
	if err := svc.DeleteNetwork(context.Background(), "net0"); !errors.Is(err, networker.ErrConflict) {
		t.Fatalf("DeleteNetwork with NIC = %v, want ErrConflict", err)
	}
	_ = fd
}
```

- [ ] **Step 4: 创建 `orchestrate_test.go`(编排顺序 + 透传 + 回滚 + 现读)**

```go
package netpool

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
)

func TestEnsureNetworkOrchestrationOrder(t *testing.T) {
	svc, rec, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	if _, err := svc.EnsureNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	got := rec.sequence()
	want := []string{
		"link.EnsureBridge",
		"route.CheckIPv4Forwarding",
		"firewall.EnsureMasquerade",
		"firewall.EnsureForwardAccept",
		"dhcp.Start",
		"link.Get",                 // status aggregation reads
		"firewall.GetRule",         // (order within status read is implementation-defined; see note)
	}
	// Assert the ensure phase order (first five calls) deterministically.
	for i := 0; i < 5; i++ {
		if got[i] != want[i] {
			t.Fatalf("ensure call[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestEnsureNICPassesGuestMACToAllThree(t *testing.T) {
	svc, _, fl, _, ff, fd := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC: %v", err)
	}
	if _, err := svc.EnsureNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("EnsureNIC: %v", err)
	}
	wantMAC := sampleNIC().MAC.String()
	if fl.tapSpec.MAC.String() != wantMAC {
		t.Fatalf("TAP MAC = %s, want %s", fl.tapSpec.MAC, wantMAC)
	}
	if fd.bindingReq.MAC.String() != wantMAC {
		t.Fatalf("DHCP binding MAC = %s, want %s", fd.bindingReq.MAC, wantMAC)
	}
	if ff.endpointSpec.MAC.String() != wantMAC {
		t.Fatalf("anti-spoofing MAC = %s, want %s", ff.endpointSpec.MAC, wantMAC)
	}
}

func TestEnsureNetworkDoesNotTearDownOnDHCPFailure(t *testing.T) {
	svc, rec, _, _, _, fd := newTestService()
	fd.errs["Start"] = errors.New("listen failed")
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	if _, err := svc.EnsureNetwork(context.Background(), "net0"); err == nil {
		t.Fatalf("EnsureNetwork expected error, got nil")
	}
	for _, call := range rec.sequence() {
		switch call {
		case "link.Delete", "firewall.DeleteMasquerade", "firewall.DeleteForwardAccept", "dhcp.Stop":
			t.Fatalf("Ensure must not tear down resources on failure, saw %q (full=%v)", call, rec.sequence())
		}
	}
}

func TestDeleteNetworkReverseOrder(t *testing.T) {
	svc, rec, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	if _, err := svc.EnsureNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	// Capture index where delete phase begins.
	before := len(rec.sequence())
	if err := svc.DeleteNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("DeleteNetwork: %v", err)
	}
	deleteCalls := rec.sequence()[before:]
	want := []string{
		"dhcp.Stop",
		"firewall.DeleteForwardAccept",
		"firewall.DeleteMasquerade",
		"link.Delete",
	}
	for i, w := range want {
		if i >= len(deleteCalls) || deleteCalls[i] != w {
			t.Fatalf("delete call[%d] = %v, want %q (full=%v)", i, deleteCalls, w, deleteCalls)
		}
	}
}

func TestGetNetworkStatusReadsLiveState(t *testing.T) {
	svc, rec, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork: %v", err)
	}
	if _, err := svc.GetNetworkStatus(context.Background(), "net0"); err != nil {
		t.Fatalf("GetNetworkStatus: %v", err)
	}
	// Must hit primitive Get/List, not return the in-memory definition.
	sawLiveRead := false
	for _, call := range rec.sequence() {
		if call == "link.Get" || call == "dhcp.GetServer" || call == "firewall.GetRule" || call == "firewall.ListRules" {
			sawLiveRead = true
		}
	}
	if !sawLiveRead {
		t.Fatalf("GetNetworkStatus did not read live primitive state: %v", rec.sequence())
	}
	_ = dhcp.ServerStateReady
}
```

注：`TestEnsureNetworkOrchestrationOrder` 只硬断言前五个 ensure 调用的确定顺序;状态聚合阶段的读调用顺序由实现决定,不硬断言其内部次序。`TestDeleteNetworkReverseOrder` 中 `before` 截取依赖 EnsureNetwork 与 DeleteNetwork 之间无其它原语调用——若 GetNetworkStatus 被编排隐式触发,调整截取逻辑或在 Delete 前清空 recorder。实现时若 `recorder` 需要 reset，加一个 `func (r *recorder) reset()` 并在 Delete 测试前调用。

- [ ] **Step 5: 运行验证**

Run: `go test -race ./internal/network/netpool/ -v`
Expected: PASS

- [ ] **Step 6: 若验证失败,修实现或过期测试**

编排顺序断言失败 → 对照 spec 4.1/4.2 修 `orchestrate.go` 的调用次序(bridge→forwarding→masq→forward→dhcp;delete 逆序)。MAC 透传断言失败 → 修 `EnsureNIC` 把 `nic.MAC` 传给三处。现读断言失败 → 修 `GetNetworkStatus` 改为调用原语 Get/List。

- [ ] **Step 7: Commit**

```bash
git add internal/network/netpool/fake_managers_test.go internal/network/netpool/service_test.go internal/network/netpool/orchestrate_test.go
git commit -m "test(network/netpool): registration, orchestration order, MAC passthrough, live status"
```

### Task B6: NetworkService(VM-facing 网段)

**Files:**
- Create: `internal/network/service.go`
- Test: `internal/network/service_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `NetworkService` 包装 `*netpool.Service`,暴露 VM-facing 网段 API(RegisterNetwork/EnsureNetwork/DeleteNetwork/GetNetworkStatus/ListNetworks),每方法 `ctx.Err()` 先验 + 委托。
Acceptance evidence:
- `go build ./internal/network/` 通过
- `go test ./internal/network/ -run TestNetworkService -v` 通过

- [ ] **Step 2: 创建 `service.go`**

```go
// Package network is the VM-facing network orchestration layer. It mirrors the
// internal/storage layering: NetworkService/NICService are the VM-facing API,
// netpool.Service is the shared registration core, and the internal/hostnet/*
// primitives are the driver layer. The registration core stores declarative
// logical intent only; observed resource state always comes from the primitives.
package network

import (
	"context"

	"github.com/suknna/govirta/internal/network/netpool"
)

// NetworkService is the VM-facing API for shared network segments.
type NetworkService struct {
	pools *netpool.Service
}

// NewNetworkService creates a VM-facing network service backed by an explicit
// netpool service.
func NewNetworkService(pools *netpool.Service) *NetworkService {
	return &NetworkService{pools: pools}
}

// RegisterNetwork registers one logical network definition without touching the
// kernel. Callers replay registrations after restart.
func (s *NetworkService) RegisterNetwork(def netpool.NetworkDefinition) error {
	return s.pools.RegisterNetwork(def)
}

// EnsureNetwork reconciles the host primitives for one registered network.
func (s *NetworkService) EnsureNetwork(ctx context.Context, name netpool.NetworkName) (netpool.NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NetworkStatus{}, err
	}
	return s.pools.EnsureNetwork(ctx, name)
}

// DeleteNetwork tears down one registered network; it fails if NICs remain.
func (s *NetworkService) DeleteNetwork(ctx context.Context, name netpool.NetworkName) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.pools.DeleteNetwork(ctx, name)
}

// GetNetworkStatus returns the observed live state aggregated from primitives.
func (s *NetworkService) GetNetworkStatus(ctx context.Context, name netpool.NetworkName) (netpool.NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NetworkStatus{}, err
	}
	return s.pools.GetNetworkStatus(ctx, name)
}

// ListNetworks returns registered network definitions (clones).
func (s *NetworkService) ListNetworks(ctx context.Context) ([]netpool.NetworkDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.pools.ListNetworks(ctx)
}
```

- [ ] **Step 3: 创建 `service_test.go`**

```go
package network

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/network/netpool"
)

func TestNetworkServiceCanceledContext(t *testing.T) {
	svc := NewNetworkService(netpool.NewService(
		nil, nil, nil, nil, // managers unused: canceled ctx short-circuits before delegation
	))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.EnsureNetwork(canceled, "net0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureNetwork canceled = %v, want context.Canceled", err)
	}
	if err := svc.DeleteNetwork(canceled, "net0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteNetwork canceled = %v, want context.Canceled", err)
	}
}
```

注:此测试依赖 `NetworkService` 方法在委托前先做 `ctx.Err()` 短路,因此即使注入 nil manager 也不会解引用(canceled 分支提前返回)。若 `NewService` 对 nil manager 有非空校验而 panic,则改为注入 B5 的 fake manager。实现时二选一。

- [ ] **Step 4: 运行验证**

Run: `go build ./internal/network/ && go test ./internal/network/ -run TestNetworkService -v`
Expected: PASS

- [ ] **Step 5: 若验证失败,修实现**

`netpool.NewService` 签名(参数顺序/数量)以 B3 定稿为准;若与本任务调用不符,对齐调用处。

- [ ] **Step 6: Commit**

```bash
git add internal/network/service.go internal/network/service_test.go
git commit -m "feat(network): add VM-facing NetworkService over netpool core"
```

### Task B7: NICService(VM-facing 网卡)

**Files:**
- Create: `internal/network/nic_service.go`
- Test: `internal/network/nic_service_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `NICService` 包装同一个 `*netpool.Service`,暴露 VM-facing 网卡 API(RegisterNIC/EnsureNIC/DeleteNIC/GetNICStatus),每方法 `ctx.Err()` 先验 + 委托。
Acceptance evidence:
- `go build ./internal/network/` 通过
- `go test ./internal/network/ -run TestNICService -v` 通过

- [ ] **Step 2: 创建 `nic_service.go`**

```go
package network

import (
	"context"

	"github.com/suknna/govirta/internal/network/netpool"
)

// NICService is the VM-facing API for per-VM network interfaces.
type NICService struct {
	pools *netpool.Service
}

// NewNICService creates a VM-facing NIC service backed by an explicit netpool
// service. It shares the same registration core as NetworkService.
func NewNICService(pools *netpool.Service) *NICService {
	return &NICService{pools: pools}
}

// RegisterNIC registers one logical NIC definition without touching the kernel.
func (s *NICService) RegisterNIC(def netpool.NICDefinition) error {
	return s.pools.RegisterNIC(def)
}

// EnsureNIC reconciles the host primitives for one registered NIC.
func (s *NICService) EnsureNIC(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) (netpool.NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NICStatus{}, err
	}
	return s.pools.EnsureNIC(ctx, networkName, vmID)
}

// DeleteNIC tears down one registered NIC in reverse dependency order.
func (s *NICService) DeleteNIC(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.pools.DeleteNIC(ctx, networkName, vmID)
}

// GetNICStatus returns the observed live NIC state aggregated from primitives.
func (s *NICService) GetNICStatus(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) (netpool.NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NICStatus{}, err
	}
	return s.pools.GetNICStatus(ctx, networkName, vmID)
}
```

- [ ] **Step 3: 创建 `nic_service_test.go`**

```go
package network

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/network/netpool"
)

func TestNICServiceCanceledContext(t *testing.T) {
	svc := NewNICService(netpool.NewService(nil, nil, nil, nil))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.EnsureNIC(canceled, "net0", "vm1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureNIC canceled = %v, want context.Canceled", err)
	}
	if err := svc.DeleteNIC(canceled, "net0", "vm1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteNIC canceled = %v, want context.Canceled", err)
	}
}
```

- [ ] **Step 4: 运行验证**

Run: `go build ./internal/network/ && go test ./internal/network/ -v`
Expected: PASS

- [ ] **Step 5: 若验证失败,修实现**

同 B6:`netpool.NewService` 签名以 B3 定稿为准。

- [ ] **Step 6: Commit**

```bash
git add internal/network/nic_service.go internal/network/nic_service_test.go
git commit -m "feat(network): add VM-facing NICService sharing netpool core"
```

---

## Phase C — 死包移除 + node 重接

### Task C1: 删除死包 internal/network/bridge 并重接 node.Agent

**Files:**
- Delete: `internal/network/bridge/bridge.go`(整个 `internal/network/bridge/` 目录)
- Modify: `internal/node/agent.go`
- Modify: `cmd/govirtlet/main.go`
- Test: `internal/node/agent_test.go`(若存在则更新;否则不新建)

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 删除已死的 `internal/network/bridge` 包,`node.Agent` 改为组合新的 `*network.NetworkService`/`*network.NICService`(注入 noop 原语构造),`cmd/govirtlet` 编译并启动通过。
Acceptance evidence:
- `go build ./...` 通过(全仓库无残留 bridge 引用)
- `go test ./internal/node/ ./cmd/govirtlet/` 通过
- `grep -r "internal/network/bridge" --include=*.go .` 无结果(文档/plan/spec 与 .githooks 路径串不算 Go 引用,保留不动)

- [ ] **Step 2: 删除死包目录**

```bash
git rm -r internal/network/bridge/
```

确认仅删除 `internal/network/bridge/bridge.go`(该目录唯一文件,545 报告确认)。

- [ ] **Step 3: 重写 `internal/node/agent.go`**

把 bridge 依赖替换为新编排层。`Agent` 现在持有 `*network.NetworkService` 与 `*network.NICService`(共享一个 `netpool.Service`),原语注入 noop 实现(`link`/`route`/`firewall`/`dhcp` 各自的 noop)。完整新文件:

```go
package node

import (
	"context"

	"github.com/rs/zerolog"
	hostdhcp "github.com/suknna/govirta/internal/hostnet/dhcp"
	hostfirewall "github.com/suknna/govirta/internal/hostnet/firewall"
	hostlink "github.com/suknna/govirta/internal/hostnet/link"
	hostroute "github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/network"
	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/internal/virt/qmp"
)

// Agent coordinates compute-node local virtualization dependencies.
type Agent struct {
	qmpClient      qmp.Client
	networkService *network.NetworkService
	nicService     *network.NICService
}

// NewAgent creates a node agent with no-op dependencies.
//
// The network services share one netpool core wired with no-op host primitives;
// real netlink/nftables/CoreDHCP managers are injected by the compute node at a
// later integration step.
func NewAgent() *Agent {
	pools := netpool.NewService(
		hostlink.NewNoopManager(),
		hostroute.NewNoopManager(),
		hostfirewall.NewNoopManager(),
		hostdhcp.NewNoopManager(),
	)
	return &Agent{
		qmpClient:      qmp.NewNoopClient(),
		networkService: network.NewNetworkService(pools),
		nicService:     network.NewNICService(pools),
	}
}

// Run starts the node agent skeleton.
func (a *Agent) Run(ctx context.Context) error {
	logger := zerolog.Ctx(ctx).With().
		Str("component", "node").
		Str("qmp_client", a.qmpClient.Name()).
		Logger()

	ctx = logger.WithContext(ctx)
	zerolog.Ctx(ctx).Info().Msg("starting node agent")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
```

注:`a.networkService` / `a.nicService` 此刻已构造但 `Run` 尚未驱动它们(skeleton 仍是 ctx-wait 循环,与原 bridge skeleton 同语义)。这保持与既有 `qmpClient` 一样的"已注入未驱动"占位状态,符合"本次搭骨架 + 完整原语/编排实现,真实接入 VM 生命周期留待后续"的范围。若 `zerolog` 日志要体现网络组件,可加 `.Str("network", "noop")`,非必需。

校验 noop 构造函数名(545 报告已确认):`hostlink.NewNoopManager()` 返回 `*link.NoopManager`、`hostfirewall.NewNoopManager()` 返回 `firewall.NoopManager`(值类型)、`hostdhcp.NewNoopManager()`、`hostroute.NewNoopManager()` 均存在且满足各自 `Manager` 接口。`netpool.NewService` 形参类型为四个原语接口(`link.Manager`/`route.Manager`/`firewall.Manager`/`dhcp.Manager`),noop 值/指针按接口实现传入。

- [ ] **Step 4: 确认 `cmd/govirtlet/main.go` 无需改动**

`main.go` 仅调用 `node.NewAgent().Run(ctx)`(545 报告确认 `NewAgent()` 无参且本任务保持无参),无需改动。若 Step 3 改了 `NewAgent` 签名(本计划未改),才需同步;本计划保持无参,跳过。

- [ ] **Step 5: 运行验证**

Run: `go build ./... && go test ./internal/node/ ./cmd/govirtlet/`
Expected: PASS

再确认无残留 Go 引用:

Run: `grep -rn "internal/network/bridge" --include=*.go .`
Expected: 无输出

- [ ] **Step 6: 若验证失败,修实现**

`internal/node/agent_test.go` 若断言了 `bridge_manager` 日志字段或 `bridgeManager` 字段 → 更新为新结构(去掉 bridge 断言;若测试只验 `Run` 不报错,无需改)。noop 构造函数名不符 → 以各 hostnet 包实际导出名为准对齐。

- [ ] **Step 7: Commit**

```bash
git add internal/node/agent.go cmd/govirtlet/main.go
git rm -r internal/network/bridge/
git commit -m "refactor(node): remove dead network/bridge skeleton, wire network services"
```

注:`git rm` 已在 Step 2 暂存;此处 `git add` + 合并提交确保删除与重接在同一逻辑提交(架构转变不留新老并存)。

---

## Phase D — Lima guest 出外网验收

### Task D1: network egress 验收测试

**Files:**
- Create: `test/acceptance/network_egress_test.go`
- Modify: `test/acceptance/doc.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 经新编排 API(`NetworkService.RegisterNetwork`+`EnsureNetwork`、`NICService.RegisterNIC`+`EnsureNIC`)一键建网 + 挂卡,启动 CirrOS,guest 经 DHCP 自动获得 IP + 默认路由 + DNS,并分两步 ping 通外网(先 `8.8.8.8`,再域名)。
Acceptance evidence:
- `scripts/acceptance.sh full` 中 `TestNetworkEgressEndToEnd` 通过
- 测试在 Lima Ubuntu arm64(vz + nestedVirtualization)guest 内以 sudo 运行,使用真实 netlink/nftables/CoreDHCP 原语

- [ ] **Step 2: 创建 `network_egress_test.go`**

复用 harness 的 `requireHostnetAcceptanceEnv`、`waitForQMPStatus`、`waitForSerialMarkerGroups`、`runSerialCommand`、`writeSerialCommand`、`stopQEMU`、`shortSocketPath`、`logFirewallDiagnostics`、`logRouteDiagnostics`、`logNetworkDiagnostics`(均在 harness.go 已确认存在)。真实 Linux 原语用 `internal/hostnet/*/linux` 的 `NewManager`,编排用 `internal/network`。

```go
//go:build acceptance && linux

package acceptance

import (
	"context"
	"net"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostdhcpcore "github.com/suknna/govirta/internal/hostnet/dhcp/coredhcp"
	hostfirewalllinux "github.com/suknna/govirta/internal/hostnet/firewall/linux"
	hostlinklinux "github.com/suknna/govirta/internal/hostnet/link/linux"
	hostroutelinux "github.com/suknna/govirta/internal/hostnet/route/linux"
	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/network"
	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/internal/virt/qmp"
)

func TestNetworkEgressEndToEnd(t *testing.T) {
	env := requireHostnetAcceptanceEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	bridgeName := link.Name("gvbr0")
	tapName := link.Name("gvtap0")
	guestMAC := mustParseAcceptanceMAC(t, "02:00:00:00:01:02")
	guestIP := netip.MustParseAddr("192.168.100.10")
	gatewayIP := netip.MustParseAddr("192.168.100.1")
	subnet := netip.MustParsePrefix("192.168.100.0/24")
	dns := netip.MustParseAddr("8.8.8.8")

	egress, err := defaultEgressInterface(ctx)
	if err != nil {
		t.Fatalf("determine egress interface: %v", err)
	}
	t.Logf("egress interface = %q", egress)

	// Real host primitives.
	linkMgr, err := hostlinklinux.NewManager()
	if err != nil {
		t.Fatalf("new link manager: %v", err)
	}
	routeMgr, err := hostroutelinux.NewManager()
	if err != nil {
		t.Fatalf("new route manager: %v", err)
	}
	firewallMgr, err := hostfirewalllinux.NewManager()
	if err != nil {
		t.Fatalf("new firewall manager: %v", err)
	}
	dhcpMgr := hostdhcpcore.NewManager()

	pools := netpool.NewService(linkMgr, routeMgr, firewallMgr, dhcpMgr)
	netSvc := network.NewNetworkService(pools)
	nicSvc := network.NewNICService(pools)

	netDef := netpool.NetworkDefinition{
		Name:               "egress-net",
		BridgeName:         bridgeName,
		BridgeMAC:          net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x01, 0x01},
		BridgeMTU:          1500,
		Subnet:             subnet,
		GatewayCIDR:        netip.MustParsePrefix("192.168.100.1/24"),
		PoolStart:          guestIP,
		PoolEnd:            guestIP,
		EgressIface:        firewall.InterfaceName(egress),
		DHCPServerID:       "egress-net-dhcp",
		Router:             dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{gatewayIP}},
		DNS:                dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{dns}},
		LeaseDuration:      time.Hour,
		FirewallTable:      "govirta-nat",
		MasqueradeChain:    "postrouting",
		ForwardChain:       "forward",
		AntiSpoofTable:     "govirta-bridge",
		AntiSpoofChain:     "antispoof",
		FirewallOwner:      "egress-net",
		MasqueradePriority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
		ForwardPriority:    firewall.ExplicitPriority(0, firewall.PriorityNameForwardFilter),
		AntiSpoofPriority:  firewall.ExplicitPriority(-300, firewall.PriorityNameBridgeFilter),
	}
	if err := netSvc.RegisterNetwork(netDef); err != nil {
		t.Fatalf("register network: %v", err)
	}
	if _, err := netSvc.EnsureNetwork(ctx, "egress-net"); err != nil {
		logFirewallDiagnostics(t, ctx)
		logRouteDiagnostics(t, ctx, "8.8.8.8", string(bridgeName))
		t.Fatalf("ensure network: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := nicSvc.DeleteNIC(cleanupCtx, "egress-net", "vm1"); err != nil {
			t.Logf("delete nic: %v", err)
		}
		if err := netSvc.DeleteNetwork(cleanupCtx, "egress-net"); err != nil {
			t.Logf("delete network: %v", err)
		}
	})

	nicDef := netpool.NICDefinition{
		NetworkName: "egress-net",
		VMID:        "vm1",
		TapName:     tapName,
		MAC:         guestMAC,
		IP:          guestIP,
		TapMTU:      1500,
		VNetHeader:  link.VNetHeaderEnabled,
		OwnerUID:    0,
		OwnerGID:    0,
	}
	if err := nicSvc.RegisterNIC(nicDef); err != nil {
		t.Fatalf("register nic: %v", err)
	}
	if _, err := nicSvc.EnsureNIC(ctx, "egress-net", "vm1"); err != nil {
		logFirewallDiagnostics(t, ctx)
		logNetworkDiagnostics(t, ctx)
		t.Fatalf("ensure nic: %v", err)
	}

	scratch := t.TempDir()
	diskPath := filepath.Join(scratch, "cirros-root.qcow2")
	qmpPath := shortSocketPath(t, scratch, "qmp.sock")
	serialPath := shortSocketPath(t, scratch, "serial.sock")
	if err := copyFile(env.Cirros, diskPath); err != nil {
		t.Fatalf("copy cirros image: %v", err)
	}

	// Router option is enabled here (egress needs a default route), so suppress
	// CirrOS metadata probing with ds=none to avoid the 169.254.169.254 delay.
	appendLine := "console=ttyAMA0 ds=none"
	args := []string{
		"-machine", "virt,accel=kvm", "-cpu", "host", "-m", "256M", "-smp", "1",
		"-kernel", env.Kernel, "-initrd", env.Initramfs, "-append", appendLine,
		"-drive", "file=" + diskPath + ",if=virtio,format=qcow2",
		"-netdev", "tap,id=net0,ifname=" + string(tapName) + ",script=no,downscript=no,vhost=on",
		"-device", "virtio-net-pci,netdev=net0,mac=" + guestMAC.String() + ",romfile=",
		"-qmp", "unix:" + qmpPath + ",server=on,wait=off",
		"-serial", "unix:" + serialPath + ",server=on,wait=on",
		"-display", "none", "-no-reboot", "-no-shutdown",
	}
	t.Logf("qemu argv: %s %s", env.QEMU, strings.Join(args, " "))

	cmd := exec.Command(env.QEMU, args...)
	qemuStderr, err := startQEMUCommand(cmd)
	if err != nil {
		t.Fatalf("start qemu: %v\nstderr:\n%s", err, qemuStderr.String())
	}
	serialDone := make(chan serialMarkerResult, 1)
	go func() {
		output, err := waitForSerialMarkerGroups(ctx, serialPath, []serialMarkerGroup{
			{Name: "serial login marker", Markers: []string{"login:"}},
		})
		serialDone <- serialMarkerResult{Output: output, Err: err}
	}()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("QEMU stderr before cleanup:\n%s", qemuStderr.String())
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := stopQEMU(cleanupCtx, qmpPath, cmd); err != nil {
			t.Logf("stop qemu: %v", err)
		}
	})

	status := waitForQMPStatus(t, ctx, qmpPath, qmp.StateRunning)
	if !status.Running {
		t.Fatalf("QMP status running = false, state = %q", status.State)
	}

	result := <-serialDone
	if result.Err != nil {
		logFirewallDiagnostics(t, context.Background())
		logRouteDiagnostics(t, context.Background(), "8.8.8.8", string(bridgeName))
		logNetworkDiagnostics(t, context.Background())
		t.Fatalf("waiting for serial login marker: %v\nserial tail:\n%s", result.Err, tailString(result.Output, 8192))
	}

	for _, command := range []string{"cirros", "gocubsgo"} {
		if err := writeSerialCommand(ctx, serialPath, command); err != nil {
			t.Fatalf("write serial login command: %v", err)
		}
		time.Sleep(time.Second)
	}

	// Verify DHCP gave the guest IP + default route.
	addrOutput, err := runSerialCommand(ctx, serialPath, "ip -4 addr show dev eth0")
	if err != nil {
		t.Fatalf("read guest address: %v\noutput:\n%s", err, tailString(addrOutput, 8192))
	}
	if !strings.Contains(addrOutput, guestIP.String()+"/24") {
		t.Fatalf("guest eth0 missing %s/24:\n%s", guestIP, addrOutput)
	}
	routeOutput, err := runSerialCommand(ctx, serialPath, "ip route show")
	if err != nil {
		t.Fatalf("read guest routes: %v\noutput:\n%s", err, tailString(routeOutput, 8192))
	}
	if !strings.Contains(routeOutput, "default via "+gatewayIP.String()) {
		t.Fatalf("guest missing default route via %s:\n%s", gatewayIP, routeOutput)
	}

	// Step 1 (hard core): ping 8.8.8.8 by IP — proves NAT + forward + default route.
	pingIPOutput, err := runSerialCommand(ctx, serialPath, "ping -c 3 -w 10 8.8.8.8")
	if err != nil {
		logFirewallDiagnostics(t, context.Background())
		logRouteDiagnostics(t, context.Background(), "8.8.8.8", string(bridgeName))
		t.Fatalf("guest ping 8.8.8.8 command failed: %v\noutput:\n%s", err, tailString(pingIPOutput, 8192))
	}
	if !strings.Contains(pingIPOutput, "0% packet loss") && !guestPingSucceeded(pingIPOutput) {
		logFirewallDiagnostics(t, context.Background())
		logRouteDiagnostics(t, context.Background(), "8.8.8.8", string(bridgeName))
		t.Fatalf("guest could not ping 8.8.8.8 (NAT/forward/default-route path broken):\n%s", pingIPOutput)
	}

	// Step 2: ping a domain — proves DNS option delivery.
	pingDNSOutput, err := runSerialCommand(ctx, serialPath, "ping -c 3 -w 10 one.one.one.one")
	if err != nil {
		t.Fatalf("guest ping domain command failed: %v\noutput:\n%s", err, tailString(pingDNSOutput, 8192))
	}
	if !guestPingSucceeded(pingDNSOutput) {
		t.Fatalf("guest could not ping one.one.one.one (DNS delivery broken):\n%s", pingDNSOutput)
	}
}

// guestPingSucceeded reports whether CirrOS busybox ping output indicates at
// least one received reply.
func guestPingSucceeded(output string) bool {
	return strings.Contains(output, "0% packet loss") ||
		strings.Contains(output, "1 packets received") ||
		strings.Contains(output, "2 packets received") ||
		strings.Contains(output, "3 packets received")
}

// defaultEgressInterface returns the host interface that owns the default route,
// used as the masquerade/forward egress interface.
func defaultEgressInterface(ctx context.Context) (string, error) {
	stdout, _, err := runCommand(ctx, "sh", "-c", "ip -o route show default | awk '{print $5; exit}'")
	if err != nil {
		return "", err
	}
	iface := strings.TrimSpace(string(stdout))
	if iface == "" {
		return "", fmt.Errorf("no default-route egress interface found")
	}
	return iface, nil
}
```

注:`mustParseAcceptanceMAC`、`copyFile`、`runCommand`、`startQEMUCommand`、`tailString` 均在既有 acceptance 包中(`hostnet_dhcp_test.go` / `harness.go` 已使用,确认存在)。本测试 import 需补 `fmt`(用于 `defaultEgressInterface`)。`guestPingSucceeded` 兼容 busybox ping 的多种输出措辞。若 `defaultEgressInterface` 在 Lima guest 内取到的不是预期出网口,在 provision 阶段固定一个已知出网口名并改为常量(实现期实测确认)。

- [ ] **Step 3: 在 `doc.go` 登记 scope**

`test/acceptance/doc.go` 现有 scope 列表(四向 union:bridge/TAP、route、firewall、DHCP)追加 network egress 一项,与既有风格一致(读 `doc.go` 当前文本后,在 scope 列表追加一行描述 `TestNetworkEgressEndToEnd` 验证经网络编排层建网挂卡后 guest 出外网)。

- [ ] **Step 4: 运行验证(macOS 本地先验编译)**

Run: `GOOS=linux GOARCH=arm64 go vet -tags "acceptance linux" ./test/acceptance/`
Expected: PASS(交叉编译验证 acceptance 测试可构建;真实运行在 Lima 内)

- [ ] **Step 5: Lima 全量验收**

Run: `scripts/acceptance.sh full`
Expected: `TestNetworkEgressEndToEnd` 与既有 DHCP/firewall/bridge-TAP/route/qemu-img 全部 PASS

- [ ] **Step 6: 若验收失败,系统化诊断**

诊断输出(测试已内置):nft ruleset、`ip route`/`ip addr`(host)、guest `ip addr`/`ip route`、QEMU argv、serial tail、QEMU stderr。常见失败:
- guest 无 IP → 检查 DHCP Start/ApplyBinding 与 TAP enslave。
- guest 有 IP 无默认路由 → 检查 Router option 是否 enabled、网关地址。
- ping 8.8.8.8 不通 → 检查 masquerade + forward-accept 规则是否生成(`nft list ruleset`)、`ip_forward` 是否为 1(provision 已设)。
- ping IP 通但域名不通 → 检查 DNS option;非阻塞核心链路,Step 2 失败单独定位。
- serial 卡住 → 确认 `ds=none` 抑制了 metadata 探测;必要时延长超时。

按 systematic-debugging 找根因,不做增量打补丁。

- [ ] **Step 7: Commit**

```bash
git add test/acceptance/network_egress_test.go test/acceptance/doc.go
git commit -m "test(acceptance): guest external egress via network orchestration layer"
```

---

## Phase E — 知识库更新

### Task E1: 新增 internal/network/AGENTS.md + 更新根 AGENTS.md + firewall 知识

**Files:**
- Create: `internal/network/AGENTS.md`
- Modify: `AGENTS.md`(根)
- Modify: `internal/hostnet/firewall` 相关 AGENTS 知识(若 firewall 有独立 AGENTS.md 则更新;否则并入根)

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: 知识库反映新增的 `internal/network/` 编排层、forward-accept 原语、死包移除、guest 出外网闭环;所有交叉引用与 `#flow-*` 锚点可解析,无悬空引用。
Acceptance evidence:
- `internal/network/AGENTS.md` 存在,含编排层 OVERVIEW/WHERE TO LOOK/CALL GRAPHS(`#flow-network-ensure`、`#flow-nic-ensure`、`#flow-guest-egress`)。
- 根 `AGENTS.md` 的 STRUCTURE/WHERE TO LOOK/AGENTS TREE/CALL GRAPHS 反映 `internal/network/`,移除 `internal/network/bridge` 条目。
- 人工核对所有新增引用指向真实文件/符号/章节。

- [ ] **Step 2: 创建 `internal/network/AGENTS.md`**

按既有子包 AGENTS.md 结构(参照 `internal/storage/AGENTS.md`)撰写:OVERVIEW(三段式编排层,仿 storage)、WHERE TO LOOK 表(service.go/nic_service.go/netpool/networker)、CONVENTIONS(逻辑意图 vs 现读真实状态、MAC 由控制面透传、Ensure 不主动拆资源、Delete 逆序)、ANTI-PATTERNS(不缓存可漂移真实状态、不在编排层生成 MAC、不改 ip_forward、forward-accept 不改宿主默认策略)、CALL GRAPHS(`#flow-network-ensure`、`#flow-nic-ensure`、`#flow-guest-egress`,引用真实符号行号)。Verified-against 指纹块列出本次涉及的源文件。

- [ ] **Step 3: 更新根 `AGENTS.md`**

- STRUCTURE 树:`internal/network/` 注释从"Linux bridge boundary"改为"VM-facing network orchestration layer";移除 `network/bridge/` 子条目。
- AGENTS TREE:新增 `internal/network/AGENTS.md` 节点。
- WHERE TO LOOK:节点入口行的 `internal/network/bridge` 改为 `internal/network`(编排层);新增网络编排层一行。
- CODE MAP:移除 `bridge.NoopManager.Ensure` 行,新增 `network.NetworkService`/`network.NICService`/`netpool.Service`/`firewall.Manager.EnsureForwardAccept` 行。
- CALL GRAPHS:`flow-govirtlet-boot` 中 `(future) internal/network/bridge` 步骤改为新编排层;新增 `flow-guest-egress` 顶层流(或引用子包 AGENTS)。
- CONVENTIONS/ANTI-PATTERNS:追加 forward-accept 只加 Govirta-owned accept 不改宿主默认策略、网络编排层逻辑意图 vs 现读真实状态、MAC 由控制面提供。
- ACCEPTANCE TESTS:追加 `TestNetworkEgressEndToEnd`。

- [ ] **Step 4: 验证引用完整性**

Run: `grep -rn "network/bridge" AGENTS.md internal/network/AGENTS.md`
Expected: 仅历史/不可避免处;STRUCTURE/CODE MAP/CALL GRAPHS 中的死包引用已清除。

人工核对:新增的每个 `#flow-*` 锚点、文件路径、符号名在对应文件中真实存在。

- [ ] **Step 5: Commit**

```bash
git add AGENTS.md internal/network/AGENTS.md
git commit -m "docs(network): knowledge base for orchestration layer, forward-accept, egress"
```

---

## 实现顺序与依赖

```text
Phase A (firewall forward-accept 原语)
  A1 契约+常量+noop → A2 forward expr → A3 forward 组路径 → A4 switch 接线+manager 方法 → A5 linux 测试
        ↑ A 完成后 firewall.Manager 接口稳定,Phase B 可注入

Phase B (网络编排层)  ← 依赖 A 的接口稳定
  B1 networker 错误 → B2 类型+克隆 → B3 注册核心 → B4 编排 → B5 netpool 测试 → B6 NetworkService → B7 NICService → B8 服务测试

Phase C (死包移除+node 重接)  ← 依赖 B 的 NetworkService/NICService 存在
  C1

Phase D (Lima 出外网验收)  ← 依赖 A+B+C 全部就绪
  D1

Phase E (知识库)  ← 依赖全部实现完成
  E1
```

**关键依赖说明:** Phase A 必须先完成,因为 `netpool.Service` 注入 `firewall.Manager` 接口——该接口在 A1 才长出 `EnsureForwardAccept`/`DeleteForwardAccept`。若 B 先行,`firewall.Manager` 接口不含 forward-accept,编排层无法调用。Phase C 删死包必须在 B 之后(node 要改接新 service)。Phase D 验收需要 A(forward-accept 真实规则)+ B(编排)+ C(可编译的 node,虽然 D 不直接用 node,但 `go build ./...` 需全绿)。

---

## Plan 自审清单(写计划者已执行)

1. **Spec 覆盖:** spec 各节 → 任务映射:§2 架构=B 全部;§3 数据模型=B1/B2;§3.4 注册核心=B3;§3.5 MAC=B4+B7+B5 透传测试;§3.6 错误=B1;§4.1 EnsureNetwork=B4;§4.2 EnsureNIC=B4;§4.4 Ensure 不拆=B5 测试;§4.5 Delete 逆序=B4+B5;§5 forward-accept=A 全部;§6.1 单测=A5+B5+B8;§6.3 Lima=D1;§6.4 两步 ping=D1;§6.5 metadata 延迟=D1 的 ds=none;死包=C1;知识库=E1。无遗漏。
2. **Placeholder 扫描:** 自审中发现并已就地修正两处占位 sentinel(原 `route.ErrNotReadySentinel()`、`dhcp.ErrAlreadyRunningSentinel()` 这两个不存在的函数),改为直接调用已核实存在的真实 sentinel(`routeerr.ErrNotReady`、`dhcperr.ErrAlreadyRunning`),并在编排代码块顶部 import 对应 `routeerr`/`dhcperr` 包。现计划无占位符,所有代码块为可直接落地的真实 API。
3. **类型一致性:** `NetworkDefinition`/`NICDefinition` 字段在 B1/B2 定义,B5 的 `sampleNetwork`/`sampleNIC`、D1 的真实 def 均引用同名字段——若 B1 字段命名微调,B5/D1 helper 需同步(已在各处注明"以 B1 定稿为准")。`netpool.NewService(link,route,firewall,dhcp)` 形参顺序在 B3 定义,B6/B7/C1/D1 调用一致(四原语顺序 link→route→firewall→dhcp)。`firewall.ForwardAcceptSpec`/`ForwardAcceptSummary`/`PriorityNameForwardFilter`/`RulePurposeForwardAccept` 在 A1 定义,A2/A3/A4 一致引用。
