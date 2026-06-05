package vmm

import "testing"

// TestObservedPhaseDerivation 穷举 spec §4 状态派生表的全部行 + Defined 特例
// + 冲突仲裁两条（live 赢）。这是上下一致铁律在 VM 层的回归保护。
func TestObservedPhaseDerivation(t *testing.T) {
	cases := []struct {
		name     string
		intended IntendedPhase
		probe    liveProbe
		want     Phase
	}{
		// spec §4 表的六行：
		{
			name:     "defined_no_process",
			intended: IntendedDefined,
			probe:    liveProbe{processAlive: false},
			want:     PhaseDefined,
		},
		{
			name:     "running_process_qmp_running",
			intended: IntendedRunning,
			probe:    liveProbe{processAlive: true, qmpReachable: true, qmpRunning: true},
			want:     PhaseRunning,
		},
		{
			name:     "running_process_qmp_not_ready",
			intended: IntendedRunning,
			probe:    liveProbe{processAlive: true, qmpReachable: false, qmpRunning: false},
			want:     PhaseStarting,
		},
		{
			name:     "running_process_dead",
			intended: IntendedRunning,
			probe:    liveProbe{processAlive: false},
			want:     PhaseFailed,
		},
		{
			name:     "stopped_process_alive",
			intended: IntendedStopped,
			probe:    liveProbe{processAlive: true, qmpReachable: true, qmpRunning: true},
			want:     PhaseStopping,
		},
		{
			name:     "stopped_process_dead",
			intended: IntendedStopped,
			probe:    liveProbe{processAlive: false},
			want:     PhaseStopped,
		},

		// 冲突仲裁（live 赢）：
		{
			// intent=running 但进程没了 → 绝不报 Running，报 Failed。
			name:     "conflict_intended_running_but_process_gone",
			intended: IntendedRunning,
			probe:    liveProbe{processAlive: false},
			want:     PhaseFailed,
		},
		{
			// intent=stopped（刚发 powerdown）但进程已消失 → 直接 Stopped。
			name:     "conflict_intended_stopped_process_gone",
			intended: IntendedStopped,
			probe:    liveProbe{processAlive: false},
			want:     PhaseStopped,
		},

		// Defined 特例的对照：intent=defined 但进程意外存活 → live 赢，报 Starting。
		{
			name:     "defined_but_process_alive",
			intended: IntendedDefined,
			probe:    liveProbe{processAlive: true, qmpReachable: false, qmpRunning: false},
			want:     PhaseStarting,
		},
		{
			// QMP reachable 但未 running（如 paused）仍归 Starting（只有 running 才 Running）。
			name:     "running_qmp_reachable_not_running",
			intended: IntendedRunning,
			probe:    liveProbe{processAlive: true, qmpReachable: true, qmpRunning: false},
			want:     PhaseStarting,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := observedPhase(tc.intended, tc.probe)
			if got != tc.want {
				t.Fatalf("observedPhase(%q, %+v) = %q, want %q", tc.intended, tc.probe, got, tc.want)
			}
		})
	}
}
