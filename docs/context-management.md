# 上下文管理说明

本文说明 `ainovel-cli` 当前的上下文管理体系：

- 为什么要做上下文管理
- 谁负责"何时压"、谁负责"怎么压"
- 一次压缩的完整流程与每一步的取舍
- 恢复包（restore pack）解决什么问题
- 出问题时应该先看哪里

目标不是介绍抽象概念，而是让维护者打开这一份文档就能理解当前实现与排障入口。

## 1. 设计目标

本项目的上下文管理面向小说创作，不是通用聊天：

1. 长跑会超出模型上下文窗口（200~500 章、每章多次工具调用）。
2. 需要保留的不是"聊天历史本身"，而是结构化叙事记忆。
3. Writer 压缩后不能丢角色状态、伏笔、章节计划、风格约束、待修审稿问题。
4. 恢复写作时不能假设模型还"记得之前聊过什么"，必须优先依赖持久化工件。

因此这是一套"分层记忆"：

- **短期记忆**：最近保留的消息尾部
- **中期记忆**：压缩生成的一份摘要（`agentcore.SummaryMessage`）
- **长期记忆**：store 中的结构化工件（章节/弧/卷摘要、计划、角色快照、伏笔、时间线）
- **恢复记忆**：`novel_context` 与 Writer 的 restore pack

## 2. 谁负责什么

v1.8.8 之后框架与应用的分工是明确的两件事：

| 职责 | 归属 | 落点 |
|---|---|---|
| **何时压** | 框架 | `agentcore.Config.CompactAt`（估算超过阈值先压再发请求）；provider 报 context overflow 时先压再重试 |
| **怎么压** | 项目 | `agentcore.Compactor` 实现：`internal/agents/ctxpack.NewWriterCompactor` |
| **history 归属** | 项目 | `agentcore.Run` 无状态，返回结束时的 history（含压缩替换），`agents.WorkerSet` 负责存回 |
| **压缩后补什么** | 项目 | `ctxpack.WriterRestorePack`（Writer 专用恢复包） |
| **素材从哪来** | 项目 | `store` 的持久事实优先，不足时才让模型摘要 |

框架只调用 `Compact`，关心三件事：返回的 history 更短、`Replaced` 指明替换了几条、
`Usage` 带上这次摘要的模型用量（由框架定价后挂在 `CompactionEnd` 上，供调用方计费）。项目不需要实现"投影/提交"钩子——压缩结果由框架一次性写回
（`CompactionEnd`），之后历史继续 append-only。

## 3. 关键文件

### 3.1 压缩器实现

- `internal/agents/ctxpack/strategy.go` — `WriterCompactor`：`Compact` / `cutPoint` /
  `summary` / `storeSummary` / `llmSummary` / `instruction` / `microcompact`
- `internal/agents/ctxpack/builder.go` — 从 store 装配摘要素材与恢复包素材
  （`buildWriterStoreSummaryText` / `buildWriterRestoreText` / `loadWriterStoreSummaryState`）
- `internal/agents/ctxpack/restore.go` — `WriterRestorePack`（内存缓存，压缩路径零 IO）

### 3.2 项目侧接线

- `internal/agents/build.go` — 给四个 Worker 各挂一个 `Compactor` 与 `CompactAt`
- `internal/agents/context_manager.go` — `newContextManager` / `newRoleContextManager` /
  `compactAt` / `roleKeepRecentTokens`
- `internal/agents/architect_context.go` / `editor_context.go` — 各自的摘要提示词与档案
- `internal/bootstrap/config.go` — `CompactRatio` / `MinCompactReserve` / `CompactReserveTokens`
- `internal/host/host.go` — `refreshWriterRestore`（生命周期时点刷新恢复包）

### 3.3 结构化上下文装配

- `internal/tools/novel_context.go` / `novel_context_builders.go` — 每轮按任务装配
  working / episodic / reference 记忆（正常运行路径，不是压缩路径）
- `internal/domain/runtime.go` — `ContextProfile`（按总章节数决定摘要/时间线窗口）

## 4. 压缩器是怎么组装的

四个 Worker 都走同一个构造函数，只有摘要提示词与恢复包不同：

```go
// Writer
Compactor: newContextManager(contextManagerConfig{Store: store, Restore: restore}),
CompactAt: compactAt(writerWindow),

// Architect / Editor
Compactor: newRoleContextManager(architectContextProfile, store),
CompactAt: compactAt(architectWindow),
```

参数（`contextManagerConfig` 与 `WriterCompactConfig`）：

- `Store` — 摘要素材来源；nil 时 store 摘要直接跳过，退回模型摘要
- `Restore` — 仅 Writer 有；非 nil 时压缩后注入恢复包
- `KeepRecentTokens` — 压缩后原样保留的最近历史预算，默认 20000（`roleKeepRecentTokens`）
- `SummaryTokenBudget` — store 摘要素材预算，默认 7000（`defaultStoreSummaryBudgetTokens`）
- `RestoreTokenBudget` — 恢复包预算，默认 6000（`restoreBudgetTokens`）
- `MinToolResultTokens` — 工具结果微压缩门槛，默认 200；负数关闭该步骤
- `Prompts` — `SummaryPrompts{System, Summary, Update, TurnPrefix}`，空值取 Writer 默认

`NewWriterCompactor` 是历史命名：**三个角色共用同一个实现**，Writer 只是多了 restore pack
与写作向提示词。Architect / Editor 是"单任务、多次读取"模式，读过的 `novel_context`
随时可以重读，因此不需要恢复包。

### 4.1 触发阈值

```
CompactAt = window - CompactReserveTokens(window)
          = window * CompactRatio (0.85)（受 MinCompactReserve=8000 兜底）
```

写进 `agentcore.Config.CompactAt` 后由框架掌握时机。窗口未知（`window <= 0`）时传 0，
框架就只在 provider 报 overflow 时压缩。运行时 `/model` 把角色切到窗口不同的模型时，
`CompactAt` 不会重建（装配期固定）；框架的 overflow 压缩路径负责自愈。

## 5. 一次压缩的完整流程

`WriterCompactor.Compact(ctx, history, call, focus)`：

```
history 为空 / 找不到切点          → 返回 nil（无可压缩）
replaced 估算 < 500 token          → 返回 nil（摘要不比它省）
摘要 = store 素材（零 LLM）或模型摘要
组装 = [摘要消息][恢复包消息?][microcompact(保留段)]
返回 Compaction{Messages, Replaced: cut, Usage}
```

### 5.1 切点（cutPoint）

从尾部往前累计 `agentcore.EstimateMessage`，凑够 `KeepRecentTokens` 就停；保留预算
不超过整段历史的一半，保证压缩总能推进（小窗口模型上尤其重要）。保留段不能以工具
结果消息开头——那说明切点落在工具循环内部，把切点退回到发起调用的那条消息，避免
"结果找不到调用"的断链。

### 5.2 摘要：store 优先，模型兜底

`storeSummary` 直接把持久事实渲染成摘要（**不调模型**），预算取
`min(7000, replacedTokens * 9 / 10)`——素材永远不超过被替换的历史，压缩不会让上下文
变大。命中前提（`loadWriterStoreSummaryState`）：

- 当前章 > 1（第一章没有历史摘要与计划可依）
- 已有可用摘要：最近章节摘要、当前弧摘要或当前卷摘要至少一项
- 已有工作态：当前章节计划或当前章节大纲至少一项

不满足时走 `llmSummary`：把被替换的历史序列化进一次普通模型请求（`call(history)` 复用
同一模型与端点，能读到提示词缓存），清空 `Tools` / `ToolChoice` / `ResponseFormat`；
摘要被输出上限截断时报错而不是假装完整；provider 报 context overflow 时丢弃最老消息
重试，最多 3 次。

两种摘要都把 **「## 当前任务」** 作为固定一节：首次压缩取自首条 user 消息，之后从上一份
摘要里按标题解析取回。store 只有小说事实、没有"这次要做什么"，缺这一节就会出现
"知道小说状态，却忘了本次任务"。

### 5.3 组装

```go
msgs = append(msgs, agentcore.SummaryMessage(summary))   // 中期记忆
if restore, ok := c.restoreMessage(); ok {
	msgs = append(msgs, restore)                          // 恢复包（仅 Writer）
}
msgs = append(msgs, c.microcompact(kept)...)              // 最近历史 + 工具结果清理
```

`SummaryMessage` 会告诉模型"这份摘要代替此前对话，从这里继续，不要复述，也不要追问
摘要已经交代过的事"；模型按 user 消息读它。

### 5.4 微压缩（保留段内的工具结果）

`microcompact` 只替换**内容**，保留 `ToolResultBlock` 的 `ToolUseID` 与 `IsError`，
让工具调用与结果仍然成对：

- 保护最新 5 条工具结果（`keepRecentToolResults`）
- 小于 200 token 的结果不清理——它们通常是状态迁移的唯一记录
- 被清掉的内容替换为 `[工具结果已清理以节省上下文]`

作用：工具返回体积大、信息密度低，很多只是"过程噪音"；砍掉它们常常能让压缩晚来一轮。

## 6. store 摘要素材清单

`writerStoreSummarySections`（有预算，按顺序渲染，超出即停）：

1. 当前进度（phase / flow / 当前章 / 已完成章节 / 卷弧）
2. 数据告警（读取失败的 section，显式暴露而不是静默丢失）
3. 最近章节摘要（窗口由 `domain.NewContextProfile(total)` 决定）
4. 当前章节计划
5. 当前章节大纲（分层的书回退到 layered_outline）
6. 当前弧摘要 / 当前卷摘要
7. 角色快照
8. 活跃伏笔
9. 待修审稿问题
10. 最近时间线
11. 风格规则

每节用 JSON 渲染并按 token 预算裁剪（`appendJSONSection`），保证总预算内可读完。

## 7. 三套摘要提示词

`ctxpack.SummaryPrompts` 有四个字段：`System`、`Summary`（整段摘要）、`Update`（把新历史
合并进旧摘要）、`TurnPrefix`（只摘要一个未完结轮次的前半段）。

| 角色 | System / Summary / Update / TurnPrefix |
|---|---|
| Writer | `ctxpack` 内置的小说叙事版（默认值） |
| Architect | `architect_context.go`：当前任务、硬性约束、已确认事实、规划决策、待处理事项、下一步；必须区分已落盘结果与未保存提议 |
| Editor | `editor_context.go`：当前任务、授权与验收约束、已读证据（按章节号）、当前发现、工具进度、下一步；禁止声称读过未读章节 |

摘要输出允许带 `<analysis>` 思考块，落库前 `stripAnalysisBlock` 剥掉，只保留
`<summary>` 内容。

## 8. 恢复包（WriterRestorePack）

压缩摘要解决"承接进度"，但摘要再怎么好也可能漏掉精确事实。恢复包是压缩后追加的一条
结构化消息（`<post-compact-context>`），保证 Writer 继续写时一定看得到：

1. 当前进度
2. 数据告警
3. 当前章节计划
4. 当前章节大纲
5. 待修审稿问题
6. 角色快照
7. 最近章节摘要
8. 活跃伏笔
9. 当前弧摘要 / 当前卷摘要
10. 最近时间线
11. 风格规则

关键设计：

- **预装配 + 内存读取**：`Refresh(store)` 在生命周期时点（启动恢复、继续、干预生效、
  `/next` 放行）由 Host 主动刷新；`Compact` 路径只读内存（RWMutex），压缩过程中零 IO。
- **预算 6000 token**：`buildMessage` 检查放不下就跳过（不因此让压缩失败）。
- **失败可见**：读取 store 出错时把错误写进"数据告警"节，而不是给旧内容。
- **不替代摘要**：它只是追加式补偿，摘要仍然是被替换历史的唯一代表。

代码位置：`internal/agents/ctxpack/restore.go`；刷新入口 `internal/host/host.go`
`refreshWriterRestore`。

## 9. 落盘与恢复语义

- `MessageEnd` 是消息进入 history 的唯一入口，也是会话落盘点（`WorkerSet.record` →
  store session JSONL，同时喂 UsageRecorder）。`Emit` 返回 error 会让该消息不入 history
  并中止本轮。
- `CompactionEnd` 是压缩结果写回 history 的唯一入口：`Compaction.Messages` 整体替换
  旧历史；`Compaction` 为 nil 表示没有可压的；`Err` 表示压缩失败（history 保持原样）。
- `agentcore.Run` 无状态：结束时把 history 返回，`WorkerSet` 存回内存。**history 只在
  进程内**——重启后每个 Worker 从空历史开始，连续性完全来自 store 工件 +
  `novel_context` + 恢复包。这是刻意的：会话历史不是事实。
- 压缩不落盘、不写 checkpoint：它不是创作事实，不改变 Progress / Checkpoint。

## 10. Token 估算

预算控制全部依赖 `agentcore.EstimateMessage`（兼容 CJK）：

- CJK 主导文本：`runes × 1.5`
- ASCII 主导文本：`bytes / 4`
- 工具调用参数（JSON）：始终 `bytes / 4`
- 图片：固定估值

为什么不能用标准 `bytes/4`：中文 UTF-8 一个字 3 bytes，`bytes/4` 会把它估成 0.75 token，
实际约 1.5，低估一倍会让压缩严重滞后。

`ctxpack` 的预算裁剪（素材/恢复包）复用同一估算函数，所以"预算"与"触发阈值"口径一致。

## 11. 可观测与排障

当前可拿到的观测事实：

- 压缩若走模型摘要，`Compaction.Usage` 会由框架按当前模型定价；它挂在
  `CompactionEnd` 事件上，**当前未接入 `usage.json` 统计**（压缩调用因此不计入 TUI 用量）
- 会话 JSONL（`meta/sessions`）记录每条进入 history 的消息；压缩替换后的历史不出现在
  会话日志里，但压缩前的消息已在日志中
- 框架提供 `agentcore.CompactionStart` / `CompactionEnd` 事件（`CompactionEnd` 携带
  `*Compaction` 与 `Err`）；当前未投影到 TUI/事件流，需要时在 `WorkerSet.Run` 的 `Emit`
  里接一个分支即可
- 输入 token 在压缩后应明显回落，是"压缩发生过"的最直接证据

### 11.1 出问题先看哪里

**场景 1：压缩后丢章节计划 / 大纲**
`loadWriterStoreSummaryState` 是否拿到 `chapterPlan` / `currentOutline`；该章是否 > 1；
数据告警节里是否记录了读取失败。

**场景 2：压缩后丢角色状态 / 伏笔**
`LoadLatestSnapshots` / `LoadActiveForeshadow` 是否有数据；store 摘要预算是否被前面的
章节摘要吃光（渲染按顺序，超预算即停）。

**场景 3：压缩很频繁**
看模型真实 `context_window` 是否写对（未登记模型兜底 200K，小窗口会反复触发）；
`CompactAt` 是否按该窗口计算；单次 `novel_context` 是否本身就撑爆窗口。

**场景 4：工具结果撑大上下文**
`microcompact` 的门槛与保护条数；`novel_context` 是否被反复整块读取而不是聚焦读取。

**场景 5：恢复后上下文不够**
`refreshWriterRestore` 是否在启动/继续时被调用；恢复包是否因超出 6000 预算被跳过
（`buildMessage` 返回错误时压缩继续，但恢复包缺席）。

**场景 6：压缩后模型答非所问**
摘要是否缺少「## 当前任务」；角色摘要提示词是否被改坏；是否发生了
"摘要被输出上限截断"（此时 `llmSummary` 会报错而不是返回半截摘要）。

## 12. 当前实现的取舍

已坚持的方向：

1. 不把小说业务逻辑塞进框架：压缩器是 `agentcore.Compactor` 的一个实现，业务素材在
   本仓库。
2. 优先依赖结构化 store，而不是聊天历史；store 摘要零 LLM 成本。
3. 压缩与恢复共用 `builder.go`，避免口径漂移。
4. 压缩结果一次性提交，之后 append-only——不为省 token 破坏提示词缓存（见
   `docs/prompt-cache-design.md` 纪律二）。

有意保留的限制：

1. store 摘要只对 Writer 有完整素材（Architect / Editor 没有计划/大纲以外的写作态）
2. 第一章不会命中 store 摘要
3. store 数据不足时回退模型摘要，模型摘要仍可能丢细节
4. restore pack 是追加式补偿，不替代摘要
5. history 不跨进程持久化；重启靠 store 重建上下文

## 13. 一句话总结

`框架管时机、项目管策略：优先用 store 的结构化小说记忆替换旧历史，必要时才让模型摘要；
Writer 再用恢复包补一次精确事实。历史只是缓存，事实永远在 store。`

改动这套系统时优先守住三条：

1. 不要让 Writer 的关键记忆只依赖模型摘要。
2. 不要让 `builder.go` 的摘要素材与恢复包素材分叉。
3. 出现连续性问题时，先查结构化工件有没有进入上下文，再决定是否改 prompt。
