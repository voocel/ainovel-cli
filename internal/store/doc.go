// Package store 提供基于Tập tin系统的持久化存储。
//
// 架构：1 个 IO 基座 + 多个子存储 + 1 个组合根。
// 每个子存储持有独立的 IO 实例和独立的 sync.RWMutex。
// 主要领域（Progress、Outline、Drafts、Summaries 等）的读写互不阻塞；
// WorldStore 将多个低频小领域合并共享一把锁。
//
// 组合根 Store 持有所有子存储的引用，并负责跨域原子操作
// （ExpandArc、AppendVolume、ClearHandledSteer）。
//
// 子存储划分：
//   - ProgressStore: Tiến độ主Trạng thái（meta/progress.json）
//   - OutlineStore: 前提、Đại cương（扁平/分层）、指南针
//   - DraftStore: Chương构思、Bản nháp、终稿
//   - SummaryStore: 章/弧/卷Tóm tắt
//   - RunMetaStore: 运行元数据（Mô hình、干预Lịch sử）
//   - SignalStore: 一次性信号Tập tin（PendingCommit Phục hồi）
//   - CheckpointStore: step 级 checkpoint（meta/checkpoints.jsonl）
//   - RuntimeStore: 运行时事件队列（meta/runtime/*.jsonl）
//   - CharacterStore: 角色档案、Trạng tháiChụp
//   - WorldStore: 时间线、伏笔、关系、Thay đổi trạng thái、世界规则、风格规则、审阅
package store
