# 用户规则统一设计

## 一句话

所有长期写作规则都归一化进同一份本书规则快照；运行时只通过 `novel_context` 注入这份快照，不再把原始规则文本反复塞进 prompt。

```text
启动 prompt / 用户 rules 文件 / 运行中长期要求
        ↓
LLM 语义归一化（按来源）
        ↓
Go 确定性合并（按优先级）  ←  系统默认规则（代码内置，直接进合并，不经 LLM）
        ↓
output/novel/meta/user_rules.json
        ↓
novel_context 注入
        ↓
Architect / Writer / Editor / commit 检查共用
```

## 实现状态（2026-07-19，已落地 + 经 review 修缺）

本设计已实现，24 包 `go build` / `go vet` / `go test` 全绿。一轮 code review 后修掉 4 个缺口（均已修复）：①启动 prompt 规则只接在死方法 `Host.Start` 上、真实入口走 `StartPrepared` 而漏建快照——已把原始 prompt 经 `Plan.RawPrompt` 透传到 quick/cocreate 两条入口，统一调 `Host.PrepareUserRules`；②快照落盘失败被吞——`PrepareUserRules` 改为落盘失败即返 error 中止开书（resume 路径保持 best-effort，避免给老书引入新失败模式）；③rules 文件读取错误静默跳过——`raw.go` 对非"不存在"错误（权限等）打日志；④README 仍教旧 YAML/front matter 且链向已删文件——已重写。

落地与本文档基本一致，结构化输出升级后的实现选择如下：

1. **归一化只有一份 `Contract.Schema`，不维护两套提示词。**
   模型声明支持时下发原生 JSON Schema；不支持或能力未知时，统一契约层把同一份 Schema 注入提示词。
   两种模式都会在 Go 侧复核 Schema，随后执行值域和跨字段业务校验。
2. **单个字段值非法时降级到"该字段缺失"，而非降级整个来源。**
   如某字段是空占位或类型非法，sanitize 把该字段丢弃（视为未声明）、保留该来源其余合法字段；
   只有"整条归一化失败"（网络/模型/非法 JSON/解析失败）才把整个来源降级为 raw preferences、
   置 `status=degraded`。这样一个坏字段不会连累同来源的其它有效规则。可由模型修复的输出错误会携带
   精确原因继续自愈，生命周期由 `context` 控制；明确的终止错误进入日志并按来源降级。

代码落点：`internal/rules`（纯数据 + 确定性合并：snapshot.go / raw.go / types.go）、`internal/userrules`
（LLM 归一化 + 编排 + 落盘：normalize.go / service.go）、`internal/store/user_rules.go`（快照存储）、
`internal/userrules/service.go`（运行中规则落盘）、`assets/prompts/arbiter-intervention.md`（三类分流）。
系统默认机械基线已从 `assets/rules/default.md` 迁入代码内置 `rules.SystemDefaults()`，YAML 解析路径与
yaml.v3 依赖已删除。**未验**：真实 LLM 开书 / 运行中 Arbiter rules 动作全链路（normalizer 离线原型已验 10/10）。

## 为什么

Writer 每章并不会稳定拿到用户最初的完整 prompt。它主要依赖本章任务和 `novel_context(chapter=N)`。

所以长期规则不能靠对话历史记忆，也不应该靠 regex 从自然语言里偷偷猜。正确做法是：把长期规则显式归一化成状态，再由 `novel_context` 统一分发。

这里的“归一化”必须利用大模型的自然语言理解能力，而不是在 Go 里枚举表达方式。程序只定义少量可机械检查字段，负责 schema、确定性合并、校验、落盘和 commit 检查；“每章一千五左右”“单章别超过两千”“不要再写命运齿轮这种话”这类表达由 LLM 语义理解。

## 统一状态

本书运行时只维护一份用户规则事实源：

```text
output/novel/meta/user_rules.json
```

形状保持简单：

```json
{
  "version": 1,
  "status": "ready",
  "structured": {
    "genre": "修仙",
    "forbidden_chars": [],
    "forbidden_phrases": ["某种程度上"],
    "fatigue_words": {}
  },
  "preferences": "主角冷静克制；少解释，多用行动和对话。",
  "sources": [
    "startup_prompt",
    ".ainovel/rules/style.md"
  ],
  "uncertain": [
    "少用比喻：没有明确阈值，按风格偏好处理"
  ]
}
```

字段边界：

- `version`：快照 schema 版本，便于未来迁移。
- `status`：`ready` / `degraded`，标记归一化是否完整成功；只用于回显与诊断，不进入创作判断。
- `structured`：代码能机械检查或稳定消费的规则。
- `preferences`：不能机械检查、但对创作长期有效的自然语言偏好。
- `sources`：来源审计，不进入创作判断。
- `uncertain`：归一化诊断，只用于回显和排查，不进入创作判断。

注入给模型的只有 `structured` 和 `preferences`；`version` / `status` / `sources` / `uncertain` 是运维与诊断元数据，不进 `working_memory.user_rules`。技术错误不进快照，只进日志（见 §失败与降级）。

## 输入源

长期规则有四个输入源：

1. **启动 prompt**：用户开书时写的长期要求。
2. **用户 rules 文件**：全局或项目级长期偏好，按普通自然语言读取。
3. **系统默认规则**：代码内置的机械基线。
4. **运行中长期要求**：用户中途说“以后都怎样”，Arbiter 提取 `rules` 动作，Host 调用 `AddRuntimeRule`。

这些输入源不直接进入 Writer prompt，也不在运行时被反复读取。它们只在生成或更新快照时参与归一化，结果合并进 `meta/user_rules.json`。

## rules 文件

rules 文件是普通长期提示词，不是运行时 prompt，也不是配置文件。它只作为归一化输入，不支持 YAML：

```md
# 写作偏好

每章 1200-1600 字。
主角冷静克制，不要圣母。
少解释，多用行动和对话推进。
不要出现“某种程度上”。
```

系统读取后归一化为：

```json
{
  "structured": {
    "forbidden_phrases": ["某种程度上"]
  },
  "preferences": "每章 1200-1600 字；主角冷静克制，不要圣母；少解释，多用行动和对话推进。"
}
```

如果文件里出现 YAML front matter，也按普通文本处理，不作为结构化声明。结构化结果只来自统一归一化流程。

启动后如果用户修改 rules 文件，当前书不会自动变化；需要重新生成快照。这样旧书不会因为全局 rules 文件变化而行为漂移。

## 语义归一化

归一化是独立的、schema 约束的 LLM 调用——每个来源各自归一化一次，不混在创作生成里，也不靠正则表达式或关键词表硬解析。

输入：

- 单一来源的原文（启动 prompt / 一个 rules 文件 / 一条运行中要求）
- 当前系统支持的 `structured` 字段说明

系统默认规则不在此列——它们是代码内置的已编译结构化规则，直接进 §合并规则，不经 normalizer。

输出：

- 该来源的候选 `structured`
- 该来源的候选 `preferences`
- `sources`
- `uncertain`

Go 侧职责：

- 提供 schema。
- 校验字段类型和值域。
- 按 §合并规则的优先级，确定性合并各来源（LLM 不裁定来源优先级）。
- 保存快照。
- 在 `novel_context` 注入快照。
- 在 `commit_chapter` 用同一份快照做机械检查。

LLM 侧职责：

- 理解单一来源的自然语言规则。
- 把明确、可机械检查的规则提升到 `structured`。
- 把审美、风格、人物偏好保留为 `preferences`。
- 对不确定内容保持保守，不自行发明阈值。

### 保守提升

`structured` 是硬规则或稳定参数，不是“模型猜测区”。提升规则必须保守：

- 只有用户明确、无歧义表达时，才写入 `structured`。
- `forbidden_chars` / `forbidden_phrases` 是 error 级字段，必须尤其保守；只有“不要出现 X”“禁用 X”“别写 X”这类明确禁止才提升。
- `fatigue_words` 只有用户给出明确词和阈值时才提升；“少用比喻”“别太书面”“减少口头禅”这类无阈值要求进入 `preferences`。
- 字数/篇幅类意愿（“每章 3000 字”“短一点”）一律进入 `preferences`：章节长短是叙事节奏的语义裁量，不做机械检查——数字化成硬线会诱导模型为跨线注水。
- 不可机械化、无明确阈值、依赖语境判断的要求都进入 `preferences`。

原则：

```text
宁可漏进 structured，降级为软偏好；
不可错进 structured，制造每章硬误报。
```

漏提炼的代价是风格偏好弱一些；错提炼的代价是每章产生错误规则事实。

## 失败与降级

归一化是增强路径，不是主创作的前置条件。模型理解失败，绝不能阻断写书。

- **按来源降级**：某个来源归一化失败（网络 / 模型 / 非法 JSON / schema 校验失败），该来源降级为 raw preferences、不产 `structured`；其它成功来源照常贡献 `structured`。
- **上下文控制自愈**：可重试请求错误、提示词模式的格式/Schema 错误和业务校验错误持续自愈，直到成功或 `context` 结束；不设固定次数。原生契约违约、拒答、截断、错误终止和不可重试请求错误立即暴露并按来源降级。
- **技术错误进日志**：JSON / schema / 网络等技术错误写入日志，不进 `working_memory.user_rules`，不作为创作输入。
- **快照标记**：任一来源降级时，快照 `status=degraded`。
- **能落盘就继续**：只要 `meta/user_rules.json` 能写入，主创作必须继续。
- **只有落盘失败才中止**：快照无法写入磁盘时才中止，因为后续运行没有稳定事实源。

`AddRuntimeRule` 契约（运行中）：normalizer 失败时保存 degraded 快照，
不把 JSON/schema/网络等归一化错误注入创作流程；只有落盘失败才返回 error。

## 系统默认规则

`System defaults` 是代码内置的机械基线，不是用户 rules 文件，也不使用 YAML。

它不经 LLM 归一化——已是结构化形态，直接作为最低优先级来源进入 §合并规则的 Go 合并。这样默认规则没有 LLM 失败、漂移、成本问题。

系统默认机械规则原暂存在 `assets/rules/default.md`（旧实现细节，非要兼容的用户 YAML）；落地本设计时已迁入代码内置 `rules.SystemDefaults()`，YAML 解析路径已删除（见 §实现状态）。

迁移时保留必要注释说明阈值来源，例如某些疲劳词阈值来自长跑产物实证。这不是为了兼容旧 YAML，而是为了让未来维护者知道默认阈值为什么存在、何时应该调整。

## 合并规则

合并顺序按“越具体越优先”：

```text
System defaults
→ Global rules 编译结果
→ Project rules 编译结果
→ Startup prompt 编译结果
→ Runtime user update
```

优先级高的来源覆盖低的来源。

合并由 Go 确定性执行：LLM 只把单一来源的自然语言归一化成候选 `structured`/`preferences`，Go 按上面的顺序做字段覆盖与文本拼接，优先级不交给 LLM 裁定。

- `structured`：按字段覆盖，后来源的同名字段覆盖前来源。
- `preferences`：不互相覆盖，按优先级顺序拼成可读文本（高优先级来源在后），让 LLM 能看到来源次序。

已知局限：`preferences` 按优先级排序，但 Go 不消解冲突。长跑中若用户先后给出相互矛盾的软偏好（如先“冷静克制”后“话痨”），两条都会留在文本里，由 LLM 按次序与上下文权衡；需要确定性硬覆盖的，应表达成可机械化的 `structured` 字段。

## 落盘入口

归一化、合并、落盘是同一套逻辑，但有两个调用方，必须分清，否则会把启动准备混进主创作上下文：

- **开书 / 刷新（启动侧，确定性）**：由 Host / 启动流程直接调用这套逻辑生成初始快照，不进主创作循环。这是确定性的启动准备任务。
- **运行中更新（干预裁定动作）**：Arbiter 分诊出的 `rules` 动作由 Host 直接调 `userrules.Service.AddRuntimeRule`，复用同一套校验 / 合并 / 落盘逻辑，把无进度起点的新规则作为 `Runtime user update` 合并进快照。

（实现上建议把这套逻辑收敛成一个内部服务，两个调用方共用；具体命名留给实现。）

无论哪个调用方，最终都写入同一份 `meta/user_rules.json`。落盘逻辑只做三件事：

1. 校验结构化字段。
2. 按 §合并规则的优先级合并进当前本书快照。
3. 返回保存后的完整规则事实。

不做：

- 不派发子代理。
- 不修改大纲。
- 不静默吞掉非法字段（记录并降级，见 §失败与降级）。
- 不把原始文本当成最终 prompt 直接注入。

运行中更新示例：用户说“以后都怎样”（无进度起点）→ Arbiter 裁定为 `rules` 动作 → Host 经 `AddRuntimeRule` 归一化该条 → 作为 `Runtime user update` 以最高优先级合并进快照 → 事件流回显。

## 回显

每次生成或更新 `user_rules` 快照，都必须把归一化结果回显给用户：

```text
已生成本书规则快照：
- 机械规则：每章 1200-1600 字；禁用短语“某种程度上”
- 风格偏好：主角冷静克制；少解释，多用行动和对话推进
- 未提升为机械规则：少用比喻（无明确阈值，按风格偏好处理）
```

- 启动 / 刷新：复用现有启动规则日志能力打印快照，不新增机制；共创场景可把回显并入共创确认环节。
- 运行中：`AddRuntimeRule` 成功后经事件流回显（"写作规则已更新并持久化"）。
- 降级：`status=degraded` 时，回显明确说明哪些来源未能解析、当前已按 raw preferences 运行、可重新生成快照。

回显不是二次审批闸门；它的作用是让用户知道系统理解成了什么，发现错误后可以重新生成快照。

## Agent 消费方式

所有 agent 只看：

```json
working_memory.user_rules
```

职责分配：

- Architect：按 `preferences` 中的字数意愿调整每章剧情密度和拆章数量。
- Writer：按 `structured` 的硬规则写作，按 `preferences` 调整风格。
- Editor：按同一份规则审阅。
- `commit_chapter`：用 `structured` 做机械检查并返回 violations。

Writer 不重新理解原始启动 prompt，也不读原始 rules 文件。

## 干预分类：三类去向

运行中干预按"要改什么"分三类：

- **怎么写**（写作笔法 / 风格 / 质量：字数、用词、禁语、句式、对话占比、标题格式等）→ Arbiter `rules` 动作，归一化合并进 `meta/user_rules.json`。例：“每章 1500 字”“标题只用中文”“主角整体冷静克制”“对话占比高一点”。
- **写什么**（剧情 / 结构 / 人物走向 / 篇幅）→ architect，落进 compass / outline / 角色档案。例：“这一卷多写战斗线”“从第 30 章起主角语气转冷”“增加到 40 章”。
- **改已写的**（重写 / 修订指定章节）→ editor，入队 PendingRewrites。

判据：**“怎么写” → rules；“写什么” → architect；“改已写的” → editor**。

## 实施步骤

1. 新增 `meta/user_rules.json` store。
2. 新增独立的 LLM 归一化 pass（按来源），使用 schema 约束输出候选 `structured/preferences/sources/uncertain`。
3. 新增 Go 侧确定性合并：按优先级对各来源做字段覆盖与文本拼接，生成快照。
4. 把归一化 / 合并 / 落盘收敛成一套逻辑，两个调用方共用：启动侧直接调用生成初始快照；运行中由干预裁定的 `rules` 动作经 `AddRuntimeRule` 复用。失败时按 §失败与降级 处理：来源降级为 raw preferences、快照 `status=degraded`、主创作继续。
5. 把当前 `assets/rules/default.md` 的系统默认机械规则迁到代码内置结构或 JSON asset，保留阈值来源注释；删除用户 rules 的 YAML 解析路径，不做兼容层。
6. rules 文件读取后不再直接把正文当 prompt 注入，而是归一化后合并进 `user_rules` 快照。
7. `novel_context` 只注入 `meta/user_rules.json` 中的 `working_memory.user_rules`。
8. `commit_chapter` 使用同一份 `user_rules.structured` 检查。
10. 干预分诊（现由 Arbiter 承担,arbiter-intervention.md）明确按"要改什么"三类分流：写作风格 / 质量类长期要求走 `rules` 动作落快照；剧情 / 结构 / 人物 / 篇幅走 architect；已写章节返工走 editor（详见 §干预分类：三类去向）。

## 验收标准

- 用户启动 prompt 写“每章 1200-1600 字”，Writer 第一章的 `novel_context` 能在 `preferences` 里看到这条意愿原文。
- rules 文件只写自然语言，也能在生成快照时归一化进同一份 `user_rules`。
- rules 文件不需要也不支持 YAML；全部按自然语言规则归一化。
- 运行时不再读取 rules 文件；只读 `meta/user_rules.json`。
- 默认机械规则不再来自 YAML rules 文件，用户 rules 也没有 YAML 兼容层。
- 归一化不使用 regex/关键词硬编码；自然语言理解由 LLM 完成。
- 模糊规则不会被提升为 error 级 `structured` 字段。
- 系统默认规则不经 LLM，直接进 Go 合并。
- 来源优先级与字段覆盖由 Go 确定性执行，相同输入产出相同快照。
- 运行中用户说“以后都怎样”，经 Arbiter rules 动作合并进快照，后续章节的 `novel_context` 能看到更新。
- 归一化失败不阻断写书：失败来源降级为 raw preferences，快照 `status=degraded`，主创作继续；只有快照无法落盘才中止。
- 归一化失败返回 `status=degraded`，不把技术错误上抛污染主流程。
- 生成或更新快照后会回显 `structured` / `preferences` / 未提升项；降级时回显说明降级来源。
- 新开一本书不会继承上一本书的 `user_rules`。
- 非法结构化字段不静默忽略：记录并降级该来源，不阻断主流程。

## 明确不做（判定不需要，非阶段切割）

以下能力在当前需求下没有收益，不进设计，避免过度设计：

- `clear_fields` 等字段级删除 / 撤销语义。
- 监听 rules 文件变化的自动刷新（改了文件就显式重新生成快照即可）。
- `preferences` 的时间锚点 / 覆盖消解（需要硬覆盖的请用 `structured`）。
- 在快照里持久化 `diagnostics` 数组（技术错误进日志即可，快照只留 `status`）。
- schema 字段说明从 Go 类型自动生成（手维护一份简短说明即可）。

设计原则不变：LLM 负责理解自然语言，Go 负责确定性合并、校验、落盘和检查。
