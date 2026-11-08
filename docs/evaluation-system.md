# ainovel-cli 评测体系

> 评测不是新造一套检查脚本，而是把项目**已有的事实诊断器（`diag`）、全书文体统计器（`stylestat`）、七维原生评审（`ReviewEntry`）当作评测器**，套一层离线批量 harness。一份事实定义，两处不再漂移。

---

## 0. 为什么需要重新设计

稳定性已经跑通：长篇 235 章 / 127 万字一次写完，滚动规划闭环成立（见 `architecture.md` §9.1）。瓶颈已经转移——**质量可迭代**：

- 改一个 prompt 后，流程是否仍稳定？工具链、状态推进、持久化事实是否还正确？
- 正文、大纲、评审质量是真的提升了，还是只是这一次随机抽到了好结果？
- 长篇里角色、时间线、伏笔、上下文是否持续可靠？
- **全书级的文体固化**（句式 tic 章均几十次、章末形态同构、跨章逐字复读）有没有变好或变坏？这是 196 章实证 6.5/10 的真凶，单章评审对它天然失明。

目前这些判断靠"凭感觉 + 人工抽读"。评测体系要把 prompt 改动从凭感觉变成**有回归、有证据、有人工读样**的工程流程。

但本项目不需要、也不应该照搬业界通用 eval 平台（dataset / experiment / scorer / 数据库 / Web UI）。原因很简单：**这些能力的核心——确定性检查与质量信号——项目里已经存在，且是 Go 写的、与运行时共享同一份事实模型。**

---

## 1. 核心论点：评测器已经存在

评测系统的四类评测器，三类在代码库里已经实现，只是从未被当作"评测器"调用：

| 评测器 | 项目已有能力 | 入口 | 产出 |
|---|---|---|---|
| **确定性事实诊断** | `internal/diag` 的一组工件规则 + 运行时规则 | `diag.Diagnose(store)` | `Report{Stats, Findings}`，Finding 带 Severity/Evidence |
| **全书级文体回归** | `internal/stylestat` | `stylestat.Compute(input)` | 句式模式章均、跨章重复句、章末短句占比、标题混用 |
| **质量裁定（rubric）** | 版本化 rubric（初始派生自 `editor.md` 七维） | LLM Judge（固定标尺做 A/B） | consistency/character/pacing/continuity/foreshadow/hook/aesthetic |
| **行为脱敏导出** | `internal/diag` 导出 | `diag.WriteExport(store, rep, rc)` | 行为骨架，供人工读样与归档 |

`diag.Analyze(s *store.Store)` 接收一个 Store 就能产出完整 `Report`——**它本来就能离线跑在任何产出目录上**。`stylestat.Compute` 是纯函数。这意味着评测系统要做的不是重新实现"章节是否落盘、progress 是否推进、checkpoint 是否存在、有没有 pending 残留、流程有没有死循环"——这些 diag 全做了，而且每条规则都对应一个踩过的真实坑（`PhaseFlowMismatch`、`OrphanedSteer`、`OutlineExhausted`、`repeatedErrors`/`stuckStep` 对应 idleResume / 大纲耗尽 livelock / 工具调用当文字打印等历史故障）。

> **评测系统的工作不是造检查，而是：批量驱动 + 把已有评测器跑在产出上 + 把 Finding/统计映射成门禁 + 聚合报告。**

---

## 2. 设计原则

### 2.1 评测器即诊断器，绝不重造确定性检查

确定性检查只调用 `diag.Diagnose`，不在评测层重新解析 `progress.json` / `checkpoints.jsonl` / `sessions/*.jsonl`。理由是这个项目的 DRY 铁律：**"什么是合法状态"只能有一份定义。** 如果评测用 Python 重新解析一遍 checkpoint 判断 commit 是否缺失，就有了两份"commit 完成"的定义，运行时改了 diag 规则、评测不跟着改，门禁立刻失真。

→ 评测 harness 用 **Go**，in-process 调用 `diag` 与 `stylestat`，与运行时共享 `internal/domain` 与 `internal/store`。这是本设计与上一版最根本的区别。

### 2.2 全书级文体回归是第一质量信号

单章 LLM Judge 看每一章都"正常"，但瓶颈恰恰是跨章固化。所以质量回归的**确定性骨干是 `stylestat`，不是 LLM Judge**。

**前提：`stylestat.Compute` 少于 5 章直接返回 nil**（`stylestat.go` `minChapters=5`，样本太小频率无意义）。因此文体回归**只在 ≥5 章的 Quality / Longform 层生效**，1 章的 Smoke 拿不到文体信号——这点决定了下文成本与默认策略。指标包括：

- variant 的句式模式章均次数 vs baseline（`patterns[].per_chapter`）
- 章末短句收尾占比（`ending.short_ratio` 逼近 1 是病）
- 跨章逐字重复句条数（`repeated_sentences`）
- 标题格式混用（`title_formats`）：运行时与离线评测都按已完成章节的终稿首个非空行统计，保留 Markdown 与章号；无可用首行时才回落到摘要标题，不使用计划大纲。章号识别兼容系统生成的“第 N 章”及无空格写法。
- 开篇时间词率（`opening_time_rate`）

这些是零 LLM 成本、确定性、且正打在质量瓶颈上的指标。**LLM Judge 是补充，stylestat delta 是主线。**

### 2.3 LLM Judge 对齐七维原生 rubric，不另起炉灶

Judge 不发明新评分维度——维度严格等于 `domain.DimensionScore` 的七项，做 baseline/variant 比较。

**但 rubric 必须版本化、可固定**，存为 `evals/rubrics/*.json` 的快照，不是运行时实时读 `editor.md`。原因：当被测对象正是 `editor.md` 本身时，若裁判跟着 `editor.md` 一起变，评测基准就漂了——裁判和被测同源会让"改 editor 是好是坏"无从判断。所以 rubric 初始**派生**自 editor 七维（保证口径一致），之后**独立演进、显式 bump 版本**；report 里记录用的是哪版 rubric。

### 2.4 确定性 Finding 决定门禁，LLM 与人工只做质量裁定

对齐架构铁律"统计归代码，裁定归 LLM"：

- **能阻塞合入的只有确定性证据**：`diag` 的 `SevCritical` Finding、case 声明的契约断言失败。
- **LLM Judge 与人工读样产出 warning 与排序线索**，不单独决定合入。
- 一句话：`Finding.Severity` 直接映射门禁等级，不引入新的严重度分类法。

### 2.5 评测只观察，不介入控制流

评测复用 `diag`，但**丢弃 diag 的 `Action` 与 `Planner`**——那是运行时控制流的东西。在评测语境里 `diag.Report` 只取 `Stats` 与 `Findings`，Action 一律忽略。评测不自动修 prompt、不自动回滚、不续跑。这是观察者纪律（`architecture.md` §2.3）在评测语境的延伸。

### 2.6 失败显式暴露

不 mock 成功、不吞错误、不用模板假装通过。模型、工具、配置、文件系统、解析、judge 任一失败，报告显式记录原因。**失败本身就是评测结果**——一个 case 跑崩了，门禁就是 FAIL，不是"跳过"。

### 2.7 每次只验证一个变量

A/B 的硬约束：同需求、同配置、同模型/provider、同风格、隔离输出目录。Baseline = 当前正式 prompt，Variant = 只替换本次要验证的 prompt 文件。一次实验不要同时改 Writer/Architect/Editor/Arbiter。

---

## 3. 架构全景

```text
[Cases]  evals/cases/*.json —— 事实层断言集，不是通用 dataset 行
   │
[Runner]  internal/eval —— in-process 装配 host 驱动（按章数上限截停），bundle.Prompts 内存覆盖做 variant
   │       baseline run ┐
   │       variant  run ┘  各自隔离 output 目录
   ▼
[Collectors]  对每个产出目录采集：
   ├── diag.Diagnose(store)      → Report{Stats, Findings}      （事实 + 运行时）
   ├── stylestat.Compute(input)  → 全书文体统计                 （质量回归骨干）
   ├── case 契约断言             → 期望 checkpoint/phase/工具契约（diag 不覆盖的）
   ├── usage / cost / token      → 从 meta/usage.json 读
   └── tool_calls                → 从 meta/sessions/*.jsonl 读真实工具调用
   ▼
[Graders]
   ├── 确定性门禁：Finding.Severity + 契约断言 → hard_fail / regression
   ├── stylestat delta：variant vs baseline 文体指标差
   ├── LLM Judge（可选）：七维 rubric A/B 比较
   └── Human：人工读 baseline/variant 产物
   ▼
[Report]  report.json（机读）+ report.md（人读）+ 行为脱敏导出
   └── Gate: PASS / WARN / FAIL
```

依赖方向：`eval → host → agents → tools → store → domain`，横向复用 `diag` / `stylestat`。评测层**不反向依赖**运行时控制流，只读 Store 与只读评测器。

> **当前实现覆盖确定性主线**：无 `--variant` 时为 `mode=single`；传 `--variant` 时为 `mode=ab`，同一 case 隔离运行 baseline 与 variant，并生成 delta。Collectors 已接 `diag.Diagnose`、case 契约、`stylestat.Compute`、`meta/usage.json`、session tool call 计数；Graders 已接确定性门禁、baseline/variant diag delta、cost/token/tool call delta、stylestat delta。Runner 直接 `host.New` 装配并自带章数上限截停，**不复用无章数上限的 `headless.Run`**。LLM Judge 与 Human 仍是后续可选层，不参与当前确定性门禁。

---

## 4. 为什么是 Go in-process，不是 shell + Python

| 维度 | shell 拷源码 + Python 解析（旧路） | Go in-process（本设计） |
|---|---|---|
| 确定性检查 | Python 重新解析 JSON，与 diag 规则两份定义 | 直接 `diag.Diagnose(store)`，一份定义 |
| variant 切换 | 拷整个源码树 + 重新 `go build` 两个二进制 | `bundle.OverridePrompt(...)` 内存覆盖后装配 host，零拷贝零重编译 |
| 文体回归 | 需在 Python 重写 stylestat 的中文分句逻辑 | 直接 `stylestat.Compute` |
| Judge rubric | 维度散落在 Python | 复用 `domain.DimensionScore`，与线上同源 |
| 漂移风险 | 高：运行时改了事实模型，评测不跟 | 低：编译期就会暴露字段变更 |

旧 `prompt_ab.sh` 之所以要拷源码重编译，是因为 prompt 是嵌入二进制的（`go:embed`）。但 `assets.Bundle.Prompts` 是普通结构体，**runner 在内存里改一个字段就能做 variant**，根本不需要拷源码。这是用 Go 写 harness 顺带拿到的最大简化。

> **实现约束**：`assets.Load` 经 `loadPrompts` 给 Worker prompt（architect/writer/editor）统一追加 `WithSimulationGuidance` 后缀。若 variant 只把裸文本塞进 `bundle.Prompts.Writer`，就丢了 baseline 有的仿写画像后缀，A/B 不等价。
>
> 正确做法是通过 `assets.OverridePrompt` 覆盖，内部走与 `Load` 完全相同的包装；eval 不复制包装逻辑。

> 上一版文档保留 `prompt_ab.sh` / `prompt_ab_report.py` 并"逐步抽取能力"。本设计放弃这条路：它们解决的问题（隔离运行 + 指标汇总）在 in-process Go harness 里是子集，强行复用反而背上 shell/Python/Go 三语言的接口胶水。**Go harness 是唯一主路**；当前 Go harness 已覆盖 baseline/variant 隔离运行、repeat 汇总与确定性 delta。旧脚本（`scripts/prompt_ab.sh`、`scripts/prompt_ab_report.py`）及其操作手册 `docs/prompt-ab.md` 已随本设计落地一并删除，不再保留。

---

## 5. Case Manifest

Case 是评测输入的最小单位，也是一组**事实层断言**。用 JSON 描述，避免规则散落在命令行参数里。

```json
{
  "id": "writer_first_chapter_xianxia",
  "category": "smoke",
  "role": "writer",
  "description": "验证 Writer 第一章正文质量与工具链稳定性",
  "prompt": "写一本修仙长篇，主角从边城杂役起步，靠异常记忆破解宗门旧案卷入长生局。",
  "style": "fantasy",
  "max_chapters": 1,
  "target_prompts": ["writer.md"],
  "rubric": "writer_chapter",

  "expect": {
    "phase": "writing",
    "min_completed_chapters": 1,
    "required_checkpoints": ["chapter:1:plan", "chapter:1:draft", "chapter:1:commit"],
    "no_pending": ["pending_commit", "pending_steer"]
  },

  "gate": {
    "max_severity": "warning",
    "max_cost_delta_ratio": 0.3,
    "max_tool_call_delta_ratio": 0.3,
    "stylestat_regression": "warn"
  }
}
```

**字段语义**：

- `expect`：case 级契约断言，**只声明 diag 通用规则覆盖不到的、与本 case 强相关的预期**（比如"这个 smoke case 必须恰好产出 chapter:1:commit"）。通用的"无 pending 残留 / phase-flow 一致 / 无章节缺口"交给 diag，不在 case 里重复声明。
- `category`：评测层 ∈ `smoke` / `workflow` / `quality` / `longform` / `recovery` / `steering`。决定跑哪套门禁与默认是否开 stylestat/Judge。
- `role`：被测的角色 ∈ `writer` / `architect` / `editor`。与 `category` 正交——层决定"验到什么深度"，角色决定"验哪个 Worker"。Workflow 层按 `role` 选断言集。
- `max_severity`：diag Finding 允许的最高严重度。超过即 hard fail。
- `gate.max_cost_delta_ratio` / `gate.max_tool_call_delta_ratio`：variant 相对 baseline 的成本与工具调用增幅阈值；省略时默认 `0.3`，显式 `0` 表示不允许增长，负数表示关闭该项 delta gate。
- `rubric`：启用哪个版本化 LLM Judge 评分表。缺省则不跑 Judge。
- `gate.stylestat_regression`：`block` / `warn` / `off`，控制文体回归是否阻塞（仅 ≥5 章 case 生效）。

---

## 6. 评测分层

每一层明确**用哪个已有评测器**，避免"评测层自己又写一遍判断"。

### 6.1 Smoke（每次 prompt 改动必跑，最小集）

只判断系统是否还能稳定跑，不判文笔。1 章 / 规划阶段即可暴露。

| case | 目标 | 主要评测器 |
|---|---|---|
| `writer_first_chapter` | Writer 完成第一章并 commit | `expect.required_checkpoints` + diag |
| `architect_short` | 短篇规划存全 premise/outline/characters/world_rules | diag `MissingSummaries` 同源的 foundation 检查 + `expect` |
| `architect_long` | 长篇规划存 layered_outline/compass，首弧展开 | diag `OutlineExhausted`/`CompassDrift` + `expect` |
| `editor_review` | 到评审点 Editor 存 review（七维齐全） | `ReviewEntry` 字段断言 |

成本：1 章 × baseline+variant，秒级到分钟级，不开 Judge、不跑 stylestat（章数不足 5，`Compute` 返回 nil）。CI 默认只跑这层。

### 6.2 Workflow（验证 Agent 行为符合架构契约）

**关键纪律：断言契约，不断言精确工具序列。** 架构押注 LLM 自主决策流程（`architecture.md` §2.1），把工具顺序写死会在评测层重新引入被 §10.13 拒绝的"为 LLM 行为写硬编码"。所以这里只断言**必然事实**：

- Writer：`chapter:N:commit` checkpoint 存在；commit 后子代理本轮结束（无超长尾随正文）；draft checkpoint 先于 commit。**不**断言"必须 novel_context→read_chapter→plan→draft→check→commit 这个精确顺序**。
- Architect：写作期 outline 只增不全覆（`expand_next_arc`/`append_volume` 的 checkpoint，没有第二条 `layered_outline` 全量写）；展开后扁平 outline 与 layered 章节数一致。
- Editor：`ReviewEntry.Verdict` 合法（accept/polish/rewrite）；rewrite/polish 必须产出 affected chapters；弧末有 `arc_summary`、卷末有 `volume_summary` checkpoint。
- Engine 派发：Route 指令与实际运行的 Worker 匹配（从 session trace 读，diag `repeatedErrors` 兜底循环）；语义裁定核对 `meta/decisions.jsonl`。

这些大部分能直接由 diag 规则 + checkpoint 断言覆盖，少量（commit 后尾随正文）需在 collector 里加一条轻量 trace 检查。

### 6.3 Quality（流程通过后才跑，评内容质量）

两条腿：

1. **stylestat delta（确定性，主线）**：variant vs baseline 的文体指标差。这是质量回归的硬证据。**要求 case 跑满 ≥5 章**（否则 `Compute` 返回 nil，此项标 `insufficient_sample`），所以纯 1 章 Quality case 拿不到文体回归，需把 `max_chapters` 设到 5 以上。
2. **LLM Judge（辅助）**：七维 rubric A/B（见 §8）。

只有 §6.1/§6.2 通过的 case 才进 Quality——流程都不对，谈质量没意义。

### 6.4 Longform & Recovery（重大改动 / nightly）

不必每次跑。覆盖长篇稳定性与恢复能力，正是 diag 运行时规则与 context 规则的主场：

- 前 3 章 / 5 章连续写作 → diag `GhostCharacter`/`TimelineGaps`/`RelationshipStagnation`/`ChapterGaps` + stylestat 跨章重复。
- 弧末评审 + 下一弧展开 → `OutlineExhausted`/`StaleForeshadow`/`CompassDrift`。
- 用户中途干预（steering case）→ user_rules 是否落 `meta/user_rules.json`、是否被后续章节遵守。
- 崩溃恢复：跑到第 N 章 draft 后 kill → Resume → diag 确认 `checkpoints.jsonl` 无重复 step、不重写已落盘 draft、`pending_commit` 最终清零。
- 工具调用膨胀 / 成本异常 → diag `repeatedErrors`/`stuckStep`/`streamIdleStorm` + usage delta。

---

## 7. 确定性门禁

门禁等级由 **diag Finding 的 Severity** + **case 契约断言** 直接派生，不另立分类法。

### 7.1 Hard Fail（阻塞合入）

- 进程 panic / headless 返回 error。
- diag 产出 `SevCritical` Finding（`InvalidPendingRewrites` / `PhaseFlowMismatch` 等）。
- case `expect` 契约断言失败：缺 commit checkpoint、phase 未达预期、声明的 pending 未清零。
- variant 的错误数 / critical Finding 数多于 baseline（回归到更坏）。

### 7.2 Regression（默认 warning，是否阻塞由 case gate 决定）

- diag 新增 `SevWarning` Finding（variant 比 baseline 多）。
- tool calls / cost / input token / output token 增幅超过 case 阈值（默认 30%）。
- **stylestat 回归**：句式模式章均次数上升、章末短句占比上升、跨章重复句增多、标题混用出现——按 `gate.stylestat_regression` 决定 warn/block。
- 章节字数低于 baseline 60% 或高于 180%（diag `WordCountAnomaly` 同源阈值）。

### 7.3 Quality Gate（人工兜底）

- LLM Judge 只做辅助与排序。
- Judge 判 variant 明显更差 → 必须人工读样确认。
- 人工读样认定退化 → 阻塞。
- Judge 判 variant 更好但确定性有 hard fail → 仍阻塞。

### 7.4 推荐合入条件

日常改 prompt：Smoke 全过 + 目标角色 Workflow 全过（Smoke 1 章不含文体回归；若跑了 ≥5 章 Quality case，则 stylestat 无明显回归）。
重大改动：再加 2-3 个 Quality case + 1-2 个 Longform case + 人工读样。

---

## 8. LLM Judge

Judge 是质量辅助，本质是**用版本化 rubric（初始派生自 editor.md 七维）离线做 baseline/variant 比较**。rubric 是固定标尺、独立于线上 `editor.md` 演进（理由见 §2.3），report 记录所用 rubric 版本。

### 8.1 输入（控制大小，绝不塞整本书）

- 用户原始需求 + 当前章节大纲/契约。
- baseline 与 variant 的**同一章**正文。
- 最近 1-2 章摘要 + 角色状态摘要（从 store 读）。
- 该章的 stylestat 相关切片（让 Judge 看到"这句在全书重复了 7 次"这类事实）。

### 8.2 输出（结构化，对齐七维）

```json
{
  "scores": {
    "consistency": 8, "character": 7, "pacing": 8, "continuity": 8,
    "foreshadow": 7, "hook": 7, "aesthetic": 6
  },
  "winner": "variant",
  "confidence": "medium",
  "reasons": ["variant 行动推进更集中", "baseline 前情复述更重"],
  "risks": ["variant 配角动机铺垫略少"]
}
```

- 维度严格等于 `domain.DimensionScore` 七项，每项 0-10。
- `winner` ∈ baseline/variant/tie；`confidence` ∈ low/medium/high。
- `reasons`/`risks` 每条 ≤ 80 字，引用原文要短。

### 8.3 边界

Judge **不能**：决定流程是否通过、修改产物、自动改 prompt、作为唯一合入依据、生成长篇原文摘录。
Judge **可以**：给人工评审排序、标出明显退化、总结 A/B 差异、暴露 prompt 改动副作用。

---

## 9. 报告

每次实验生成 `report.json`（机读，可重生 markdown）+ `report.md`（人读）+ `artifacts/{case_id}/{baseline,variant}/`（原始产物）。`--repeat N` 时路径为 `artifacts/{case_id}/rN/{baseline,variant}/`。

### 9.1 指标 delta

报告显示 variant 相对 baseline 的差异，绝对值与比例并列：

```text
completed: baseline=5 variant=5   ← ≥5 章，文体指标才有意义
tool_calls: baseline=12 variant=16  +4 (+33.3%)
cost_usd: baseline=0.42 variant=0.55  +0.13 (+31.0%)
output_tokens: baseline=8200 variant=9100  +900 (+11.0%)
critical_findings: baseline=0 variant=0
warning_findings: baseline=1 variant=2  +1
stylestat.pattern_top_per_chapter: baseline=3.1 variant=5.4  +2.3   ← 文体回归
stylestat.ending_short_ratio: baseline=0.42 variant=0.71  +0.29     ← 章末同构加重
```

### 9.2 Repeat 汇总

`--repeat N` 时不只看最后一次，当前实现展示通过率、hard fail 次数、warning 次数、cost/tool_calls 的 min/avg/max。Judge 接入后再追加 winner 分布，避免在默认确定性报告里混入模型裁判噪声。

```text
writer_first_chapter_xianxia repeat=3
- pass_rate: 3/3
- cost_usd: avg=0.41 min=0.38 max=0.44
- tool_calls: avg=13 min=12 max=15
- stylestat.pattern_top_per_chapter: avg delta=+0.4（无显著回归）
```

### 9.3 最小可行报告

```text
Gate: FAIL

Hard Fail:
- writer_first_chapter_xianxia: missing checkpoint chapter:1:commit

Warnings:
- writer_dialogue_density: tool_calls +35%
- writer_anti_ai_tone: ending_short_ratio +0.28 (文体回归)

Quality:
- writer_anti_ai_tone: judge prefers variant, confidence=medium

Artifacts:
- workspace/evals/20260629-120000/report.json
```

---

## 10. 目录结构与命令

```text
internal/eval/
  case.go        Case manifest 结构 + 加载
  eval.go        CLI 编排：single / A/B / repeat
  runner.go      装配 host 驱动（按章数上限截停 + drain 到 Done），bundle.OverridePrompt 内存覆盖
  collect.go     对产出目录跑 diag.Diagnose + stylestat.Compute + usage/tool_calls + 契约断言
  grade.go       Finding→门禁映射 + baseline/variant delta + stylestat gate 决策
  report.go      report.json + report.md

cmd/ainovel-cli  eval 子命令入口

evals/
  cases/         smoke/ workflow/ quality/ longform/ recovery/ steering/
  rubrics/       writer_chapter.json / architect_outline.json / editor_review.json
  variants/      writer-anti-ai-tone/writer.md 等（每目录只放要替换的 prompt）
  reports/       历史报告归档
```

命令：

```bash
# 多 case 批量（CI 默认只跑 smoke、不开 judge）
ainovel-cli eval --cases evals/cases/smoke \
  --variant evals/variants/writer-anti-ai-tone \
  --out workspace/evals/writer-anti-ai-tone --ci
```

**本期已实现的参数**：`--cases`（目录或单 manifest）、`--variant`（变体 prompt 目录；传入后自动跑 baseline+variant A/B）、`--repeat N`（每个 case 重复运行 N 次）、`--config`、`--out`、`--max-chapters N`（覆盖 case 默认）、`--timeout`（单 case 墙钟上限）、`--ci`（抑制逐事件输出；退出码非 0 即 hard fail，不带也生效）。

**规划中（尚未实现，勿在命令行使用，否则报 flag 未定义）**：`--judge`/`--no-judge`（Phase 3 LLM Judge）。重大 prompt 改动当前可先用确定性 A/B + repeat：

```bash
# 重大 prompt 改动：A/B + repeat 降随机性
ainovel-cli eval --cases evals/cases/quality \
  --variant evals/variants/writer-anti-ai-tone \
  --repeat 3 --ci
```

---

## 11. 明确不做的事

违反即代表评测偏离定位。

1. **不在评测层复制 diag 的通用诊断逻辑** —— 通用判断（pending 残留、phase/flow 一致、章节缺口、死循环）一律走 `diag`，事实判断只有一份定义。case 级契约断言（`expect.required_checkpoints` 等）允许直接读 `store`/checkpoint API，但只做**薄断言**——验证本 case 强相关的具体预期，绝不重写一遍 diag 已有的通用规则。
2. **不重新实现确定性规则** —— diag 已有一组工件规则 + 运行时规则。缺规则就去 diag 加，评测层只消费。
3. **不在 Python 里重写 stylestat 的中文文体逻辑** —— 直接调 Go 包。
4. **不让 LLM Judge 决定流程是否通过** —— 门禁只认确定性证据。
5. **不让评测介入控制流** —— 丢弃 diag 的 Action/Planner，不自动修 prompt、不回滚、不续跑、不发布。
6. **不断言精确工具调用序列** —— 只断言契约（commit 发生、checkpoint 存在），保护"LLM 驱动流程"的押注。
7. **不引入数据库 / Web UI / 在线评测平台** —— 当前阶段需要的是可重复、可落地、低成本的本地回归。
8. **不拷源码重编译做 variant** —— 内存覆盖 `bundle.Prompts`。
9. **不 mock 成功、不吞错误** —— 任何环节失败显式记录，case 跑崩即 FAIL。
10. **case 不随 prompt 频繁改动** —— case 是稳定测试集；为了让 variant 通过去改 case 是作弊。

---

## 12. 分阶段落地

### Phase 1 · Runner + 确定性门禁（MVP，先证假设）

- `internal/eval`：Case 结构 + runner（in-process headless + bundle 覆盖）+ collect（调 `diag.Diagnose`）+ grade（Finding→门禁 + `expect` 契约）。
- `evals/cases/smoke/` 放 3-4 个 case。
- 报告先出 `report.json` + 最小 markdown。

**验收**：一条命令跑完 smoke；Writer 跳过 commit、pending 残留、checkpoint 缺失、phase 不符**都能被门禁拦下**（这些 diag 本就能查，验证的是 harness 把它接对了）。

### Phase 2 · A/B + repeat + stylestat 回归（已实现）

- `--variant` 自动跑 baseline 与 variant，输出隔离 artifacts。
- `--repeat N` 汇总 pass rate、hard fail runs、warning runs、cost/tool_calls min/avg/max。
- collect 加 `stylestat.Compute`，grade 加文体 delta。
- 报告展示句式章均 / 章末短句占比 / 跨章重复句 / 标题混用的 baseline-variant 对比。

**验收**：用一个 ≥5 章的 case + 一个"句式 tic 加重"的 variant，能被文体回归标出 warning；章数不足的 case 明确显示 `insufficient_sample` 而非误判通过。

### Phase 3 · LLM Judge

- `evals/rubrics/` + `judge.go`，七维 rubric A/B。
- Judge 失败（非法 JSON）→ 报告记失败，不影响确定性结果。

**验收**：Judge 输出进 json+md，且不污染确定性门禁。

### Phase 4 · Longform & Recovery

- 3-5 章连续 / 弧末评审 / 用户干预 / pending_commit replay / 上下文压缩压力 case。
- 复用 diag context+运行时规则。

**验收**：能发现重复时间线、pending 残留、弧末摘要缺失、工具循环。

---

## 13. Case 维护规范

- **数量克制**：Smoke 3-5、Workflow 各角色 3-5、Quality 2-4、Longform/Recovery 各 2-3。过多没人愿意跑。
- **好 case**：输入短而明确、覆盖真实风险、少章内暴露问题、不依赖模型生成固定句子、不把风格偏好写太细。
- **差 case**：输入过长、同时多目标、要跑几十章才判断、只能靠主观感受。
- **Variant 命名**：`writer-anti-ai-tone` / `architect-rolling-outline` / `editor-strict-review`，每目录只放要替换的 prompt。

---

## 14. 风险与边界

- **模型随机性**：同 prompt 多跑也会变。重要改动 `--repeat 3` 看趋势。
- **成本**：Judge 与 longform 烧钱。本地默认只跑 **smoke**（1 章 × baseline+variant，确定性 diag 门禁、不开 Judge、不跑 stylestat）；**stylestat 在 ≥5 章的 Quality/Longform 才启用**（smoke 章数不足，`Compute` 返回 nil，报告标 `insufficient_sample`）；完整 suite 留给重大改动。
- **Judge 偏差**：Judge 也是模型，偏好工整解释性文本，未必等于好看的小说——所以只做辅助，stylestat 是确定性主线。
- **过度指标化**：字数/工具次数/成本/文体统计都是信号不是目标。stylestat 数字是否成病由人按题材裁定，**阈值不写死**（与 editor.md 一致）。
- **不做线上自动回滚**：离线回归工具，不负责线上自动改 prompt / 发布。

---

## 15. 总结

这套评测体系的价值不是自动判断文学质量，而是把 prompt 改动从"凭感觉"变成"有回归、有证据、有人工读样"。

它与上一版设计的根本区别只有一句：**评测器已经在代码库里了。** `diag` 是确定性事实诊断器，`stylestat` 是全书文体回归器，`ReviewEntry` 七维是原生 rubric。评测系统要做的是一层薄薄的 Go harness——批量驱动、采集、把 Finding 与统计映射成门禁、聚合报告——而不是用另一种语言把这些事实判断重写一遍。

一份事实定义，永不漂移。这正是这个项目从架构到评测一以贯之的纪律：**最小 harness、最强复用、确定性归代码、裁定归 LLM 与人。**

---

## 16. 参考

业界 LLM eval 的通用结构（dataset / experiment / scorer / trace / regression gate）是本设计的思想来源，但**刻意不照搬**——本项目的"scorer"是已有的 `diag`/`stylestat`，"trace"是已有的 checkpoint/session 事实层，"dataset"是贴事实层断言的 case。

- OpenAI Evals · https://developers.openai.com/api/docs/guides/evals （注：其托管 Evals 平台已公布退役时间线，只引其结构化测试/自动评分/人工校准的**思想**，不作为未来依赖）
- Braintrust · https://www.braintrust.dev/foundations/what-is-an-eval
- LangSmith · https://docs.langchain.com/langsmith/evaluation-concepts
