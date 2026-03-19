# ainovel-cli

全自动 AI 长篇小说创作引擎。基于多智能体协作架构，从一句话需求到完整小说，全程无需人工干预。

<p align="center">
  <img src="scripts/sample.gif" alt="ainovel-cli demo" width="800">
</p>

## 特性

- **多智能体协作** — Coordinator 调度 Architect / Writer / Editor 三个专职智能体，各司其职
- **确定性控制面** — 宿主程序通过信号文件驱动流程，不依赖 LLM 判断控制流
- **章节级断点恢复** — 中断后从上次写到的章节续写，不丢失进度
- **自适应上下文策略** — 根据总章节数自动切换全量 / 滑窗 / 分层摘要，支持 500+ 章长篇
- **七维质量评审** — Editor 从设定一致性、角色行为、节奏、叙事连贯、伏笔、钩子、审美品质七个维度评审，审美维度必须引用原文举证
- **用户实时干预** — 写作过程中可随时注入修改意见，系统自动评估影响范围并重写
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
用户需求 → Architect 建基 → Writer 逐章写作 → Editor 评审
                                    ↑                │
                                    └── 重写/打磨 ◄───┘
```

Writer 自主决定每章的创作流程，建议路径：

1. `novel_context` — 加载上下文（前情摘要、时间线、伏笔、角色状态、风格锚点、声纹）
2. `read_chapter` — 回读前一章结尾和角色对话，找回语气和节奏
3. `plan_chapter` — 构思本章目标、冲突、情绪弧线
4. `draft_chapter` — 写入整章正文
5. `read_chapter` + `check_consistency` — 自审：回读草稿，对照状态数据检查一致性
6. `commit_chapter` — 提交终稿，更新全局状态（可选附带大纲偏离反馈）

### 长篇分层架构

500+ 章小说采用三级结构自动管理上下文：

```
卷（Volume）
└── 弧（Arc）
    └── 章（Chapter）
        └── 场景（Scene）
```

- **卷摘要** — 压缩整卷为一段话，供后续卷参考
- **弧摘要 + 角色快照** — 弧结束时自动生成，追踪角色状态演变
- **章摘要** — 滑窗加载最近 3 章，远处靠弧/卷摘要覆盖
- **弧边界检测** — 自动识别弧/卷结束，触发对应评审和摘要生成

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

选择任意 Provider 后填写代理地址即可，或使用 Custom Proxy 并指定 API 协议类型：

```jsonc
{
  "provider": "my-proxy",
  "model": "gpt-4o",
  "providers": {
    "my-proxy": {
      "type": "openai",
      "api_key": "sk-xxx",
      "base_url": "https://proxy.example.com/v1"
    }
  }
}
```

支持的 Provider：`openrouter` / `anthropic` / `gemini` / `openai` / `deepseek` / `qwen` / `glm` / `grok` / `ollama` / `bedrock` 及任意自定义代理。

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
│   ├── outline.json    # 章节大纲
│   ├── characters.json # 角色档案
│   ├── world_rules.json# 世界规则
│   ├── progress.json   # 进度状态
│   ├── timeline.json   # 时间线
│   ├── foreshadow.json # 伏笔台账
│   ├── snapshots/      # 角色状态快照（长篇）
│   ├── characters.md   # 角色档案（可读版）
│   └── world_rules.md  # 世界规则（可读版）
```

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
| `volume_end=true` | 额外注入卷摘要生成指令 |
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
