package controllers

import (
	"context"
	"errors"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"

	"github.com/suknna/govirta/internal/node/client"
)

// 编译期断言：真实生产类型 *client.Client 满足控制器依赖的窄接口 FinalizerRemover
// （积木式 + 可测）。若 client.RemoveFinalizer 的签名漂移，这里会编译失败。
var _ FinalizerRemover = (*client.Client)(nil)

// TestTeardownIsDeleting 断言 isDeleting 严格以 DeletionTimestamp 非空为判据：
// 空 → 未删（false），非空 → 删除中（true）。
func TestTeardownIsDeleting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta metav1.ObjectMeta
		want bool
	}{
		{
			name: "empty deletion timestamp is not deleting",
			meta: metav1.ObjectMeta{Name: "vm-1"},
			want: false,
		},
		{
			name: "non-empty deletion timestamp is deleting",
			meta: metav1.ObjectMeta{Name: "vm-1", DeletionTimestamp: "2026-01-02T15:04:05Z"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isDeleting(tt.meta); got != tt.want {
				t.Fatalf("isDeleting(%+v) = %v, want %v", tt.meta, got, tt.want)
			}
		})
	}
}

// fakeFinalizerRemover 记录 RemoveFinalizer 的真实调用参数并可注入返回错误，
// 用于验真实行为（remover 被真正调用且参数正确），而非断言 mock 存在。
type fakeFinalizerRemover struct {
	called       bool
	gotKind      string
	gotName      string
	gotFinalizer string
	returnErr    error
}

func (f *fakeFinalizerRemover) RemoveFinalizer(ctx context.Context, kind, name, finalizer string) error {
	f.called = true
	f.gotKind = kind
	f.gotName = name
	f.gotFinalizer = finalizer
	return f.returnErr
}

// TestTeardownRemoveTeardownFinalizer 断言 removeTeardownFinalizer 把摘除请求透传到
// 注入的 FinalizerRemover，且传入的 finalizer 恰为强类型常量对应的字符串
// "govirta.io/node-teardown"；并断言 remover 返回的错误被原样向上传播。
func TestTeardownRemoveTeardownFinalizer(t *testing.T) {
	t.Parallel()

	t.Run("passes through kind/name and the node-teardown finalizer", func(t *testing.T) {
		t.Parallel()
		fake := &fakeFinalizerRemover{}

		if err := removeTeardownFinalizer(context.Background(), fake, "VirtualMachine", "vm-1"); err != nil {
			t.Fatalf("removeTeardownFinalizer() error = %v, want nil", err)
		}

		if !fake.called {
			t.Fatal("removeTeardownFinalizer did not call the remover")
		}
		if fake.gotKind != "VirtualMachine" {
			t.Fatalf("kind = %q, want %q", fake.gotKind, "VirtualMachine")
		}
		if fake.gotName != "vm-1" {
			t.Fatalf("name = %q, want %q", fake.gotName, "vm-1")
		}
		// finalizer 必须等于强类型常量转成的字符串，验证 removeTeardownFinalizer
		// 没有传成别的 bare string。
		wantFinalizer := string(metav1.FinalizerNodeTeardown)
		if fake.gotFinalizer != wantFinalizer {
			t.Fatalf("finalizer = %q, want %q", fake.gotFinalizer, wantFinalizer)
		}
		if fake.gotFinalizer != "govirta.io/node-teardown" {
			t.Fatalf("finalizer = %q, want literal %q", fake.gotFinalizer, "govirta.io/node-teardown")
		}
	})

	t.Run("propagates the remover error", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("remove finalizer failed")
		fake := &fakeFinalizerRemover{returnErr: sentinel}

		err := removeTeardownFinalizer(context.Background(), fake, "VirtualMachine", "vm-1")
		if !errors.Is(err, sentinel) {
			t.Fatalf("removeTeardownFinalizer() error = %v, want errors.Is(err, sentinel)", err)
		}
		if !fake.called {
			t.Fatal("removeTeardownFinalizer did not call the remover")
		}
	})
}
