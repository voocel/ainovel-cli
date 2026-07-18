# 重构提案：Hybrid Coordinator — Host 路由 × LLM 裁定

> **历史文档，已废弃。** Hybrid Coordinator 于 2026-07-12 被 Engine + Arbiter 架构取代；现行设计见 `docs/architecture.md`、`docs/engine-rfc.md`。本文仅保留决策演进记录，不得作为实现依据。
>
> 原状态：**已采纳并落地**（2026-04-20）
> 调研时间：2026-04-20
> 对应现行文档：`docs/architecture.md` §2 / §3 / §7 / §8 / §13 已同步更新
>
> **本文档是第二稿。**第一稿激进方案（完全删除 Coordinator）的问题详见附录 A，保留该节避免重走弯路。
>
> 落地结果：
> - `internal/host/flow/` 新建（router.go / state.go / dispatcher.go / router_test.go，15 个分支单测全通过）
> - `internal/host/reminder/` 删除 `flow.go` / `queue_guard.go` / `book_complete.go`；保留 StopGuard 与子代理 Guard
> - `assets/prompts/coordinator.md` 从 88 行压到 ~45 行（职责收窄到执行 Host 指令 + 裁定 + 启动选型）
> - `internal/host/resume.go` 大幅简化，只生成 label 与简短 prompt，具体下一步由 Router 在首次 TurnEnd 后派发
> - `internal/store/` 新增 `HasArcReview` / `HasArcSummary` / `HasVolumeSummary` / `CheckConsistency` 辅助方法
> - `observer.go` agent state 不再停在 working 的 bug 一并修复

---

## 1. 背景

### 1.1 项目定位

```
agentcore       — 通用 agent 框架
litellm         — 通用 LLM 网关
ainovel-cli     — 小说创作垂直 agent（本项目）
```

垂类 agent 的决策空间是**封闭的**：流程图固定，分支有限，事实驱动。通用 agent 的设计哲学（"押注模型能力"）套到垂类场景有过度纯粹的嫌疑。

### 1.2 用户目标（按优先级）

1. **稳定性** — 持续不断写下去，不因路由错误中断
2. **吃 LLM 升级福利** — 架构不对抗模型能力
3. **充分利用多 agent 能力** — 职能分工清晰

本提案在三者之间做**帕累托改进**（不牺牲任一目标换取另一目标）。

---

## 2. 现状调研

### 2.1 Coordinator 的决策点分类

逐条提取 `coordinator.md` 决策点：

| # | 决策点 | 性质 | 频率 |
|---|---|---|---|
| 1 | 启动时选 architect_long / short | 裁定（语义理解）| 一本书 1 次 |
| 2 | 输入扩展（<20 字自动补充）| 裁定（创作性）| 一本书 0-1 次 |
| 3 | 规划补齐循环 | 路由（事实驱动）| 1-3 次 |
| 4 | 每章 commit 后下一步 | **路由** | **每章 1-2 次** |
| 5 | 弧末评审分步执行 | 路由 | 每弧 3-5 次 |
| 6 | 评审 verdict 分叉 | 路由（已代码化，见 §2.3）| 每弧 1 次 |
| 7 | 用户干预处理 | 裁定（必须 LLM）| 任意 |
| 8 | 子代理报错重派 | 路由 | 偶发 |
| 9 | 全书完成输出总结 | 路由 | 1 次 |

**结论**：9 个决策点里 6 个是纯路由（查表），3 个是真正需要 LLM 的裁定。**路由发生频率远高于裁定**（每章 1-2 次 vs 一本书几次）。

### 2.2 Reminder 通道已经是流程代码化的半成品

`internal/host/reminder/` 下的生成器每轮根据事实生成**具体到动作的指令**：

- `flow.go` → `"当前 flow=writing，next_chapter=37。请直接调 subagent(writer, \"写第 37 章\")..."`
- `queue_guard.go` → `"当前 flow=rewriting，待处理队列：[3,5]。请立即调 writer 逐章重写..."`
- `book_complete.go` → `"全书已完成。请输出全书总结..."`

**当前架构存在 double dispatch**：
```
规则层：coordinator.md 定义"如果 A 则 B"
  ↓
Reminder 层：每轮根据事实把规则具体化 → 生成"现在请做 B"
  ↓
LLM 层：读 reminder 生成 tool_call（基本就是复述 reminder）
  ↓
SubAgent 执行
```

**LLM 实际上只是在"执行" Reminder 给它的指令**。这中间环节既消耗 tokens，又引入不确定性（LLM 可能不完全遵守 reminder，比如观察到的 mid 路由错误）。

### 2.3 工具层曾承担过多语义判断

- `save_review` 旧实现曾按固定评分阈值和 contract 状态覆盖 Editor verdict；现已删除，文学裁定归 Editor，工具只做协议校验与原子状态映射
- `commit_chapter.CheckArcBoundary()`：即时计算 `arc_end / needs_expansion / needs_new_volume`
- `commit_chapter.applyCompletion()`：即时判定 `book_complete`
- `CommitResult` 返回 17 个事实字段

**结论**：确定性的存储与阶段不变量留在工具层，文学和语义判断交给模型。

### 2.4 现状的实际成本

每章 Coordinator LLM 轮次：
- **每章 1-2 turns**（读 system prompt ~3000 tokens + reminder ~200 tokens + 历史 + CommitResult ~500 tokens → 生成 tool_call ~50 tokens）
- 200 章长篇约 **200-400 turns** Coordinator LLM 调用
- 其中 **~90% 是纯路由**（LLM 复述 reminder），**~10% 是裁定**

**每章 ~3500-7000 tokens 花在 Coordinator 决策上，95% 是冗余**（Reminder 已经算出答案）。

---

## 3. 设计方案：Hybrid Coordinator

### 3.1 核心思路

**把流程决策从 LLM 搬到 Host，但保留 Coordinator 作为裁定节点和指令执行通道**。

```
┌──────────────────────────────────────────────────────────┐
│                   Entry (TUI / headless)                   │
└────────────────────────────────┬─────────────────────────┘
                                 │ Start / Resume / Steer
┌────────────────────────────────▼─────────────────────────┐
│                            Host                            │
│                                                             │
│   ┌──────────────────────────────────────────────────┐     │
│   │  Flow Router（新增核心）                           │     │
│   │  ───────────                                      │     │
│   │  订阅 Coordinator 事件：subagent tool 返回时触发    │     │
│   │  纯函数：route(Progress, Checkpoint, Boundary)     │     │
│   │      → NextInstruction                             │     │
│   │  有指令 → coordinator.FollowUp(指令)                │     │
│   │  无指令（裁定场景）→ 不干预，让 LLM 自主            │     │
│   └──────────────────────────────────────────────────┘     │
│                                                             │
│   保留：生命周期 API / Observer / Usage Tracker             │
│   保留：resume.go（简化，不变核心逻辑）                       │
└────────────────────────────────┬─────────────────────────┘
                                 │
┌────────────────────────────────▼─────────────────────────┐
│                    Coordinator Agent (LLM)                  │
│                                                             │
│   职责收窄到两类：                                             │
│   1. 接收 Host FollowUp 指令 → 生成对应 tool_call             │
│   2. 用户 Steer 到达时自主裁定（查询/修改评估）                  │
│                                                             │
│   coordinator.md: 88 行 → ~25 行                             │
│   MaxTurns: 1000 保留（响应用户 steer + 执行 Host 指令）      │
└────────────────────────────────┬─────────────────────────┘
                                 │
                                 ▼
         ┌──────────────────────┼───────────────────────┐
         ▼                      ▼                       ▼
    ┌────────┐             ┌────────┐             ┌────────┐
    │Architect│             │ Writer │             │ Editor │
    └────────┘             └────────┘             └────────┘
```

### 3.2 职责重新划分

| 层 | 做什么 | 不做什么 |
|---|---|---|
| **Host / Flow Router** | 读事实 → 纯函数路由 → FollowUp 指令 | 自己调 SubAgent（仍通过 Coordinator）|
| **Coordinator** | 执行 Host 指令 + 裁定用户干预 + 启动时选规划师 | 自主决策"下一步做什么" |
| **SubAgent（A/W/E）** | 各自本职工作 | 无变化 |
| **工具层** | 原子落盘 + 返回事实 | 无变化 |

**关键不变性**：
- ✅ Coordinator 仍然是一个连续的 agent run，保留全书"连续感知"
- ✅ 用户 Steer 仍通过 `coordinator.Inject()`，立即打断能力保留
- ✅ SubAgentTool 仍由 LLM 调用（走 agentcore 原生路径），事件流 / ContextManager / 模型切换都不变
- ✅ agentcore 零修改

### 3.3 Flow Router 的具体逻辑

```go
// internal/host/flow/router.go

type NextInstruction struct {
    Agent  string   // architect_long / architect_short / writer / editor
    Task   string   // 给子代理的任务描述
    Reason string   // 给 Coordinator 看的理由（可选，方便调试）
}

type RouterState struct {
    Progress        *domain.Progress
    LatestCheckpoint *domain.Checkpoint
    // 分层模式的弧边界（上一章已完成时计算）
    LastCompleted   int
    ArcBoundary     *store.ArcBoundary
    HasArcReview    bool
    HasArcSummary   bool
    // 基础设定缺项
    FoundationMissing []string
}

// Route 返回下一步指令。返回 nil 表示让 Coordinator 自主裁定（裁定场景）。
func Route(s RouterState) *NextInstruction {
    p := s.Progress

    // 0. 终态：让 LLM 输出总结，不路由
    if p.Phase == domain.PhaseComplete {
        return nil
    }

    // 1. 规划阶段：裁定（选规划师）由 LLM 做，不路由
    if p.Phase != domain.PhaseWriting {
        return nil
    }

    // 2. 写作阶段
    // 2a. 重写/打磨队列优先
    if len(p.PendingRewrites) > 0 {
        ch := p.PendingRewrites[0]
        verb := "重写"
        if p.Flow == domain.FlowPolishing {
            verb = "打磨"
        }
        return &NextInstruction{
            Agent:  "writer",
            Task:   fmt.Sprintf("%s第 %d 章", verb, ch),
            Reason: fmt.Sprintf("PendingRewrites 队列剩余 %d 章", len(p.PendingRewrites)),
        }
    }

    // 2b. 审阅中：不路由，让 Coordinator 根据 save_review 结果走 verdict 分叉
    if p.Flow == domain.FlowReviewing {
        return nil
    }

    // 2c. 分层模式的弧末后处理
    if p.Layered && s.ArcBoundary != nil && s.ArcBoundary.IsArcEnd {
        b := s.ArcBoundary
        if !s.HasArcReview {
            return &NextInstruction{
                Agent:  "editor",
                Task:   fmt.Sprintf("对第 %d 卷第 %d 弧做弧级评审", b.Volume, b.Arc),
                Reason: "弧末评审未完成",
            }
        }
        if !s.HasArcSummary {
            return &NextInstruction{
                Agent:  "editor",
                Task:   fmt.Sprintf("生成第 %d 卷第 %d 弧摘要", b.Volume, b.Arc),
                Reason: "弧摘要未完成",
            }
        }
        if b.NeedsExpansion {
            return &NextInstruction{
                Agent:  "architect_long",
                Task:   fmt.Sprintf("展开第 %d 卷第 %d 弧（save_foundation type=expand_arc）", b.NextVolume, b.NextArc),
                Reason: "下一弧骨架待展开",
            }
        }
        if b.NeedsNewVolume {
            return &NextInstruction{
                Agent:  "architect_long",
                Task:   "评估并执行 save_foundation(type=append_volume) 或 mark_final",
                Reason: "卷结束需决定追加新卷",
            }
        }
    }

    // 2d. 正常续写
    next := p.NextChapter()
    return &NextInstruction{
        Agent:  "writer",
        Task:   fmt.Sprintf("写第 %d 章", next),
        Reason: "续写",
    }
}
```

**函数特性**：
- 纯函数（输入 RouterState，输出 NextInstruction）
- 可单测（给定状态，断言路由结果）
- **返回 nil 是合法的**——表示"这是裁定场景，请让 LLM 自主"

### 3.4 触发时机

Host 订阅 `agentcore.EventToolExecEnd` 事件：

```go
coordinator.Subscribe(func(ev agentcore.Event) {
    if ev.Type == agentcore.EventToolExecEnd && ev.Tool == "subagent" && !ev.IsError {
        // SubAgent 刚返回 → 读最新状态 → 路由
        h.flowRouter.Dispatch()
    }
})
```

```go
func (r *FlowRouter) Dispatch() {
    state := r.loadState()
    instruction := Route(state)
    if instruction == nil {
        return // 裁定场景，让 LLM 自主
    }
    msg := formatInstruction(instruction)
    _ = r.coordinator.FollowUp(agentcore.UserMsg(msg))
}

func formatInstruction(i *NextInstruction) string {
    return fmt.Sprintf(
        "[Host 下达指令] 下一步：调用 subagent(%s, %q)\n"+
        "理由：%s\n"+
        "这是流程层的明确指令，请立即执行，不要先调 novel_context，不要先输出推理。",
        i.Agent, i.Task, i.Reason,
    )
}
```

### 3.5 响应性与并发

**用户 Steer 路径**（无变化）：
```
Steer → coordinator.Inject(UserMsg("[用户干预] xxx"))
```

- 正在运行：消息插入当前 run 队列
- Idle：resume run
- Paused：排队

**路由指令 + Steer 的并发**：
- 都进入 Coordinator 的消息队列，按 agentcore 原生顺序处理
- 如果 Host 刚发 `FollowUp("[Host 指令] 写第 37 章")`，紧接着用户 Steer `"停一下，调整风格"`
  - Coordinator 先处理 Host 指令？还是先处理 Steer？
  - **`Inject` 的语义是插队到当前队列头部**，所以 Steer 先被处理
  - 这是期望行为：用户干预优先级高于 Host 例行调度

**避免 Host 指令与 Steer 冲突**：
- Flow Router 在收到"Steer 已注入"信号后**短暂暂停**几 turn（让 Coordinator 处理完 Steer 再路由）
- 通过订阅 `agentcore.EventMessageEnd` + 检查 Progress 状态变化感知 Steer 处理结果

### 3.6 coordinator.md 简化示例

从 88 行砍到约 25 行：

```markdown
你是小说创作总协调者。

## 你的工作模式

**主线**：Host 会在每次子代理返回后下达 `[Host 下达指令]` 消息，告诉你下一步调哪个子代理做什么。收到指令立即生成对应 tool_call，不要先调 novel_context 推理，不要复述。

**裁定**：遇到以下情况时你需要自主判断（Host 不会下达指令，你必须主动行动）：

### 启动时：选规划师

- 默认 → `architect_long`
- 仅当用户显式要求短篇/单卷/小品且篇幅限定在 25 章内 → `architect_short`

如用户输入 < 20 字，先在 task 描述里补充差异化方向、目标读者、至少一个非常规故事钩子，再派发。

### 用户 Steer

格式：`[用户干预] xxx`

- **查询类**（问状态/设定）：直接输出文字答案，无需再调工具；Host 会继续派发。
- **修改类**（要求改设定/重写/调整风格）：评估影响范围：
  - 涉及设定变更 → 调 architect_* 做 `save_foundation(type=...)`
  - 涉及已写章节 → 让工具自动把目标章节写入 `PendingRewrites`（可通过再次调 writer 时说明重写意图）
  - 仅影响后续风格 → 把要求简短描述后，下次收到 Host 指令时附加到 writer 的 task 描述里

## 工具

- `subagent(agent, task)`：调用子代理
- `novel_context`：仅在用户查询需要时使用，不要在 Host 指令到达后先调

## 子代理

- `architect_long` / `architect_short` / `writer` / `editor`

## 禁止

- 在 Host 指令到达时先调 novel_context 再行动
- 在没有用户 Steer 且没有 Host 指令的情况下自行决定下一步
```

### 3.7 Reminder 通道大幅瘦身

**删除**：
- `flow.go`（Host FollowUp 已经下达具体指令，Reminder 的路由提醒失去价值）
- `queue_guard.go`（队列由 Host Router 保证）
- `book_complete.go`（Host 在 Phase=Complete 时 FollowUp 输出总结指令）

**保留**：
- `subagent_guards.go`（Writer/Architect/Editor 的 StopGuard，确保子代理不空手结束）
- 新增一个轻量 `foundation_reminder.go`：规划阶段告知 Coordinator 缺项（这是**裁定需要的信息**而非路由指令）

**StopGuard 保留**：
- Coordinator 的 StopGuard 保留（`Phase != Complete` 时拦 end_turn 作兜底）
- 新增"收到 Host 指令但本轮未调对应 subagent"时注入提醒

### 3.8 resume.go 小幅简化

当前 `buildResumePrompt` 按 checkpoint 生成精确到 step 的自然语言指令（121 行）。

新架构：
- Resume 时先读 Progress，Flow Router 算出 `NextInstruction`
- Coordinator 收到一个**非常简短**的 resume prompt，然后等 Host 的 FollowUp 指令

```
[恢复] 本书「xxx」已完成 N 章，进入 XX 阶段。
请等待 Host 下一步指令，或处理可能在停机期间留下的用户干预。
```

几乎所有分支逻辑下沉到 Flow Router（Router 本来就要按状态路由，Resume 不需要特殊路径）。

---

## 4. 目标达成度评估

### 4.1 稳定性

| 风险 | 当前 | 新架构 |
|---|---|---|
| Coordinator 选错 architect | 发生过（mid 路由错）| 启动时仍是裁定，但 prompt 从三档变二元（已做），错误面大幅缩小 |
| Coordinator 不遵守"只说写第 N 章" | 发生过 | Host 下达固定格式指令，不再需要 LLM 生成 task 描述 |
| Coordinator 漏掉 queue_drained 检查 | 发生过 | Host Router 强制按顺序走 |
| 弧末 commit 后 Coordinator 忘记调 editor | 可能 | Host Router 检测到 IsArcEnd && !HasArcReview 直接派发 |
| 崩溃恢复分支遗漏 | 已知缺口 | Flow Router 的状态机天然覆盖所有分支 |
| StopGuard 连续拦 5 次升级 fatal | 存在 | Host 指令明确后 LLM 很难连续拦（除非 prompt 严重失灵）|

### 4.2 LLM 升级红利

| 维度 | 保留度 |
|---|---|
| Writer 模型升级 → 写作质量 | 100% |
| Editor 模型升级 → 评审准确 | 100% |
| Architect 模型升级 → 规划精致 | 100% |
| **Coordinator 模型升级 → 裁定更准** | **100%**（裁定场景保留）|
| ~~Coordinator 模型升级 → 路由更准~~ | 放弃（路由错误率本来就应该是 0，不需要 LLM 变聪明）|

**重要保留**：用户干预评估、规划师选型、verdict 边界判断等裁定场景仍由 LLM 处理，模型升级直接受益。

### 4.3 多 agent 能力

- SubAgent 数量、职能、装配方式**完全不变**
- 模型异构（coordinator/architect/writer/editor 独立配置）**完全不变**
- Coordinator 仍是连续 run，保留"全书视角"
- 协作媒介（Store 中的产物）不变

### 4.4 响应性

- 用户 Steer 通过 `coordinator.Inject` 打断的能力**完全保留**
- Host Router 在 SubAgent 返回时派发指令，和用户 Steer 走同一条消息通道
- Inject 的优先级高于 FollowUp（`Inject` 语义是插队），Steer 不会被 Host 指令挤掉

### 4.5 Token 成本

当前每章：Coordinator ~3500-7000 tokens × 1-2 turns = 3500-14000 tokens

新架构每章：
- Coordinator prompt 从 ~3000 tokens 压到 ~800 tokens
- 每章仍需 1 个 turn（Coordinator 读 FollowUp 指令 + 生成 tool_call）
- 总计 ~1000-1500 tokens

**节省 60-80%**。200 章长篇节省约 400k-1M tokens（不如激进方案的 100%，但不牺牲响应性和全书视角）。

---

## 5. 对 docs/architecture.md 的影响

### 5.1 §2 核心原则调整

**原则一**（LLM 驱动主循环）→ 调整为：
```
LLM 驱动创作与裁定，Host 驱动流程路由。

- 创作与裁定（需要语义理解、质量判断、意图识别的决策）仍留给 LLM
- 流程路由（读事实→查表→发指令）由 Host 代码承担
- Host 不绕过 Coordinator 直接调 SubAgent，而是通过 FollowUp 下达明确指令，
  保留 Coordinator 作为指令执行通道和裁定节点
```

**原则二**（押注模型能力，不押注硬编码）→ 调整为：
```
在创作与裁定维度押注模型（Writer/Editor/Architect/Coordinator 裁定能力），
在流程路由维度用代码表达（垂类 agent 的决策空间是封闭的，查表任务 LLM 无红利）。
```

### 5.2 §13 禁止列表调整

- §13.13 "不做 Host 读信号文件 → 注入下一步指令的确定性控制面" →
  **修正措辞**："不用信号文件做 IPC（直接读 Progress + Checkpoint 即可），Host 读事实后通过 `coordinator.FollowUp` 下达明确的子代理调用指令，是合理的垂类路由"
- §13.14 "不做状态机硬编码 Flow 迁移" →
  **修正措辞**："Flow 标签仍只由工具更新（不在 Host 里写'如果 A 则 SetFlow(B)'的状态机），但 Flow Router 可以根据 Flow 和其他事实决定下一步调谁"

### 5.3 §7 Agent 装配调整

- 保留 Coordinator 装配
- `coordinator.md` 从 88 行砍到 ~25 行
- Reminder 通道缩减（删 flow/queue_guard/book_complete，保留 foundation/subagent_guards）
- 新增 `internal/host/flow/` 包

---

## 6. 已知弱点（诚实列出）

### 6.1 Flow Router 的长期演化

- 随着新场景加入（新 flow 状态、新的弧末后处理），Router 的 switch-case 会变长
- 需要严格约束：**只处理路由，不处理业务逻辑**；决策规则写单测
- 类似 v0.0.1 `handleSubAgentDone` 的警示永远有效；但本方案通过"纯函数 + 单测 + 只调用纯事实"避免滑向上帝对象

### 6.2 用户干预的复杂性

- 当前设计把 Steer 完全交给 Coordinator 的 LLM 裁定
- 但某些 Steer 跨多个类别（如"把角色 A 前几章改清楚 + 后面给他加支线"）
- 需要依赖 LLM 的能力来拆解，prompt 需要给出清晰指引
- **这部分模型升级直接受益**（比起硬编码 InterventionAgent 的 enum 分类，LLM 灵活裁定更匹配真实场景）

### 6.3 事实层一致性的前置依赖

- Router 基于 Progress + Checkpoint 做决策，事实层必须可靠
- 单文件 `withWriteLock` + tmp/rename 保证原子替换；`commit_chapter` 跨文件步骤由 PendingCommit 完整载荷、正文快照和阶段化幂等重放恢复，结构操作则按同参数修复派生视图；均不宣称数据库式原子事务
- 但如果事实层出现不一致（如 Progress 说第 3 章完成但 chapters/ 下没有），Router 会做错决策
- 建议：启动时加一次**事实层一致性检查**（如发现 Progress.CompletedChapters 与 chapters/ 目录对不上，报 warning）

### 6.4 Coordinator 仍保留 LLM 路由可能性

- 即使指令明确，LLM 可能"创造性"地不执行（比如生成一段思考文字后才调工具）
- StopGuard 兜底：收到 Host 指令但本轮未调 subagent 则注入提醒
- 这是兜底，不是禁止——强模型偶尔的"多一步思考"也不是坏事

### 6.5 测试覆盖要求提高

- Flow Router 是纯函数，必须有完备单测（覆盖所有 Phase × Flow × Boundary 组合）
- 集成测试：模拟"commit → router → FollowUp → coordinator 响应 → subagent"的完整链路
- 崩溃恢复测试：kill 进程后 resume，断言 Router 推导出正确的下一步

---

## 7. 实施路线

### 阶段 1：事实层强化（约 0.5 天）

- 补齐 §6.3 的一致性检查：启动/Resume 时扫描一次，生成 warning
- 确保 `store.HasArcReview(vol, arc)` 和 `HasArcSummary(vol, arc)` API 可用（如没有就加）

### 阶段 2：引入 Flow Router 骨架（约 1 天）

- 新建 `internal/host/flow/` 包：
  - `route.go` — 纯函数 `Route(state) → *NextInstruction`
  - `dispatcher.go` — 订阅事件 + FollowUp 下达
  - `route_test.go` — 覆盖所有分支的单测
- 通过 config 开关 `flow_driven: true/false` 控制是否激活
- 默认关闭（false），先做对照运行

### 阶段 3：激活并验证（约 1 天）

- 打开 `flow_driven: true`
- 跑一本 30-50 章的小说，对比指标：
  - Coordinator LLM 调用次数
  - 路由错误数（应为 0）
  - 响应性（steer 打断是否正常）
- 修 bug，调整 Router 规则

### 阶段 4：coordinator.md 简化 + Reminder 瘦身（约 0.5 天）

- 按 §3.6 改 coordinator.md
- 删除 `reminder/flow.go / queue_guard.go / book_complete.go`
- 保留必要的 foundation reminder
- 更新 subagent StopGuard 如需（一般不用）

### 阶段 5：resume.go 简化（约 0.5 天）

- 删除 `buildResumePrompt` 的大部分分支
- 替换为简短通用的 "[恢复] 请等待 Host 指令" 消息
- Resume 后 Router 自然推导出继续的动作

### 阶段 6：架构文档更新（约 0.5 天）

- 按 §5 修改 `docs/architecture.md` §2 / §13 / §7
- 把本提案文档状态改为"已采纳"，归档到 `docs/history/`

### 阶段 7：观察期（2-4 周）

- 连续跑 2-3 本长篇（各 100+ 章）
- 记录所有路由错误（如有）、响应性问题、Coordinator 意外行为
- 根据观察微调 Router 规则和 coordinator.md

**总计约 4 天实施 + 观察期**。

---

## 8. 对比表

| 维度 | 当前架构 | Hybrid（本方案）| 激进方案（附录 A）|
|---|---|---|---|
| 稳定性 | 中（LLM 偶尔路由错）| **高** | 高 |
| 响应性 | 高 | **高** | **低**（Host 直调 SubAgent 无法打断）|
| LLM 红利 | 100% | **100%** | 85%（路由维度放弃）|
| Token 节省 | 0 | ~70% | ~95% |
| 全书视角 | 有 | **有** | 无（每次 SubAgent 独立）|
| 实施成本 | - | 中（约 4 天）| 高（约 1 周 + 改 agentcore）|
| 文档更新 | - | 小（§2/§13 微调）| 大（§2 原则重写）|
| 需要改 agentcore | - | 否 | 可能（直接调 SubAgent）|
| 回滚难度 | - | 低（config 开关）| 高 |

---

## 9. 决策点

1. **是否采纳本提案（Hybrid Coordinator）？** [ ] 采纳 · [ ] 修改后采纳 · [ ] 不采纳
2. 阶段 3 是否作为独立 PR 先落地验证？ [ ]
3. `docs/architecture.md` §2 / §13 调整是否一并在本次处理？ [ ]
4. 观察期长度：[ ] 2 周 · [ ] 4 周 · [ ] 更长

---

## 附录 A：已评估的激进方案（完全删除 Coordinator）

> 第一稿方案。因响应性退步、技术可行性存疑、Coordinator 全书视角丢失等问题降级为参考。

激进方案的核心：Host 直接调 `SubAgentTool.Execute`，不经过 Coordinator LLM。

**已识别的问题**：

1. **响应性倒退**：`SubAgentTool.Execute` 是阻塞同步调用，用户 Steer 必须等当前 SubAgent 返回后才能处理。当前架构的 `Inject` 可以立即打断。
2. **技术可行性存疑**：
   - Host 直接调 SubAgentTool 违反 agentcore 使用惯例
   - 事件流（`Subscribe` 的 Event）可能不会正确冒泡给 observer
   - SubAgent 的 `ContextManagerFactory` / `OnMessage` 回调路径未知
   - 需要改 agentcore 或大改 observer
3. **Coordinator 全书视角丢失**：每次 SubAgent 独立 run，没有"连续 LLM 守望者"。长跑中风格漂移、角色割裂等问题少了一层隐形守护。
4. **InterventionAgent 简化过度**：激进方案用 enum（query/modify_setting/rewrite_chapters/adjust_style/noop）分类用户意图，真实 Steer 可能横跨多类别，强制 schema 会误分类。
5. **架构文档重写工作量大**：§2 核心原则推翻，文档 30% 论述受影响。
6. **FlowDriver 会长成上帝对象**：一个循环塞所有路由逻辑，每加场景都要改，和 v0.0.1 `handleSubAgentDone` 同构。

Hybrid 方案规避了前 4 条问题，第 5 条降为微调，第 6 条通过"纯函数 + 单测"管控。

---

## 附录 B：决策点落位明细

| 决策点 | 当前位置 | 新架构位置 | 类型 |
|---|---|---|---|
| 选规划师 | coordinator.md L26-29 | Coordinator LLM 裁定（启动时）| 裁定 |
| 输入扩展 | coordinator.md L31 | Coordinator LLM 裁定（启动时）| 裁定 |
| 规划补齐循环 | coordinator.md L36-38 | Host Router Phase=Premise/Outline 分支（返回 nil 放 LLM 自主 or 显式 FollowUp architect）| 混合 |
| 每章下一步 | coordinator.md L46-51 + reminder/flow | **Host Router 2d 分支**（FollowUp writer）| 路由 |
| 弧末评审 | coordinator.md L78-82 | **Host Router 2c 分支**（FollowUp editor/architect）| 路由 |
| verdict 分叉 | coordinator.md L59-61 + save_review 工具 | 工具层已代码化，Router 只读 Flow | 路由（已完成）|
| 用户干预 | coordinator.md L67-70 | Coordinator LLM 裁定（收到 Inject 消息时）| 裁定 |
| 规划师报错重派 | coordinator.md L40 | Host Router 检测到 FoundationMissing 未变化，重试计数 | 路由 |
| 全书完成总结 | coordinator.md L63-65 + reminder/book_complete | Host Router 检测 Phase=Complete → FollowUp "输出总结" | 路由 |

---

## 附录 C：参考源码位置

- `assets/prompts/coordinator.md` — 待简化
- `internal/host/reminder/flow.go` / `queue_guard.go` / `book_complete.go` — 待删除
- `internal/host/reminder/subagent_guards.go` — 保留
- `internal/host/reminder/stop_guard.go` — 保留 + 加"收到 Host 指令必须执行"检查
- `internal/host/resume.go` — 大幅简化
- `internal/host/observer.go` — 新订阅 EventToolExecEnd 触发 Router
- `internal/host/flow/` — 新增包
- `internal/tools/commit_chapter.go` L220-280 — CommitResult 17 字段已完备
- `internal/tools/save_review.go` — Editor verdict 到 Flow/返工队列的原子映射
- `internal/store/outline.go` `CheckArcBoundary` — 弧边界事实 API
