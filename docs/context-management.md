# 上下文管理说明

本文档说明 `ainovel-cli` 当前的上下文管理体系，包括：

- 为什么要做上下文管理
- 上下文从哪里来
- 运行时如何压缩、恢复、交接
- 每个策略的价值、触发条件与适用场景
- 出问题时应该先看哪里

目标不是介绍抽象概念，而是让后续维护者打开这一份文档，就能快速理解当前实现和排障入口。

## 1. 设计目标

本项目的上下文管理不是通用聊天场景，而是面向小说创作场景。它要同时解决几类问题：

1. 长对话会超出模型上下文窗口。
2. 小说创作需要保留的不是“聊天历史本身”，而是结构化叙事记忆。
3. Writer 在压缩后不能丢掉角色状态、伏笔、章节计划、风格约束、审稿待修项。
4. 恢复写作时不能假设模型还“记得之前聊过什么”，必须优先依赖持久化工件。

因此我们采用的是一套“分层记忆”方案：

- 短期记忆：最近保留的消息尾部
- 中期记忆：压缩生成的 `ContextSummary`
- 长期记忆：项目 store 中的结构化工件
- 恢复记忆：handoff / restore pack / novel_context

## 2. 整体架构

### 2.1 主要分层

当前上下文管理分成四层：

1. `agentcore/context`
   负责通用的上下文预算、策略管线、压缩/恢复框架。

2. `internal/tools/novel_context`
   负责把小说项目中的结构化数据装配成当前轮可用上下文。

3. `internal/agents/ctxpack`
   负责 Writer 专用的 store-based 快速压缩。

4. `internal/agents/ctxpack/restore.go`
   负责在 `FullSummary` 之后追加一份压缩后恢复包，确保 Writer 能继续写。

### 2.2 数据流

运行时主要有两条上下文路径：

1. 正常工作路径
   - Agent 调用 `novel_context`
   - `novel_context` 从 store 读取章节摘要、计划、角色、时间线等数据
   - 这些数据进入当前轮 prompt

2. 上下文过长路径
   - `ContextManager` 检测到 token 压力
   - 按策略顺序压缩
   - 优先尝试轻量压缩和 store-based 压缩
   - 还不够时才走 LLM `FullSummary`
   - `FullSummary` 后注入 restore pack

## 3. 关键文件

### 3.1 通用上下文引擎

- `../agentcore/context/strategy.go`
- `../agentcore/context/engine.go`
- `../agentcore/context/strategy_tool.go`
- `../agentcore/context/strategy_trim.go`
- `../agentcore/context/strategy_summary.go`
- `../agentcore/context/message.go`
- `../agentcore/context/summary_run.go`

作用：

- 定义 `Strategy` / `ForceCompactionStrategy`
- 负责基于预算执行策略链
- 负责 `ContextSummary` 的表示与 LLM 转换
- 负责 `FullSummary` 的 LLM 摘要压缩

### 3.2 项目侧接线

- `internal/agents/build.go`
- `internal/agents/context_manager.go`
- `internal/agents/architect_context.go`
- `internal/agents/editor_context.go`

作用：

- `build.go` 给 Writer / Architect / Editor 各挂一个 `ContextManagerFactory`，每次运行按当前模型窗口重建
- `context_manager.go` 提供统一的 `newContextManager`，以及 Architect / Editor 共用的 `roleContextProfile`
- `architect_context.go` / `editor_context.go` 只放各自的摘要 prompt 和档案参数
- Writer 额外挂 `StoreSummaryCompact` 与 restore pack，见 3.3

### 3.3 项目侧压缩与恢复

- `internal/agents/ctxpack/strategy.go`
- `internal/agents/ctxpack/builder.go`
- `internal/agents/ctxpack/restore.go`

作用：

- 在 LLM 摘要之前，优先使用 store 数据做快速压缩
- 统一构建 Writer 压缩与恢复所需的结构化上下文
- 在 `FullSummary` 后追加一份纯内存 restore message

### 3.4 结构化上下文装配

- `internal/tools/novel_context.go`
- `internal/tools/novel_context_builders.go`
- `internal/domain/runtime.go`

作用：

- 定义 `ContextProfile` / `MemoryPolicy`
- 决定加载多少章节摘要、多少时间线、是否启用分层摘要
- 把 store 中的章节、角色、伏笔、时间线、审稿经验等装配出来

### 3.5 交接与恢复

- `internal/orchestrator/handoff_policy.go`
- `internal/orchestrator/recovery_engine.go`

作用：

- 在长篇/返工/审阅阶段优先依赖 handoff
- 恢复时把结构化交接包拼进 prompt

### 3.6 可观测性

- `internal/agents/context_manager.go`（`contextRewriteCallback`）
- `internal/entry/tui/panels.go`
- `internal/entry/tui/layout.go`

作用：

- 三个角色共用同一个重写回调，按 `agent` 字段区分，写入 slog
- 输出策略名称、token 变化、消息保留量
- 让 TUI 能看到当前上下文是 `projected` 还是 `compacted`

## 4. ContextManager 是怎么组装的

三个 Worker 都走 `newContextManager`，由各自的 `ContextManagerFactory` 在每次运行时按当前模型窗口重建（`/model` 切换后自动跟随）。Writer 的配置直接写在 `build.go`；Architect / Editor 共用 `roleContextProfile`，只差摘要 prompt 和保留的读取次数。

当前 `contextManagerConfig` 的关键参数：

- `ContextWindow`
  模型总上下文窗口。

- `ReserveTokens`
  触发压缩前保留的余量，统一由 `bootstrap.CompactReserveTokens` 计算：窗口的 15%，不低于 8000。

- `CommitProjected`
  压缩结果是否提交为新基线。三个角色都为 true，避免后续轮次反复改写请求前缀、打穿提示词缓存。

- `ToolMicrocompact`
  工具结果微压缩配置。

- `ExtraStrategies`
  项目侧额外压缩策略。当前只有 Writer 挂 `StoreSummaryCompact`。

- `Summary`
  `FullSummary` 的配置，包括角色专属 prompt 和 post-summary hook。

当前实际配置：

| 参数 | Writer | Architect | Editor |
|------|--------|-----------|--------|
| 微压缩范围 | 全部工具结果 | 仅 `novel_context` | 仅 `novel_context` |
| 微压缩保留最近 | 5 条 | 3 条 | 2 条 |
| 微压缩下限 | 200 token | 200 token | 200 token |
| ExtraStrategies | StoreSummaryCompact（尾部保留 20,000 token） | 无 | 无 |
| Summary prompt | 小说叙事版（ctxpack） | 规划检查点版 | 审阅检查点版 |
| PostSummaryHook | writerRestorePack | 无 | 无 |

压缩触发阈值 = `ContextWindow - ReserveTokens`。例如窗口 200K 时在 170K 触发，窗口 128K 时在约 109K 触发。

切点按完整工具组对齐（`agentcore/context.FindCutPoint`）：倒数 token 落在工具结果上时，退回到发起它的 assistant 调用，整组留在尾部。Worker 一次运行只有一条任务消息、其余全是工具组，旧的"向前找 user 边界"逻辑在这种形态下会一路走到末尾放弃，等于从不压缩。

当前策略管线顺序：

Writer：

1. `ToolResultMicrocompact`
2. `StoreSummaryCompact`
3. `FullSummary`

Architect / Editor：

1. `ToolResultMicrocompact`
2. `FullSummary`

这个顺序有明确含义：

- 先用最便宜的办法清理工具噪音
- Writer 如果 store 数据够，直接做零 LLM 的结构化压缩
- 最后才退到 LLM 摘要

Architect / Editor 只清理旧的 `novel_context` 读取结果：这些数据都在 store 里，需要时可以重读。而 `save_*` 等写工具结果和 `read_chapter` 的章节原文是当前任务的事实与证据，不做微压缩，只在最终 `FullSummary` 时进入摘要。

`novel_context` 工具本身不按固定字节预算裁剪分区：它只做与任务相关的语义选择（聚焦弧、最近窗口、活跃伏笔），体积由各 Worker 按真实模型窗口管理。一个超过就报错的工具层上限对用户没有任何可操作性，而窗口是用户可以在配置里写明的。

## 5. 每个策略的作用

### 5.1 ToolResultMicrocompact

实现位置：

- `../agentcore/context/strategy_tool.go`

作用：

- 清理历史 `tool_result`
- 给旧工具结果替换成简短占位文本

价值：

- 工具返回内容通常体积大、信息密度低
- 很多旧工具结果只是“过程噪音”，不是小说记忆

当前配置特点：

- Writer 对所有工具结果生效，保留最近 5 条
- Architect / Editor 只对 `novel_context` 生效，分别保留最近 3 条和 2 条；相同参数的重复读取只保护最新一次
- 低于 200 token 的小结果不清理，它们通常是状态迁移的唯一记录

适用场景：

- Writer 多轮 read / check / draft 工具之后
- 长篇 Architect 按卷弧多次聚焦读取 `novel_context`
- Editor 审阅一段范围时逐章读取 `novel_context`

### 5.2 LightTrim

实现位置：

- `../agentcore/context/strategy_trim.go`

作用：

- 截断非常长的文本块
- 保留头部和尾部，中间用占位符替代

价值：

- 保住消息结构不变
- 代价低
- 很适合处理超长章节原文或大段输出

适用场景：

- 单条消息过长，但还不需要整段历史做 summary

当前状态：agentcore 提供，但本项目没有接入任何 Worker 管线。它会跳过最近几条消息，且对文本块掐头去尾硬截，用在 `novel_context` 这类 JSON 结果上会破坏结构，微压缩整条清掉再重读更合适。

### 5.3 StoreSummaryCompact

实现位置：

- `internal/agents/ctxpack/strategy.go`
- `internal/agents/ctxpack/builder.go`

作用：

- 当 Writer 上下文过长时
- 优先使用持久化 store 中的结构化记忆来替换旧消息
- 不调用 LLM

它不是对话摘要，而是“结构化记忆替换”。

"当前任务"是 Writer 所有摘要格式的固定一节：store 摘要和 LLM 摘要（`WriterSummaryPrompt`）都带，首次压缩取自首条 user 消息，之后从上一份摘要按下一个标题解析取回，两种摘要交替出现也不丢。store 只有小说事实，没有本次任务，不这样做会出现"知道小说状态，却忘了这次要做什么"。

当前保留的核心数据包括：

- 当前进度
- 最近章节摘要
- 当前章节计划
- 当前章节大纲
- 当前弧摘要
- 当前卷摘要
- 角色快照
- 活跃伏笔
- 待修审稿问题
- 最近时间线
- 风格规则

触发前提：

- 当前章节大于 1
- store 中已经有足够的历史摘要
- 且当前章至少有工作态数据
  - `chapter_plan` 或 `current_outline`

价值：

- 降低 LLM 压缩次数
- 避免小说关键信息在摘要时漂移
- 让长期记忆优先依赖落盘事实，而不是聊天历史

为什么只给 Writer 用：

- 这是小说业务策略，不是通用框架策略
- 它围绕写章工作态（章节计划、当前大纲、待修问题）构建；Architect / Editor 是单任务、多次读取的模式，旧的 `novel_context` 结果直接清掉重读即可，不需要用 store 替换历史
- 先在最需要连续创作记忆的 Writer 上验证最合理

### 5.4 FullSummary

实现位置：

- `../agentcore/context/strategy_summary.go`
- `../agentcore/context/summary_run.go`

作用：

- 当上面几层还不够时，使用模型生成 `ContextSummary`
- 保留最近消息尾部
- 把更早的上下文变成结构化 checkpoint

Writer 与默认代码助手不同的地方：

- Writer 使用了自定义 summary prompt
- 摘要内容明确要求保留：
  - 当前任务（原样）
  - 当前进度
  - 角色即时状态
  - 活跃伏笔与线索
  - 审稿反馈与待修问题
  - 风格与节奏
  - 关键决策
  - 下一步
  - 关键上下文

Architect / Editor 各有一套检查点式 prompt（`architect_context.go` / `editor_context.go`）：

- Architect 摘要保留：当前任务、硬性约束、已确认事实、规划决策、待处理事项、下一步；并要求区分已落盘结果与尚未保存的提议
- Editor 摘要保留：当前任务、授权与验收约束、已读证据（按章节号）、当前发现、工具进度、下一步；并禁止声称读过未读章节

两者都不挂 restore pack：压缩后模型可以重新调用 `novel_context` / `read_chapter` 取回事实与原文。代价是 Editor 在整卷摘要这类长任务里，早期章节的原文引证会经过一次摘要，精度低于直接引用。

价值：

- 是最终兜底策略
- 即使 store 数据不足，也仍然可以通过 LLM 维持连续性

### 5.5 熔断器（Circuit Breaker）

实现位置：

- `../agentcore/context/engine.go`

作用：

- 当压缩连续失败达到阈值（默认 3 次）时，跳过当前轮压缩
- 跳过时仍然发出 `RewriteEvent`（`Reason = “circuit_breaker”`）
- TUI 会显示 scope 为”熔断跳过”
- 采用半开模式：跳过一轮后下次会重试，成功则复位，再失败再跳过

为什么需要：

- LLM 摘要可能因网络、模型拒绝等原因连续失败
- 没有熔断的话，每轮 Project 都会尝试并失败，浪费 API 调用
- 长篇写作会话中这个浪费会累积

排障：

- 如果 TUI 持续显示”熔断跳过”，说明 LLM 摘要路径有问题
- 检查 slog 中 `reason=circuit_breaker` 的上下文重写事件
- 熔断不影响 `StoreSummaryCompact`（它不调 LLM）

### 5.6 Token 估算（CJK 感知）

实现位置：

- `../agentcore/context/usage.go`

作用：

- 所有预算控制、压缩触发时机都依赖 token 估算
- `estimateTextTokens` 自动检测文本是否以 CJK 字符为主
- CJK 主导文本：`runes × 1.5`
- ASCII 主导文本：`bytes / 4`

为什么不能用标准 `bytes/4`：

- 中文 UTF-8 一个字 = 3 bytes
- `bytes/4` 会把一个中文字估为 0.75 token，实际约 1.5 token
- 低估 2 倍会导致压缩触发严重滞后

影响范围：

- `EstimateTokens`（单条消息）
- `EstimateTotal`（消息列表）
- `EstimateContextTokens`（混合估算：LLM 上报 Usage + 尾部消息估算）
- `ctxpack/builder.go` 中的预算裁剪

注意：ToolCall 的 args 是 JSON（ASCII 主导），仍使用 `bytes/4`，不受 CJK 调整影响。

## 6. Writer 为什么有两套”压缩后记忆”

当前 Writer 有两条看起来相近、但职责不同的链路：

### 6.1 StoreSummaryCompact

职责：

- 在压缩过程中直接替换旧消息

特点：

- 发生在 `FullSummary` 之前
- 零 LLM
- 用 store 替换更早历史

### 6.2 writerRestorePack

实现位置：

- `internal/agents/ctxpack/restore.go`

职责：

- 在 `FullSummary` 之后追加一条 restore message

特点：

- 发生在 LLM 压缩之后
- 通过 `PostSummaryHook` 注入
- 用于补充 Writer 恢复继续创作时必须看到的结构化信息

为什么两者都需要：

- `StoreSummaryCompact` 不是总能命中
  - 比如第一章或 store 数据不够时
- `FullSummary` 即使做得再好，也可能遗漏 store 中的精确信息
- 所以 restore pack 作为最后一道保险

现在这两者已经共用 `ctxpack/builder.go`，避免口径漂移。

## 7. novel_context 的作用

实现位置：

- `internal/tools/novel_context.go`
- `internal/tools/novel_context_builders.go`

`novel_context` 不是压缩策略，它是运行时的“结构化上下文装配器”。

它把 store 中的数据分成几类：

- `working_memory`
  - 当前章节计划
  - 当前章节大纲
  - 最近章节摘要
  - 时间线
  - checkpoint
  - previous tail

- `episodic_memory`
  - 角色状态
  - 关系状态
  - 最近状态变化
  - 伏笔

- `reference_pack`
  - 更稳定的设定和参考数据

- `selected_memory`
  - 按当前任务挑选出来的少量重要记忆

价值：

- 它决定了每一轮真正“喂给模型”的结构化小说上下文
- `StoreSummaryCompact` 不是调用它本身，但和它复用同类数据来源与装配思路

分层书的章节上下文以 `chapter=N` 的大纲位置为准，不能以全书 `Progress.CurrentVolume/CurrentArc`
替代目标章的位置；完本返工时两者通常不同。角色快照只使用目标章之前已结束弧的最近一份，
没有历史快照时回到初始角色设定。卷摘要仅包含目标卷之前的卷，弧摘要仅包含目标弧之前的弧。

文风规则只有来源弧早于目标弧时才可继承。旧项目仅保存最新一份规则，若其来源为目标弧、
未来弧或来源不明，则不把它作为当前规则，而从目标章之前已提交的正文提取风格锚点和对话参考；
首章没有前文时省略这些参考。压缩摘要与恢复包同样遵守目标章的边界；全书级风格统计仍用于整体纠偏。

## 8. ContextProfile 与 MemoryPolicy

实现位置：

- `internal/domain/runtime.go`

### 8.1 ContextProfile

作用：

- 按总章节数决定加载窗口大小

当前规则：

- `<= 15` 章
  - 最近 `10` 章摘要
  - 最近 `10` 章时间线

- `<= 50` 章
  - 最近 `5` 章摘要
  - 最近 `8` 章时间线

- `> 50` 章
  - 最近 `3` 章摘要
  - 最近 `5` 章时间线
  - 启用分层摘要

价值：

- 控制上下文规模
- 避免长篇时把所有历史都塞进 prompt

### 8.2 MemoryPolicy

作用：

- 把当前上下文使用策略显式写出来
- 供 `novel_context` 输出
- 供 handoff / reminder / 诊断逻辑使用

关键字段：

- `SummaryWindow`
- `TimelineWindow`
- `LayeredSummaries`
- `SummaryStrategy`
- `HandoffPreferred`
- `ReadOnlyThreshold`

价值：

- 把“当前系统应该如何使用记忆”从隐式逻辑变成显式运行时策略

## 9. handoff 的作用

实现位置：

- `internal/orchestrator/handoff_policy.go`

当作品进入更长、更复杂、更依赖结构化工件的阶段时，系统会偏向 handoff。

handoff pack 会记录：

- 当前阶段与 flow
- 下一章位置
- 最近提交
- 最近审阅
- 最近摘要
- 当前 memory policy
- 恢复指导语

价值：

- 中断恢复时不依赖聊天历史
- 在返工、审阅、长篇场景中优先依赖结构化工件

## 10. 可观测性与排障

### 10.1 上下文重写事件

实现位置：

- `internal/agents/context_manager.go`

每次上下文重写都会通过 `contextRewriteCallback` 写入 slog，`agent` 字段为 `writer` / `architect` / `editor`，其余字段：

- `reason`
- `strategy`
- `committed`
- `tokens_before`
- `tokens_after`
- `messages_before`
- `messages_after`
- `compacted_count`
- `kept_count`
- `split_turn`
- `incremental`
- `summary_runes`
- `duration_ms`

这会同时进入：

- `slog`
- runtime boundary 队列
- TUI `COMPACT` 事件

### 10.2 TUI 中能看到什么

TUI 会展示：

- 当前上下文 token（带健康度渐变色）
- context window
- 当前上下文 scope（含"熔断跳过"）
- 当前最后一次策略名称
- summary 数量

上下文百分比的颜色含义（实现在 `internal/entry/tui/layout.go`）：

| 颜色 | 条件 | 含义 |
|------|------|------|
| 绿色 | < 70% | 充裕，远离压缩阈值 |
| 黄色 | 70-85% | 接近压缩阈值 |
| 红色 | > 85% | 即将或正在压缩 |

Scope 的中文标签：

| Scope | 显示 | 含义 |
|-------|------|------|
| baseline | 基线 | 正常状态 |
| projected | 投影 | 临时压缩预览 |
| compacted | 已提交 | 压缩已生效 |
| recovered | 恢复 | 溢出后恢复 |
| skipped | 熔断跳过 | 压缩被熔断器跳过 |

价值：

- 能快速判断当前上下文健康度
- 黄色/红色时可以预期即将发生压缩
- 看到"熔断跳过"说明 LLM 摘要路径有问题

### 10.3 出问题先看哪里

#### 场景 1：Writer 压缩后丢章节计划

先看：

- `novel_context` 是否稳定注入 `chapter_plan`
- `ctxpack/builder.go` 是否拿到 `chapterPlan`
- `writerRestorePack` 是否刷新

重点文件：

- `internal/tools/novel_context_builders.go`
- `internal/agents/ctxpack/builder.go`
- `internal/orchestrator/session.go`

#### 场景 2：压缩后丢角色状态/伏笔

先看：

- `LoadLatestSnapshots`
- `LoadActiveForeshadow`
- `ctxpack/builder.go`
- Writer summary prompt 是否被覆盖

#### 场景 3：压缩频繁但总是不命中 store_summary

先看：

- 当前章节是不是 `<= 1`
- 是否已有 recent summaries / arc / volume summary
- 是否存在 `chapter_plan` 或 `current_outline`
- `writer.Context.Strategy` 最终记录的是不是 `full_summary`

#### 场景 4：恢复后上下文不够

先看：

- handoff 是否生成
- restore pack 是否刷新
- recovery prompt 是否注入 handoff

#### 场景 5：工具结果太多导致上下文膨胀

先看：

- `ToolResultMicrocompact` 是否命中
- slog 中 `agent=writer|architect|editor` 的上下文重写事件，`strategy` 是 `tool_result_microcompact` 还是 `full_summary`

#### 场景 6：单次 `novel_context` 就撑爆窗口

先看：

- 启动日志里该角色的上下文窗口来源。自定义代理下未登记的模型会兜底到 200K；实际窗口更小的模型要在 `providers.<name>.models[].context_window` 写明真实值，压缩才会按真实窗口触发

## 11. 当前实现的取舍

### 已明确坚持的方向

1. 不把小说业务逻辑塞进 `agentcore`
2. 优先依赖结构化 store，而不是聊天历史
3. Writer 使用专门的小说摘要 prompt
4. 压缩与恢复尽量共用 builder，避免口径漂移

### 当前仍然有意保留的限制

1. `StoreSummaryCompact` 只给 Writer 用
2. 第一章不会命中 store-based compact
3. store 数据不足时仍然回退到 `FullSummary`
4. `writerRestorePack` 是追加式补偿，不替代 `FullSummary`
5. Architect / Editor 只有微压缩加 `FullSummary`，没有 store-based compact 和 restore pack

这些限制不是缺陷，而是当前阶段为了控制复杂度做的边界。

## 12. 一句话总结

本项目的上下文管理不是“把长对话压短”这么简单，而是：

`优先用结构化小说记忆维持连续性，在必要时才让 LLM 去摘要对话；并且在压缩、恢复、交接三个环节都尽量依赖同一套持久化工件。`

如果你后续要改这套系统，优先守住下面三条：

1. 不要让 Writer 的关键记忆再次只依赖聊天历史。
2. 不要让 `ctxpack` 的 strategy 和 restore 口径分叉。
3. 出现连续性问题时，先查结构化工件有没有进入上下文，再决定是否改 prompt。
