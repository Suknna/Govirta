package scheduler

import (
	"context"
	"errors"
	"testing"

	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// TestNoopSchedulerSchedule 表驱动验证 NoopScheduler 的三条契约行为：有 node 时
// 绑定第一个、空列表返回 ErrNoNodes、ctx 取消返回 context.Canceled。
func TestNoopSchedulerSchedule(t *testing.T) {
	t.Parallel()

	// canceledCtx 是一个已取消的上下文，用于断言取消优先于一切决策。
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name      string
		ctx       context.Context
		nodeNames []string
		wantNode  string
		wantErr   error
	}{
		{
			name:      "binds first node when nodes available",
			ctx:       context.Background(),
			nodeNames: []string{"node-a", "node-b"},
			wantNode:  "node-a",
			wantErr:   nil,
		},
		{
			name:      "returns ErrNoNodes on empty node list",
			ctx:       context.Background(),
			nodeNames: nil,
			wantNode:  "",
			wantErr:   ErrNoNodes,
		},
		{
			name:      "returns ctx error when context canceled",
			ctx:       canceledCtx,
			nodeNames: []string{"node-a"},
			wantNode:  "",
			wantErr:   context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := NewNoopScheduler()
			got, err := s.Schedule(tt.ctx, vmv1.VM{}, tt.nodeNames)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Schedule() error = %v, want errors.Is(_, %v)", err, tt.wantErr)
				}
				if got != "" {
					t.Fatalf("Schedule() node = %q, want empty on error", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("Schedule() error = %v, want nil", err)
			}
			if got != tt.wantNode {
				t.Fatalf("Schedule() node = %q, want %q", got, tt.wantNode)
			}
		})
	}
}

// 确保 NoopScheduler 满足 Scheduler 接口；接口漂移会在编译期被此断言捕获。
var _ Scheduler = (*NoopScheduler)(nil)
