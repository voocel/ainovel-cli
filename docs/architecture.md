# ainovel-cli 运行时架构

> 事实层确定，语义层自主：一个串行确定性 Engine、三个自主 Worker、少数几个按需 Arbiter 函数、一个文件系统事实层。
>
> 2026-07-12 控制面更替完成：Coordinator LLM 长循环退役，由 Engine（确定性循环）+ Arbiter（语义裁定函数）接管。设计决策与评审记录见 `docs/engine-arbiter.md`，RFC 见 `docs/engine-rfc.md`。

---

## 1. 目标（按优先级）

1. **稳定性**：一句话输入，稳定写完整本小说（200~500 章）。中间不因架构问题自行中断。
2. **质量可迭代**：prompt / 参考资料 / 评审维度 / 上下文策略可独立调整，不牵连架构。
3. **可恢复**：崩溃、断网、暂停后能从最近 checkpoint 继续。
4. **可观测**：每章每 step 的进度、产物、用时可查。

"稳定"是前提，"质量"是上层。每个架构决策优先服务稳定性。

---

## 2. 核心原则

### 2.1 三分法：决策按性质归位

- **可枚举的状态迁移 → 代码**。"写完一章后派谁"是读事实查表：`flow.Route` 纯函数 + 万级组合穷举规格测试，错误率趋近 0、零 LLM 开销。
- **边界清晰的语义判断 → LLM 函数（Arbiter）**。选规划师、用户干预分诊、失败/僵局出路：事实进、结构化决策出、机械校验兜底、每次裁定落盘可回放。
- **开放式创作 → LLM 循环（Worker）**。一章、一次评审、一次规划之内，architect/writer/editor 完全自主。

两平面对称是贯穿性纪律——未来任何新决策点照此形状，不发明新模式：

```
确定性平面:  flow.LoadState   → flow.Route     → Instruction   (穷举规格测试)
语义平面:    arbiter.Collect* → arbiter.Decide* → XxxDecision   (decisions.jsonl + eval 回归)
              └── 事实采集(IO) ──┘└── 核心(可离线重放) ──┘└── Engine 执行 ──┘
```

### 2.2 工具是事实层唯一接口

所有与文件系统、Progress、Checkpoint 的交互都由工具完成。单个文件使用 `temp + fsync + rename` 原子替换；跨文件顺序写入不冒充数据库事务：章节提交使用持久化 `PendingCommit` Saga，结构写入使用确定性幂等重放并显式暴露失败。每一步都必须检查错误；只有已持久化恢复意图的流程才能承诺跨重启按原载荷恢复。

### 2.3 观察层只观察

UI、诊断、事件日志都是从事件流 / 只读工件投影出来的被动消费者。读事实，不产生事实，不影响控制流。

**`internal/diag` 是引擎唯一的可观测性子系统**——一等支撑设施，但不是产品核心。它跨读几乎所有工件 + session + log + checkpoint，承担两职：① **创作质量诊断**（规则 → Finding，`/diag` 屏上报告）；② **运行时排错 + 脱敏导出**（行为骨架剥正文 + 循环聚合 → 覆盖式 `meta/diag-export.md`）。

**观察者纪律（不可松动）**：diag 可以诊断、可以建议，但**永不自己动手**——不自动修复、不续跑、不改流程（历史教训见 §10 第 5 条）。

### 2.4 事实层扁平

只有三类事实：

- **Progress** — 进度索引（写到第几章、待重写列表）
- **Checkpoint** — step 级推进记录（plan / draft / commit / review / arc_summary）
- **Artifact** — 章节正文、大纲、角色、摘要等产物

不引入 WorkflowInstance / TaskInstance / Command 等抽象。附属事实（大纲反馈池、机械违规记录、裁定审计）同样是扁平 jsonl，各有唯一生产者与消费者。

### 2.5 四铁律

**铁律一：工具只返事实，不返跨调度指令**。`commit_chapter` 返回 `arc_end` / `needs_expansion` 等结构化字段；不夹带 `[系统]` 类指令字符串。子代理内的 `next_step` 字段是事实陈述的内联指引（"我刚保存了 plan，下一步是 draft"），不算违反——见 §6.3。

**铁律二：流程路由由 Flow Router 承担，执行由 Engine 承担**。`internal/flow/router.go` 的 `Route(state) → *Instruction` 是纯函数（万级组合穷举规格测试钉死）；Engine 每轮从 store 读事实、Route 推导指令、**直接程序化运行 Worker**（`subagent.Tool.Run`，类型化入参/结果/错误链），无 LLM 转发层。返回 nil 表示语义场景（完本收尾/等待干预）或自然停机。**僵局有显式限界**（RFC §5）：上一轮后 Route 仍产生同一 `Agent+Task`，即路由后置条件未满足；3 次咨询 Arbiter、5 次硬熔断暂停。Worker 内部中间 checkpoint 不重置计数，确定性 Engine 不允许无限空转。

**铁律三：语义裁定走 Arbiter，每次裁定落盘**。启动选规划师、用户干预分诊、失败/僵局出路由 `internal/arbiter` 的逐场景 Decide 函数裁定：事实进、结构化决策出、机械校验兜底、decisions.jsonl 审计（可离线重放回归）。三个 Worker 保留各自的 `CheckpointDeltaGuard`（事实护栏：产物未落盘不得收工）。

**铁律四：硬编码边界，不硬编码不可枚举的语义判断**。代码只固化可证明的不变量（权限、阶段、顺序、幂等、结构完整性）并向模型提供完整事实与足够的操作空间；创作取舍、质量判断、计划如何适应正文等开放问题必须留给 Worker / Arbiter。禁止用关键词、评分阈值、偏离枚举或规则表代替模型理解，也禁止因担心模型出错而缩窄其合法决策空间。新增代码规则前必须先证明决策空间封闭且结果可机械验证；否则应改善上下文与工具表达能力，让模型升级的收益无需改外壳即可兑现。

---

## 3. 架构全景

```
[Entry: TUI / headless]
        │ prompt / steer
[Host 外壳]
   ├── observer            Worker 进度中继 + Engine 派发事件 → UI/日志投影
   ├── engine              确定性循环：LoadState → Route → 前置校验 → 运行 Worker → 哨兵边界
   ├── 干预路径             Steer/Continue → Arbiter 裁定 → 动作执行(即时/边界提交)
   └── usage / 预算 / 停靠点 / 模型管理
        │ 程序化调用 subagent.Tool.Run（进度经 ctx ToolProgress 中继）
[architect_short/long · writer · editor]（各自独立 run + context + 模型）
        │ 工具调用
[Tools]  novel_context · read_chapter · plan_chapter · draft_chapter · edit_chapter
         check_consistency · commit_chapter · save_review · save_arc_summary
         save_volume_summary · save_foundation
        │ 单文件原子 + 幂等重放（commit 使用持久化 Saga）
[Store: 文件系统 (tmp + rename)]
   Progress · Checkpoints · Outline · Drafts · Summaries · Characters · World
   · Signals · Decisions(裁定审计) · 反馈池 · 违规记录
```

| 层 | 做什么 | 不做什么 |
|---|---|---|
| Entry | 展示、接收输入 | 业务决策 |
| Host/Engine | 生命周期、Route 执行、Worker 运行、哨兵边界、干预编排 | 文学判断；写创作事实（控制态动作经工具内核） |
| Arbiter | 语义裁定（结构化决策） | 亲自创作；执行动作 |
| Workers | 思考、写作、审阅 | 直接读写 Store（必须经工具） |
| Tools | 单文件原子 IO + 显式错误 + 幂等；commit 使用 Saga | 跨子代理调度指令 |
| Store | 文件系统落盘 | 业务逻辑 |

依赖单向：`entry → host → agents/arbiter → tools → store → domain`；`flow` 为顶层纯策略包（store 之上、host 之下）。横向独立：`errs/` 可被任何层引用，`diag/` 订阅 host 事件流 + 只读 `store/`。

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

RunMeta（`meta/run.json`）承载**用户运行意图**（非创作事实）：PlanningTier、PlanStart（启动裁定固化，规划期崩溃恢复的唯一依据）、PendingSteer（干预崩溃保护，单在途槽位）、AdvanceMode / AdvancePermitChapter（逐章验收政策与精确章节许可）、AdvanceHold（干预签署的一次性暂停）。`RunMeta.Init` 跨重启保留全部意图字段。

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

### 4.3 Artifact 与附属事实

Artifact 在 `store/outline.go` `drafts.go` `summaries.go` `characters.go` `world.go`。

- **Signals**：`PendingCommit`（commit 中断恢复）。启动/恢复时读，运行时不读。
- **Decisions**（`meta/decisions.jsonl`）：每次 Arbiter 裁定的审计记录（facts+input+decision），可离线重放；**不是恢复数据源**（恢复只依赖 Progress/Checkpoint/RunMeta）。
- **大纲反馈池**（`meta/outline_feedback.jsonl`）：writer 的 commit feedback 落盘（仅分层书），architect 下次结构操作经 novel_context 参考后清空。
- **机械违规记录**（`meta/rule_violations.jsonl`）：commit 时按 user_rules 检查的结果，editor 评审经 `novel_context(chapter=N)` 消费；best-effort 质量元数据，非与提交同级强一致。

### 4.4 分层大纲与完本收敛（收官卷）

滚动规划（compass 锚点 + 卷骨架 + 弧按需展开）解决"开与滚"，但让"何时结束"从一个数字变成每卷末的开放裁定——完本收敛必须显式设计，否则出现两类僵局：账面写完收不了尾（越界续写死循环，已由结构兜底修复）与叙事写完账面不让停（estimated_scale 高估 + 完结门槛硬否决 → 注水或熔断）。

**收官卷是收敛的一等概念**，完本 = 一次方向裁定 + 一段确定性滑行：

- **宣告（LLM 语义裁定）**：架构师在卷末三选一——append_volume（继续）/ append_volume 带 `"final": true`（收官卷）/ complete_book（条件当下全满足）。estimated_scale 在完结判定里是**证据不是否决权**。
- **执行（代码事实查表）**：收官事实 = `domain.FinaleVolume`。终卷结构写完（`layeredStructurallyComplete`）**且卷末收尾三连齐备（弧评审/弧摘要/卷摘要）**即自动 MarkComplete——完结不抢在 editor 质量闸之前。未宣告的书仍走质量级 `layeredBookComplete`（伏笔+长线归零）。
- **解除（数据推导，无撤销工具）**：宣告后又追加未标记新卷 → 收束态自然解除。状态永远可从 layered_outline 推导。
- **完结判定的派发**：卷末由 Route 分支 10 派 architect_long 走完结判定清单——完结裁定权在架构师（一个 Worker），不在控制面。

---

## 5. 工具规约

工具是事实层与 Agent 的唯一交互点。

### 5.1 读类工具

`novel_context(scope)` / `read_chapter(n)` —— 任何时候可调用，不依赖前置状态，返回数据足够 LLM 独立决策。`novel_context(chapter=N)` 额外注入该章机械违规（如有）；architect 路径注入已完成卷/当前卷弧摘要、角色快照、大纲反馈池与 foundation 状态。扩弧时，已发生内容是事实，骨架只是计划；Architect 可在 `expand_arc` 中同步修订目标弧的 title/goal 并展开章节。

### 5.2 写类工具（单文件原子 + 分级恢复语义）

单文件写入原子；跨文件步骤不承诺数据库式原子性。`commit_chapter` 的普通提交与返工提交共用 `PendingCommit`，按“完整意图 → artifact/状态 → Progress → checkpoint → 清除意图”推进；恢复只使用首次落盘的规范化 payload 与正文快照，禁止采用重启后模型重新生成的参数或被覆盖的 draft。`expand_arc` / `append_volume` 等结构操作没有持久化意图，只承诺同一参数的幂等重放、派生视图修复和错误显式返回。

| 工具 | Artifact | Step |
|---|---|---|
| `plan_chapter` | drafts/chXX.plan.json | plan |
| `draft_chapter` | drafts/chXX.draft.md | draft |
| `edit_chapter` | drafts/chXX.draft.md | edit |
| `check_consistency` | 无（只读，inline 返回） | consistency_check |
| `commit_chapter` | chapters/chXX.md + Progress（+ 反馈池/违规记录 best-effort） | commit |
| `save_review` | reviews/chXX.json（global 为 chXX-global.json） | review |
| `save_arc_summary` | summaries/arc-vNNaNN.json | arc_summary |
| `save_volume_summary` | summaries/vol-vNN.json | volume_summary |
| `save_foundation` | foundation/*.json（expand_arc/append_volume/update_compass 成功即消费反馈池） | premise / outline / layered_outline / characters / world_rules / expand_arc / append_volume / update_compass / complete_book |

`commit_chapter` 承担弧/卷/全书完成检测，返回 19 个事实字段；`save_review` 承担 verdict 升级（评分卡门禁、契约 missed → rewrite）。这些逻辑固化在工具内部。

`edit_chapter` 是 `agentcore.EditTool` 的薄封装，归属检查保证已完成章节必须在 `PendingRewrites` 中才能编辑。

### 5.3 错误分层

| 错误类型 | 处理层 | 动作 |
|---|---|---|
| 网络超时 / 流式 EOF | Tools | 重试 3 次 |
| provider 429/503 | litellm | failover 到备用 provider |
| 鉴权 / 模型不存在 | Tools | terminal 上抛 |
| 缺前置 artifact | Tools | conflict 上抛，LLM 调 `novel_context` 后重试 |
| 工具参数非法 | Tools | validation 上抛，LLM 改参数 |
| retryable（stream-idle 等） | subagent 层 | MaxRetries=7 就近重试，不出 Worker |
| Worker 失败（guard 升级/hard_stop 等） | Engine | 确定性错误直接暂停；其余同指令重试一次 → Arbiter 裁定 retry/reroute/abort |
| 僵局（同一路由指令连续重现） | Engine | 3 次咨询 Arbiter，5 次硬熔断暂停 |
| 流式空响应 / 长思考 | litellm (`StreamIdleTimeout=5min`) | watchdog 触发重试 |

### 5.4 幂等

每个写类工具执行前先检查 checkpoint：如果当前 scope 最新 checkpoint 的 `Step+Digest` 与本次相同，直接返回已有产物。重试与崩溃恢复后的重复派发都是安全的——这也是 Engine 恢复模型（读 store 续跑）成立的根基。

---

## 6. Worker 装配

> 单一超大 Prompt + 单一 Agent 跑完一本书理论可行，但三件事会阻塞稳定性：**上下文爆炸**（200 章再强压缩也退化）、**职责干扰**（规划严谨 / 写作想象 / 审阅批判在同一 prompt 互相冲淡）、**模型异构红利损失**（规划/写作/审阅独立选模型是显著的成本/质量优化空间）。多 Worker 拓扑因此必要。

### 6.1 装配与运行

`agents.BuildWorkers`（`internal/agents/build.go`）把三类 Worker 装配为一个可程序化调用的 `subagent.Tool`：Engine 直接调用其 `Execute({agent, task})`，每次调用是一个完整的 `agentcore.AgentLoop`（独立 context、独立模型、独立重试）。全部装配一次生效：角色模型 + failover、prompt cache key（每 spawn 自增 #seq）、ThinkingLevel、UsageRecorder/SessionLogger（OnMessage）、Writer ContextManagerFactory（窗口随 /model 切换自动重建）、RestorePack、StopGuardFactory、StopAfterTools。

Worker 进度中继走 **ctx 的 ToolProgress 回调**：Engine 以 `agentcore.WithToolProgress(ctx, relay)` 调 Execute，子代理的工具调用/流式正文/thinking/retry/context 事件经 relay 进入 observer——与 Coordinator 时代同一 ProgressPayload 形态，观察层复用。

```
Engine ── Execute({agent, task}) ──▶ architect_short/long · writer · editor
                                          │ 工具调用
                                        Store（协作媒介，Worker 之间不直接通信）
```

`bootstrap.ModelSet` 支持角色级模型：architect/writer/editor 各自独立配置 + provider failover。Writer 跑 Sonnet 而不是 Opus 在 200 章长篇上能省一个数量级成本。Arbiter 统一使用 Default 模型（经 usageTrackedModel 计费），当前不开放独立角色配置。

### 6.2 三类协作模式

Worker 之间不直接通信，所有信息流经 Store 中的结构化工件：

**模式 A · 串行移交（主干）**：Route 派 Architect 规划 → Writer 章 1..N → Editor 弧末评审 → Writer 重写。每一步"下一个派谁"由 Route 从事实推导。

**模式 B · 反馈闭环**：Writer 在 commit 中报告大纲偏离 → 反馈池落盘（仅分层书）→ Architect 下次结构操作经 novel_context 参考 → 操作成功即消费清空。Writer 不直接呼叫 Architect，反馈经事实层流转。

**模式 C · 骨架展开（滚动规划）**：commit 后事实显示下一弧仍是骨架 → Route（或 Engine precheck）派 architect_long 展开 → Writer 继续。长篇"滚动规划"能力就是这个闭环。

### 6.3 Worker 流程的代码约束（不靠 prompt 拐杖）

> 早期 writer 流程靠 `writer.md` 的"严格按以下顺序推进"约束。LLM 经常违反——跳过 plan 直接 draft、把正文只写到聊天里不落盘。**提示词约束流程不稳定**，模型升级反而可能让它"创造性地不遵守"。

四层代码约束（同时生效）：

| 层 | 落点 | 作用 |
|---|---|---|
| `StopAfterTools` / `StopAfterToolResult` | `agents/build.go` SubAgentConfig | 关键工具成功即退出 Worker run（终态退出仍咨询 StopGuard，见契约测试）。Writer `commit_chapter` 命中即停；Editor 的 `save_review`/`save_arc_summary`/`save_volume_summary`、Architect 弧/卷收尾走 `StopAfterToolResult` |
| `CheckpointDeltaGuard` | `agents/guard/subagent_guards.go` | 以 baseline checkpoint 为分界，本轮结束前必须看到对应 step 的新 checkpoint，否则拒绝 `end_turn`；连续拦 3 次升级 terminate（弱模型死循环兜底）。Editor 的 guard 任务感知：被派生成摘要时仅复核不算完成 |
| 工具内联 `next_step` | 各工具返回值字段 | 每个事实自带"下一步建议"，LLM 看到事实就知道下一步 |
| 工具内归属/前置检查 | `edit_chapter` `commit_chapter` 等 | 数据层物理拦截：改未入队的已完成章被拒、空提交被拒、`ConcurrencySafe=false` 阻止并发竞态 |

writer.md 只承担：执行协议、断点续跑认知模型、章节契约解读；写作标准在文风层（`{{VOICE}}` 占位回填，用户可覆盖，见 `docs/voice-layer.md`）。**这正是文风层敢开放给用户的前提：不变量住在工具层，prompt 随便改坏不了状态机。**

### 6.4 agentcore 依赖

`../agentcore` 是本项目自有的通用 Agent 库（go.work 关联）。Engine 用到的原语：`subagent.Tool.Run`（程序化直调，类型化结果与错误链——`errors.Is(err, subagent.ErrUnknownAgent)` 等分类不依赖错误文案）、`SetEventObserver` / ctx `ToolProgress`（事件中继）、`SubAgentConfig` 全家、`StopGuard`/`StopAfterTools`。

**修改边界**：可进 agentcore——新 ContextManager 策略、新 provider 适配、新事件类型；不进 agentcore——业务模型与业务工具。判断准则：假设 agentcore 未来会被 coding agent / 客服 agent 引入，新能力在那个场景仍有意义才允许进。**禁止在应用层写兜底补丁**——缺能力直接改上游。

**契约测试**（`internal/agents/agentcore_contract_test.go`，5 条，全部经 `Tool.Run` 驱动）：把本项目依赖的框架行为钉成可执行断言（终态退出咨询 StopGuard、Error/Aborted 不触达 guard、Escalate 错误链可 `errors.Is` 匹配、`Run` 的类型化 `ErrUnknownAgent` 等）。**bump agentcore 前必须全绿**——注释会过时，测试不会（这条纪律已经抓到过一次失效假设并省下一个 workaround）。

### 6.5 提示词缓存

长跑成本的第二杠杆（第一是模型选型）。完整讲解版见 `docs/prompt-cache-design.md`。三层分工：**litellm 只做协议翻译**，**agentcore 决定缓存放置与身份**，**ainovel 一行配置接入**。

缓存收益的前提是**请求前缀字节稳定**，由三条纪律保证（都在 agentcore）：

1. **tools 字节确定性** — Description/Schema 每次重建，任何 map 迭代都先排序
2. **历史 append-only** — 消息只追加不改写；上下文压缩是"付一次全 miss 换窗口"的显式交易，投影必须 `CommitOnProject`
3. **动态内容进尾部** — 信封/指令全部尾部追加，永不回写早期消息

配置为「一书一基、一角色一名、一会话一键」：OpenAI 系 `PromptCacheKey = nvl-<书哈希>-<角色>#<spawn序号>` 做路由亲和（默认只对官方端发送，中转可显式开启）；Claude 系 `CacheLastMessage: "ephemeral"` 滚动断点 + system 地板断点。**闩锁红线**：一切进缓存键的量会话内首算即冻结，宁陈旧不破缓存。断裂检测（`host/usage.go noteCacheBreak`）纯观测不修复，计数进 `usage.json cache_breaks` 与 TUI 缓存面板。

---

## 7. Engine 与 Arbiter

### 7.1 Engine 循环（`internal/host/engine.go`）

```
for {
    应用干预控制态动作(排空;hold+dispatch 先建立返工事实)
    advanceGate.HandleBoundary() // hold 消费 + review 许可对账
    inst := 干预派单 ?? Route(LoadState) ?? planStartFallback
    inst == nil → return          // 完本 / 语义停机,等 Continue
    precheck(inst)                // 原 ToolGate 的确定性化身:完本期丢弃派发;
                                  // writer 目标章未展开 → 改派 architect 展开
    advanceGate.Allow(inst)       // 仅阻断未获许可的正向新章
    trackDeadlock(inst)           // 同一 Agent+Task 连续重现:3 次问 Arbiter,5 次熔断
    runWorker(inst)               // subagent.Tool.Run + 进度中继 + DISPATCH 事件
    错误分类:确定性错误→暂停;首败重试一次;再败→Arbiter(retry/reroute/abort)
    政策边界:budget → advanceGate
}
```

单 goroutine 串行；`ctx` cancel = 暂停（checkpoint 保证无损）。**控制状态只在循环边界变更**：干预的 hold/reopen/dispatch 排队至边界提交（hold+dispatch 组合先执行派单建队列，再允许 Gate 消费 hold）；answer/rules 即时执行。`review` 模式只约束正向新章，不阻断返工、评审、结构维护与提交恢复。Arbiter 派单执行前做 Expect 对账（Phase/Flow/QueueHead 语义字段；CheckpointSeq 只审计不对账——干预时 worker 多在跑，seq 必变），不符则丢弃并把原始干预**同步**送回完整裁定路径重询。

### 7.2 Arbiter（`internal/arbiter/`）

四个场景，每场景一对 `Collect*Facts`（IO 边界）/ `Decide*`（除一次 LLM 调用外无 IO，可离线重放）+ 专属 Decision 类型（场景不匹配的动作在类型上不可表达）：

| 场景 | 触发 | 决策类型 |
|---|---|---|
| `plan_start` | 新书启动 | 选 short/long 规划师 + 扩充过短需求 |
| `intervention` | 用户干预 | answer/rules/hold/reopen/dispatch 组合（执行顺序由 Engine 固定） |
| `worker_failure` | Worker 报错且确定性分类无出路 | retry / reroute / abort |
| `deadlock` | 同指令反复无进展 | retry / reroute / abort |

失败路径：解析或校验失败带反馈重问（最多 3 次）→ 仍失败或模型调用失败时，干预显式回显真实错误且不产生写入，启动显式报错，failure/deadlock 保守暂停。**Arbiter 输出与一切 LLM 输出同样不可信**——`Validate` 按事实做机械校验（phase 约束、reopen 仅限完本、章节越界）是最后一道门。用量经 `usageTrackedModel` 进预算与 usage 系统。

### 7.3 Host 外壳（`internal/host/host.go`）

生命周期（`StartPrepared`/`Resume`/`Continue`/`Steer`/`Abort`/`Close`）、干预编排（FIFO 串行 + PendingSteer 崩溃保护）、事件投影、模型管理。观察通道 `Events`/`Stream`/`Done`，UI 聚合 `Snapshot()`，扩展入口（导入/导出/共创/仿写/模型切换）。

`runEnded`（engine.onDone 回调）按 store 事实定终态：Phase=Complete → completed + 确定性完本总结（不花 LLM 调用）；其余 → idle/paused。**禁止任何"自动续跑"逻辑出现在此**（历史教训 §10 第 5 条）。

---

## 8. 启动、恢复与干预

### 8.1 新建

```
User: "一句话需求"
  → StartPrepared(raw)
    → Progress.Init / Checkpoints.Reset
    → StartPrompt 固化进 RunMeta(输入事实先于裁定落盘)
    → Arbiter plan_start 裁定(选规划师+扩充需求) → 失败显式报错(审计带 error)
    → PlanStartRecord 固化进 RunMeta(裁定先落事实,再起执行)
    → engine.start(首条派单指令)
```

裁定失败不是死局:StartPrompt 已在,之后任何一次恢复/继续都会由引擎补裁(见 §8.2)。

### 8.2 恢复（崩溃后重启）

```
进程启动 → resumeLabel(纯 UI 标签) → 一致性告警 → AdvanceGate 对账
  → PendingSteer 存在 → 同步走干预裁定路径(干预先于续跑生效)后拉起引擎
  → 否则 engine.start(nil):只恢复事实,Route 从 store 重算续跑
```

没有会话需要恢复。规划期崩溃（裁定已落盘、首个 foundation 未落盘）由 `planStartFallback` 按 PlanStartRecord 续派，不重做已有裁定。若启动裁定**从未完成**（启动时模型故障），`planStartFallback` 依据 StartPrompt 现场补裁——这是首次裁定的重试，不违反"恢复不重新裁定"；补裁失败显式暂停告知，不允许无声停机。重复派发安全由工具幂等保证（§5.4）。

### 8.3 用户干预

`Steer`/`Continue` 统一走 Arbiter 裁定路径（`doIntervention`）：

```
持久化 PendingSteer(崩溃保护) → Collect facts → Decide(秒级)
  → 落 decisions.jsonl → answer 回显 / rules 即时落盘
  → hold/reopen/dispatch 入队边界提交(引擎停机时立即执行并视意图拉起引擎)
  → 全部动作成功 → 原子清除 PendingSteer(ClearHandledSteer)
```

崩溃保护是 **best-effort 单在途持久化**：首次 `SetPendingSteer` 失败会显式报错并停止裁定，绝不在无恢复记录时继续执行；裁定期、动作失败（保留待重放）、正常退出/Abort（defer 回存残留派单）受保护。仍有两个明确不保证的窗口——派单转入内存执行队列后被硬杀（毫秒级）、interMu 等待中的并发输入。用户在场可感知，重发成本秒级。

**长效干预的持久层**：写作风格/质量规则由裁定的 `rules` 动作经 `userrules.Service` 归一化进本书规则快照，`novel_context` 注入 `working_memory.user_rules`——跨压缩、跨重启生效（详见 [用户规则快照](user-rules-runtime.md)）。其余出路本就落 store（篇幅/剧情→architect 派单，改旧章→editor 入队 PendingRewrites，完本返工→reopen）。

### 8.4 章节推进控制

`ChapterAdvanceGate` 统一执行两种不同时间尺度的用户意图：

| 意图 | 来源 | 语义 |
|---|---|---|
| `AdvanceMode=review` + 精确 permit | `/review on`、`/next` | 持久政策：每个正向新章必须单独放行 |
| `AdvanceHold` | Arbiter intervention | 一次性意图：当前边界或返工排空后暂停 |

许可绑定章节号。只有目标章进入 CompletedChapters、PendingCommit 清空且 commit checkpoint 存在才消费，因此提交 saga 任一窗口崩溃都不会把同一许可用于下一章。详细不变量见 [Chapter Advance Gate](chapter-advance-gate.md)。

---

## 9. 目录结构

```
internal/
  domain/         纯数据：Phase / FlowState / Progress / Checkpoint / Scope / Story / Plan /
                  Review / StateChange / Phase-Flow 迁移规则
  store/          文件系统持久化（tmp+rename + 幂等协调；commit 有 Saga 阶段事实）：progress / checkpoints / outline /
                  drafts / summaries / characters / world / signals / run_meta / runtime /
                  session / decisions(裁定审计)
  tools/          11 个 Agent 工具，写类单文件原子 + 显式错误 + 幂等；commit 额外使用持久化 Saga
  flow/           路由策略（纯函数 + IO 边界）：router.go (Route 决策表) + state.go (LoadState)
                  + pause.go (停靠点裁定)
  arbiter/        语义裁定层（LLM-as-function）：plan_start / intervention / failure(deadlock)
                  逐场景 Collect/Decide 函数对 + 逐场景 Decision 类型 + 机械校验
  agents/         build.go 装配三个 Worker(subagent.Tool,Engine 程序化直调)；ctxpack/ Writer 上下文压缩策略
    guard/        subagent_guards.go (CheckpointDeltaGuard ×3,Worker 事实护栏)
  host/           host.go (生命周期/干预编排) + engine.go (确定性执行循环) + observer*.go
                  + events.go + usage*.go + budget.go + advance_gate.go + resume.go + cocreate.go
    imp/          外部小说语义编译导入：ingest → segment → analyze → synthesize → publish（纯状态推导 + LLM 作函数）
    exp/          已完成章节导出：TXT / EPUB 3；纯只读
  entry/          tui (Bubble Tea) / headless / startup
  bootstrap/      config + ModelSet + provider failover + setup 向导
  eval/           离线评测（prompt/voice A/B、回归）
  diag/ errs/ models/ notify/ rules/ userrules/ stylestat/ ...

assets/
  prompts/        arbiter-plan-start / arbiter-intervention / arbiter-failure / architect-short|long
                  / writer(协议模板,{{VOICE}} 占位) / editor / import-* / simulation-*
  voice.md        写作标准(文风层内置默认;三层覆盖见 docs/voice-layer.md)
  references/     写作技巧 + anti-ai-tone + 体裁模板等
  styles/         默认/奇幻/言情/悬疑(用户可覆盖/新增)

../agentcore     通用 Agent 框架（go.work 兄弟目录，可加通用能力，不加业务）
../litellm       LLM 网关
```

### 9.1 演进里程碑

| 时间 | 重构 | 净效果 |
|---|---|---|
| 2026-04-10 | `internal/orchestrator/` (6342 行) → `host/` + `agents/` | 运行时核心 -74% |
| 2026-04-20 | Hybrid Coordinator：新建 `host/flow/`，路由收归纯函数 | 路由错误率趋近 0 |
| 2026-05-02 | agentcore 慢思考/流式修复；删除 `idleResumeCount` 续跑补丁 | mimo / 慢思考流式跑通 |
| 2026-06-05 | 滚动规划闭环 + `/import` 反推续写 | 200+ 章首次跑通 |
| 2026-07-12 | **Engine + Arbiter 控制面更替**：Coordinator 长循环及七项补丁生态退役；文风层三层覆盖；五轮对抗评审加固 | 每边界省一次 LLM 转发；控制面 100% 离线可测；语义裁定可回放 |
| 2026-07-15 | **`/import` 语义编译管线**：硬编码切分规则退役，改为 ingest→segment→analyze→synthesize→publish 分阶段编译；纯状态推导（`NextAction(Facts)`）+ 输入指纹绑定工件，全程可恢复幂等 | 切分随模型能力自然增强；无漂移阶段枚举；中断可续、控制面离线可测 |

实测：hy3-preview free 12 章 / 73 分钟、mimo-v2.5-pro 10 章 / 8.4 万字，均一次跑完；长篇 gpt-5.4《凡骨》235 章 / 127 万字滚动规划闭环跑通（Coordinator 时代数据，Engine 时代首跑待补）。

---

## 10. 明确不做的事

违反即代表架构偏离。

1. **不引入 Task / Job / WorkItem 概念**。UI 显示的"当前任务"是事件流投影，不是事实。
2. **不在 Route 之外发明第二个调度器**。所有"下一步派谁"必须经 Route 决策表（穷举规格钉死）或 Arbiter 裁定（落盘审计），不允许散落的 if-else 派发。
3. **不做"空闲续跑"机制**。Engine 循环结束 = Host 进入终态；再动起来只有用户 `Continue` 或重启 `Resume`。
4. **不给 prompt 加行为规训**。需要行为护栏说明分层错了——不变量进工具前置条件，判断进 Arbiter，流程进 Route。
5. **不在 Host 为异常停机加自动续跑补丁**。曾经的 `idleResumeCount` 在唯一一次实际触发的长跑里 100% 没救场，反而掩盖了 agentcore 层真因（详见 `feedback_no_host_resilience.md`）。
6. **不基于"tool exec end"推断任务完成**。完成的唯一证据是 checkpoint 写入。
7. **不做 WorkflowInstance / Command + Apply 等四层模型**。事实层只有 Progress + Checkpoint + Artifact。
8. **不支持并行 Worker**。单活跃 Engine 循环，单本书串行推进。多本小说请用多进程。
9. **不在工具层做 LLM 调用**（除 Agent 工具自身）。纯 IO + 校验 + 幂等。
10. **不让 UI 直接读 Store**。只能订阅事件或读 Host `Snapshot()`。
11. **不写 Host 端的 Flow 状态机**。Flow 标签只由工具更新，Route 只读不写。
12. **不为"LLM 幻觉"写兜底硬编码**。优化 prompt、改进工具返回值、让 novel_context 更清楚地呈现事实。
13. **不让 diag / 观察层介入控制流**。诊断只读；自动修复 / 续跑 / 改流程一律不做。
14. **预算与章节推进政策不进 Route/工具层**。`BudgetSentinel` / `ChapterAdvanceGate` 是 Engine 边界的政策组件（执行用户预先签署的指令，不评估文学行为）；`notify` 纯观察。
15. **控制面改动必须先改穷举规格再改实现**；**bump agentcore 前必须过契约测试**。
16. **不做通用工作流 DSL、事件溯源、全局 State Digest**。Route 是一个领域一张表，泛化即过度设计。

---

## 11. 验证策略

### 11.1 测试资产清单

| 层 | 资产 | 覆盖 |
|---|---|---|
| 控制面规格 | `flow/router_exhaustive_test.go` | Route 决策表 12 万组合穷举 + 纯函数/确定性/守恒性质 |
| 框架契约 | `agents/agentcore_contract_test.go` | 5 条 agentcore 行为假设，经 `Tool.Run` 驱动（升级前必跑） |
| 引擎端到端 | `host/engine_test.go` | fake 模型 + 真实工具:写完整书 / 失败裁定 / 僵局裁定 / 返工验收时序 / boundary hold 即停 / 退出竞态保全 / 单许可单章节 |
| 裁定 | `arbiter/arbiter_test.go` | 解析/反馈重试/逐场景校验矩阵/事实采集 |
| 事实管道契约 | store/tools 测试 | 反馈池跨重启、违规记录 latest-wins/重写清除/novel_context 注入、PlanStart 跨 Init 保留 |
| 文风层 | `assets/load_test.go` | 拆分逐字节一致 / 三层覆盖语义 / eval 同组装路径 |
| 语义质量 | `internal/eval` + decisions.jsonl | prompt/voice A/B、裁定离线重放（回归集建设中） |

### 11.2 稳定性场景

- **A 长跑**：80~200 章一次跑完，Phase=complete。允许 provider failover、重试；禁止任何自动续跑。
- **B 崩溃恢复**：任意 step 后 kill 进程 → Resume → Route 从事实续跑，不重写已落盘产物，checkpoints 无重复 step。规划期崩溃走 PlanStartRecord。
- **C provider 抖动**：间歇 503 → litellm failover，Worker 无感知。
- **D 用户干预**：运行中 Steer → 秒级裁定回显、动作边界提交；停机 Steer → 裁定后按意图拉起；崩溃 → PendingSteer 重放。

### 11.3 合规性（可写成 linter / test）

- `flow.Route` 必须纯函数：禁止读 Store / 任何 IO
- `runEnded` 函数体内不允许出现任何启动引擎的调用
- 新裁定场景必须成对新增 Collect/Decide + Decision 类型 + 落盘
- recovery 相关代码只能出现在 `host/resume.go` 与 `engine.planStartFallback`

### 11.4 质量迭代

改文风 → 改 `<书目录>/style/`（用户级）或 assets/voice.md（内置），文风评测集 A/B 验证；新增评审维度 → 改 editor.md（save_review 结构化接收）；新增参考资料 → 三处显式接线（`tools.References` + `loadReferences` + novel_context 注入映射）。

**全书级风格统计（`internal/stylestat`）**：对全部已完成章节跑确定性统计（句式模式/高频短语/跨章重复句/章末形态），注入 `episodic_memory.style_stats`：editor 按数字裁定，writer 据此自避免。**统计归代码，裁定归 LLM**。

---

## 12. 总结

> **事实层确定，语义层自主。**模型自由在验证不可能的地方（写什么、怎么写、怎么判），被约束在验证可能的地方（顺序、幂等、阶段）。

没有 task queue，没有 policy engine，没有常驻会话。有的只是：

- 一个串行确定性 Engine 循环（~500 行，六条端到端路径钉死）
- 一张 Route 决策表（纯函数，12 万组合穷举规格）
- 四个 Arbiter 裁定函数（事实进、结构化决策出、落盘可回放）
- 三类职能 Worker（context 与模型独立，事实护栏零打扰）
- 11 个单文件原子、跨文件显式失败/幂等恢复的工具；其中 commit 使用持久化 Saga + 一个 jsonl checkpoint 文件

模型升级的收益流向何处一目了然：创作更好（Writer/Architect/Editor 的全部输出）、裁定更准（Arbiter 四场景）、摘要更好（ctxpack）——全部换模型即得，外壳一行不改。控制面不吃模型红利，因为**查表不需要智力**；它需要的是被证明正确，而它已经被证明了。

流程刚性是有意的、标了价的、留了门的：想放开 writer 的工具顺序 → 松一段协议 prompt（不变量在工具层兜底）；想按弧派发 → Route 加一行分支；想扩裁定能力 → 加一对 Collect/Decide。每一次松绑都有裁判（穷举规格、文风评测、decisions 回放）——**用证据决定给模型多少绳子，而不是用信仰**。

唯一的纪律：**有人想加一个决策点时，先过三分法——可枚举的进 Route，边界清晰的进 Arbiter，开放式的进 Worker**。三者都不是的决策，重新想清楚它是不是真的存在。
