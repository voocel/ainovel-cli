# ainovel-cli 运行时架构

> 让 LLM 在一次 Run 里把一本小说写完，Host 只做启动 / 恢复 / 路由 / 观察，决策权尽量留给模型。

---

## 1. 目标（按优先级）

1. **稳定性**：一句话输入，稳定写完整本小说（200~500 章）。中间不因架构问题自行中断。
2. **质量可迭代**：prompt / 参考资料 / 评审维度 / 上下文策略可独立调整，不牵连架构。
3. **可恢复**：崩溃、断网、暂停后能从最近 checkpoint 继续。
4. **可观测**：每章每 step 的进度、产物、用时可查。

"稳定"是前提，"质量"是上层。每个架构决策优先服务稳定性。

---

## 2. 核心原则

### 2.1 LLM 驱动创作与裁定，Host 驱动流程路由

垂类 agent 的决策空间封闭：流程图固定、分支有限、事实驱动。两类决策走不同载体：

- **创作与裁定**（语义/质量/意图理解）→ LLM。Writer/Editor/Architect/Coordinator 裁定能力随模型升级线性受益
- **流程路由**（读事实查表）→ 代码。`flow.Router` 纯函数 + 单测，错误率趋近 0

Host 不直接调 SubAgent，而是在 Coordinator 的 `subagent` / `reopen_book` 工具成功返回的同步边界由 Flow Router 计算指令，通过 `coordinator.Steer("[Host 下达指令]…")` 注入当前 run 的下一轮。`FollowUp` 只在 agent 自然空闲后排空，不能承载主流程路由。

### 2.2 工具是事实层唯一接口

所有与文件系统、Progress、Checkpoint 的交互都由工具完成。**写类工具必须原子三件套**：artifact 落盘 + Progress 推进 + Checkpoint 追加，互斥锁内完成。重跑同一工具得到相同结果或直接跳过（digest 幂等）。

### 2.3 观察层只观察

UI、诊断、事件日志都是从事件流 / 只读工件投影出来的被动消费者。读事实，不产生事实，不影响控制流。

**`internal/diag` 是引擎唯一的可观测性子系统**——一等支撑设施，但不是产品核心（核心是 §6 的创作引擎；diag 没了照样写小说）。它跨读几乎所有工件 + session + log + checkpoint，承担两职：① **创作质量诊断**（规则 → Finding，`/diag` 屏上报告）；② **运行时排错 + 脱敏导出**（行为骨架剥正文 + 循环聚合 → 覆盖式 `meta/diag-export.md`，供用户贴 issue；维护者拿不到本地 output 也能定位死循环/中断类问题）。

**观察者纪律（不可松动）**：diag 可以诊断、可以建议，但**永不自己动手**——不自动修复、不续跑、不改流程。它越强，越有人想让它"顺手修一下"，越要守住这条，否则撞回 idleResume / StallDetector 那类已删除的坑（见 §10.5、§10.14）。对外结构（如 `RuntimeCapture`）当基础设施契约维护，别随意改字段。

### 2.4 事实层扁平

只有三类事实：

- **Progress** — 进度索引（写到第几章、待重写列表）
- **Checkpoint** — step 级推进记录（plan / draft / commit / review / arc_summary）
- **Artifact** — 章节正文、大纲、角色、摘要等产物

不引入 WorkflowInstance / TaskInstance / Command / Dispatcher 等抽象。

### 2.5 三铁律

**铁律一：工具只返事实，不返跨调度指令**。`commit_chapter` 返回 `arc_end_reached` / `next_skeleton_arc` 等结构化字段；不夹带 `[系统]` 类指令字符串。子代理内的 `next_step` 字段是事实陈述的内联指引（"我刚保存了 plan，下一步是 draft"），不算违反——见 §6.4。

**铁律二：流程路由由 Flow Router 承担**。`internal/host/flow/router.go` 的 `Route(state) → *Instruction` 是纯函数；Host 在 Coordinator 工具执行链的同步边界触发 `Dispatch`，用 `Steer` 把 `[Host 下达指令]` 放进当前 run 的下一轮输入。返回 nil 表示"裁定场景，让 LLM 自主"。**指令通道不沉默**：Route 连续算出同一指令（说明上次派发后状态未推进）时，Dispatcher 附"第 N 次下达"事实重发而非静默吞掉——"路由结果重复"是只有 Host 能观测到的事实，沉默会让 Coordinator 落入"无指令不得行动 / StopGuard 不许停"的双重矛盾。不设阈值、不熔断，如何脱困由 LLM 裁定。

**铁律三：Coordinator 不能物理 end_turn，除非 Phase=Complete**。StopGuard 在 agentcore 层拦截 `end_turn` 注入 user message；连续 5 次拦不住升级 terminate。三个子代理（architect / writer / editor）有各自的 `CheckpointDeltaGuard`。

---

## 3. 架构全景

```
[Entry: TUI / headless]
        │ prompt / steer
[Host 薄外壳]
   ├── observer        事件 → UI/日志投影
   ├── flow.Dispatcher 同步工具边界 → Route(state) → Steer
   └── usage / 模型管理
        │
[Coordinator (LLM, MaxTurns=100_000)]
   ├── 启动时裁定 architect_short / long
   ├── 收 [Host 下达指令] → 生成 subagent tool_call
   └── 收 [用户干预] → 自主裁定
        │
[architect / writer / editor SubAgent (各自独立 run + context + 模型)]
        │ 工具调用
[Tools]  novel_context · read_chapter · plan_chapter · draft_chapter · edit_chapter
         check_consistency · commit_chapter · save_review · save_arc_summary
         save_volume_summary · save_foundation
        │ 原子三件套
[Store: 文件系统 (tmp + rename)]
   Progress · Checkpoints · Outline · Drafts · Summaries · Characters · World · Signals
```

| 层 | 做什么 | 不做什么 |
|---|---|---|
| Entry | 展示、接收输入 | 业务决策 |
| Host | 启动/恢复/干预/事件投影/Flow 路由 | 绕过 Coordinator 直接调 SubAgent；写状态 |
| Coordinator | 执行 Host 指令、裁定用户 Steer、启动选规划师 | 自行决定每章下一步；写文件 |
| Agents | 思考、写作、审阅 | 直接读写 Store |
| Tools | 原子 IO + checkpoint + 幂等 | 跨子代理调度指令 |
| Store | 文件系统落盘 | 业务逻辑 |

依赖单向：`entry → host → agents → tools → store → domain`。`tools/` 不引用 `agents/host/`，`host/` 不直接引用 `tools/store/`。横向独立模块：`errs/` 可被任何层引用，`diag/` 订阅 host 事件流 + 只读 `store/`。

---

## 4. 数据模型

### 4.1 Progress（`internal/domain/runtime.go`）

```go
type Progress struct {
    NovelName         string
    Phase             Phase           // init / premise / outline / writing / complete
    CurrentChapter    int
    TotalChapters     int
    CompletedChapters []int
    TotalWordCount    int
    ChapterWordCounts map[int]int
    InProgressChapter int             // 正在写作的章节
    Flow              FlowState       // writing / reviewing / rewriting / polishing / steering
    PendingRewrites   []int
    StrandHistory     []string        // dominant_strand 序列
    HookHistory       []string        // hook_type 序列
    CurrentVolume, CurrentArc int     // 长篇分层
    Layered           bool
}
```

控制逻辑只读上述事实字段，不依赖任何"更新时间戳"——时间信息由 checkpoint 的 `OccurredAt` 承载。

### 4.2 Checkpoint（`internal/domain/checkpoint.go`）

```go
type Scope      struct { Kind ScopeKind; Chapter, Volume, Arc int }
type Checkpoint struct {
    Seq        int64       // 单调自增
    Scope      Scope       // chapter / arc / volume / global
    Step       string      // plan / draft / commit / review / arc_summary / ...
    Artifact   string
    Digest     string
    OccurredAt time.Time
}
```

存储：`meta/checkpoints.jsonl`，只追加。重复写入相同 `Scope+Step+Digest` 视为幂等不产生新行。

### 4.3 Artifact 与 Signals

Artifact 在 `store/outline.go` `drafts.go` `summaries.go` `characters.go` `world.go` —— 每种产物都能被 checkpoint 引用。

Signals：`PendingCommit`（commit 中断恢复）/ `PendingSteer`（停机期间用户干预）。启动/恢复时读，运行时不读。

### 4.4 分层大纲与完本收敛（收官卷）

滚动规划（compass 锚点 + 卷骨架 + 弧按需展开）解决"开与滚"，但让"何时结束"从一个数字变成每卷末的开放裁定——完本收敛必须显式设计，否则出现两类僵局：账面写完收不了尾（越界续写死循环，已由结构兜底修复）与叙事写完账面不让停（estimated_scale 高估 + 完结门槛硬否决 → 注水或熔断）。

**收官卷是收敛的一等概念**，完本 = 一次方向裁定 + 一段确定性滑行：

- **宣告（LLM 语义裁定）**：架构师在卷末三选一——append_volume（继续）/ append_volume 带 `"final": true`（收官卷：整卷以收线为目标，open_threads 与活跃伏笔全部分配进各弧）/ complete_book（条件当下全满足）。estimated_scale 在完结判定里是**证据不是否决权**：语义条件已满足而规模未达 → 宣布收官卷提前收束并下调 scale，禁止注水。
- **执行（代码事实查表）**：收官事实 = `domain.FinaleVolume`（最后一卷带 Final）。宣告后 `completion_signals.final_volume` 与 writer 信封 `finale` 纪律（禁开新线）随事实曝光；终卷结构写完（`layeredStructurallyComplete`）**且卷末收尾三连齐备（弧评审/弧摘要/卷摘要，`finaleWrapped`）**即自动 MarkComplete，**不再要求伏笔/长线归零**——但完结不抢在 editor 质量闸之前，结局必须过末弧评审。完结检查发生在"最后一块事实落地"的工具里：正向主路径为 `save_volume_summary`（卷摘要是三连最后一块），返工 drain 后三连已齐时为 `commit_chapter`。未宣告的书仍走质量级 `layeredBookComplete`（伏笔+长线归零），防大纲耗尽处过早收尾。
- **解除（数据推导，无撤销工具）**：宣告后又追加未标记新卷 → 新卷成为最后一卷，收束态自然解除。状态永远可从 layered_outline 推导，无跨层状态。
- **分歧出口**：Coordinator 认为故事已到终点而 Host 仍派单时，路由到 architect 走完结裁定（coordinator.md"完结分歧"），不允许以 end_turn 表达立场（StopGuard 会拦截至熔断）。

---

## 5. 工具规约

工具是事实层与 Agent 的唯一交互点。

### 5.1 读类工具

`novel_context(scope)` / `read_chapter(n)` —— 任何时候可调用，不依赖前置状态，返回数据足够 LLM 独立决策。

### 5.2 写类工具（原子三件套）

每次成功调用必须：artifact 落盘 → Progress 推进 → checkpoint 追加。三步互斥锁内完成。

| 工具 | Artifact | Step |
|---|---|---|
| `plan_chapter` | drafts/chXX.plan.json | plan |
| `draft_chapter` | drafts/chXX.draft.md | draft |
| `edit_chapter` | drafts/chXX.draft.md | edit |
| `check_consistency` | 无（只读，inline 返回） | consistency_check |
| `commit_chapter` | chapters/chXX.md + Progress | commit |
| `save_review` | reviews/chXX.json（global 为 chXX-global.json） | review |
| `save_arc_summary` | summaries/arc-vNNaNN.json | arc_summary |
| `save_volume_summary` | summaries/vol-vNN.json | volume_summary |
| `save_foundation` | foundation/*.json | premise / outline / layered_outline / characters / world_rules / expand_arc / append_volume / update_compass / complete_book |

`commit_chapter` 承担弧/卷/全书完成检测，返回 19 个事实字段（`arc_end` / `needs_expansion` / `book_complete` 等；启用机械规则检查时再附 `rule_violations`）。`save_review` 承担 verdict 升级（评分卡门禁、契约 missed → rewrite）。这些过去散落在 policy 层的逻辑现在固化在工具内部。

`edit_chapter` 是 `agentcore.EditTool` 的薄封装，归属检查保证已完成章节必须在 `PendingRewrites` 中才能编辑。

### 5.3 错误分层

| 错误类型 | 处理层 | 动作 |
|---|---|---|
| 网络超时 / 流式 EOF | Tools | 重试 3 次 |
| provider 429/503 | litellm | failover 到备用 provider |
| 鉴权 / 模型不存在 | Tools | terminal 上抛 |
| 缺前置 artifact | Tools | conflict 上抛，LLM 调 `novel_context` 后重试 |
| 工具参数非法 | Tools | validation 上抛，LLM 改参数 |
| MaxTurns 耗尽 | agentcore | run 结束，Host 发 done |
| LLM 不合规消息（thinking-only stop 等） | agentcore (`llm/litellm.go` `convertMessages`) | 入栈兜底 + 出栈过滤；Host 不感知 |
| 流式空响应 / 长思考 | litellm (`StreamIdleTimeout=5min`) | watchdog 触发重试 |

### 5.4 幂等

每个写类工具执行前先检查 checkpoint：如果当前 scope 最新 checkpoint 的 `Step+Digest` 与本次相同，直接返回已有产物。LLM 可以放心重试，不会产生重复章节或错位进度。

---

## 6. Agent 装配

> 单一超大 Prompt + 单一 Agent 跑完一本书理论可行，但三件事会阻塞稳定性：**上下文爆炸**（200 章再强压缩也退化）、**职责干扰**（规划严谨 / 写作想象 / 审阅批判在同一 prompt 互相冲淡）、**模型异构红利损失**（规划用 Opus、写作用 Sonnet、审阅用 Pro，独立选模型在长篇上是显著的成本/质量优化空间）。多 agent 拓扑因此必要。

### 6.1 Coordinator

唯一的主循环驱动者。装配在 `internal/agents/build.go`：

```go
agent := agentcore.NewAgent(
    agentcore.WithModel(coordinatorModel),
    agentcore.WithSystemPrompt(bundle.Prompts.Coordinator),
    agentcore.WithTools(subagentTool, contextTool),
    agentcore.WithMaxTurns(100_000),
    agentcore.WithToolsAreIdempotent(true),
    agentcore.WithMaxToolErrors(0),  // subagent 不熔断
    agentcore.WithMaxRetries(subagentMaxRetries),
    agentcore.WithContextManager(...),
    agentcore.WithStopGuard(reminder.NewStopGuard(store, nil)),
    agentcore.WithToolGate(completePhaseGate(store)),  // phase=complete 硬拦 subagent 派发
)
```

职责：启动时选规划师 → 规划补齐循环 → 收 `[Host 下达指令]` 立即生成对应 `subagent` tool_call → 处理 `[用户干预]` 自主裁定 → `book_complete=true` 后输出总结。

不做：写文件、直接读 Progress（用 novel_context）、Host 指令到达时自行决定下一步。

> **为什么不删掉 Coordinator 让 Host 直接调子代理？** 看起来更"干净"，但会失去四样东西：(1) "下一步做什么"的决策保留在 LLM 层，模型升级直接受益；(2) 评审 verdict 的软判断（accept/polish/rewrite + 影响范围）从 Go 代码移出去；(3) 用户 Steer 的影响评估交给模型——一句"配角动机要更清晰"该重写哪几章，Coordinator 能判断、Host 硬编码不行；(4) 异常分支（writer 大纲反馈、editor 发现世界观漏洞）由模型自己处理，避免为每个分支写 Go 状态机。**删掉 Coordinator 等于把赌注从"模型越来越强"换成"我的 Go 代码越来越强"——这不是好赌注**。

### 6.2 子代理拓扑与模型异构

```
Coordinator (1 agent run, MaxTurns=100_000)
    ↓ subagent()
architect_short/long  ·  writer  ·  editor
    ↓ 工具调用
Store (协作媒介，子代理之间不直接通信)
```

子代理 turn 计数独立（agentcore 原生），不占 Coordinator 的 100_000 turn 配额。子代理之间通过 Store 中的结构化工件通信，Coordinator 只传"任务描述"不搬内容。

`bootstrap.ModelSet` 支持角色级模型：coordinator/architect/writer/editor 各自独立配置 + provider failover。Writer 跑 Sonnet 而不是 Opus 在 200 章长篇上能省一个数量级成本。

### 6.3 三类协作模式

子代理之间不直接通信，所有信息流经 Store 中的结构化工件。三类模式覆盖系统的全部工作流：

**模式 A · 串行移交（主干）**：Coordinator → Architect 规划 → Writer 章 1..N → Editor 弧末评审 → Writer 重写。最常见的模式，Coordinator 通过 `novel_context` 查当前状态判断下一步调谁。

**模式 B · 审阅反馈（闭环）**：Writer 在 draft 中发现大纲偏离 → `commit_chapter` 返回值携带 `writer_feedback` 字段 → Coordinator 看到反馈判断是否升级为 architect 调用调整大纲。Writer 不直接呼叫 Architect，反馈通过结构化字段送回 Coordinator。

**模式 C · 骨架展开（滚动规划）**：`commit_chapter` 检测到下一弧仍是骨架 → 返回 `arc_end_reached + next_skeleton_arc` → Flow Router 派发指令 → Coordinator 调 architect_long 展开下一弧详细章节 → Writer 继续。长篇"滚动规划"能力就是这个闭环实现。

### 6.4 子代理流程的代码约束（不靠 prompt 拐杖）

> 早期 writer 流程靠 `writer.md` 的"严格按以下顺序推进"约束。LLM 经常违反——跳过 plan 直接 draft、commit 后继续多说一段消耗 token、把正文只写到聊天里不落盘。**提示词约束流程不稳定**，强弱完全取决于模型当下"听话程度"，模型升级反而可能让它"创造性地不遵守"。

四层代码约束（同时生效）：

| 层 | 落点 | 作用 |
|---|---|---|
| `StopAfterTools` / `StopAfterToolResult` | `agents/build.go` SubAgentConfig | 关键工具成功即 end_turn 退出 subagent run。Writer `commit_chapter` 命中即停（`StopAfterTools`）；Editor 的 `save_arc_summary`/`save_volume_summary`、Architect 弧/卷收尾走 `StopAfterToolResult`。Editor 的 `save_review` 不硬停——否则绕过 StopGuard 砍断弧摘要 run，收尾交 `NewEditorStopGuard` |
| `CheckpointDeltaGuard` | `host/reminder/subagent_guards.go` | 以 baseline checkpoint 为分界，本轮结束前必须看到对应 step 的新 checkpoint，否则拒绝 `end_turn`；连续拦 3 次升级 terminate（弱模型死循环兜底） |
| 工具内联 `next_step` | 各工具返回值字段 | 每个事实自带"下一步建议"。如 `plan_chapter` 返回 `next_step: "立即调用 draft_chapter..."`。LLM 看到事实就知道下一步，不用回到 system prompt 找 |
| 工具内归属/前置检查 | `edit_chapter` `commit_chapter` 等 | 数据层物理拦截：`edit_chapter` 拒绝改未列入 `PendingRewrites` 的已完成章；`commit_chapter` 拒绝草稿==终稿的空提交；`ConcurrencySafe=false` 阻止并发竞态 |

writer.md 在新架构里只承担：写作质量指南、断点续跑认知模型、章节契约解读。**不再做流程编排** —— LLM 跳步时 prompt 不会救场，代码会。architect / editor 同样的四层约束在各自工具/Guard 里。

> 关于铁律一：`next_step` 是工具内联的事实陈述（"我刚保存了 plan"），不是 Host 跨调用注入的流程编排。Coordinator 层的跨子代理调度仍严格走 Flow Router → Steer。

### 6.5 agentcore 依赖

`../agentcore` 是本项目自有的通用 Agent 库（go.work 关联）。新架构用到的原语全部已存在：`Prompt` / `Inject` / `Steer` / `Subscribe` / `WithMaxTurns` / `WithStopGuard` / `WithToolGate` / `WithMiddlewares` / `SubAgentConfig` / `WithContextManager`。

**修改边界**：

- 可进 agentcore：新 ContextManager 策略、新 provider 适配、新事件类型、通用消息注入模式
- 不进 agentcore：Progress/Checkpoint/Scope 等业务模型，novel_context/commit_chapter 等业务工具，弧结束检测/评审门禁等业务规则

判断准则：假设 agentcore 未来会被 coding agent / 客服 agent 引入，新加能力在那个场景下仍有意义才允许进。**禁止在应用层写兜底补丁**（代理、包装器、monkey patch）—— 缺能力直接去 agentcore 改。

**故意不用的能力**（避免误用）：

- `Agent.TaskRuntime() / Tasks() / StopTask()` — agentcore 内置的后台任务管理器（fire-and-forget background subagent）。新架构所有子代理调用都是前台同步的，**不使用**
- `Agent.Steer(msg)` — `flow.Dispatcher` 的流程指令通道，用于把 `[Host 下达指令]` 注入正在运行的 Coordinator run；必须在同步工具边界触发，保证工具结果后、下一次模型调用前送达
- `Agent.FollowUp(msg)` — 空闲后续消息通道，不用于 Flow Router；它只在 agent 准备自然停机时排空，拿来下达主流程指令会造成指令晚到
- `Agent.Inject(msg)` / `InjectContext` — 用户/外部干预入口：运行中写入 steering 队列，空闲且可继续时自动恢复 run；Host 的 `Steer(text)` 走它，Resume 走 `Prompt` 启新 run
- `WithPermission*` — 权限审批机制（人工 approve 危险操作），小说应用没有危险操作，**不使用**

**已启用的策略 hook**：`WithToolGate` —— 唯一用途是 `phase=complete` 时硬拦 `subagent` 派发（`agents/build.go` `completePhaseGate`）。完结后用户若请求续写/重写，Coordinator LLM 仍可能自派子代理，而 Writer 写越界章会被 `commit_chapter` 拒绝、`CheckpointDeltaGuard` 又不放行 `end_turn` → 死循环。Flow Router 在 complete 时返回 nil 只挡住 Host 自动派发，挡不住 LLM 主动派发，故由 Gate 在咽喉点补一道终态防护。它是窄用途的流程兜底，**不是 `WithPermission*` 那种审批流**，两者勿混。

---

## 7. Host 层

### 7.1 结构

```go
type Host struct {
    cfg               bootstrap.Config
    bundle            assets.Bundle
    store             *store.Store
    models            *bootstrap.ModelSet
    coordinator       *agentcore.Agent
    coordinatorCtxMgr *corecontext.ContextEngine  // 切模型时联动上下文窗口
    askUser           *tools.AskUserTool
    writerRestore     *ctxpack.WriterRestorePack

    observer     *observer
    router       *flow.Dispatcher  // 同步工具边界 + Route + Steer
    usage        *UsageTracker
    usageCancel  context.CancelFunc
    budget       *BudgetSentinel   // Host 政策组件：执行用户预算声明（等同代为 Abort），同步边界先于 Dispatcher
    pauser       *PausePointSentinel // Host 政策组件：执行用户验收停靠点（§8.4），边界顺序 budget → pauser → Dispatch
    notifier     *notify.Notifier  // 观察层：run_end/repeat/budget/pause_point 告警的离屏副本，永不介入控制流

    events, streamCh, done chan ...

    mu        sync.Mutex
    lifecycle lifecycle  // idle / running / paused / completed
    closeOnce sync.Once
}
```

### 7.2 公开 API

**生命周期**（Coordinator 的 Run 入口）：`Start` / `StartPrepared` / `Resume` / `Continue` / `Steer` / `Abort` / `Close`

**观察通道**：`Events` / `Stream` / `Done`（清空流走 streamCh 内 sentinel）

**UI 聚合**：`Snapshot()` —— TUI 一次拉取所有展示数据

**配置/扩展**：模型管理（`SwitchModel`）、外部小说反推导入（`ImportFrom`）、共创对话（`CoCreateStream`）、事件回放（`ReplayQueue`）、仿写画像（`Simulate`/`ImportSimulationProfile`）、导出（`Export`）

无 `decideNext` `retryActiveTask` 等业务调度方法。Flow Router 是纯函数 + Steer 派发的薄组合，不持有"正在重试的任务"之类的隐式状态。

### 7.3 `waitDone` 形态

```go
func (h *Host) waitDone() {
    h.coordinator.WaitForIdle()
    h.observer.finalize()

    if Phase == Complete { lifecycle=completed; 发"创作完成"事件 }
    else if running        { lifecycle=idle;     发"Coordinator 停止 (已完成 N 章)"事件 }

    select { case h.done <- struct{}{}: default: }
}
```

三件事：等待 idle → 切换 lifecycle → 发终态事件 + 投递 done 信号。**禁止 `Inject` / `FollowUp` / `Prompt` 出现在函数体**。LLM 跑完一次 Run 后整个 Host 进入终态。

要再动起来只有两种方式：用户主动 `Continue`/`Start`，或重启进程走 `Resume`。

> 历史教训：曾经在此函数加过 `idleResumeCount` 自动重启 Run 的补丁。在唯一一次实际触发的 mimo 长跑里 100% 没救场，反而掩盖了 agentcore 层"thinking-only stop 消息进历史"的真因。**Host 层的"防御性重启"永远是错位修复**。详见 `feedback_no_host_resilience.md` 与 §10 第 5 条。

---

## 8. 启动与恢复

### 8.1 新建

```
User: "一句话需求"
  → Host.Start
    → store.Progress.Init / store.Checkpoints.Reset
    → coordinator.Prompt(userPrompt) + flow.Dispatcher.Enable + Dispatch
    → Coordinator long loop: 规划 → 写 1..N → 审阅 → done
```

### 8.2 恢复（崩溃后重启）

```
进程启动
  → 读 Progress + 最近 Checkpoint + PendingCommit + PendingSteer
  → buildResumePrompt → 短通告（不是 step 级指令）
  → coordinator.Prompt(resumePrompt) + Dispatcher.Enable + Dispatch
  → Coordinator 按 Host 指令继续
```

Resume 用 `Prompt` 启新 Run（turn 计数重置、context 清洁），不是 `FollowUp`。恢复后的首条明确步骤由 Flow Router 立即 `Dispatch`，后续步骤在子代理工具成功返回的同步边界派生。

### 8.3 用户干预

| 入口 | 前缀 | 语义 | 实现 |
|---|---|---|---|
| `Steer(text)` | `[用户干预]` | 修改/查询，需 Coordinator 裁定 | 运行中走 `Inject`；停机写 PendingSteer 到 `meta/run.json` |
| `Continue(text)` | `[用户干预]` | 续写、停机后唤醒 | 运行中走 `FollowUp`；停机走 `Inject` 自动恢复 run |

两入口统一经 `interventionMsg` helper 加 `[用户干预]` 前缀——它是 `coordinator.md` 干预分类的锚点；曾经 Continue 发裸文本会绕过分类、被误派 writer 改已写章（已修）。

`Inject` 语义：运行中插队当前 run 队列；空闲时自动恢复 run 并注入；暂停时排队等候恢复。

**长效干预的持久层**：干预分类里"怎么写"的长效要求（写作风格/质量规则）由 Coordinator 调 `save_user_rules` 经 LLM 归一化、合并进本书规则快照 `meta/user_rules.json`，`novel_context` 注入 `working_memory.user_rules`——所有子代理每章自动看到，跨压缩、跨重启生效，不依赖 Coordinator 对话记忆与派单转达（详见 [用户规则快照](user-rules-runtime.md)）。其余三类干预出路本就落 store（篇幅/剧情/结构→compass/outline，设定/人物→foundation，改旧章→PendingRewrites）。走信封不走 system prompt：保护 writer 跨章 system 前缀缓存。

> 历史：早期"风格类长效要求"走独立的 `save_directive` → `meta/user_directives.json`（带 at_chapter 进度锚点）。2026-06-28 与 `save_user_rules` 合并——两者在自由文本偏好上重叠、"带不带锚点"是道模糊分类题，故砍掉 `save_directive`，长期写作要求统一归 user_rules；旧 `meta/user_directives.json` 不再读取或迁移。真正绑定剧情进度/结构的需求归 architect，不再用文本指令承载。

### 8.4 用户停靠点

用户干预"重写某几章"通常意味着正在交互精修：改完立刻续写会建立在未验收的状态上（改不满意则新章连带返工）。停靠点让这类**有边界的干预**在完成后自动暂停等验收：

| 环节 | 归属 | 实现 |
|---|---|---|
| 意图裁定（"纯重写"还是"改完继续写"） | Coordinator LLM | `coordinator.md` 干预分类；歧义默认设（宁停勿跑） |
| 意图落盘 | 工具 | `save_pause_point(after=rewrites_drained, reason)` → `RunMeta.PausePoint`（用户运行意图层，非创作事实层；`cancel=true` 取消） |
| 条件裁定 | 纯函数 | `flow.ResolvePausePoint`：队列未排空→保留；排空且写作中→消费并停机；排空但 phase=complete→只消费（完本收尾优先，run 自然结束即验收点，防残留） |
| 执行 | Host 政策组件 | `PausePointSentinel.HandleBoundary` 在子代理边界（预算之后、Dispatch 之前）消费并 `abortWithEvent`，事件+notify（kind=pause_point）成对 |

停靠点**一次性**：命中即消费，Continue 恢复后不再触发。停机窗口里条件已满足但未及消费时（崩溃恰在排空后、预算/Esc 停机抢在边界前），由 Resume/Continue 恢复路径 `ReconcileOnResume` 对账解除（用户显式恢复=放行，解除时事件+通知成对）。已知窗口：设点后 editor 未及入队即停机，磁盘状态与"真排空"无法区分，对账连同解除——报告带诉求摘要，用户重新下达即可，不为极端窗口引入跨层状态。合宪定位与 BudgetSentinel 相同（§10.15）：不评估模型行为，只代为执行用户预先签署的暂停指令；StopGuard 不受影响——它拦的是 LLM 自行 end_turn，Host abort 不经过它。未来"写到第 N 章停/每卷停"只需扩展 `After` 枚举。

---

## 9. 目录结构

```
internal/
  domain/         纯数据：Phase / FlowState / Progress / Checkpoint / Scope / Story / Plan /
                  Review / StateChange / Phase-Flow 迁移规则
  store/          文件系统持久化（tmp+rename + 三件套）：progress / checkpoints / outline /
                  drafts / summaries / characters / world / signals / run_meta / runtime / session
  tools/          11 个 Agent 工具，写类全部原子三件套 + digest 幂等 + ConcurrencySafe=false
                  + premise_structure (save_foundation 内部用) + ask_user
  agents/         build.go 装配 Coordinator + 三子代理；ctxpack/ Writer 上下文压缩策略
  host/           host.go + resume.go + observer.go + events.go + usage.go + usage_replay.go
                  + stream_extract.go + cocreate.go
    flow/         router.go (纯函数 11 分支) + state.go + dispatcher.go + router_test.go
    reminder/     stop_guard.go (Coordinator) + subagent_guards.go (CheckpointDeltaGuard ×3)
    imp/          外部小说反推导入：split → foundation → 逐章分析
    exp/          已完成章节导出：合并章节 → TXT / EPUB 3，路径后缀驱动；纯只读，不依赖 LLM
  entry/          tui (Bubble Tea) / headless / startup
  bootstrap/      config + ModelSet + provider failover + setup 向导
  models/         OpenRouter 等公共模型注册表 + 价格刷新 (24h 磁盘缓存)
  errs/           错误分层
  diag/           订阅 host 事件流的只读诊断模块
  utils/          旧架构遗留（少量解析工具，新增代码不应依赖）

assets/
  prompts/        coordinator (~55 行) / architect-short|long / writer / editor / import-* / simulation-*
  references/     写作技巧 + 体裁模板 + 长篇规划等
  styles/         默认/奇幻/言情/悬疑

../agentcore     通用 Agent 框架（go.work 兄弟目录，可加通用能力，不加业务）
../litellm       LLM 网关
```

### 9.1 演进里程碑

| 时间 | 重构 | 净效果 |
|---|---|---|
| 2026-04-10 | `internal/orchestrator/` (6342 行) → `host/` + `agents/` | 运行时核心 -74% |
| 2026-04-20 | Hybrid Coordinator：新建 `host/flow/`，`reminder/` 瘦身，`coordinator.md` 88 行 → 45 行 | 路由错误率趋近 0 |
| 2026-05-02 | agentcore `WithMaxToolErrors(0)` + `isReasoningOnlyStopAssistant`；`StreamIdleTimeout=5min`；删除 `idleResumeCount` 续跑补丁 | mimo / 慢思考流式跑通 |
| 2026-06-05 | 滚动规划闭环（`expand_arc`/`append_volume`）+ `/import` 反推分层续写 + 用户篇幅干预 | 200+ 章首次跑通 |

实测：hy3-preview free 12 章 / 73 分钟、mimo-v2.5-pro 10 章 / 8.4 万字（章均 8400），均一次跑完；长篇 gpt-5.4《凡骨》235 章 / 127 万字 / 章均 5407，滚动规划闭环跑通。

---

## 10. 明确不做的事

违反即代表架构偏离。

1. **不引入 Task / Job / WorkItem 概念**。UI 显示的"当前任务"是事件流投影，不是事实。
2. **不引入 Dispatcher / Scheduler / Ready Evaluator**。决策权在 Coordinator LLM 与工具层。
3. **不做 `idle_dispatch` 类的"空闲续跑"机制**。Coordinator Run 结束 = Host 发 done。
4. **不在 Host 绕过 Coordinator 直接调 SubAgent**。Flow Router 通过 `coordinator.Steer` 下达 `[Host 下达指令]`，让 Coordinator 生成 tool_call。Resume 用 `Prompt` 启新 Run。
5. **不在 Host 为 LLM 异常停机加自动续跑补丁**。Run 结束 = Host 进入终态。曾经的 `idleResumeCount` 已删除（详见 §7.3、`feedback_no_host_resilience.md`）。
6. **不基于"tool exec end"推断任务完成**。完成的唯一证据是 checkpoint 写入。
7. **不做 WorkflowInstance / TaskInstance / Command + Apply 等四层模型**。事实层只有 Progress + Checkpoint + Artifact 三类。
8. **不支持并行 task**。单活跃 Coordinator Run，单本书串行推进。多本小说请用多进程。
9. **不在工具层做 LLM 调用**（除 Agent 工具自身）。纯 IO + 校验 + 幂等。
10. **不让 UI 直接读 Store**。只能订阅事件或读 Host `Snapshot()`。
11. **不用信号文件做 IPC**。Host 直读 Progress + Checkpoint + 分层大纲，`flow.Route` 从事实派生指令是合理的垂类路由。
12. **不写 Host 端的 Flow 状态机**。Flow 标签只由工具更新，Router 只读不写。
13. **不为"LLM 幻觉"写兜底硬编码**。优化 prompt、改进工具返回值结构、让 `novel_context` 更清楚地呈现事实——而不是 Host 强制改流程。
14. **不让 diag / 观察层介入控制流**。诊断只读、只产 Finding 与脱敏导出；自动修复 / 续跑 / 改流程一律不做（见 §2.3 观察者纪律）。
15. **预算与告警不进 Route/工具层，告警不进控制流**。`BudgetSentinel` / `PausePointSentinel` 是 Host 政策组件（执行用户预先签署的 Abort/暂停，不评估模型行为）；`notify` 是纯观察（不重试、不改派、不停机）。`flow.Route` 保持纯函数，对两者无感知。

---

## 11. 验证策略

### 11.1 稳定性场景

- **A 长跑**：80~200 章一次跑完，Phase=complete。允许 provider failover、tools transient 重试；禁止 Host 续跑或 Coordinator 多次 Run。
- **B 崩溃恢复**：第 N 章 draft 后 / commit 前 kill 进程 → Resume → 从 consistency_check 继续，不重写已落盘 draft。`checkpoints.jsonl` 无重复 step。
- **C provider 抖动**：模拟间歇 503 → litellm failover；LLM 主循环无感知。
- **D 用户干预**：运行时 Steer → Coordinator 下一 turn 处理；停机后 Steer → 下次 Resume prompt 包含。

### 11.2 合规性（可写成 linter / test）

- `internal/host/` 不允许 `import "internal/scheduler"` 之类调度包
- `host.go` 的生命周期 API 数量稳定；新增公开方法只能是"扩展入口"类（共创/导入/模型管理）
- `waitDone` 函数体内不允许 `coordinator.Inject` / `FollowUp` / `Prompt`
- `recovery` 相关代码只能出现在 `host/resume.go`
- `flow.Route` 必须是纯函数：禁止读 Store / 任何 IO

### 11.3 质量迭代

改 `writer.md` 立刻产生风格变化；新增 editor 评审维度向后兼容（save_review 接收结构化 JSON）。新增一篇参考资料 md 需三处接线（`tools.References` 字段 + `assets/load.go` 的 `loadReferences` + `novel_context.go` 的 `writerReferences`/`architectReferences` 注入），不是放进目录即自动加载——`References` 是显式字段映射，便于按角色/章节裁剪。

**全书级风格统计（`internal/stylestat`）**：弧内评审窗口对"句式 tic 章均几十次、章末形态同构、跨章逐字复读"这类全书级固化天然失明——单章看每处都正常。`novel_context` 章节路径对全部已完成章节跑确定性统计（句式模式类/近窗高频短语/跨章重复句/章末形态/标题格式混用），注入 `episodic_memory.style_stats`：editor 在 aesthetic 维度按数字裁定，writer 据此自避免。**统计归代码，裁定归 LLM**——阈值不写死在代码里，数字是否成病由模型按题材判断。与其并列的产品底线 `rules.Lint`（markdown 残留/非中文片段）在 commit_chapter 始终执行，仅返事实。

---

## 12. 总结

> **让 LLM 在一次 Run 里把一本小说写完，Host 只做启动 / 恢复 / 路由 / 观察，事实记录由工具原子落盘，决策权尽量留给模型。**

没有 workflow engine，没有 task queue，没有 dispatcher，没有 scheduler。有的只是：

- 一个 100_000 turn 的 Coordinator
- 三类职能子代理（context 与模型独立）
- 11 个原子工具
- 一个 jsonl checkpoint 文件
- ~860 行的 Host 外壳
- ~150 行的 Flow Router 纯函数（11 分支 + 单测）

每一行 Host 业务代码都是在跟模型升级对冲的押注。**最小 Host、最肥 Prompt（质量层）、最强工具** 让架构每年自动变得更好——Coordinator 决策更准、Writer 写得更好、Editor 评审更准、Architect 规划更精，全是直接换模型架构无感的收益。

反过来在 Host 里硬编码"上次 review 说要重写第 3、5 章"或"连续 3 次没进展就停机"这种规则，模型升级会让它变成**负收益**：本应 LLM 做的判断变冗余、保护逻辑变误报。**最糟的是没人敢删——删了就等于"相信模型"，心理上的包袱比代码更难清理**。这种代码留下越多，未来重构成本越高。

**扩展性来自对的扩展点**：改风格 → 改 prompt；新评审维度 → 改 prompt；新题材 → 加参考资料；新子代理类型 → 加一行 SubAgentConfig；并行多本小说 → 多进程。

唯一的纪律：**有人想"让 Host 更聪明一点"时，先问"为什么不让 LLM 更聪明一点"**。这个问题回答不出"Host 必须"的理由，就不要往 Host 里加代码。
