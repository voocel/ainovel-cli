# 文档索引

本目录用于存放项目内部设计、运行机制与排障文档。

## 当前文档

- [上下文管理说明](./context-management.md)
  说明项目当前的上下文管理体系，包括：
  - `agentcore` 上下文策略管线
  - Writer 的 `store_summary` 压缩
  - `writerRestorePack`
  - `novel_context`
  - `ContextProfile` / `MemoryPolicy`
  - handoff / recovery
  - 可观测性与排障入口

## 建议后续补充

- `runtime-and-recovery.md`
  聚焦运行时任务流、恢复链路和宿主调度。

- `writing-pipeline.md`
  聚焦 Architect / Writer / Editor 三个角色的协作流程。

- `diagnostics.md`
  聚焦 `diag` 规则体系、证据来源和常见问题定位方式。

