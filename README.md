# ainovel-cli v1

> 用户拥有故事事实，AI 在用户可随时调整的创作边界内，将其想象力持续、可靠地写成完整小说。

> 版本约定：历史实现统一称为 **v0**，当前重构实现统一称为 **v1**，目标稳定版本为 `v1.0.0`。v0 的说明保留在 [`README_v0.md`](./README_v0.md) 和 `v0.x` tags 中。

v1 代码位于仓库根目录，Go module 为 `github.com/voocel/ainovel-cli`。开发期间由 `v1` 分支承载，达到发布验收后合入 `main`；它不读取、不导入也不依赖 v0。

核心架构见 [`./docs/v1-architecture-plan.md`](./docs/v1-architecture-plan.md)；产品完成度、交付顺序与真实验收见 [`./docs/product/v1-product-plan.md`](./docs/product/v1-product-plan.md)。

## 当前可运行闭环

- 一句话模式：创建只含 Intent 的 Project，由同一 Operation/Change 内核规划并连续写出指定章数；中断后按稳定 ID 恢复，不重复提交。
- 详细创作：导入 Intent、Plan、Canon 与 Ownership，按 Operation 生成、改写和评审。
- 持续修改：导出带 `base_revision` 的可编辑投影，修改后显式导入；过期版本明确冲突。
- 用户控制：`locked / guided / open`、结构与按需语义影响、三种冲突处理、审批、拒绝、追加式回滚。
- 能力定制：安装/分享 `.novelpack`、执行 Pack eval、保存分层 Creator Profile，并从真实正文 Revision Diff 生成跨书偏好候选。
- Prompt 可观测：查看最终 Prompt、来源、真实文本 Diff 和 Pack Overlay lint；template/reference 明确作为 data。
- 创作上下文：确定性 Context Builder 按任务装配依赖和约束，并按 Project Revision 持久化 Derived Cache；几十章以上所需的分层摘要与相关事实检索仍列在产品计划中。
- 长任务控制：精确运行、心跳续租、暂停、恢复、取消、调优先级、回收过期 lease 和显式 restart；Agent 消息与 Workspace 均可恢复。

## 启动

```bash
go build ./cmd/ainovel-cli
go test ./...
```

外层启动只区分交互形态：

```bash
go run ./cmd/ainovel-cli                 # 人类主入口：进入 TUI
go run ./cmd/ainovel-cli --headless ...  # 无交互执行动作，供脚本与外部集成
go run ./cmd/ainovel-cli --version       # 程序级信息
```

TUI 首次启动会进入配置向导（选择 provider、填写模型与 API Key），配置保存在 `~/.ainovel/v1/config.json`（v1 目录独立于 v0，格式不兼容也不迁移），之后在首页输入一句话回车即开写。作品库共用 `~/.ainovel/v1/ainovel.db`，作品身份与工作目录无关；`--db` 可为脚本或测试指定其他库。环境变量 `AINOVEL_PROVIDER/AINOVEL_MODEL/AINOVEL_API_KEY/AINOVEL_BASE_URL` 可临时覆盖配置文件；只设置 provider 或 model 会直接报错，不会降级成模板结果。

连接名称可以自定义：顶层 `provider` 引用 `providers` 中的连接，连接的 `type` 指定协议（如 `openai`），`api` 选择 OpenAI 的 `chat` 或 `responses` 接口。每个连接保存自己的密钥、地址和模型列表，参见 [配置示例](config.example.jsonc)。旧 v1 扁平配置仍可读取，保存时自动转为连接映射。`AINOVEL_PROVIDER_TYPE` 和 `AINOVEL_API` 可覆盖当前连接的协议和接口；选择其他连接不会沿用原连接的密钥与地址。

Headless 一句话写前三章：

```bash
go run ./cmd/ainovel-cli --headless quick write \
  --project book-1 \
  --user user-1 \
  --premise "一个失忆的邮差替亡者送完最后一封信" \
  --chapters 3
```

成功输出只展示作品 revision 和章节进度，不暴露 Proposal/ChangeSet 内部术语。若模型、工具、约束检查或版本发生真实错误，命令会明确失败，已落盘 Operation 与 Workspace 可供检查和恢复。

## 成品导出

在作品工作台按 `e`（或点击“导出”），输入以 `.txt` 或 `.epub` 结尾的文件路径。导出只包含当前工作台版本的已确认正文，不包含候选稿、工作区草稿或流式预览；尚未写完也可导出已有章节。

```sh
ainovel-cli --headless project export --project book-1 --format txt --file ./book.txt
ainovel-cli --headless project export --project book-1 --format epub --file ./book.epub --title "亡者来信" --author "作者"
```

TXT 使用 UTF-8 编码；EPUB 包含书名、署名（可选）、目录和按章号排列的正文。书名默认使用创作意图，可通过 `--title` 指定。`--revision` 可选历史版本，`--from` / `--to` 可选章节范围，保留原章号。输出目录须已存在，默认拒绝覆盖已有文件；命令行显式指定 `--overwrite` 才替换。写入成功后才发布文件，失败不会留下半成品或损坏旧文件。

不指定 `--format` 时，`project export` 继续向标准输出导出 JSON 工程投影，用于编辑和重新导入。

## 详细创作入口（Headless 动作）

以下动作均通过 `ainovel-cli --headless <动作>` 执行，与 TUI 调用同一组具名应用用例：

```text
creation start|show|strategy|pause|cancel|events
project create|show|export|import|derived|revert|approval|overlay|assets|directive|lock|unlock
proposal show|approve|reject|resolve
operation start|restart|run|show|events|pause|resume|cancel|priority|recover
prompt show|sources|diff|lint|reload
pack install|export|eval
profile save|show|learn|candidates|confirm
```

精细创作先用 `creation start` 展开 Goal 与 RunStrategy，再在内容类 `operation start` 上通过 `--run` 归属同一运行；`quick write` 只是自动完成这两步并持续驱动相同 Coordinator。`creation strategy` 可在运行中途调整窗口与自动修订预算（只影响之后创建的 Operation），预算用尽落 waiting_user 后调高预算即可从落点继续。

典型的可编辑故事循环：

1. `project export --project book-1 > book-1.jsonc`
2. 用户修改大纲、Canon、正文或 Ownership，同时保留 `project_id` 与 `base_revision`。
3. `project import --semantic --file book-1.jsonc --proposal edit-1 --user user-1 --reason "调整第三卷结局"`
4. `proposal show --id edit-1` 查看结构与语义影响；冲突时用 `proposal resolve --strategy rewrite_affected|reinterpret_future|abandon` 明确选择。
5. `proposal approve --id edit-1 --user user-1` 提交新 Revision；需要撤销时用 `project revert` 生成反向提案。

`operation start` 会冻结 Project Revision、Worker Profile、工具 Schema、Prompt、Pack、Creator Profile、模型配置与审批策略。配置变化只影响新 Operation；可用 `prompt diff` 比较两个 Execution Profile。

安装 Pack 或保存 Creator Profile 后，可用 `project assets` 把确认过的版本固定到作品；之后的新 Operation 会自动装配，也可在 `operation start`、`prompt reload` 或 `quick write` 上显式覆盖。书级创作规则用 `project overlay` 维护；用户创作要求用 `project directive add|retire|list` 维护（`--scope` 定范围、`--text` 保留原话、`--target-words` 等给出可校验的字数约束），命中的要求随任务进入写作与审阅，否决理由也会自动入账为该章要求。`prompt reload` 只编译并保存新的 Execution Profile，便于先执行 `show / sources / lint / diff`；它不会修改任何已存在的 Operation。旧任务要使用新配置时执行 `operation restart --from <old> --id <new>`，旧 Workspace 会显式播种到新任务。

Pack 开发目录通过 `pack install --dir` 安装；`pack export --id <id> --file style.novelpack` 生成单文件分发包，接收方可用 `pack install --file` 或 `pack install --url` 安装。Pack 内 eval 是严格 JSONC，使用 `pack eval --id <id> --output chapter.txt` 执行。

Creator Profile 学习不是直接猜风格：先用 `profile learn --project <book> --from <revision> --to <revision>` 比较用户实际改稿，`profile candidates` 查看候选，最后 `profile confirm` 才让规则跨书生效。

Canon 事实使用受控 `kind` 与 predicate namespace；`new_value` 是当前事实，正式 Proposal 修改已有事实必须携带匹配基线的 `old_value`。AI 提交工具允许省略旧值，由程序从任务冻结的 Revision 补齐；不变的事实通过 `confirm_canon` 按 ID 明确确认，不再要求模型重抄原文。显式提供的旧值仍须精确匹配，标点也不能改动。AI Writer 提交正文时仍须在同一个 Proposal 中提交各章 Canon Delta，用户导入投影同样依据基线补齐旧值。

同一次执行中，`proposal_submit` 或 `verdict_submit` 连续三次返回相同错误时，会以 `submission_blocked` 停止本次执行、保留工作区并记录原始原因；协调器转入等待用户，不再自动重开同一失败。中间成功读取资料或编辑草稿不会清零提交错误次数，不同错误或成功提交会重置计数。该保护不替代创作运行的自动修订预算，也不放宽提交校验。外部结果未知（`result_unknown`）同样不自动重开，需用户明确决定。

审阅工具通过 `review_key` + `review_version` 引用已保存的问题列表，宿主装配完整裁定，不要求模型重复抄写 findings；版本不符直接报错，审阅范围、意图核验与阻塞问题仍执行原有严格校验。已冻结的旧工具协议继续接受并核验显式 findings。

写作与审阅在提交校验通过、工具结果落盘后正常结束模型循环，再由 Operation Engine 完成权威提交；不再额外请求模型输出结束语。提交校验失败仍允许模型修正，落盘失败则明确报错。恢复时复用已保存的对话与草稿，历史消息用量不计入新一次执行。

## 模块边界

```text
internal/
├── domain/       创作领域模型、变更与执行机制、目标推进、上下文推导
├── app/          小说、作品、资源、任务、审批和工作台用例
├── infra/        SQLite、工作区、模型运行时、Prompt/Pack、活动和配置适配
├── entry/        TUI 与 headless 入口
├── bootstrap/    静态装配具名组件，不承载业务或转发方法
└── archtest/     包依赖与架构契约检查
```

`domain/model` 同时承载通用协议与当前创作内容类型；这是创作应用的领域内核，不是纯通用媒体框架。`domain/change`、`domain/operation`、`domain/creation` 自己声明所需的持久化接口，由 `infra/store` 实现；推进驱动通过任务接口调用 `app/task` 装配的执行能力，不反向依赖应用包。新增功能按领域规则、应用用例、外部适配各自归属，具体边界以架构文档 §11–12 为准。

硬约束：

- Authority 的唯一写入口是 Change Engine。
- Agent 只能写所属 Operation Workspace，正式候选必须提交 Proposal。
- AI 直接修改 locked 内容不能自动批准。
- AI 正文遇到 locked/guided 约束时，独立语义合规检查只有返回 `pass` 才能自动提交；`conflict / uncertain / unavailable` 均进入用户确认。
- Prompt、Pack、Profile 和模型配置不会在运行中偷偷替换。
- v1 不引入 v0 实现，也不读取或复用 v0 运行数据。
- 不吞错、不伪造成功、不提供静默回退。

`custom` 审批枚举已保留，但当前没有可验证的策略契约，因此启动时会明确拒绝；不会把它偷偷当成 `auto` 或 `manual`。首版可直接使用 `auto / milestone / manual`。

## 项目状态

当前状态统一表述为：**v1 核心协议与本地闭环已完成，产品化和真实长篇验收进行中。**

README 不维护第二份完成度清单；当前缺口、优先级和“何时可以替代 v0”的验收标准统一以 [产品演进计划](./docs/product/v1-product-plan.md)为准。
