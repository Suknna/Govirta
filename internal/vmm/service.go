package vmm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/vmm/proc"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

// QMPFactory 按 socket 路径构造一个 qmp.Client（按需、瞬时；不长期持有）。
// 注入此工厂使单测可用 fake qmp.Client，生产用真实 SocketClient——贴合
// 「QEMU 进程生命周期与编排器解耦」：qmp 连接是瞬时可重连的，不是 guest
// 存活的依赖（spec 硬约束 1）。
type QMPFactory func(socketPath string) (qmp.Client, error)

// VMMService 是节点本地 QEMU 进程生命周期领域服务（spec §3）。
//
// 它不长期持有 qmp.Client，也不缓存运行时态：运行时态永远从 live 探测
// （进程存活 + QMP query-status）派生（spec §4 上下一致）。持久化只承载
// 身份/spec/路径/意图（desired 维度），不承载运行态权威。
type VMMService struct {
	runtimeRoot string
	proc        proc.ProcessController
	qmpFactory  QMPFactory
}

// NewVMMService 构造服务。runtimeRoot 通常是 /var/lib/govirtlet。
// 三个依赖全部必填（显式铁律：不为调用方推断默认值）。
func NewVMMService(runtimeRoot string, pc proc.ProcessController, qf QMPFactory) (*VMMService, error) {
	if runtimeRoot == "" {
		return nil, fmt.Errorf("%w: runtime root is required", ErrInvalidRequest)
	}
	if pc == nil {
		return nil, fmt.Errorf("%w: process controller is required", ErrInvalidRequest)
	}
	if qf == nil {
		return nil, fmt.Errorf("%w: qmp factory is required", ErrInvalidRequest)
	}
	return &VMMService{runtimeRoot: runtimeRoot, proc: pc, qmpFactory: qf}, nil
}

// Create 渲染 facility-injected argv 并落盘 vm.json，不 spawn。intent=Defined。
// 重复 uuid（vm.json 已存在）返回 ErrAlreadyExists（不覆盖，与 storage image 同构）。
func (s *VMMService) Create(ctx context.Context, req CreateRequest) (VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "create").Str("vm_id", req.UUID).Logger()
	if req.UUID == "" {
		return VM{}, fmt.Errorf("%w: uuid is required", ErrInvalidRequest)
	}
	if req.Builder == nil {
		return VM{}, fmt.Errorf("%w: builder is required", ErrInvalidRequest)
	}
	paths := runtimePathsFor(s.runtimeRoot, req.UUID)

	// 重复检测：vm.json 已存在即拒绝（不覆盖）。
	if _, err := s.proc.ReadState(ctx, paths.StateFile); err == nil {
		return VM{}, fmt.Errorf("%w: %s", ErrAlreadyExists, req.UUID)
	} else if !errors.Is(err, proc.ErrStateNotFound) {
		return VM{}, fmt.Errorf("vmm: probe existing state for %s: %w", req.UUID, err)
	}

	argv, err := injectFacilityFlags(req.Builder, paths)
	if err != nil {
		return VM{}, err
	}
	now := time.Now().UTC()
	st := persistedState{
		UUID:      req.UUID,
		Spec:      req.Spec,
		Paths:     paths,
		Argv:      argv,
		Intended:  IntendedDefined,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.writeState(ctx, st); err != nil {
		return VM{}, err
	}
	log.Info().Str("outcome", "success").Msg("vm created")
	return VM{UUID: req.UUID, Spec: req.Spec, Paths: paths, Intended: IntendedDefined, Phase: PhaseDefined}, nil
}

// Delete 删除逻辑定义 + 整个运行时目录。要求无 live 进程，否则 ErrConflict。
func (s *VMMService) Delete(ctx context.Context, uuid string) error {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "delete").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return err
	}
	alive, err := s.proc.ProcessAlive(ctx, st.Paths.PidFile)
	if err != nil {
		return fmt.Errorf("vmm: probe process for delete %s: %w", uuid, err)
	}
	if alive {
		return fmt.Errorf("%w: cannot delete running vm %s", ErrConflict, uuid)
	}
	if err := s.proc.RemoveState(ctx, st.Paths.Dir); err != nil {
		return fmt.Errorf("vmm: remove runtime dir %s: %w", uuid, err)
	}
	log.Info().Str("outcome", "success").Msg("vm deleted")
	return nil
}
