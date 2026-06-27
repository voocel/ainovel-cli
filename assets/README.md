# assets 内容地图

给系统加"一段话 / 一篇资料 / 一条规则"之前，先查下表确定归属，再看接线方式。

| 目录 | 装什么 | 谁消费 | 接线方式 |
|---|---|---|---|
| `prompts/` | 常驻角色 system prompt（coordinator / writer / editor / architect×2）与一次性任务 prompt（import×2 / simulation×2） | `agents/build.go` 装配；imp / sim runner | `load.go` Prompts 字段。注意：simulation_guidance 由 `load.go` 加载时注入，md 文件里看不到 |
| `references/` | 题材无关的写作知识材料。不进 system prompt，由 novel_context 按角色 / 章节裁剪后注入 `reference_pack` | writer / editor / architect | **三处接线**：`tools.References` 加字段 + `load.go` loadReferences 读取 + `novel_context.go` writerReferences / architectReferences 注入。放进目录不会自动加载 |
| `references/genres/<style>/` | 题材专属知识（style-references / arc-templates） | 同上，`style != default` 时加载 | `load.go` loadReferences |
| `rules/` | 机械规则默认值（字数 / 禁词 / 疲劳词），commit 时由代码强制检查 | rules loader 三层合并：内置 → `~/.ainovel/rules/` → 项目 `./.ainovel/rules/` | `rules/default.md`；用户层格式见根目录 `rules.md.example`。只放定长固定串，带变量的模式交 editor 语义判断 |
| `styles/<style>.md` | 题材写作风格指令 | 拼进 **writer** 的 system prompt（`agents/build.go`） | 文件名即 `config.style` 取值。与 `references/genres/<style>/` 是同一题材概念的两种载体：前者是风格指令，后者是知识材料 |

## 新内容归属判断（五问）

1. 这个流程必须被**保证**？→ 不写 prompt，写代码约束（StopAfterTools / 工具守卫 / Flow Router）
2. 这是裁定判据（什么时候派谁）？→ `prompts/coordinator.md`
3. 这是某个角色的审美 / 执行标准？→ `prompts/<role>.md`
4. 这是可机械枚举的规则（禁词 / 字数 / 阈值）？→ `rules/`（代码强制，零 LLM 成本）
5. 这是写作知识材料？→ `references/`（记得三处接线）

## 一致性保障

prompt 引用的信封路径（`working_memory.*` 等）与 writer.md 的 commit_chapter 参数文档
由 `prompts_consistency_test.go` 机检——这两类漂移不报错、只让模型悄悄变笨，靠测试红灯暴露。
prompt 里的流程段是"用户手册"，流程真理在代码层；两者脱节时以代码为准并回头修 prompt。
