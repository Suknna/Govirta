# Govirta 路线图说明

本目录不再维护按阶段拆分的目标文档。里程碑明细不写入 `README.md` 或根 `AGENTS.md`。

## 文档分工

- `docs/superpowers/specs/`:保存设计说明和技术决策。
- `docs/superpowers/plans/`:保存可执行计划和验收步骤。
- `docs/roadmap/`:仅保留路线图维护说明，不存放 `cycle-*.md` 里程碑文档。

## 约束

- **不接入 libvirt**:Govirta 的虚拟化栈永久只走 QEMU + QMP + qemu-img + netlink。
- 不在本目录重新引入按阶段拆分的目标文档；需要规划时写入 specs/plans。
