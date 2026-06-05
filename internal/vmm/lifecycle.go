package vmm

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"
)

// Start exec vm.json 里存好的 daemonized argv，等待 QMP 就绪，落 intent=Running。
// 幂等：已有活进程则直接返回 live 态，不重复 spawn（防双进程）。
func (s *VMMService) Start(ctx context.Context, uuid string) (VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "start").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return VM{}, err
	}
	alive, err := s.proc.ProcessAlive(ctx, st.Paths.PidFile)
	if err != nil {
		return VM{}, fmt.Errorf("vmm: probe process for start %s: %w", uuid, err)
	}
	if alive {
		// 已有活进程：幂等返回 live 态，不重复 spawn。
		return s.statusFrom(ctx, st)
	}

	if err := s.proc.SpawnDaemonized(ctx, st.Argv, st.Paths.Dir); err != nil {
		// spawn 失败：意图仍记 Running，下次探测会派生 Failed（live 赢）。
		st.Intended = IntendedRunning
		if werr := s.writeState(ctx, st); werr != nil {
			return VM{}, errors.Join(fmt.Errorf("vmm: spawn %s: %w", uuid, err), werr)
		}
		return VM{}, fmt.Errorf("vmm: spawn %s: %w", uuid, err)
	}
	st.Intended = IntendedRunning
	if err := s.writeState(ctx, st); err != nil {
		return VM{}, err
	}

	// 等待 QMP 就绪（瞬时连接，用完即断）。失败不阻断：Phase 由 live 探测派生。
	if err := s.waitQMPReady(ctx, st.Paths.QMPSocket); err != nil {
		log.Warn().Err(err).Msg("qmp not ready after spawn; phase will derive from live probe")
	}
	log.Info().Str("outcome", "success").Msg("vm started")
	return s.statusFrom(ctx, st)
}

// Stop 优雅停止：QMP system_powerdown，落 intent=Stopped。
func (s *VMMService) Stop(ctx context.Context, uuid string) error {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "stop").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return err
	}
	client, err := s.qmpFactory(st.Paths.QMPSocket)
	if err != nil {
		return fmt.Errorf("vmm: qmp client for stop %s: %w", uuid, err)
	}
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("vmm: qmp connect for stop %s: %w", uuid, err)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	if err := client.SystemPowerdown(ctx); err != nil {
		return fmt.Errorf("vmm: powerdown %s: %w", uuid, err)
	}
	st.Intended = IntendedStopped
	if err := s.writeState(ctx, st); err != nil {
		return err
	}
	log.Info().Str("outcome", "success").Msg("vm powerdown requested")
	return nil
}

// Kill 强制停止：先试 QMP quit，不可达则 ProcessController SIGKILL 兜底。
// 两者都失败用 errors.Join 合并保全（spec §10）。intent→Stopped。
func (s *VMMService) Kill(ctx context.Context, uuid string) error {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "kill").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return err
	}
	qmpErr := s.tryQMPQuit(ctx, st.Paths.QMPSocket)
	if qmpErr != nil {
		// QMP 不可达：SIGKILL 兜底。
		if kerr := s.proc.ForceKill(ctx, st.Paths.PidFile); kerr != nil {
			return errors.Join(
				fmt.Errorf("vmm: qmp quit %s: %w", uuid, qmpErr),
				fmt.Errorf("vmm: sigkill %s: %w", uuid, kerr),
			)
		}
		log.Warn().Err(qmpErr).Msg("qmp quit failed; used SIGKILL fallback")
	}
	st.Intended = IntendedStopped
	if err := s.writeState(ctx, st); err != nil {
		return err
	}
	log.Info().Str("outcome", "success").Msg("vm killed")
	return nil
}

// tryQMPQuit 用瞬时 client 尝试 QMP quit；连接或命令失败返回 error（由 Kill 兜底）。
func (s *VMMService) tryQMPQuit(ctx context.Context, socketPath string) error {
	client, err := s.qmpFactory(socketPath)
	if err != nil {
		return err
	}
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = client.Disconnect(ctx) }()
	return client.Quit(ctx)
}
