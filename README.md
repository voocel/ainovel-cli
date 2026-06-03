# ainovel-cli

全自动 AI 长篇小说创作引擎。Coordinator 在一次 Prompt 里驱动 Architect / Writer / Editor 三个子代理完成整本书的创作，Host 只做启动、恢复和观察。从一句话需求到完整小说，全程无需人工干预。

<p align="center">
  <img src="scripts/sample.gif" alt="ainovel-cli demo" width="800">
  <img src="scripts/novel.png" alt="ainovel-cli bg" width="800">
</p>

## 特性

- **多智能体协作** — Coordinator 在一次长循环中调度 Architect / Writer / Editor 三个子代理，自主决策创作流程
- **LLM 驱动长循环** — 一次 Prompt 写完整本书，Host 不介入调度。越简单越稳定，拒绝复杂编排
- **Step 级断点恢复** — 每个工具执行成功后写入 checkpoint，崩溃后精确到 plan/draft/check/commit 步骤级恢复
- **卷弧双层滚动规划** — 长篇不再一次性规划全部章节。初始只规划前 2 卷弧骨架 + 第 1 弧详细章节，后续弧/卷在写作推进到时再由 Architect 展开，每次展开都参考前文摘要和角色状态，远期规划不空洞
- **相关章节智能推荐** — 每章写作时从伏笔、角色出场、状态变化、关系四个维度自动推荐相关历史章节，配合下一章预告，确保 500+ 章长篇的连续性
- **自适应上下文策略** — 根据总章节数自动切换全量 / 滑窗 / 分层摘要，支持 500+ 章长篇
- **七维质量评审** — Editor 从设定一致性、角色行为、节奏、叙事连贯、伏笔、钩子、审美品质七个维度评审，审美维度细分描写质感/叙事手法/对话区分度/用词质量/情感打动力五项，每项必须引用原文举证
- **用户实时干预** — 写作过程中随时在输入框注入修改意见（无需暂停），系统自动评估影响范围并重写受影响章节
- **统一 TUI 入口** — 交互界面实时观察进度，也支持携带一句需求直接启动
- **多 LLM 支持** — OpenRouter / Anthropic / Gemini / OpenAI 等等随意切换
- **多人格竞稿** — 配置 ≥2 个作者名后，每章由多个人格各写一稿，Judge 选优并给出修改意见，中选者润色后提交；不配置或 <2 个即退回普通单 Writer 模式，零额外成本

## 架构

核心设计：**LLM 驱动，Host 服务**。Coordinator 在一次 Run 中自主决策整本书的创作流程，Host 只做启动、恢复和事件观察。

```
┌─────────────────────────────────────────────────┐
│                Host（薄外壳）                     │
│           启动 / 恢复 / 观察 / 干预注入            │
└──────────────────────┬──────────────────────────┘
                       │ 一次 Prompt
┌──────────────────────▼──────────────────────────┐
│              Coordinator（LLM 长循环）            │
│    读 novel_context → 调子代理 → 读结果 → 继续     │
└────┬──────────┬──────────┬──────────────────────┘
     │          │          │
 ┌───▼────┐ ┌───▼───┐ ┌────▼────┐
 │Architect│ │Writer │ │ Editor  │
 └───┬────┘ └───┬───┘ └────┬────┘
     └──────────┼──────────┘
                │ 工具调用（IO + checkpoint）
┌───────────────▼─────────────────────────────────┐
│                   Store                         │
│  Progress / Checkpoint / Outline / Drafts / ... │
└─────────────────────────────────────────────────┘
```

- **Host** — 启动 Coordinator、崩溃恢复、事件投影给 TUI。不做任何调度决策
- **Coordinator** — 唯一的决策者，在一次 Run 里驱动规划→写作→评审→总结的完整流程
- **SubAgents** — Architect / Writer / Editor 各自独立 context，通过 Store 中的工件协作
- **Tools** — 原子 IO + checkpoint 写入，只返事实 JSON，不夹带指令

### 智能体职责

| 智能体 | 职责 | 工具 |
|--------|------|------|
| **Coordinator** | 调度全局，处理评审裁定和用户干预 | `subagent` `novel_context` |
| **Architect** | 生成前提、大纲、角色档案、世界规则 | `novel_context` `save_foundation` |
| **Writer** | 自主完成一章的构思、写作、自审和提交 | `novel_context` `read_chapter` `plan_chapter` `draft_chapter` `check_consistency` `commit_chapter` |
| **Editor** | 阅读原文，从结构和审美两个层面审阅 | `novel_context` `read_chapter` `save_review` `save_arc_summary` `save_volume_summary` |

### 写作流程

```
用户需求 → Architect 规划骨架 + 首弧章节 → Writer 逐章写作 → Editor 弧级评审
                                                  ↑                   │
                                                  ├── 重写/打磨 ◄──────┘
                                                  │
                                           Architect 展开下一弧/卷
                                          （参考前文摘要+角色快照）
```

Writer 按固定顺序完成每章（写作内容完全自主，工具调用顺序严格）：

1. `novel_context` — 加载上下文（前情摘要、伏笔、角色状态、风格规则、相关章节推荐）
2. `read_chapter` — 回读前文找回语气和节奏
3. `plan_chapter` — 构思本章目标、冲突、情绪弧线
4. `draft_chapter` — 写入整章正文
5. `check_consistency` — 对照状态数据检查一致性（必须在 draft 之后）
6. `commit_chapter` — 提交终稿，返回事实字段（`arc_end_reached` / `next_chapter` 等），下一步由 Reminder 驱动

### 状态迁移规则

系统内部把运行状态拆成两层：

- **Phase** — 大阶段，表示作品目前处于设定期、写作期还是已完成
- **Flow** — 当前活跃流程，表示系统此刻是在正常写作、审阅、重写、打磨还是处理用户干预

#### Phase

`Phase` 采用“只前进不回退”的规则：

```text
init -> premise -> outline -> writing -> complete
  \-------> outline ------^
  \--------------> writing
```

含义：

- `init` — 任务已创建，尚未形成稳定设定
- `premise` — 已保存故事前提
- `outline` — 已保存大纲，可以进入正式写作
- `writing` — 已进入章节创作期
- `complete` — 全书流程结束

规则说明：

- 允许同态更新，例如 `writing -> writing`
- 允许前进，例如 `outline -> writing`
- 不允许回退，例如 `writing -> premise`、`complete -> writing`

#### Flow

`Flow` 只描述写作期内的活跃流程，允许在几个工作流之间切换：

```text
writing   -> reviewing / rewriting / polishing / steering / writing
reviewing -> writing / rewriting / polishing / steering / reviewing
rewriting -> writing / steering / rewriting
polishing -> writing / steering / polishing
steering  -> writing / reviewing / rewriting / polishing / steering
```

含义：

- `writing` — 正常推进下一章
- `reviewing` — Editor 正在评审
- `rewriting` — 处理必须重写的章节
- `polishing` — 处理只需打磨的章节
- `steering` — 正在评估并处理用户干预

规则说明：

- 允许 `writing -> reviewing`，例如章节提交后触发评审
- 允许 `reviewing -> rewriting/polishing/writing`，由评审结果决定
- 允许 `steering -> writing/reviewing/rewriting/polishing`，由干预影响范围决定
- 不允许明显反常的跳转，例如 `rewriting -> reviewing`

这些规则现在由代码中的轻量校验统一约束，避免状态回退或跳到不合理的流程分支。

### 长篇滚动规划

传统方案一次规划所有章节，300+ 章时大纲空洞、节奏像赶进度。本系统采用**指南针 + 视野滚动规划**，模拟网文作者的真实创作流程：

```
初始规划                     弧结束时                      卷结束时
┌────────────────────┐    ┌─────────────────────┐    ┌─────────────────────┐
│ 终局方向（指南针）    │    │ Editor 弧级评审      │    │ Editor 卷级评审       │
│ 起步 2 卷，后续按需   │    │ 弧摘要 + 角色快照     │    │ 卷摘要               │
│ 第1弧详细章节        │ →  │ Architect 展开下一弧  │ →  │ Architect 自主创建   │
│ 角色 + 世界观        │    │ Writer 继续写作      │    │ 下一卷 + 更新指南针    │
└────────────────────┘    └─────────────────────┘    └─────────────────────┘
```

- **指南针（Compass）** — 终局方向 + 活跃长线 + 规模估计，每次卷边界由 Architect 更新，故事方向可随创作演化
- **按需生成** — 当前卷写完后，Architect 根据已写内容自主创建下一卷。初始规划生成 2 卷作为起步，后续卷按需生成
- **骨架弧** — 只有 goal + 预估章数，到达时再展开详细章节
- **渐进细化** — 每次展开都参考前文摘要、角色快照、风格规则，越往后写越精确
- **通用节奏模板** — 成长突破弧 / 竞技对抗弧 / 探索发现弧 / 恩怨冲突弧 / 日常过渡弧，每种弧型有参考密度和适用题材映射

### 长篇上下文管理

500+ 章小说采用三级摘要 + 四级压缩管线 + 智能推荐：

```
卷（Volume）→ 卷摘要
└── 弧（Arc）→ 弧摘要 + 角色快照 + 风格规则
    └── 章（Chapter）→ 章摘要（滑窗最近3章）
```

- **分层摘要** — 近处用章摘要，中距离用弧摘要，远处用卷摘要，层层压缩不丢信息
- **相关章节推荐** — 每章写作时从伏笔、角色出场、状态变化、关系四个维度反查历史章节，推荐 Writer 按需回读
- **下一章预告** — 加载下一章大纲，帮 Writer 设计章末钩子和伏笔衔接
- **弧边界检测** — 自动识别弧/卷结束，触发评审、摘要生成和下一弧/卷展开

#### 上下文压缩管线

当对话超出模型上下文窗口时，按代价从低到高逐级压缩：

```
ToolResultMicrocompact → LightTrim → StoreSummaryCompact → FullSummary
     清理旧工具结果        截断长文本      store 零 LLM 压缩      LLM 摘要兜底
```

- **StoreSummaryCompact** — Writer 专用，用 store 中已有的章节摘要、角色快照、伏笔台账直接替换旧消息，零 LLM 开销
- **FullSummary 小说定制** — Writer 使用面向叙事连续性的摘要提示词，明确要求保留角色状态、伏笔线索、审稿待修项、风格锚点
- **压缩后恢复包** — FullSummary 后自动注入当前章节计划、大纲和角色快照，防止 Writer 压缩后"失忆"
- **熔断器** — 压缩连续失败时自动跳过并显式告警，采用半开模式，下轮自动重试
- **CJK Token 估算** — 中文 `runes × 1.5`，不会因为 `bytes/4` 低估而导致压缩触发滞后
- **TUI 健康度渐变** — 上下文占用绿(<70%)→黄(70-85%)→红(>85%)实时展示

## 快速开始

```bash
# 安装
go install github.com/voocel/ainovel-cli/cmd/ainovel-cli@latest

# 本地开发运行
go run ./cmd/ainovel-cli

# 首次运行，自动进入引导流程（选择 Provider → 输入 API Key → Base URL → 模型名）
ainovel-cli
```

进入 TUI 后，启动阶段支持两种前置交互：

- `快速开始`：一句话直接进入创作
- `共创规划`：与 AI 多轮对话澄清需求，**右侧实时同步整理出的创作指令草稿**；AI 每轮主动提供 1-3 条引导建议，按数字键一键填入输入框，按 `Ctrl+S` 进入正式创作

两种模式最终都会收敛为同一份创作指令，再进入同一套创作引擎。

### 管理多本小说

每本小说绑定到启动目录，产物落在 `{cwd}/output/novel/`。换目录启动 = 换一本，`cd` 回去启动 = 自动从最近 checkpoint 恢复。配置 `~/.ainovel/config.json` 全局共享，无需复制。

### 配置文件

首次运行时自动引导生成配置文件 `~/.ainovel/config.json`，后续可直接编辑该文件调整设置。删除配置文件后重新运行会再次进入引导流程。

也可以手动创建配置文件，参考 `~/.ainovel/config.example.jsonc`（引导时自动生成）。

```jsonc
{
  "provider": "openrouter",
  "model": "google/gemini-2.5-flash",
  "providers": {
    "openrouter": {
      "api_key": "sk-or-v1-xxx",
      "base_url": "https://openrouter.ai/api/v1",
      "models": ["google/gemini-2.5-flash", "google/gemini-2.5-pro"]
    }
  },
  "style": "default"
}
```

#### 配置文件查找顺序（后者覆盖前者）

1. `~/.ainovel/config.json` — 全局配置
2. `./ainovel.json` — 项目级覆盖（可选）
3. `--config path/to/config.json` — 命令行指定

覆盖规则说明：

- 标量字段按后者覆盖前者，例如 `provider`、`model`、`style`
- `providers` 和 `roles` 按 key 合并，同名项内部按字段覆盖
- 未填写的字段会继承上层配置，例如项目级配置只写 `base_url` 时会保留全局配置中的 `api_key`
- 当前不支持用空字符串显式清空上层已有值；如需清空，请直接编辑更高优先级的配置文件

`providers.<name>.models` 为可选字段，用于声明该 provider 下允许在 TUI `/model` 面板中切换的模型列表；如果未配置，系统会回退为当前配置文件里已经出现过的该 provider 模型。

## 诊断报告

在 TUI 中输入 `/report` 可对当前小说的 output 产物进行诊断分析，产出可执行的发现和改进建议。

诊断覆盖四个维度：

- **流程** — 改写循环卡顿、未消费的转向指令、阶段/流程状态异常、章节跳号
- **质量** — 评审维度持续低分、合同履约率、改写率、章节字数异常
- **规划** — 伏笔停滞、指南针过时、大纲耗尽、摘要缺失
- **上下文** — 角色消失、时间线缺口、关系数据停滞

每条发现包含：问题描述、数据证据、改进建议（指向具体的 prompt/flow/config）。

## 导入

在 TUI 中输入 `/import <文件路径>` 可把一本已有的小说反推导入：先按章切分，再用 LLM 反推出前提 / 角色 / 世界观 / 大纲，逐章落盘。导入完成后状态等同于"已写完 N 章"，输入"继续创作"即可让 Coordinator 接力续写。

```
/import ~/我的小说.txt              # 从头导入并反推 foundation
/import ~/我的小说.txt from=50      # 从第 50 章接着导入（跳过反推）
/import ~/我的小说.txt regex=^第.+話$  # 自定义章节标题正则
```

**章节切分规则**：默认正则识别这些标题格式（行首，可带 `#`/`##` Markdown 前缀）：

- 中文编号：`第一章` `第3回` `第十话` `第二卷`、独立 `卷一`，可带副标题（`第三章：决战`）
- 中文特殊单元：`序章` `楔子` `引子` `前言` `尾声` `终章` `后记` `番外`
- 英文：`Chapter 1` `Chapter II`、`Prologue` `Epilogue`，可带副标题（`Chapter 1: The Beginning`）

若提示**"未识别到任何章节"**，说明你的文件用了其它标题格式（如 `001`、`（一）`、剧本式），用 `regex=` 参数传入自定义正则即可（至少含一个捕获组用于提取标题）。

> 导入是确定性回放，不经过 Coordinator；原文会逐字落盘为已完成章节，因此适合"续写同一本书"。如果只想借鉴设定做全新创作，请用普通方式起一本新书、在需求里描述想要的风格设定。

## 导出

在 TUI 中输入 `/export` 可把已完成的章节合并导出，默认 TXT，写到 `{novelDir}/{NovelName}.txt`。导出是只读操作，写作中途也可以随时拿"现阶段成品"，不影响 Coordinator 运行。

格式由**输出路径后缀**决定（命令永远只这一行，零新增参数）：

```text
/export                            # 默认 TXT，{novelDir}/{NovelName}.txt
/export ~/光斑.txt                  # 后缀 .txt → TXT
/export ~/光斑.epub                 # 后缀 .epub → EPUB（Apple Books / 微信读书 / Kindle 转换器可读）
/export from=10 to=30 --overwrite  # 章节区间 + 覆盖
/export from=10 ~/x.epub --overwrite
```

- **TXT** — 标题页（`《书名》` + premise）→ 卷分隔 → 弧分隔 → 章节正文，长篇分层模式自动加卷/弧分隔。
- **EPUB** — EPUB 3 标准容器，含封面页、目录、按章拆分的 XHTML，标识符基于内容稳定派生（重导出同一本书阅读器识别为更新版本）。不带封面图。

范围内未完成的章节会跳过并显示在结果里，不算错误。

#### 按角色使用不同模型

通过 `roles` 字段为不同智能体分配不同的模型，未配置的角色使用默认模型：

```jsonc
{
  "provider": "openrouter",
  "model": "google/gemini-2.5-flash",
  "providers": {
    "openrouter": { "api_key": "sk-or-v1-xxx", "base_url": "https://openrouter.ai/api/v1" },
    "anthropic": { "api_key": "sk-ant-xxx" }
  },
  "roles": {
    "writer": { "provider": "anthropic", "model": "claude-sonnet-4" },
    "architect": { "provider": "openrouter", "model": "google/gemini-2.5-pro" }
  }
}
```

可配置的角色：`coordinator` / `architect` / `writer` / `editor`

#### 自定义代理

选择任意 Provider 后填写代理地址即可，或使用 Custom Proxy 并指定 API 协议类型。自定义代理的 `api_key` 可选；如果你的代理不需要认证，可以省略：

```jsonc
{
  "provider": "my-proxy",
  "model": "gpt-4o",
  "providers": {
    "my-proxy": {
      "type": "openai",
      "base_url": "https://proxy.example.com/v1"
    }
  }
}
```

支持的 Provider：`openrouter` / `anthropic` / `gemini` / `openai` / `deepseek` / `qwen` / `glm` / `grok` / `ollama` / `bedrock` 及任意自定义代理。

关于 `api_key`：

- `openrouter` / `anthropic` / `gemini` / `openai` / `deepseek` / `qwen` / `glm` / `grok` 这类托管接口通常需要填写 `api_key`
- `ollama` 和 `bedrock` 允许不填 `api_key`
- 显式指定了 `type` 的自定义代理允许不填 `api_key`

例如本地 `ollama` 配置：

```jsonc
{
  "provider": "ollama",
  "model": "qwen3:latest",
  "providers": {
    "ollama": {
      "base_url": "http://localhost:11434"
    }
  }
}
```

### 写作风格

通过配置文件的 `style` 字段切换：

- `default` — 通用风格
- `suspense` — 悬疑推理
- `fantasy` — 奇幻仙侠
- `romance` — 言情

### 多人格竞稿（writing_contest）

在配置文件中添加 `writing_contest.personas`（≥2 个作者名）即可开启竞稿模式。

```jsonc
{
  "writing_contest": {
    "personas": ["乌贼", "卖报小郎君", "土豆"]
  }
}
```

工作流程：每章写作时，系统为每个 persona 各调用一次 Writer，生成对应文风的候选稿；随后 Judge 子代理评分并选出最优稿，附上修改意见；中选 Writer 按意见润色后提交终稿。

- **文风来源** — 启动时由 LLM 根据作者名自动生成并缓存到 `personas.json`，全书写作期间保持稳定，不会因恢复而重新生成
- **成本** — 串行执行，写作开销约为 persona 数量的倍数
- **向下兼容** — 不配置该字段、或 `personas` 少于 2 个时，退回普通单 Writer 模式，行为与旧版完全一致
- **judge 字段** — 当前预留，暂复用 `editor` 模型，配置不生效

### 候选并发

配置 `writing_contest.concurrency: true` 后，同一章的多个 persona 候选稿并发生成（一次 `subagent(tasks=[...])` 调用，受 agentcore `maxConcurrency=4` 限流），judge 选优、中选稿提升与润色仍串行。某 persona 候选连续失败 3 次自动弃权，候选数减一继续；全部弃权降级为单 Writer。崩溃恢复由磁盘事实（候选槽 / verdict / contest.json）驱动，幂等。

## 输出结构

所有创作数据（章节、大纲、角色、进度等）保存在output目录中。中断后重新运行会自动从上次进度续写。删除output目录将重新开始创作。

```
output/{novel_name}/
├── chapters/           # 终稿（Markdown）
│   ├── 01.md
│   └── ...
├── summaries/          # 章节摘要（JSON）
├── drafts/             # 章节草稿
├── reviews/            # 评审报告
├── meta/
│   ├── premise.md      # 故事前提
│   ├── outline.json    # 扁平章节大纲（仅含已展开的章节）
│   ├── layered_outline.json # 分层大纲（当前卷 + 预览卷，长篇模式）
│   ├── compass.json   # 终局方向指南针（长篇模式）
│   ├── characters.json # 角色档案
│   ├── world_rules.json# 世界规则
│   ├── progress.json   # 进度状态
│   ├── timeline.json   # 时间线
│   ├── foreshadow.json # 伏笔台账
│   ├── state_changes.json # 角色状态变化记录
│   ├── style_rules.json# 写作风格规则（弧边界时提炼）
│   ├── snapshots/      # 角色状态快照（长篇）
│   ├── checkpoints.jsonl # Step 级 checkpoint（每个工具成功后追加）
│   ├── characters.md   # 角色档案（可读版）
│   └── world_rules.md  # 世界规则（可读版）
```

## 断点恢复

写一部长篇小说可能需要数小时甚至数天，中途崩溃、断网、Ctrl+C 都是常见情况。系统在**同一目录再次运行时自动恢复**，无需手动操作。

### 恢复场景

| 中断时机 | 恢复行为 |
|---|---|
| 规划阶段（正在构建世界观/大纲） | 检查已保存的设定，自动补全缺失项 |
| 某章正在写作（有草稿未提交） | 从该章续写，读取已有草稿继续 |
| 审阅进行中 | 重新触发 Editor 评审 |
| 重写/打磨队列未清空 | 继续处理待重写的章节 |
| 弧/卷展开中断（评审完但下一弧未展开） | 自动检测骨架弧/卷，触发 Architect 展开 |
| 用户干预未完成 | 重新注入上次的干预指令 |
| 正常写作中断 | 从下一章继续 |

### 工作原理

所有创作产物持久化在 `output/` 目录。每个工具执行成功后写入 checkpoint (`meta/checkpoints.jsonl`)。重启时：

1. 读取 `progress.json` + 最近 checkpoint + 待处理信号
2. 精确到 step 级生成恢复指令（如"第 7 章 draft 已落盘，请继续 check_consistency"）
3. 一次 `Prompt` 启动 Coordinator，进入长循环继续创作

> 文件写入使用 temp + fsync + rename 原子操作，即使在写入过程中断电也不会损坏已有数据。

## 实时干预（Steer）

创作过程中可以随时通过输入框注入修改意见，**不需要暂停或重启**。

### TUI 模式

创作启动后，底部输入框自动切换为干预模式：

```
❯ 把感情线提前到第4章，增加男女主的对手戏
```

输入后按 Enter，系统自动：
1. 记录干预指令到 `run.json`（崩溃恢复用）
2. 注入到正在运行的 Coordinator
3. Coordinator 评估影响范围，决定是修改设定、重写已有章节，还是在后续章节调整

### 干预示例

| 干预指令 | 系统可能的响应 |
|---|---|
| "主角改成女性" | 修改角色设定，评估已写章节是否需要重写 |
| "把感情线提前到第4章" | 调整大纲，可能重写第4章及后续 |
| "加入一个反派角色" | 更新角色档案和世界规则，在后续章节引入 |
| "节奏太慢了，加快推进" | 调整后续章节的大纲密度 |

## 设计理念

> **把复杂度从代码搬到模型里。** 代码越少，能坏的地方越少。决策权交给更擅长做决策的角色。

### LLM 驱动，越简单越稳定

- **决策权归 LLM** — 流程决策全部由 Coordinator 自主判断，Host 不介入。工具失败时返回结构化错误，由 LLM 自行决定重试或调整策略
- **工具只返事实** — 原子 IO + checkpoint 写入，返回值是 JSON 事实字段（`final_verdict` / `pending_rewrites` / `arc_end_reached`），不夹带任何指令字符串
- **Reminder 驱动每轮** — Host 在每轮 LLM 调用前读事实层，运行纯函数 generator 生成 `<system-reminder>` 注入，指令不进持久历史、每轮从事实重算
- **StopGuard 物理守门** — `Phase ≠ Complete` 时 Coordinator 物理上不可 `end_turn`，连续阻拦超限才升级终止
- **拒绝复杂编排** — 没有 task queue、没有 scheduler、没有 policy engine。Coordinator 的一次 Run 就是唯一的控制流
- **模型越强收益越大** — 架构把决策权留在 prompt 和工具语义里，模型升级后直接吃到收益，Host 一行不用改

### 全自动闭环

一句话输入，完整小说输出：

```
“写一部悬疑小说” → 构建世界观 → 设计角色 → 规划大纲
                → 逐章写作 → 质量评审 → 自动重写
                → 弧级摘要 → 角色快照 → 完整成书
```

- **Coordinator 自主调度** — 在一次长循环里读事实层 + Reminder 决定下一步，无需 Host 干预
- **Writer 自主创作** — 每章独立完成 plan → draft → check → commit 的完整闭环
- **Editor 自主评审** — 跨章节分析结构问题，输出裁定及影响范围
- **Architect 自主构建** — 从一句话需求推导出完整设定，弧/卷边界时自主展开后续规划
- **自动伏笔管理** — 埋设、推进、回收全程由 Agent 自行追踪
- **自动节奏调控** — 追踪叙事线和钩子类型历史，避免连续章节结构雷同

### 事实与指令解耦

工具只返事实，指令由 Reminder 每轮从事实层重算：

- `commit_chapter` / `save_review` 返回结构化事实（`final_verdict` / `pending_rewrites` / `arc_end_reached` / `next_chapter`），不夹带任何 `[系统]` 字符串
- `internal/host/reminder/` 下的纯函数 generator 读 `Progress` + `Outline`，每轮 pre-turn 生成 `<system-reminder>`：`flow`（当前该做什么 / 弧末刹车）/ `queue_guard`（队列未清禁止新章）/ `book_complete`（全书完成才放行）。物理兜底由 `StopGuard` 在 `phase≠Complete` 时拒绝 `end_turn` 承担
- Reminder 只存活一轮，不进历史、不参与压缩；规则有单元测试，退化可被回归捕获

这样指令不会被链式调用吞掉，也不会在工具产物里漂移。改 bug 只需加一个 generator + 一个测试。

## 技术栈

- **Go 1.25** — 主语言
- **[agentcore](https://github.com/voocel/agentcore)** — 极简 Agent 内核（tool-calling + streaming）
- **[litellm](https://github.com/voocel/litellm)** — 统一 LLM 接口适配
- **[Bubble Tea](https://github.com/charmbracelet/bubbletea)** — 终端 TUI 框架

## License

MIT

本项目积极参与并认可 [linux.do 社区](https://linux.do/)。
