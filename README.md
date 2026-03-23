# ainovel-cli

全自动 AI 长篇小说创作引擎。基于多智能体协作架构，从一句话需求到完整小说，全程无需人工干预。

<p align="center">
  <img src="scripts/sample.gif" alt="ainovel-cli demo" width="800">
</p>

## 特性

- **多智能体协作** — Coordinator 调度 Architect / Writer / Editor 三个专职智能体，各司其职
- **确定性控制面** — 宿主程序通过信号文件驱动流程，不依赖 LLM 判断控制流
- **章节级断点恢复** — Ctrl+C、崩溃、断网后再次运行自动从上次进度续写，覆盖规划/写作/审阅/重写/干预全部阶段
- **卷弧双层滚动规划** — 长篇不再一次性规划全部章节。初始只规划前 2 卷弧骨架 + 第 1 弧详细章节，后续弧/卷在写作推进到时再由 Architect 展开，每次展开都参考前文摘要和角色状态，远期规划不空洞
- **相关章节智能推荐** — 每章写作时从伏笔、角色出场、状态变化、关系四个维度自动推荐相关历史章节，配合下一章预告，确保 500+ 章长篇的连续性
- **自适应上下文策略** — 根据总章节数自动切换全量 / 滑窗 / 分层摘要，支持 500+ 章长篇
- **七维质量评审** — Editor 从设定一致性、角色行为、节奏、叙事连贯、伏笔、钩子、审美品质七个维度评审，审美维度细分描写质感/叙事手法/对话区分度/用词质量/情感打动力五项，每项必须引用原文举证
- **用户实时干预** — 写作过程中随时在输入框注入修改意见（无需暂停），系统自动评估影响范围并重写受影响章节
- **双模式运行** — CLI 一行命令直接跑，TUI 交互界面实时观察进度
- **多 LLM 支持** — OpenRouter / Anthropic / Gemini / OpenAI 等等随意切换

## 架构

```
┌─────────────────────────────────────────────────┐
│                   Host（控制面）                  │
│  读取信号文件 → 确定性决策 → 注入 FollowUp 指令      │
└────────────┬────────────────────────┬───────────┘
             │                        │
     ┌───────▼───────┐      ┌────────▼────────┐
     │  Coordinator  │◄────►│   State Store   │
     │  （调度中枢）   │      │  （JSON 持久化）  │
     └──┬────┬────┬──┘      └─────────────────┘
        │    │    │
   ┌────▼┐ ┌▼───┐ ┌▼─────┐
   │Arch.│ │Wri.│ │Edit. │
   │建筑师│ │作家 │ │编辑  │
   └─────┘ └────┘ └──────┘
```

### 智能体职责

| 智能体 | 职责 | 工具 |
|--------|------|------|
| **Coordinator** | 调度全局，处理评审裁定和用户干预 | `subagent` `novel_context` `ask_user` |
| **Architect** | 生成前提、大纲、角色档案、世界规则 | `novel_context` `save_foundation` |
| **Writer** | 自主完成一章的构思、写作、自审和提交 | `novel_context` `read_chapter` `plan_chapter` `draft_chapter` `check_consistency` `commit_chapter` |
| **Editor** | 阅读原文，从结构和审美两个层面审阅 | `novel_context` `read_chapter` `save_review` `save_arc_summary` `save_volume_summary` |

### 写作流程

```
用户需求 → Architect 规划骨架 + 首弧章节 → Writer 逐章写作 → Editor 弧级评审
                                                  ↑                    │
                                                  ├── 重写/打磨 ◄──────┘
                                                  │
                                           Architect 展开下一弧/卷
                                          （参考前文摘要+角色快照）
```

Writer 自主决定每章的创作流程，建议路径：

1. `novel_context` — 加载上下文（前情摘要、时间线、伏笔、角色状态、风格规则、下一章预告、相关章节推荐）
2. `read_chapter` — 回读前一章结尾和角色对话，找回语气和节奏
3. `plan_chapter` — 构思本章目标、冲突、情绪弧线
4. `draft_chapter` — 写入整章正文
5. `read_chapter` + `check_consistency` — 自审：回读草稿，对照状态数据检查一致性
6. `commit_chapter` — 提交终稿，更新全局状态（可选附带大纲偏离反馈）

### 长篇滚动规划

传统方案一次规划所有章节，300+ 章时大纲空洞、节奏像赶进度。本系统采用**卷弧双层滚动规划**，模拟网文作者的真实创作流程：

```
初始规划                     弧结束时                      卷结束时
┌────────────────────┐    ┌─────────────────────┐    ┌─────────────────────┐
│ 全书远景（所有卷）    │    │ Editor 弧级评审      │    │ Editor 卷级评审      │
│ 前2卷弧骨架          │    │ 弧摘要 + 角色快照     │    │ 卷摘要               │
│ 第1弧详细章节        │ →  │ Architect 展开下一弧  │ →  │ Architect 展开下一卷  │
│ 角色 + 世界观        │    │ Writer 继续写作       │    │ Writer 继续写作       │
└────────────────────┘    └─────────────────────┘    └─────────────────────┘
```

- **骨架卷** — 只有 title + theme + 预估章数，到达时再展开弧结构
- **骨架弧** — 只有 goal + 预估章数，到达时再展开详细章节
- **渐进细化** — 每次展开都参考前文摘要、角色快照、风格规则，越往后写越精确
- **通用节奏模板** — 成长突破弧 / 竞技对抗弧 / 探索发现弧 / 恩怨冲突弧 / 日常过渡弧，每种弧型有参考密度和适用题材映射

### 长篇上下文管理

500+ 章小说采用三级摘要 + 智能推荐：

```
卷（Volume）→ 卷摘要
└── 弧（Arc）→ 弧摘要 + 角色快照 + 风格规则
    └── 章（Chapter）→ 章摘要（滑窗最近3章）
```

- **分层摘要** — 近处用章摘要，中距离用弧摘要，远处用卷摘要，层层压缩不丢信息
- **相关章节推荐** — 每章写作时从伏笔、角色出场、状态变化、关系四个维度反查历史章节，推荐 Writer 按需回读
- **下一章预告** — 加载下一章大纲，帮 Writer 设计章末钩子和伏笔衔接
- **弧边界检测** — 自动识别弧/卷结束，触发评审、摘要生成和下一弧/卷展开

## 快速开始

```bash
# 安装
go install github.com/voocel/ainovel-cli/cmd/ainovel-cli@latest

# 本地开发运行
go run ./cmd/ainovel-cli

# 首次运行，自动进入引导流程（选择 Provider → 输入 API Key → Base URL → 模型名）
ainovel-cli

# CLI 模式：一行启动
ainovel-cli "写一部12章都市悬疑小说，主角是刑警，暗线是家族秘密"
```

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
      "base_url": "https://openrouter.ai/api/v1"
    }
  },
  "style": "default",
  "context_window": 128000
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
│   ├── layered_outline.json # 分层大纲（含骨架卷/弧，长篇模式）
│   ├── characters.json # 角色档案
│   ├── world_rules.json# 世界规则
│   ├── progress.json   # 进度状态
│   ├── timeline.json   # 时间线
│   ├── foreshadow.json # 伏笔台账
│   ├── state_changes.json # 角色状态变化记录
│   ├── style_rules.json# 写作风格规则（弧边界时提炼）
│   ├── snapshots/      # 角色状态快照（长篇）
│   ├── checkpoints/    # 进度快照（每次提交/评审后保存）
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

所有创作产物（大纲、角色、摘要、终稿、时间线、伏笔、关系）都以 JSON 文件持久化在 `output/` 目录。重启时：

1. 读取 `progress.json`（阶段、已完成章节、当前流程状态）和 `run.json`（规划级别、未完成干预）
2. 自动判断恢复类型，生成对应的恢复指令
3. Coordinator 通过 `novel_context` 工具重新加载上下文（摘要、角色、世界观等），恢复创作

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

### CLI 模式

CLI 模式下直接在终端输入文字按 Enter，效果相同：

```bash
ainovel-cli "写一部悬疑小说"
# 运行中直接输入：
主角的父亲应该是幕后黑手
# 按 Enter 注入
```

### 干预示例

| 干预指令 | 系统可能的响应 |
|---|---|
| "主角改成女性" | 修改角色设定，评估已写章节是否需要重写 |
| "把感情线提前到第4章" | 调整大纲，可能重写第4章及后续 |
| "加入一个反派角色" | 更新角色档案和世界规则，在后续章节引入 |
| "节奏太慢了，加快推进" | 调整后续章节的大纲密度 |

## 设计理念

### Agent 驱动原则

**工具负责 IO，Agent 负责思考。不要用流水线绑住 Agent 的手脚。**

这是本项目所有设计决策的最高优先级准则。具体要求：

1. **工具只做数据读写** — 工具不包含业务逻辑判断，不强制执行顺序。工具是 Agent 的手和眼，不是 Agent 的脑。
2. **决策权归 Agent** — 规划、写作、打磨、自审都是 Agent 的思考行为，不是工具调用节点。Agent 自主决定何时读、何时写、何时审。
3. **不用流水线约束创作** — 不强制"先规划→再按场景写→再打磨→再检查"的固定流程。Writer 可以先写完整章，回读后修改，自审后提交，顺序自定。
4. **给 Agent 感知能力** — Agent 能回读自己写的文字和前文原文，而非只看结构化摘要。风格保持靠阅读原文，不靠字段描述。
5. **Host 只兜底控制流** — 确定性状态机只负责"下一步该做什么"的流程判断，不干预创作内容。

任何新增功能或工具设计，都必须先问：**这是 IO 操作还是思考行为？** 如果是思考，交给 Agent；如果是 IO，才做成工具。

### 全自动闭环

一句话输入，完整小说输出，中间零人工干预。系统自主完成全部创作决策：

```
"写一部悬疑小说" → 构建世界观 → 设计角色 → 规划大纲
                → 逐章写作 → 质量评审 → 自动重写
                → 弧级摘要 → 角色快照 → 完整成书
```

**自主决策能力：**

- **Architect 自主构建** — 从用户一句话需求推导出完整的前提、大纲、角色关系和世界规则
- **Writer 自主创作** — 每章独立完成规划、写作、打磨、一致性校验的完整闭环
- **Editor 自主评审** — 跨章节分析结构问题，输出裁定（通过 / 打磨 / 重写）及影响范围
- **Coordinator 自主调度** — 根据评审裁定安排重写，根据弧边界触发摘要生成，无需外部指令
- **自动伏笔管理** — 埋设、推进、回收全程由 Agent 自行追踪，不会烂尾
- **自动节奏调控** — 追踪叙事线和钩子类型历史，避免连续章节结构雷同

### 确定性控制面

Agent 负责创造，Host 负责兜底。**控制流不交给 LLM 判断**。

Writer 调用 `commit_chapter` 后，宿主程序读取信号文件 `meta/last_commit.json`，确定性地决定下一步：

| 信号 | 宿主动作 |
|------|----------|
| 全部章节完成 | 标记完成，通知 Coordinator 总结全书 |
| `review_required=true` | 注入 Editor 评审指令 |
| `arc_end=true` | 注入弧级评审 + 弧摘要生成指令 |
| `arc_end` + `needs_expansion` | 额外注入 Architect 展开下一弧指令 |
| `volume_end=true` | 额外注入卷摘要生成指令 |
| `volume_end` + `needs_volume_expansion` | 额外注入 Architect 展开下一卷指令 |
| 有待重写章节 | 注入重写指令 |
| 以上皆否 | 注入"继续写下一章"指令 |

Editor 评审裁定同理：`accept` → 继续，`polish/rewrite` → 注入修改指令。

这种设计保证：即使 LLM 幻觉或遗忘，宿主层的状态机也能把流程拉回正轨。

## 技术栈

- **Go 1.25** — 主语言
- **[agentcore](https://github.com/voocel/agentcore)** — 极简 Agent 内核（tool-calling + streaming）
- **[litellm](https://github.com/voocel/litellm)** — 统一 LLM 接口适配
- **[Bubble Tea](https://github.com/charmbracelet/bubbletea)** — 终端 TUI 框架

## License

MIT
