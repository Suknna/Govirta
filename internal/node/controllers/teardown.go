package controllers

// teardown.go 提供 finalizer 两阶段删除模型里「控制器无关」的共享物，供 6 个
// 下层控制器（VM/Volume/Image/StoragePool/Network/NIC）在接拆除分支时复用：
//
//   - isDeleting：判断对象是否带 deletionTimestamp（删除中）。
//   - FinalizerRemover：控制器对 master 摘除 finalizer 的窄依赖（最小接口，
//     便于测试注入 fake）。
//   - removeTeardownFinalizer：live 资源拆净后摘除 node-teardown finalizer。
//
// 删除模型回顾：apiserver DELETE 只打 deletionTimestamp（对象不消失）；node 控制器
// watch 到带删除戳的对象后拆除 live 资源，拆净后摘除 govirta.io/node-teardown
// finalizer；apiserver 见 finalizers 清空才真正 store.Delete。本文件不实现任何
// 具体资源的拆除逻辑——那是各控制器（Task 9）的事。
//
// ============================================================================
// 下层 Delete* 真实幂等行为核实（memory 799 铁律：先读下层源码再据此设计容错）
// ============================================================================
//
// 结论先行：6 个下层 Delete* 在删「不存在/已删」资源时，没有一个是「静默幂等
// 成功（返回 nil）」——全部返回一个 sentinel 错误。因此 Task 9 的每个控制器在拆除
// 分支里都必须对相应的 NotFound sentinel 做容错（errors.Is 命中则视为已拆净、继续
// 摘 finalizer），否则会卡在「拆已不存在的资源 → 报错 → 永不摘 finalizer」死循环。
//
//  1. vmm.VMMService.Delete(ctx, uuid)  —— internal/vmm/{service.go,store.go}
//     缺失（vm.json 不存在）：返回 vmm.ErrNotFound（loadState 将
//     proc.ErrStateNotFound 映射为 ErrNotFound；service_test.go:170 已断言）。
//     另：进程仍 alive 时返回 vmm.ErrConflict（不可删运行中的 VM）。
//     → 非幂等，Task 9 需对 vmm.ErrNotFound 容错。
//
//  2. storage.VolumeService.DeleteVolume(ctx, req)  —— internal/storage/{service.go,pool/service.go}
//     透传到 pool.Service.DeleteVolume。池不存在：pool.ErrPoolNotFound；
//     卷不存在：volume.ErrVolumeNotFound；卷仍 attached：volume.ErrVolumeInUse。
//     → 非幂等，Task 9 需对 volume.ErrVolumeNotFound（及池已删时 pool.ErrPoolNotFound）容错。
//
//  3. storage.ImageService.DeleteImage(ctx, req)  —— internal/storage/{image_service.go,pool/service.go}
//     透传到 pool.Service.DeleteImage。池不存在：pool.ErrPoolNotFound；
//     镜像不存在或非 Ready 态：image.ErrImageNotFound（memory 357：无引用计数，
//     删除是按 ID 直接拆，不存在即 NotFound）。
//     → 非幂等，Task 9 需对 image.ErrImageNotFound（及 pool.ErrPoolNotFound）容错。
//
//  4. pool.Service.UnregisterPool(name)  —— internal/storage/pool/service.go
//     已注销：pool.ErrPoolNotFound；仍有卷/镜像：pool.ErrPoolNotEmpty。
//     → 非幂等，Task 9 需对 pool.ErrPoolNotFound 容错（ErrPoolNotEmpty 是真冲突，
//     应保留 finalizer 并 requeue，等下层卷/镜像先删）。
//
//  5. network.NetworkService.DeleteNetwork(ctx, name)  —— internal/network/{service.go,netpool/orchestrate.go}
//     透传到 netpool.Service.DeleteNetwork。网络未注册：getRecord 返回
//     networker.ErrNotFound；仍有已注册 NIC：networker.ErrConflict。
//     注意：内部 masquerade/forward-accept firewall rule-ref 已自解析，rule 已不存在
//     （networker.ErrNotFound）在内部被跳过——故 firewall/dhcp 层是幂等的，但
//     「网络注册项本身不存在」这一层不是。
//     → 顶层非幂等，Task 9 需对 networker.ErrNotFound 容错（ErrConflict 是真冲突，
//     保留 finalizer 并 requeue，等 NIC 先删）。
//
//  6. network.NICService.DeleteNIC(ctx, networkName, vmID)  —— internal/network/{nic_service.go,netpool/orchestrate.go}
//     透传到 netpool.Service.DeleteNIC。网络未注册或该 vmID 的 NIC 不存在：
//     networker.ErrNotFound。内部 anti-spoofing rule-ref 已自解析并对 rule 已不存在
//     （networker.ErrNotFound）幂等跳过——firewall/dhcp/link 层幂等，但
//     「NIC 注册项本身不存在」这一层不是。
//     → 顶层非幂等，Task 9 需对 networker.ErrNotFound 容错。

import (
	"context"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// isDeleting 报告对象是否处于删除中（带 deletionTimestamp）。
//
// 为什么以 DeletionTimestamp 非空为判据：finalizer 两阶段删除里 apiserver 的 DELETE
// 只打删除戳、对象不消失，因此控制器靠这个戳而非「对象消失」来识别删除意图，再据此
// 进入拆除分支而非 reconcile-to-ready 分支。
func isDeleting(meta metav1.ObjectMeta) bool { return meta.DeletionTimestamp != "" }

// FinalizerRemover 是控制器对 master 摘除 finalizer 的窄依赖（最小接口，便于测试
// 注入 fake）。*client.Client 满足它——见 teardown_test.go 的编译期断言，以及各控制器
// 已对 *client.Client 持有此能力（client.RemoveFinalizer）。
type FinalizerRemover interface {
	RemoveFinalizer(ctx context.Context, kind, name, finalizer string) error
}

// removeTeardownFinalizer 在 live 资源拆净后摘除 node-teardown finalizer，让 apiserver
// 得以真正删除对象。finalizer 名取自强类型常量 metav1.FinalizerNodeTeardown（转 string
// 传给 client，禁止 bare string 字面量），错误原样向上传播由调用方决定 requeue。
func removeTeardownFinalizer(ctx context.Context, r FinalizerRemover, kind, name string) error {
	return r.RemoveFinalizer(ctx, kind, name, string(metav1.FinalizerNodeTeardown))
}
