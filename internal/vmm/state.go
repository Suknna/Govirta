package vmm

// liveProbe 是一次 live 探测的物理事实输入（spec §4 的两条物理信号）。
type liveProbe struct {
	processAlive bool // pidfile → signal 0 探测进程是否存活
	qmpReachable bool // QMP socket 是否连得上（区分 Starting vs Running）
	qmpRunning   bool // 仅当 processAlive 且 QMP 可连且 query-status=running 时 true
}

// derivePhase 把持久意图 + live 探测派生为对外观测 Phase。
//
// 核心铁律（spec §4 上下一致）：live 永远是物理事实权威；意图只在 live 信号
// 相同的态之间消歧；冲突时 live 赢。本函数不产生 PhaseDefined——那是
// observedPhase 叠加的特例（区分「从未启动」与「启动后已停」）。
func derivePhase(intended IntendedPhase, probe liveProbe) Phase {
	if !probe.processAlive {
		// 进程死：物理事实压倒一切意图。
		if intended == IntendedRunning {
			return PhaseFailed // 意图运行但进程没了 = 异常退出 / spawn 失败
		}
		return PhaseStopped // intent=stopped/defined 且进程死 → 已停
	}
	// 进程活：
	switch intended {
	case IntendedStopped:
		return PhaseStopping // 已发 powerdown 但进程未退
	case IntendedRunning:
		if probe.qmpRunning {
			return PhaseRunning
		}
		return PhaseStarting // 进程活但 QMP 未就绪/未 running
	default: // IntendedDefined：理论上不该有活进程，但 live 赢 → 报 Starting
		return PhaseStarting
	}
}

// observedPhase 在 derivePhase 之上叠加 Defined 特例：intent=defined 且进程不存在
// 时报 Defined（而非 Stopped），区分「从未启动」与「启动后已停」。
func observedPhase(intended IntendedPhase, probe liveProbe) Phase {
	if intended == IntendedDefined && !probe.processAlive {
		return PhaseDefined
	}
	return derivePhase(intended, probe)
}
