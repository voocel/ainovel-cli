// Package store 提供基于文件系统的持久化存储。
//
// 架构：1 个 IO 基座 + 8 个子存储 + 1 个组合根。
// 每个子存储持有独立的 IO 实例和独立的 sync.RWMutex。
// 主要领域（Progress、Outline、Drafts、Summaries 等）的读写互不阻塞；
// WorldStore 将多个低频小领域合并共享一把锁，如需独立并发可进一步拆分。
//
// 组合根 Store 持有所有子存储的引用，并负责跨域原子操作
// （ExpandArc、AppendVolume、ClearHandledSteer）。
//
// 子存储划分：
//   - ProgressStore: 进度主状态（meta/progress.json）
//   - OutlineStore: 前提、大纲（扁平/分层）、指南针
//   - DraftStore: 章节构思、草稿、终稿
//   - SummaryStore: 章/弧/卷摘要
//   - RunMetaStore: 运行元数据（模型、干预历史）
//   - SignalStore: 一次性信号文件（commit/review 结果）
//   - CharacterStore: 角色档案、状态快照
//   - WorldStore: 时间线、伏笔、关系、状态变化、世界规则、风格规则、审阅、交接
package store
