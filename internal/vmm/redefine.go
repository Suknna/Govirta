package vmm

import (
	"context"

	"github.com/rs/zerolog"
)

// Redefine 覆写 vm.json 的配置权威：用新 Spec 替换 persistedState.Spec，据新 Spec
// 确定性重派生 argv 替换 persistedState.Argv，原子落盘。不碰 Intended、不碰进程
// （纯磁盘操作）。下次 Start 自然 exec 新 argv。
//
// 单一职责 = 磁盘配置权威：vmm 不探测进程、不校验运行态——冷态前提（进程已停可
// 安全重建）由调用方（控制器）负责。任何状态都可调用 Redefine（重建无副作用）。
// 幂等：同样的 spec 重复 Redefine 产生同样的 json（argv 由 spec 确定性派生）。
//
// 镜像 Create 的 argv 派生路径（deriveBuilder + injectFacilityFlags），不重写
// argv 构造逻辑（积木式拼接）：
//  1. loadState 加载 vm.json（不存在 → ErrNotFound）。
//  2. deriveBuilder(spec) + injectFacilityFlags 据新 spec 重派生 argv（arch 校验
//     复用，不支持的 arch → ErrInvalidRequest，由控制器作永久失败处理）。
//  3. 覆写 st.Spec/st.Argv，保 st.Intended/st.UUID/st.Paths/st.CreatedAt 不变。
//  4. writeState 原子落盘（自动更新 UpdatedAt）。
//  5. 返回 statusFrom（含 live phase）。
func (s *VMMService) Redefine(ctx context.Context, uuid string, spec SpecSummary) (VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "redefine").Str("vm_id", uuid).Logger()

	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return VM{}, err
	}

	builder, err := deriveBuilder(spec, s.env)
	if err != nil {
		return VM{}, err
	}
	argv, err := injectFacilityFlags(builder, st.Paths)
	if err != nil {
		return VM{}, err
	}

	// 覆写配置权威 + 重派生 argv；身份/路径/意图/创建时间不变（UpdatedAt 由 writeState 更新）。
	st.Spec = spec
	st.Argv = argv
	if err := s.writeState(ctx, st); err != nil {
		return VM{}, err
	}
	log.Info().Str("outcome", "success").Msg("vm redefined")
	return s.statusFrom(ctx, st)
}
