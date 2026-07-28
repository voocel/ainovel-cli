# assets 内容地图

给系统加"一段话 / 一篇资料 / 一条规则"之前，先查下表确定归属，再看接线方式。

| 目录 | 装什么 | 谁消费 | 接线方式 |
|---|---|---|---|
| `prompts/` | Worker system prompt（writer / editor / architect×2）、Arbiter 裁定 prompt 与一次性任务 prompt（import×2 / simulation×2） | `agents/build.go`、`internal/arbiter`、imp / sim runner | `load.go` Prompts 字段。注意：simulation_guidance 由 `load.go` 加载时注入，md 文件里看不到 |
| `references/` | 题材无关的写作知识材料。不进 system prompt，由 novel_context 按角色 / 章节裁剪后注入 `reference_pack` | writer / editor / architect | **三处接线**：`tools.References` 加字段 + `load.go` loadReferences 读取 + `novel_context.go` writerReferences / architectReferences 注入。放进目录不会自动加载 |
| `references/genres/<style>/` | 题材专属知识（style-references / arc-templates） | 同上，`style != default` 时加载 | `load.go` loadReferences |
| `rules/` | 已废弃的旧内置规则目录；机械基线已迁到代码，用户规则来自 `~/.ainovel/rules/*.md` / `./.ainovel/rules/*.md` 的自然语言快照 | `userrules.Service` 归一化为 `meta/user_rules.json`；`novel_context` 注入；`commit_chapter` 检查 | 内置基线见 `internal/rules/snapshot.go` 的 `SystemDefaults()`；用户 `.md` 零格式、零 YAML，按自然语言归一化 |
| `styles/<style>.md` | 题材写作风格指令 | 拼进 **writer** 的 system prompt（`agents/build.go`） | 文件名即 `config.style` 取值。与 `references/genres/<style>/` 是同一题材概念的两种载体：前者是风格指令，后者是知识材料 |
| `skills/*.md` | 专项技能：一次性、带作用范围的处理方法（去 AI 味、收紧节奏等），干预时定向调用 | 命中后由 Engine 拼进本次 Worker 的 task（`internal/host/engine.go`） | `load.go` 只暴露 `SkillsFS()` 提供字节；解析与三层覆盖在 `internal/skills`。用户可加 `~/.ainovel/skills/*.md`（全局）与 `./.ainovel/skills/*.md`（本书），同名整文件替换。**与 `rules/` 的区别**：skill 是"这次这么处理"（一次性，不落持久状态），rules 是"以后都这么写"（进 user_rules 快照） |

## 新内容归属判断（五问）

1. 这个流程必须被**保证**？→ 不写 prompt，写代码约束（StopAfterTools / 工具守卫 / Flow Router）
2. 这是裁定判据？→ 查表型流程写 `internal/flow/router.go`；语义判断写 `prompts/arbiter-*.md`
3. 这是某个角色的审美 / 执行标准？→ `prompts/<role>.md`
4. 这是可机械枚举的默认规则（禁词 / 阈值）？→ `internal/rules/snapshot.go` 的 `SystemDefaults()`；用户自定义规则写进 `.ainovel/rules/*.md`，由归一化快照消费（字数/篇幅是语义软约束，走 preferences，不做机械规则）
5. 这是写作知识材料？→ `references/`（记得三处接线）
6. 这是一套**按需定向调用**的处理方法（"把这几章的 AI 味清一下"）？→ `skills/`。判据：需要用户或 Arbiter 显式挑选、带作用范围、跑完即止。若它应当无条件对每章生效，那它是 `references/` 或 rules 层的事，不是 skill

## 一致性保障

prompt 引用的信封路径（`working_memory.*` 等）必须与 `novel_context` 保持一致。工具参数形状只在工具 Schema 中定义；prompt 只补充 Schema 无法表达的业务语义，不再复制 JSON 参数清单和形状示例。

prompt 可以描述单个 Worker 的执行方法，但全局路由、状态迁移和恢复逻辑只以代码为准。能够从 Store 事实确定的步骤放进 Router/Tool；需要理解小说内容或用户意图的判断才留给模型。
