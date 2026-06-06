// Package scheduler 决定 VM 落在哪个 node 上。它是控制面放置策略的边界：
// 输入一个 VM 对象和当前可用的 node 名列表，输出要写入 VM.Spec.NodeName 的
// node 名。当前实现是 noop 策略（恒选第一个 node），后续可在不改接口的前提下
// 替换为真实的容量/亲和性调度。
//
// Package scheduler decides which node a VM is placed on. It is the control-plane
// placement boundary: given a VM object and the currently available node names,
// it returns the node name to write into VM.Spec.NodeName.
package scheduler

import (
	"context"
	"errors"

	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// ErrNoNodes 表示没有任何可用 node 供放置 VM。调用方应区分该错误与上下文取消，
// 通常意味着需要等待 node 注册或扩容，而非立即失败。
//
// ErrNoNodes indicates no node is available to place the VM on.
var ErrNoNodes = errors.New("scheduler: no nodes available")

// Scheduler 通过返回要写入 VM.Spec.NodeName 的 node 名来决定 VM 的放置位置。
// 调用契约：
//   - ctx 已取消时返回 ctx.Err()（不做任何放置决策）；
//   - nodeNames 为空时返回 ErrNoNodes，调用方用 errors.Is 判定；
//   - 否则返回一个属于 nodeNames 的 node 名。
//
// Scheduler decides which node a VM is placed on by returning the node name to
// write into VM.Spec.NodeName.
type Scheduler interface {
	Schedule(ctx context.Context, vm vmv1.VM, nodeNames []string) (string, error)
}

// NoopScheduler 是不做任何策略的放置器：忽略 VM 规格与 node 容量，恒选列表中的
// 第一个 node。它是 fast-iteration 阶段的第一刀实现，用于打通控制面流程。
//
// NoopScheduler returns the first available node without applying any policy.
type NoopScheduler struct{}

// NewNoopScheduler 构造一个 NoopScheduler。返回指针以便后续在不破坏调用方的前提下
// 为其增加内部状态（如容量缓存）。
//
// NewNoopScheduler constructs a NoopScheduler.
func NewNoopScheduler() *NoopScheduler {
	return &NoopScheduler{}
}

// Schedule 按 Scheduler 契约执行：先检查 ctx 取消，再对空 node 列表返回 ErrNoNodes，
// 否则返回第一个 node 名。vm 在 noop 策略下不参与决策，但保留在签名中以满足接口、
// 并让未来的真实策略能基于 VM 规格做放置。
//
// Schedule checks context cancellation, returns ErrNoNodes on an empty node list,
// and otherwise returns the first node name.
func (s *NoopScheduler) Schedule(ctx context.Context, vm vmv1.VM, nodeNames []string) (string, error) {
	// 先于任何决策检查取消，避免在调用方已放弃时浪费工作或返回误导性结果。
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// vm 在 noop 策略下不影响放置；显式忽略以表明这是有意为之而非遗漏。
	_ = vm

	if len(nodeNames) == 0 {
		return "", ErrNoNodes
	}

	return nodeNames[0], nil
}
