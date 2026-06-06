package vmm

import (
	"context"
	"fmt"
	"sort"

	"github.com/rs/zerolog"
)

// probe 对一台 VM 做一次 live 探测（进程存活 + QMP 可达/running）。
// QMP 连不上不是错误（→ Starting）：进程活但 QMP 未就绪是合法过渡态。
func (s *VMMService) probe(ctx context.Context, st persistedState) (liveProbe, error) {
	alive, err := s.proc.ProcessAlive(ctx, st.Paths.PidFile)
	if err != nil {
		return liveProbe{}, fmt.Errorf("vmm: probe process %s: %w", st.UUID, err)
	}
	p := liveProbe{processAlive: alive}
	if !alive {
		return p, nil
	}
	// 进程活：尝试瞬时 QMP query-status。连不上 → Starting，非错误。
	client, err := s.qmpFactory(st.Paths.QMPSocket)
	if err != nil {
		return p, nil
	}
	if cerr := client.Connect(ctx); cerr != nil {
		return p, nil
	}
	defer func() { _ = client.Disconnect(ctx) }()
	p.qmpReachable = true
	status, qerr := client.QueryStatus(ctx)
	if qerr != nil {
		return p, nil
	}
	p.qmpRunning = status.Running
	return p, nil
}

// statusFrom 从 persistedState live 派生 VM 视图。
func (s *VMMService) statusFrom(ctx context.Context, st persistedState) (VM, error) {
	p, err := s.probe(ctx, st)
	if err != nil {
		return VM{}, err
	}
	return VM{
		UUID:     st.UUID,
		Spec:     st.Spec,
		Paths:    st.Paths,
		Intended: st.Intended,
		Phase:    observedPhase(st.Intended, p),
	}, nil
}

// Status 单台 live 探测。
func (s *VMMService) Status(ctx context.Context, uuid string) (VM, error) {
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return VM{}, err
	}
	return s.statusFrom(ctx, st)
}

// Discover 扫 runtimeRoot 读所有 vm.json + 逐个 live 验证（kubelet CRI-list 角色）。
// 结构上无「自动 start」路径：死进程派生 Failed/Stopped，绝不在此拉起（spec §8 防脑裂）。
func (s *VMMService) Discover(ctx context.Context) ([]VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "discover").Logger()
	dirs, err := s.proc.ListStateDirs(ctx, s.runtimeRoot)
	if err != nil {
		return nil, fmt.Errorf("vmm: list runtime dirs: %w", err)
	}
	vms := make([]VM, 0, len(dirs))
	for _, uuid := range dirs {
		st, lerr := s.loadState(ctx, uuid)
		if lerr != nil {
			// 损坏/无 vm.json 的目录：记录并跳过，不让单个坏目录污染全量发现。
			log.Warn().Err(lerr).Str("vm_id", uuid).Msg("skip undiscoverable runtime dir")
			continue
		}
		vm, serr := s.statusFrom(ctx, st)
		if serr != nil {
			log.Warn().Err(serr).Str("vm_id", uuid).Msg("skip vm with probe error")
			continue
		}
		vms = append(vms, vm)
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].UUID < vms[j].UUID })
	return vms, nil
}

// List 等价 Discover（当前无独立内存索引；单一事实源是磁盘 vm.json + live 探测）。
func (s *VMMService) List(ctx context.Context) ([]VM, error) {
	return s.Discover(ctx)
}

// Reattach 对给定 uuid 验证 live 进程并重建 QMP 连接（只接管活进程，spec §8）。
// 进程不存在返回 ErrNotReady——绝不在此 spawn（防止重启拉起已迁走的 VM 导致脑裂）。
func (s *VMMService) Reattach(ctx context.Context, uuid string) (VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "reattach").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return VM{}, err
	}
	alive, err := s.proc.ProcessAlive(ctx, st.Paths.PidFile)
	if err != nil {
		return VM{}, fmt.Errorf("vmm: probe process for reattach %s: %w", uuid, err)
	}
	if !alive {
		// 进程已死：不拉起，上报 ErrNotReady，由上层/控制面裁决（spec §8 问题2）。
		return VM{}, fmt.Errorf("%w: vm %s process not alive, will not auto-start", ErrNotReady, uuid)
	}
	vm, err := s.statusFrom(ctx, st)
	if err != nil {
		return VM{}, err
	}
	log.Info().Str("outcome", "success").Str("phase", string(vm.Phase)).Msg("vm reattached")
	return vm, nil
}

// waitQMPReady 用瞬时 client 等待 QMP 就绪（Start 内部用）。
func (s *VMMService) waitQMPReady(ctx context.Context, socketPath string) error {
	client, err := s.qmpFactory(socketPath)
	if err != nil {
		return err
	}
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = client.Disconnect(ctx) }()
	return client.WaitReady(ctx)
}
