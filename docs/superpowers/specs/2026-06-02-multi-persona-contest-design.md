# 多人格竞稿写作 — 设计文档

- 日期：2026-06-02
- 状态：设计已确认，待实现
- 作者：msc + Claude

## 1. 背景与目标

当前写作流程是 Coordinator 顺序驱动**单个** Writer 逐章创作（`plan_chapter → draft_chapter → check_consistency → commit_chapter`），由 `flow.Route()` 状态机查表决定下一步、`coordinator.FollowUp()` 注入指令、`reminder.StopGuard` 约束工具调用顺序。

本功能新增**多人格竞稿**能力：

1. **多 Writer 竞稿** — 同一章由 N 个带不同作者人格的 Writer 各写一稿。
2. **评审选优** — 新增 Judge 子代理对 N 稿打分选优并给出修改意见。
3. **中选润色提交** — 中选 Writer 在自己被选中的草稿上按 Judge 意见继续修改，再提交。
4. **人格化** — 每个 Writer 绑定一个知名网文作者人格（如乌贼、卖报小郎君、土豆），文风由 LLM 依据作者名自动生成。

## 2. 已确认的核心决策

| # | 决策点 | 结论 |
|---|--------|------|
| 1 | 触发范围 | **全程每章竞稿**（质量优先，成本接受） |
| 2 | 人格 ↔ Writer | **一一对应**，N 个人格 = N 个 Writer，数量可动态配置 |
| 3 | 开关 | **配置 opt-in**；不配置 `personas` → 退回原单 Writer 行为，零成本、完全向后兼容 |
| 4 | 人格来源 | **配置只填作者名** → 启动时 LLM 生成文风 prompt → 缓存到 store，全书复用 |
| 5 | 选优流程 | **新增 Judge** 打分选优 + 给意见 → 中选 Writer 润色 → 走原 check → commit |
| 6 | 执行模型 | **先串行、预留并发接口**（agentcore 原生支持 parallel，将来无痛切换） |
| 7 | 编排层 | **Host 编排器**：把"写第 N 章"拆成 `flow.Route` 状态机的多个子步骤，与现有"弧末 editor 三步走"同构 |

## 3. 关键架构事实（实现依据）

- **写作驱动闭环**：Coordinator 调 `subagent(writer, "写第N章")` → subagent 工具成功 emit `EventToolExecEnd(Tool=="subagent")` → `flow/dispatcher.go` 监听 → `flow.Route(LoadState(store))` 读 store 事实算下一步 → `coordinator.FollowUp(指令文本)` → Coordinator 执行下一个 subagent 调用。Coordinator 始终是 subagent 唯一执行者，Host 只注入"下一步调谁"的文字指令（"LLM 驱动，Host 服务"）。
- **agentcore subagent 执行模式**（`subagent/subagent.go`）：支持 single / parallel / chain / background / team。`executeParallel`（:562）用 goroutine + WaitGroup + 信号量，`maxParallelTasks=8`、`maxConcurrency=4`，LLM 传 `tasks` 数组即触发。→ 串行用多次 single，并发换 parallel，底层现成。
- **草稿存储**（`store/drafts.go`）：当前草稿固定写 `drafts/{ch}.draft.md`；`commit_chapter` 从该固定路径 `LoadDraft` 读稿提交（commit_chapter.go:189）。→ 多 Writer 各写一稿必然互相覆盖，**候选稿隔离存储是必须新增的核心机制**。
- **工具并发安全**：`draft_chapter.ConcurrencySafe()` 返回 `false`（draft_chapter.go:33），写工具禁止并发写同一 store。→ 串行期无碍；并发期靠候选槽路径隔离 + 各 persona 独立工具实例规避竞态。
- **`subagent.Config.Tools` 每 agent 独立**：可为每个 persona writer 注入绑定了 persona id 的专属工具实例。

## 4. 总体设计

竞稿**不绕过 Coordinator**。把"写第 N 章"从单步扩展为 Route 状态机的多子步骤，复用断点恢复 / 事件投影 / 工具链全套现有机制。

### 4.1 向后兼容（最高优先级约束）

配置不写 `writing_contest.personas` 时，`Route` 在"写 next_chapter"分支返回原来的单 `writer` 指令，行为与现状逐字节一致。竞稿是纯 opt-in，默认零成本。

### 4.2 五层改动

#### 层 1：配置层（`bootstrap/config.go`）

新增可选字段：

```jsonc
"writing_contest": {
  "personas": ["乌贼", "卖报小郎君", "土豆"],   // 只填作者名；数量 = 并行 writer 数
  "judge": { "provider": "...", "model": "..." } // 可选；缺省复用 editor 角色模型
}
```

- `personas` 为空或字段缺失 → 单 Writer 模式（现状）。
- 人格数即 Writer 数（决策 2）。
- 解析后归一化：去空白、去重、保序；非法（全空）按未配置处理。

#### 层 2：人格生成层（新增 `host/persona` 包）

- 启动时（`host.New` / 首次 `Start` 后）对每个作者名调一次 LLM，生成结构化文风 prompt：句式特征、节奏、用词偏好、擅长题材、标志性手法。
- 结果缓存到 store 的 `personas.json`（persona id → 文风 block + 源作者名 + 生成模型 + 时间）。
- 全书只生成一次；`Resume` 时直接读缓存，不重生成，保证全书文风稳定。
- persona id 取作者名的稳定 slug（如 `wuzei`），用于工具路径与 agent 命名。
- 生成失败处理：单个失败 → 用一段通用"模仿 <作者名> 风格"的兜底 block 并告警，不阻塞启动。

#### 层 3：Agent 构建层（`agents/build.go`）

- 现有 1 个 `writer` config → 扩展为 N 个 `writer_<persona>` config：
  - **agent 名必须唯一**（`writer_<persona-slug>`，如 `writer_wuzei`）。这是 dispatcher dedupe（`dispatcher.go:85-93` 按 `Agent+Task` 去重）正确工作的前提：同名会被去重误杀第二个候选派发，也会被 agentcore 的 `New()` config map（`subagent.go:185-191` 按 name 建键）覆盖。
  - SystemPrompt = 基础 writer prompt + 该人格文风 block。
  - **绑定专属工具实例**：该 persona 的 `draft_chapter` 写入隔离候选槽 `drafts/{ch}.cand-<persona>.md`（其余只读工具可共享）。
  - **StopGuard【修订：原"复用 WriterStopGuard"有误】**：候选稿阶段 persona writer 只写候选稿、不调 `commit_chapter`，而现有 `NewWriterStopGuard`（`subagent_guards.go:74-79`）强制每轮必须产生 `commit` checkpoint，否则拦截 end_turn 并在连续 3 次后升级终止 —— 会直接卡死候选 writer。因此**新增 `NewCandidateStopGuard`**（基于现有 `newCheckpointDeltaGuard`，`requiredSteps=["draft"]`），要求候选 writer 本轮至少产生一次成功的 `draft_chapter`。注意：persona writer 在候选阶段与润色阶段需要不同 guard（候选要 `draft`、润色要 `commit`），通过 `StopGuardFactory`（`subagent.go:69-70` 每次 run 重建 guard）按当前任务类型选择对应 guard。ContextManagerFactory 机制照常复用。
  - 同理需移除 persona writer 候选 config 的 `StopAfterTools: commit_chapter`（`build.go:181-194`），候选阶段应在 `draft_chapter` 后停（或不设早停、由 CandidateStopGuard 控制）；润色阶段仍 `StopAfterTools: commit_chapter`。
- 新增 `judge` subagent config：
  - agent 名 `judge`（唯一）。
  - 工具：`novel_context`、`read_chapter`（读各候选稿）、新增 `save_verdict`。
  - 模型：`writing_contest.judge` 指定，缺省复用 editor 角色。
  - StopGuard：新增 `NewJudgeStopGuard`（`requiredSteps=["verdict"]`），要求本轮至少产生一次 `save_verdict`。

#### 层 4：候选稿隔离存储（`store/drafts.go`）

- 候选槽：`drafts/{ch}.cand-<persona>.md`（每 persona 一份）。
- 裁定文件：`drafts/{ch}.verdict.json`，结构：
  ```json
  {
    "chapter": 12,
    "winner": "wuzei",
    "scores": [ {"persona":"wuzei","score":8.5,"comment":"..."}, ... ],
    "revision_notes": "给中选 writer 的具体修改意见",
    "promoted": false
  }
  ```
- **中选稿提升（幂等）**：Judge 裁定后，Host 把 `cand-<winner>.md` 复制为标准 `{ch}.draft.md`，然后把 verdict 的 `promoted` 置 `true`。`promoted` 字段是"提升已完成"的**显式事实标记**，供 Route 判定是否需要（重）做提升 —— 不能用"`draft.md` 内容 == 某 `cand.md`"这种脆弱比较。提升操作幂等：崩在"复制后、置 `promoted` 前"时，重做只是再复制一次相同内容 + 再置标记，无副作用。置标记应在复制成功之后（顺序保证崩溃后宁可重做、不会漏做）。
- 提升后润色/提交完全复用现有单 Writer 工具链（`draft_chapter` append/rewrite → check → commit 均读写 `{ch}.draft.md`，零改动）。
- 新增方法：`SaveCandidate(ch, persona, content)`、`LoadCandidate(ch, persona)`、`ListCandidates(ch)`、`SaveVerdict` / `LoadVerdict`、`PromoteCandidate(ch, persona)`（内部完成复制 + 置 `promoted`）、`IsPromoted(ch)`。

#### 层 5：Route 状态机（`flow/router.go`）

竞稿章节的"写第 N 章"子状态机（互斥，自上而下匹配第一个）：

| 事实判定（读 store） | 下一步指令 |
|---|---|
| 候选槽不齐（某 persona 缺 `cand-*.md`） | `writer_<persona[k]>` 写候选 k（串行逐个），Task 含"写候选"语义 |
| 候选齐、无 `verdict.json` | `judge` 选优 + 给修改意见 |
| 有 verdict、`promoted=false` | Host 提升中选稿为 `draft.md`（store 操作，非 subagent）——见下方触发说明 |
| `promoted=true`、该章未 commit 完成 | 中选 `writer_<winner>` 在 `draft.md` 上按 `revision_notes` 润色 → `check_consistency` → `commit_chapter`（用原 commit-guard，一个 run 内完成；Task 含"润色"语义，与候选 Task 文本不同） |

- 单 Writer 模式：跳过全部上述分支，直接返回原 `writer` 指令。
- `LoadState` 扩展：读候选槽完成情况、verdict 是否存在、`promoted` 标记。
- `Route` 保持纯函数；所有事实经 `State` 显式传入，可单测。
- **dedupe 兼容**：候选阶段各 persona 的 agent 名互不相同（`writer_<persona>`），中选 writer 的两次调用（写候选 / 润色）虽 agent 名相同但被 `judge` 派发隔在中间、且 Task 文本不同（"写候选第N章" vs "按意见润色第N章"），故均不触发 `dispatcher.dedupe`（按 `Agent+Task`）误杀。

**"提升中选稿"步骤的触发**：该步是纯 store 操作，不调 subagent，因而不会自然产生 `EventToolExecEnd` 来驱动下一次 `Dispatch`。处理方式：`judge` 的 `save_verdict` 成功返回（这本身触发 `ToolExecEnd`）后，在 `dispatcher.Dispatch` 计算路由前，由 Host 检测"有 verdict 且 `promoted=false`"并**同步执行提升**（置 `promoted=true`），再继续算路由直接得出"润色"指令。即提升与紧随其后的润色指令在同一次 Dispatch 内完成，不占用独立的状态机往返。

**恢复正确性**：执行恢复由 `flow.Route` 读 store 事实驱动，**不依赖** `resume.go`（`resume.go:57-58` 注释明确：`describeResume` 只生成 UI 标签、不影响 Coordinator 行为）。竞稿子状态机实现后，崩溃在任一子步骤（候选写一半 / judge 前 / 提升前 / 润色前）重启时，Route 依据磁盘事实（`cand-*.md` / `verdict.json` / `promoted`）精确算出下一步，不会卡死、不会重复生成 verdict。`describeResume` 另**补一个竞稿中间态 UI 标签分支**（如"恢复：第 N 章竞稿中（3 候选/已裁定/润色中）"）——仅 UI 增强，不影响恢复正确性；不补则 fallthrough 到现有"第 N 章进行中"标签，功能不受损。

### 4.3 串行 → 并发预留接口

把"候选稿生成"抽象为 `CandidateStrategy` 接口：

```go
type CandidateStrategy interface {
    // 给定缺失的候选 persona 列表，返回 Route 应下达的指令
    NextCandidateInstruction(chapter int, pending []string) *Instruction
}
```

- **串行实现（本期）**：返回单个 `writer_<persona>` 指令，逐个补齐。
- **并发实现（将来）**：返回一条 `subagent(tasks=[...])` parallel 指令，命中 agentcore `executeParallel`。

切换只替换策略实现，Route / store / checkpoint / 恢复逻辑均不动。

## 5. 数据流（竞稿一章，串行）

```
Route: 候选槽空 → writer_乌贼 写候选（CandidateStopGuard 要 draft）
  → cand-wuzei.md + draft checkpoint → ToolExecEnd → Dispatch
Route: 缺卖报 → writer_卖报 写候选
  → cand-maibao.md + draft checkpoint → Dispatch
Route: 缺土豆 → writer_土豆 写候选
  → cand-tudou.md + draft checkpoint → Dispatch
Route: 候选齐、无 verdict → judge 选优（JudgeStopGuard 要 verdict）
  → verdict.json(winner=wuzei, notes, promoted=false) + verdict checkpoint → Dispatch
  ↳ Dispatch 内联：检测 promoted=false → Host 提升 cand-wuzei→draft.md，置 promoted=true
Route: promoted=true、未润色 → writer_乌贼 润色 draft.md（commit guard，Task="润色第N章")
  → draft.md 更新 + commit_chapter → chapters/N.md 终稿 + Progress 完成
  → Dispatch → 下一章
```

每个 subagent 成功落 checkpoint；崩溃后 `Route` 读 store 事实精确恢复到中断子步骤。

## 6. 错误处理

- **persona writer 失败**：对应候选槽缺失，Route 自动重派（复用 dispatcher 去重 + subagent MaxRetries=5）；同一 persona 连续失败超限 → 标记该 persona 本章弃权，候选数减一继续；全部失败 → 本章降级为单 Writer（用基础 writer 直接写 `draft.md`）并告警。
- **judge 失败**：重试；超限则默认选取字数最多（或首个）候选作为 winner、`revision_notes` 置空并告警，保证流程推进。
- **中选润色失败**：`draft.md` 已是中选原稿，可直接进入 check/commit（润色为增强项，失败不阻塞提交）。
- **配置非法**：personas 全空白 → 按未配置处理；judge 模型解析失败 → 回退 editor 模型。

## 7. 测试计划

| 测试 | 重点 |
|---|---|
| `flow.Route` 竞稿子状态机单测 | 候选不齐/齐/有 verdict/已提升/润色完成 各分支；单 Writer 模式回归 |
| 候选槽存储读写测试 | SaveCandidate/LoadCandidate/ListCandidates/Verdict/Promote |
| persona 生成缓存测试 | 首次生成→缓存→Resume 读缓存不重生成；生成失败兜底 |
| 恢复测试 | 竞稿中途（候选写一半 / judge 前 / 润色前）崩溃后精确恢复 |
| 向后兼容回归 | 无 personas 配置时 Route 输出与现状一致 |

## 8. 不做（YAGNI）

- 本期不做真并发执行（仅预留接口）。
- 不做关键章/普通章差异化触发（决策为全程竞稿）。
- 不做动态人格抽取（人格整本书固定）。
- 不做人格库内置模板（人格全部由作者名 LLM 生成）。
- 不做 TUI 内人格编辑器（配置文件管理即可）。

## 9. 受影响文件清单

- 新增：`internal/host/persona/`（人格生成 + 缓存）、`internal/tools/save_verdict.go`
- 修改：
  - `internal/bootstrap/config.go`（`writing_contest` 配置字段 + 归一化）
  - `internal/agents/build.go`（N 个唯一命名 persona writer + judge；候选/润色双 guard 切换；候选 config 去掉 commit 早停）
  - `internal/host/reminder/subagent_guards.go`（新增 `NewCandidateStopGuard` requiredSteps=`["draft"]`、`NewJudgeStopGuard` requiredSteps=`["verdict"]`）
  - `internal/store/drafts.go`（候选槽 SaveCandidate/LoadCandidate/ListCandidates + Verdict 读写 + PromoteCandidate/IsPromoted）
  - `internal/host/flow/router.go` + `state.go`（竞稿子状态机 + LoadState 读候选/verdict/promoted）
  - `internal/host/flow/dispatcher.go`（save_verdict 后内联执行提升再算路由）
  - `internal/host/resume.go`（可选：describeResume 补竞稿中间态 UI 标签）
  - `internal/tools/draft_chapter.go`（候选 writer 写候选槽时落 `draft` checkpoint —— 确认现状已落或补齐）
  - `config.example.jsonc`（示例）、`README.md`（文档）

## 10. 对抗性审查记录（2026-06-02，Codex + 独立复核）

经 Codex 对抗性审查 + 对照代码独立复核，对原设计做了如下修订：

1. **【硬伤·已修订】StopGuard**：原"复用 WriterStopGuard"与"候选 writer 不 commit"自相矛盾，会被 `subagent_guards.go:74-79` 卡死。改为新增 `NewCandidateStopGuard`（要 `draft`）+ 候选/润色按任务切换 guard。
2. **【细节·已修订】提升幂等**：verdict 增 `promoted` 显式标记，Route 据此幂等重做提升，避免脆弱的内容比较。
3. **【细节·已修订】dedupe 兼容**：写明 persona writer 唯一命名 + 润色 Task 文本区分，规避 `dispatcher.dedupe`。
4. **【澄清·无需改】** Codex 将候选槽/judge/save_verdict/opt-in 字段列为"代码中不存在的硬伤"——实为本设计的待新增工作项（§4.2 各层已明确），非缺陷；其确认现状的证据准确。
5. **【澄清·无需改】恢复**：Codex 称"resume.go 不认竞稿态会卡死"。实际 `resume.go:57-58` 注释明确该文件只生成 UI 标签、不影响执行；恢复由 `flow.Route` 事实驱动，竞稿状态机实现后自动正确。仅补可选 UI 标签。
