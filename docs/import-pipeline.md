# 外部小说语义导入管线

> 状态：已实现（v1，`internal/host/imp`；截断前缀打捞含阶段三·补）
> 日期：2026-07-15
> 目标：让外部小说导入既能持续获得模型能力升级的收益，又具备全文不丢失、失败可诊断、崩溃可恢复和发布可验证的工程保证。
> 修订：SourceUnit 顺序按 `(Line, Part)` 数值序（§7.3/§8.3）；截断前缀打捞降级为可后置的效率优化并要求可观测（§9.5/§13.3/§19）；语义函数模型档位开放为旋钮（§13.1/§17）。
> 修订 2026-07-16：模型档位旋钮落地为 roles 配置 `import_segment/import_analyze/import_synthesize`（§13.1）；自然语言重切分落地为 `--guide` 与工作区 `guidance.txt` 语义输入（§18.3）；语义失败统一保存原始响应到 failures/（§14.2）；切分确认支持面板内 `y` 一次性放行（§8.4）；未完成导入在启动时主动提示（§18.2）。JSON Schema 模式（§13.2 第 1 级）暂未实现，标记 TODO 待与全仓其它模型调用点统一改造。

## 1. 一句话

导入不是“用正则切文本，再让模型一次吐出整本书 JSON”，也不是一个自由运行的 Import Agent；它是一条**分阶段语义编译管线**：

> 模型负责理解开放语义，代码负责坐标、覆盖、类型、哈希、顺序和幂等；全部语义产物在独立工作区验证完成后，才发布到正式书籍状态。

```text
外部文本
  → 确定性读取与归一化
  → LLM 识别章节/卷/附属文本边界
  → 代码验证全文覆盖
  → 用户确认切分（可显式授权自动接受）
  → LLM 按连续章节批次提取逐章事实
  → LLM 分层聚合全书语义
  → 代码组装并验证 Foundation
  → 幂等发布 Foundation 与章节
  → 默认一次性暂停；显式 --continue 才按正常门禁接力
```

## 2. 为什么必须重构

当前实现是：

```text
正则切章
  → 全部章节正文一次性送入 ReverseFoundation
  → 模型一次输出 premise / characters / world_rules / 全量章节大纲 / compass
  → 立即写入正式 Foundation
  → 再逐章读取同一正文、分析并 commit
```

它有四个结构性问题。

### 2.1 切章在枚举开放语义

章节标题没有封闭语法。继续添加“第 N 章”“卷 N”“Chapter N”等正则，只能覆盖已经见过的格式，无法覆盖作者自定义标题、混合排版、卷章层级和未来格式。

更严重的是，当前切分会让未命中的边界直接消失在结果里，并可能静默丢弃首个标题前文本、空章和被判断为尾部噪声的内容。代码无法证明这些内容应该被丢弃。

### 2.2 Foundation 调用输入和输出都随章数线性增长

`ReverseFoundation` 同时承担整书理解和全量章节大纲生成：输入包含全部正文，输出包含每一章的详细结构。54 章已经可能截断 JSON；提高 `max_tokens` 只会把失败点推迟到更长的书。

### 2.3 失败前已经修改正式状态

Foundation 和章节边分析边发布。后续步骤失败时，用户得到的是一半导入完成、一半尚未分析的正式书籍状态。当前 `from=N` 只是假设用户知道从哪里恢复，无法证明源文件、切分结果和既有章节仍然一致。

### 2.4 多处语义结论被硬编码

当前方案还固定了：

- 导入正文只能是一卷；
- 只能划分为 1～3 个弧；
- 按已导入章数的 25/80 阈值选择 short/mid/long；
- 为了允许续写而倾向强行制造 `open_threads`；
- 每章必须有角色、固定数量事件和固定类型钩子。

这些都不是可以从文件格式机械证明的事实，应由模型依据正文判断，或由用户明确表达意图。

## 3. 目标与非目标

### 3.1 目标

1. **开放格式可理解**：不要求用户把小说改成内置标题格式，也不要求用户编写正则。
2. **全文可交代**：每段非空源文本都必须属于明确的章节或附属区域，禁止静默丢失。
3. **规模可控**：不再有一次调用读取全书正文并输出全部章节对象；分段、双预算章节批次和区间综合都有局部输入输出边界，全局输出只随人物、卷弧等真实语义复杂度增长。
4. **失败不污染**：语义分析和 Foundation 校验完成前，不写正式创作状态。
5. **精确恢复**：恢复依据源快照和工件 `InputDigest`，不依赖 `from=N` 或用户记忆。
6. **模型红利直达**：更强的模型直接改善边界识别、事实提取、卷弧划分和续写判断，无需增加 Go 规则。
7. **复用正式提交语义**：发布章节继续使用 `commit_chapter` 的 PendingCommit、checkpoint 和 digest 幂等能力。
8. **完整可观测**：进度、模型身份、用量、原始失败响应和最终错误都有明确落点。
9. **交互与自动化并存**：默认让用户确认高风险语义边界，同时提供显式无人值守授权；自动路径不依赖静默猜测。

### 3.2 非目标

- 不构建 Coordinator 或通用 Agent 长循环。
- 不构建通用 Workflow/PolicyEngine/任务图框架。
- 不自动修复或改写用户原文。
- 不为导入实现数据库、向量检索或分布式并行。
- 不支持把另一部小说模糊合并进已有书籍。
- 不实现旧 `from=N` 状态迁移或双轨兼容。
- 不在本 RFC 中扩展 EPUB/PDF；第一版仍只接收 txt/md，读取层保持局部，未来可替换而不改变后续契约。

## 4. 职责边界

| 问题 | 归属 | 原因 |
|---|---|---|
| 字节解码、换行归一化 | Go | 文件格式和确定性转换 |
| 哪个源位置是章标题、卷标题或附属文本 | LLM | 开放语义，不可穷举 |
| 标题对应哪个稳定源位置 | Go | SourceUnit、原文锚点和字节范围可机械验证 |
| 某章发生了什么 | LLM | 文学语义理解 |
| 人物、世界规则、伏笔和关系如何归纳 | LLM | 跨章语义归纳 |
| 卷弧边界、故事是否收束、规划级别 | LLM | 取决于叙事形状，不取决于固定阈值 |
| 章节范围是否递增、不重叠、全覆盖 | Go | 可证明不变量 |
| JSON 类型、闭集枚举、引用章号是否合法 | Go | 类型化契约 |
| 是否可以复用已有分析 | Host/Workspace | 真实语义输入能重建相同 `InputDigest` 才可复用 |
| 何时写正式书籍状态 | Host/Store | 发布协议和崩溃恢复 |
| 是否授权按当前切分继续 | 用户/Intent | 交互确认或显式 `--yes`，不由代码偷偷代答 |

这里的 LLM 调用不是 Arbiter 控制面，也不是 Worker 创作循环。它们是边界明确的**语义函数**：类型化事实进，类型化语义结果出，Host 校验后执行。

## 5. 总体架构

```text
[TUI / Headless]
       │ /import <path> / 自动授权 / 确认 / 取消
[Host]
       │ 独占导入生命周期、事件、模型运行时
[imp.Runner]
       ├── LoadState → NextAction（只从工作区事实推导）
       ├── Source     读取、解码、归一化、快照
       ├── Segment    结构投影 → LLM 边界识别 → 覆盖校验
       ├── Analyze    双预算连续批次 → 逐章事实暂存
       ├── Synthesize 分层归纳 → BookSynthesis
       ├── Validate   组装并验证完整 Foundation
       └── Publish    正式 Foundation → commit_chapter
               │
[meta/import 工作区]          [正式 Store]
源快照/切分/分析/综合结果      Progress/Checkpoint/Artifact/PendingCommit
```

Runner 是普通的确定性阶段编排，不拥有自由决策能力。它每次只执行 `NextAction` 推导出的一个动作，动作完成后重新读取事实。

## 6. 工作区与状态推导

导入中的事实存放在书目录下：

```text
meta/import/
├── manifest.json
├── intent.json
├── source.txt
├── guidance.txt          # 存在时：用户自然语言切分指导（--guide），是 segmentation 的语义输入
├── segmentation.json
├── confirmation.json
├── analyses/
│   ├── 000001.json
│   ├── 000002.json
│   └── ...
├── range-digests/
│   ├── 000001-000050.json
│   └── ...
├── synthesis.json
├── story-resolution.json
└── failures/
    ├── last.json
    └── last-response.txt
```

第一版保留工作区。它既是恢复依据，也是导入审计记录；不增加自动清理和历史归档机制。

`intent.json` 保存用户启动导入时的显式授权（自动确认、uncertain 故事状态预选、是否跳过完成 Hold）。这些是恢复后仍必须遵守的用户意图，不是可由工件猜出的阶段状态；创建后不被 Runner 静默改写。

### 6.1 Manifest

```go
type ImportManifest struct {
	Version          int    `json:"version"`
	SourceName       string `json:"source_name"`
	RawSHA256        string `json:"raw_sha256"`
	NormalizedSHA256 string `json:"normalized_sha256"`
	Encoding         string `json:"encoding"`
	SizeBytes        int64  `json:"size_bytes"`
	CreatedAt        string `json:"created_at"`
}

type ImportIntent struct {
	Version             int    `json:"version"`
	AutoConfirm         bool   `json:"auto_confirm,omitempty"`
	StoryResolution     string `json:"story_resolution,omitempty"` // open / closed
	ContinueAfterImport bool   `json:"continue_after_import,omitempty"`
}
```

- `source.txt` 是归一化后的本地快照，恢复不再依赖原路径仍然存在；
- Manifest 不保存绝对源路径，避免泄露机器目录并消除移动文件带来的恢复问题；
- Intent 只接受闭集值，精确保存启动命令中的用户授权；恢复时不从当前 advance mode 反推旧意图；
- schema 版本不匹配时显式要求使用匹配版本继续或重新导入，不猜测迁移。

首次创建时先在同级临时目录写齐并校验 manifest、intent、source，再以目录 rename 发布为 `meta/import/`；`meta/import/` 不存在就不算活动工作区。这样初始三件套不会以半初始化形态进入 `NextAction`，也不需要为创建过程增加 `stage=initializing`。启动时发现残留初始化目录要显式提示并保留诊断信息，不自动当作成功工作区，也不静默删除。

### 6.2 不保存可漂移的阶段枚举

持久状态不写 `stage=analyzing`、`current=37` 之类控制字段。下一动作由工件推导：

```text
无 manifest/intent/source   → ingest
无 segmentation            → segment
无匹配 segmentation 输入摘要的 confirmation → await_confirmation / auto_confirm
存在缺失或输入摘要不符的章节分析             → analyze_first_missing
缺少输入匹配的 RangeDigest 或 synthesis      → synthesize_first_missing
story_status=uncertain 且无匹配的用户选择     → await_story_resolution
正式工件与 synthesis 不一致                 → publish
全部正式工件一致                            → done
```

事件中的 `Stage` 只用于 UI 展示，不是恢复事实源。

### 6.3 统一工件身份

不实现依赖图。工作区中的每份语义工件统一采用同一个身份规则：

```go
type Artifact[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	InputDigest   string `json:"input_digest"`
	Payload       T      `json:"payload"`
}
```

`InputDigest` 覆盖该动作实际消费的全部**语义输入**，按固定顺序编码后计算：

- segmentation：归一化源内容、SourceUnit 投影、用户指导和分段 prompt/schema 版本；
- confirmation：segmentation 内容和确认方式；
- 章节分析：批次章节范围及正文、进入批次前的连续性 ledger、prompt/schema 版本和用户指导；
- RangeDigest/BookSynthesis：各自消费的有序分析或下层 digest 内容、综合 prompt/schema 版本；
- story resolution：synthesis 内容和用户选择；
- 发布：待发布领域对象的规范化内容。

provider/model、usage、thinking 等执行事实写入 provenance/session，不因模型配置变化自动使已成功分析失效；用户要求重新分析时显式删除对应工件。缓存复用判断只看当前动作能否重建出相同 `InputDigest`。

`NextAction` 沿固定线性管线寻找第一份缺失、解析失败或 `InputDigest` 不匹配的工件。重新切分、修改用户指导或改变上游事实时，下游自然失配；不编写“切分变化时手工删除哪些文件”的失效规则。

发布时正式工件与综合结果逐项比对；相同则幂等跳过，不同则报告冲突，不覆盖猜测。因此删除 `ResumeFrom`。恢复只需要再次执行 `/import`；Runner 会从第一份缺失事实继续。

## 7. 源文件读取

### 7.1 解码

第一版支持：

- UTF-8 / UTF-8 BOM；
- GB18030（覆盖常见 GBK 小说文本）。

解码结果必须返回所选 encoding，并写入 Manifest 和进度事件。不能把“尝试 GB18030”藏成无声兜底。无法可靠解码或出现不可接受替换字符时直接失败，错误包含检测结果。

### 7.2 归一化

只做不会改变文学内容的转换：

- 移除 BOM；
- CRLF/CR 统一为 LF；
- 保留空行、缩进、标题行和正文字符；
- 不删除首部文本、空章、广告、版权信息或所谓尾部噪声。

所有排除决定留给分段语义结果并在预览中显示。

### 7.3 稳定坐标

归一化文本建立统一的 `SourceUnit` 表：

```go
type SourceUnit struct {
	ID        string // L1257；超预算行拆为 L1257.1、L1257.2
	Line      int
	Part      int
	StartByte int
	EndByte   int
	Text      string
}
```

- `ID` 仅用于展示与模型引用；所有顺序、包含与递增判断一律按 `(Line, Part)` 数值元组比较，禁止对 ID 字符串做字典序比较（`"L900"` 字典序会大于 `"L1000"`）；投影 JSON 保留字符串 id，Go 侧解析成 `(Line, Part)` 后再比；
- 正常行对应一个 unit，常见路径仍然是直观的行号坐标；
- 单行超过结构投影预算时，Go 只在 UTF-8 字符边界生成多个**虚拟 unit**；
- 虚拟分片不写回 `source.txt`，不插入软换行，不改变任何源字符；
- 同一 unit 内存在边界时，模型返回 unit ID 和一段逐字复制的原文锚点；Go 要求锚点在该 unit 内唯一，再映射为精确字节位置；
- 锚点不存在或不唯一时，把具体错误反馈给模型，禁止猜 offset、截断文本或要求用户先修改原稿。

因此普通分章文本保持行号模型，整段无换行、同一行包含多个章节或异常长行也使用同一套坐标类型处理。

## 8. 语义切分

### 8.1 结构投影

模型看到按上下文预算分块的结构投影：

```json
{
  "owned_units": {"start": "L1200", "end": "L1800"},
  "context_units": {"start": "L1180", "end": "L1820"},
  "units": [
    {"id": "L1200", "line": 1200, "text": "风从城门外吹来。", "blank_before": true},
    {"id": "L1257", "line": 1257, "text": "卷二·北境", "blank_before": true, "blank_after": true}
  ],
  "user_guidance": ""
}
```

上下文区可以重叠，但每次调用只能为 `owned_units` 返回结果，因此不存在重叠块投票或冲突合并。坐标纪律由 Go 执行（2026-07-16 修订）：模型在上下文区返回的边界不触发语义重问——该边界归相邻块管辖（它会在自己的 owned 区间再报告一次），代码直接裁掉并回显说明；语义重试只留给真正的语义失败（投影外的幻觉 ID、非法 kind 等）。旧行为对越界反馈重问，弱模型常把 3 次尝试全部耗尽拖垮整块。

分块大小由当前 architect 模型的 context window 和保留预算计算，不按固定行数或固定章节数切块。模型上下文扩大后，调用次数自然下降。规划预算并非满额使用（2026-07-16 修订）：owned 正文只是请求的一部分，规划时扣除系统提示与指导的实际长度、再按 3/4 折算投影 JSON 包装的暴涨；上下文区另有字节上限（chunkBytes/8，下限 4096），拦截超长行虚拟分片吞掉输入预算。输出侧对称兜底：单块边界 JSON 被长度截断（大量短章节）时对半缩块递归重试——半块有独立缓存路径，重试成果不重付；单元级仍截断才是真容量不足。

每块的边界决策以工件形式落盘（`segment-chunks/chunk-*.json`，身份 = 切分身份 + MaxUnitBytes + 块 owned 范围——unit 表由「归一化源 + MaxUnitBytes」唯一确定，换档位重塑超长行分片时缓存自然失配，不会复用错位的旧边界），任何一块失败或中断，重跑时已完成块零调用复用——与 analyze 逐章、synthesize 逐区间同一哲学；最终 segmentation 落盘后块级缓存删除。终局整合（resolve）失败时同样清除块缓存并把决策快照落 `failures/`：此时缓存 digest 恒匹配，保留它会让重跑零调用复读同一批边界、确定性复现同一失败。空正文的章节边界（真实网络小说源常见"已锁定/付费章节"占位标题）不整体失败：并入前段（文本一字不丢），记入 `Segmentation.Notes` 由确认预览呈现，人工不认可可用 `--guide` 裁定。

### 8.2 模型输出

```go
type BoundaryDecision struct {
	UnitID    string   `json:"unit_id"`
	Anchor    string   `json:"anchor,omitempty"`
	Kind      string   `json:"kind"` // chapter / group / front_matter / back_matter
	Title     string   `json:"title,omitempty"`
	Uncertain bool     `json:"uncertain,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}
```

- `chapter` 是可提交正文单元，包括序章、楔子、番外等是否算章的语义判断；
- `group` 是卷、部、篇等上层结构证据，不直接当作章节；
- `front_matter` / `back_matter` 标记明确不进入章节正文的附属区域；
- `anchor` 必须逐字来自对应 unit；边界位于 unit 起点时可以省略；
- `uncertain` 只用于预览提示，不由代码设置置信度阈值。

不让模型生成正则。正则仍然会把开放语义压回有限语法，并引入转义、局部匹配和统一格式假设。

### 8.3 代码校验

Go 只校验：

1. 所有 unit ID 存在且落在调用的投影内（owned + 上下文区；投影外是幻觉，带反馈重问）；
2. owned 区边界的 kind 属闭集、非空 anchor 在对应 unit 内唯一并映射到 UTF-8 字节边界、同位不语义冲突（kind/标题不同时保留哪个不由 Go 裁定，重问交模型；完全相同的重复是机械冗余，放行后静默去重）、首块须有边界兜住文本起点（开头是不是前言由模型判断，不由 Go 代答）——都在调用期校验（2026-07-16 修订）：坏值放进块缓存后终局才发现，digest 恒匹配会让失败确定性复现；上下文区边界注定被裁掉，不为其重问；
2a. **标题回显**（2026-07-16 修订，seg-v2）：chapter/group 边界的 title 归一化（去空白）后必须真实存在于边界单元原文，否则调用期重问——实测某分页抓取源 157 章里 67 章是模型在章中续文上编造的边界与标题（覆盖纪律歧义迫使每块块首设边界），全部能被此项事实核对拦下。语义裁量仍归模型：真无标题规约的源置 `uncertain=true` 可保留归纳标题（预览呈现存疑标记）；front/back matter 的描述性标题低风险，不核对。prompt 同步收紧：边界只落真实结构分隔处，块首为上一章延续正文时返回空 boundaries 是正确输出（首块头部覆盖除外）；
3. 边界次序与重复由 Go 确定性修复而非否决（2026-07-16 修订）：按解析后的字节稳定排序恢复真实顺序——块间顺序由 owned 区间不重叠保证，乱序只可能发生在块内，排序零信息损失；同字节重复保留先出现者并记入 `Notes`。旧行为要求严格递增否则整体失败，实测 319 个边界败于 1 处块内倒序，且块缓存会让该失败确定性复现。顺序判断一律按 `(Line, Part)` 数值序，不对 ID 做字典序比较；
4. 每个产出章节正文范围非空（空正文占位标题并入前段，见 §8.1）；
5. 所有非空源文本恰好属于一个章节、一个 group 标题或明确的 front/back matter（起始未归属的非空文本——书首简介/广告被漏报边界——由 Go 确定性收为 front_matter 并记 `Notes` 交确认预览，不终局否决）；
5a. 同名章节（标题去空白后相同）记入 `Notes` 交人工核对（2026-07-16 修订）——有标题规约的源里章名不该重复，重复是"同章被误切"的确定性信号；是否合并不由 Go 裁定，Notes 非空即阻断 `--yes`；
6. 没有重叠、越界或未归属范围；
7. group 不被错误计入章节总数。

“L1257 语义上是不是章标题”不由 Go 复判。

### 8.4 用户确认

交互模式下，确认前不调用章节分析，也不写正式 Store。预览至少显示：

- 卷/group 数和章节数；
- 全部章节标题，可滚动查看；
- 首尾附属文本的范围与摘要；
- 空章、异常长章和模型标记 uncertain 的边界；
- 每章起止行，方便用户对照原稿。

用户可以：

- 确认（TUI 预览面板按 `y`，内部以 AcceptSegmentation 重跑；一次性放行当前切分，不写入 intent，confirmation 记 `method=user_confirmed`）；
- 输入自然语言说明后重新识别，例如 `/import --guide=幕间·X 也是独立章节`；
- 取消并保留工作区（Esc）。

`/import <path> --yes` 是显式无人值守授权：Runner 在覆盖校验通过后写入同样的 confirmation 工件，记录 `method=auto_authorized`，随后继续分析。`--yes` 即使存在 uncertain 边界也表示用户选择信任本次切分，但 uncertain 仍保留在工件和日志中。**例外（2026-07-16 修订）**：切分带容错说明（`Notes` 非空——空章吸收、起始兜底、重合去重发生过）时 `--yes` 不自动放行，仍停在确认预览——结构被确定性改写过，未看预览的盲授权不该吞掉它；看过预览的 `y`（AcceptSegmentation）不受此限。

`--yes` 只跳过切分确认，不替用户决定 `story_status=uncertain`，也不跳过导入完成 Hold。用户不需要编写正则或手工填写 `from=N`。

## 9. 连续批次的逐章事实提取

确认后从第一份缺失分析开始，把连续章节按当前模型的**输入和输出双预算**组成批次。第一版批次间串行，不做窗口间并发：伏笔 ID、人物别名和状态变化具有时间顺序，前一批次产生的紧凑 ledger 是后一批次的输入。

串行只约束第一版执行策略，不是永久架构限制；分析工件仍按章独立落盘，未来有证据证明并行归并能保持语义质量时，可以只替换批次调度。

### 9.1 批次输出、逐章工件

废除 `=== TAG ===` 混合 envelope。每次调用返回一个结构化批次对象，每个数组元素仍是一章事实：

```go
type ImportedChapterFacts struct {
	Chapter             int                        `json:"chapter"`
	Title               string                     `json:"title"`
	Summary             string                     `json:"summary"`
	KeyEvents           []string                   `json:"key_events"`
	CoreEvent           string                     `json:"core_event"`
	Hook                string                     `json:"hook"`
	Scenes              []string                   `json:"scenes"`
	Characters          []string                   `json:"characters"`
	CharacterEvidence   []ImportedCharacterFact    `json:"character_evidence,omitempty"`
	WorldEvidence       []ImportedWorldFact        `json:"world_evidence,omitempty"`
	TimelineEvents      []domain.TimelineEvent      `json:"timeline_events,omitempty"`
	ForeshadowUpdates   []domain.ForeshadowUpdate  `json:"foreshadow_updates,omitempty"`
	RelationshipChanges []domain.RelationshipEntry `json:"relationship_changes,omitempty"`
	StateChanges        []domain.StateChange       `json:"state_changes,omitempty"`
	HookType            string                     `json:"hook_type"`
	DominantStrand      string                     `json:"dominant_strand"`
}

type AnalysisBatchResult struct {
	Chapters []ImportedChapterFacts `json:"chapters"`
}

type ChapterAnalysisPayload struct {
	BatchStart int                  `json:"batch_start"`
	BatchEnd   int                  `json:"batch_end"`
	Facts      ImportedChapterFacts `json:"facts"`
}
```

每个 `analyses/NNNNNN.json` 都是 `Artifact[ChapterAnalysisPayload]`。同一批次落盘的章节记录相同 `BatchStart/BatchEnd`；其 `InputDigest` 采用**逐章绑定**：切分身份（segmentation 工件的 `InputDigest`）+ prompt/schema 版本 + 章号 + 单章正文。之所以逐章而非按批次划分绑定，是因为批次边界随模型输入/输出能力变化（换更强模型批次自然变大）；若把批次划分写进身份，换模型后已成功的分析会整体失配、被迫重算重复收费。绑定切分身份则保证「重新切分、改 prompt/schema 版本、改源」时下游分析自然失配，而单纯换模型不误伤——这才是恢复真正需要的失效语义。

`ImportedCharacterFact` 和 `ImportedWorldFact` 是用于全书综合的紧凑观察，不直接写正式角色或世界规则。它们至少携带章节号，使综合结果有稳定来源。

### 9.2 双预算组批

批次规划同时满足：

```text
预计输入 + system/prompt/ledger + 推理预留 + 预计可见输出 ≤ context window
预计可见输出 ≤ provider/model 可用 completion 上限
```

- 输入估算覆盖每章标题、正文和批次前 ledger；
- 输出估算由 analyzer schema 的固定结构开销和每章保守事实预留组成，只决定本次装入多少章，不截断任何字段；
- reasoning token 与可见 JSON 共享 completion 预算的模型，必须先扣除推理预留；
- provider/model 输出能力越强，批次自然变大；不能写固定“每批 10/20 章”规则；
- 单章输入本身无法进入 context，或单章最小结构输出也无法进入 completion 时，显式报告该章和模型容量，不截断正文或伪造精简成功。

因此总章数增长只增加批次数，不再让任意一次响应随全书规模无限增长；同时不会把 #83 从整书粒度搬到一个不受输出约束的批次粒度。

### 9.3 批次上下文

单个批次调用只包含：

- 当前连续章节范围的原文和标题；
- 之前章节派生的紧凑人物别名表；
- 活跃伏笔 ID 与一句话状态；
- 必要的最近状态摘要。

模型按数组顺序处理批次内章节，可以在批次内部延续别名、伏笔和状态；批次结束后，Go 按已验证事实顺序更新紧凑 ledger。它不依赖尚未生成的全书 Premise，也不重复读取全部前文。章节事实是 Foundation 的输入，而不是反过来形成循环依赖。

### 9.4 完整响应校验

代码分两层校验结构、值域和引用，不硬编码文学质量：

- 批次级：chapters 数组按预期章节号连续、无重复、无缺口，批次范围、`InputDigest` 和 schema version 匹配；
- 逐章级：chapter/title 与源分段一致，summary/core_event 非空，hook type、strand 等正式 domain 闭集字段合法，时间线、伏笔和状态变化字段类型合法。

代码不要求“必须 3～6 个事件”“必须有出场角色”“必须有三个场景”。安静章节、书信、环境章或无名人物章节都是合法文学形状。

完整响应出现 JSON 或语义校验错误时，不提交其中任何新章节；把具体错误反馈给同一模型，走 §13.3 的输出层重试。模型可能在修正后改写前面的对象，因此普通校验失败不能擅自保存部分数组。

### 9.5 长度截断时的连续前缀

> 实施定位：本节是**错误路径上的 token 优化**，不是恢复正确性依赖。v1（阶段三）截断即「失败 + 缩小重组批」，本身正确且可恢复；连续前缀打捞在独立子阶段（阶段三·补）实现，可单独开关、单独验收。

只有响应明确标记 `StopReasonLength` 且返回了可解析的部分文本时，允许从失败响应中保存**最大连续合法前缀**：

1. 使用流式 JSON decoder 进入顶层 `chapters` 数组；
2. 从批次首章开始逐个读取已经完整闭合的 JSON 对象；
3. 每个对象独立通过 §9.4 的逐章校验，并与此前对象组成从批次首章开始的连续序列后，立即原子写入对应章节分析工件；
4. 遇到第一个不完整、非法、跳号或重复对象立即停止，之后的字节一律不解释；
5. 禁止补括号、续写半个 JSON、猜测缺失字段或从后续位置捞取非连续对象；
6. 原始响应、StopReason、已保存前缀范围和首个失败章节全部写入 failure artifact、事件和日志；
7. `NextAction` 从第一份缺失分析重新组批，不重做已经提交的合法前缀。

typed-call 必须记录本次是否拿到可用部分文本：JSON Schema 等非流式结构化模式可能在长度停止时给不出可解析前缀。若 provider 没有返回部分文本、不能明确证明是长度截断，或一个合法对象都没有完成，则不保存任何结果，发出 `prefix_salvage=unavailable` 事件/日志并回退到「失败 + 缩小重组批」，而不是静默空转。单章批次仍然截断时直接报告模型输出能力不足，不循环缩减或制造空事实。

长度截断是容量错误，不进入“把校验错误反馈给同一模型”的语义自修复循环，也不原样重试同一批次。

### 9.6 恢复

每章分析成功即原子写入 `analyses/NNNNNN.json`。崩溃后：

- `InputDigest` 匹配的分析直接复用，不重复收费；
- 第一份缺失或失配分析成为下一批次起点；
- 上游语义输入变化后，无法重建相同 `InputDigest` 的分析自然失效；
- 长度截断已经提交的连续合法前缀和正常完成的工件使用完全相同的恢复规则；
- 不允许用户越过一个失败章节继续生成不连续的后续语义事实。

## 10. 分层综合

### 10.1 为什么不能再做整书单次输出

全书综合需要跨章理解，但不需要再次读取全部正文，也不应输出每章详细对象。逐章事实已经包含章节级语义；综合只处理这些紧凑事实。

### 10.2 Map/Reduce 形状

```text
ImportedChapterFacts × N
        ↓ 按当前 context window 分连续区间
RangeDigest × M
        ↓ 必要时继续归并
BookSynthesis
```

短书若一次能容纳全部章节事实，直接生成 `BookSynthesis`；长书才产生 `RangeDigest`。是否分层由 token 预算机械决定，不由章数阈值决定。

`RangeDigest` 包含该连续范围的情节推进、角色变化、世界事实、已开/已收伏笔和候选结构边界。它的输出大小受单个区间约束；最终综合不再重复输出 N 份章节详细对象，只输出全局事实和卷弧范围。

### 10.3 最终综合结果

```go
type BookSynthesis struct {
	Premise       string                 `json:"premise"`
	Characters    []domain.Character     `json:"characters"`
	WorldRules    []domain.WorldRule     `json:"world_rules"`
	Structure     []ImportedVolumeRange  `json:"structure"`
	Compass       domain.StoryCompass    `json:"compass"`
	PlanningTier  domain.PlanningTier    `json:"planning_tier"`
	StoryStatus   string                 `json:"story_status"` // open / closed / uncertain
	StatusReason  string                 `json:"status_reason"`
}
```

结构只返回范围，不重复输出所有章节：

```go
type ImportedVolumeRange struct {
	Title string             `json:"title"`
	Theme string             `json:"theme"`
	Arcs  []ImportedArcRange `json:"arcs"`
}

type ImportedArcRange struct {
	Title        string `json:"title"`
	Goal         string `json:"goal"`
	StartChapter int    `json:"start_chapter"`
	EndChapter   int    `json:"end_chapter"`
}
```

模型自行决定卷数和弧数，可以参考源文件中的 group 标题，但不受“一卷”“1～3 弧”限制。Go 使用 `ImportedChapterFacts` 的 title/core_event/hook/scenes 组装正式 `OutlineEntry`。

### 10.4 故事状态

导入只重建正文事实，不为了让 Engine 继续而伪造未收束长线：

- `open`：正文存在真实未收束目标或张力，正常生成 Compass；
- `closed`：按已完结作品发布，最后一卷标记 Final；需要写续作时由用户明确 reopen 并给出新方向；
- `uncertain`：发布前要求用户选择按未完或完结处理；若 Intent 已通过 `--story=open|closed` 保存选择则直接使用，否则进入交互等待。选择作为以当前 synthesis 为输入的 `story-resolution.json` 工件保存。

代码不通过 `open_threads` 是否为空偷偷猜测用户意图。

## 11. Foundation 组装与验证

模型输出综合语义，Go 负责组装正式领域对象。发布前必须满足：

1. Premise 有合法书名标题；正文无法确认书名时使用源文件 basename，并把来源标为 filename，不让模型宣称它是“真实书名”；
2. 所有卷和弧范围按顺序连续；
3. 第一个范围从第 1 章开始，最后一个范围在第 N 章结束；
4. 每章恰好属于一个弧；
5. `FlattenOutline` 后章节数为 N，标题和逐章事实一致；
6. 角色名、世界规则和 Compass 满足现有 domain 类型约束；
7. PlanningTier 是合法闭集值，但选择理由来自模型而非章数阈值；
8. closed/open 状态与 Final、Compass 的发布形状一致；
9. Synthesis 工件的 `InputDigest` 能由当前有序分析集合重建。

违反结构约束时把具体错误反馈给模型重新生成，持续到成功或 context 取消；不落盘半成品。

## 12. 正式发布

### 12.1 发布前置条件

新导入只允许进入：

- 没有已完成章节；
- 没有在途章节或 PendingCommit；
- 没有另一份非同源导入工作区；
- 正式 Foundation 为空，或与当前工作区已经发布的 digest 完全一致。

已有小说与新外部文本的合并语义不清楚，第一版明确拒绝，不猜测覆盖或追加。

### 12.2 Foundation 发布

按正式依赖顺序发布：

```text
planning tier
→ premise
→ characters
→ world rules
→ layered outline + flat outline
→ compass
→ progress 对账
```

每一步：

1. 计算待发布内容 digest；
2. 正式工件不存在则原子写入并追加 checkpoint；
3. 已存在且 digest 相同则幂等跳过；
4. 已存在但不同则返回冲突错误，不覆盖。

中途崩溃后重新从第一项对账即可，不需要跨文件事务或 Foundation Pending 状态机。

### 12.3 章节发布

按章节顺序复用现有流程：

```text
保存 draft
→ Progress.StartChapter
→ commit_chapter(逐章事实)
```

`commit_chapter` 已有 PendingCommit saga、checkpoint 和完成章节幂等检查。导入不复制第二套提交逻辑。

崩溃窗口：

| 窗口 | 恢复行为 |
|---|---|
| draft 前 | 重新保存同一正文 |
| draft 后、StartChapter 前 | 对账 digest 后继续 |
| StartChapter 后、PendingCommit 前 | 重新执行同章 commit |
| PendingCommit 中 | 由现有提交 saga 恢复 |
| chapter complete 后 | digest/checkpoint 一致则跳过 |
| 正式内容与源 digest 冲突 | 显式停止，报告冲突章节 |

### 12.4 导入完成边界

全部章节稳定提交后设置一次 `AdvanceHoldAtBoundary`，原因明确为“外部小说导入完成，等待验收后续写”。它只保护这次跨系统导入，不改变用户长期 `auto/review` 模式。

`--yes` 只授权自动接受切分，不能隐式跳过这次 Hold。只有用户同时传入独立的 `--continue`，Runner 才不创建导入专用 Hold；随后仍遵守正常 advance mode：`auto` 可以继续续写，`review` 仍等待 `/next`。

默认 TUI 不再自动无提示接力。用户检查 Foundation 和章节状态后，使用现有继续入口恢复创作。

**关面板落点**：从欢迎页发起的导入成功收尾后，Esc 关闭面板会补跑一次 `Resume()`（bootstrap 的恢复门禁只在启动时跑一次），用户直接落到工作台、被导入完成 Hold 拦在下一章边界等验收——而不是留在误按 Enter 即"开新书"的欢迎页。出错终态与工作台场景只关面板。

**新建防线**：`PrepareUserRules` / `StartPrepared` 在书目录已有成章（`CompletedChapters` 非空）时拒绝新建——StartPrepared 开头即重置 checkpoints 与 progress，误触会静默清掉整本书（含刚导入的全部章节）。无成章的规划残留放行，保留共创 Ctrl+S 同会话重试与恢复补裁的自愈路径。

### 12.5 跨重启发布门禁

工作区存在且 `NextAction != done` 时，`Host.New/Resume` 必须把书识别为未完成导入：

- 允许查看、诊断和执行 `/import` 恢复；
- 禁止普通 Engine 启动、Continue 或派发 Writer；
- 明确显示当前恢复动作，不把已经部分发布的 Foundation/章节当作一本文义完整的可续写书籍。

门禁直接读取工作区和正式 Store 推导，不新增 `published bool`。这样发布任意窗口崩溃后，都不会在 Runner 尚未恢复时由普通创作流程消费半发布状态。

## 13. 模型调用内核

`imp` 内部保留一个小而专用的 typed-call helper，不建设通用 LLM 工作流框架。

### 13.1 模型选择

- 默认使用 architect 角色模型；
- 语义函数的模型档位是开放旋钮：segment/analyze/synthesize 可各自声明档位，默认落到 architect，配置层可把机械性更强的 segment 指到更便宜档位。这是调用配置，不改任何语义契约，也不把「单角色」写成架构前提——目的是让「廉价档位模型变强」的成本红利也进得来；
  - 实现落点：roles 配置支持 `import_segment` / `import_analyze` / `import_synthesize` 三个角色 key；未配置时落 architect。各函数的双预算与 thinking/结构化选项按各自档位的真实能力独立派生（小档位的小窗口只约束它自己的函数），用量按实际档位角色记账；
- 接入所选角色已配置的 failover；
- 使用所选角色 reasoning effort，并通过能力探测决定是否发送 thinking 参数；
- 按真实 provider/model 写 session 元数据和 usage；
- 纳入现有预算哨兵（2026-07-16 落地）：启动前过 `Refuse()` 与 Start/Resume/Continue 同一纪律；运行中预算硬停经 `abortWithEvent` 取消导入自己的 context（Host 登记独占作业的 cancel，不再只暂停并未运行的 Engine）。

### 13.2 结构化输出能力

四类导入产物共用 `llmcontract.Execute`：模型或用户配置明确支持时发送原生 JSON Schema；能力未知或明确不支持时，从同一份 Schema 自动生成 Prompt Contract。原生模式校验完整响应，兼容模式才提取平衡 JSON 对象；两条路径均先执行同一份 Schema 校验，再解码 DTO 并执行业务校验。请求报错后不得静默删除 schema 重试，能力判断错误或 Provider 拒绝必须暴露。

### 13.3 请求、语义与容量失败分离

- 请求层错误：只重试适配器明确标记 retryable 的超时、限流、网络错误，沿用现有退避语义并持续到成功或 context 取消；
- 输出层错误：把 JSON 解析或 Validate 的具体错误反馈给同一模型，持续自修复直到成功或上下文被用户/预算系统取消；原生 Schema 契约违约、拒答和截断不会盲目重问。
- 重试不允许静默：请求层每次退避（"进行第 N 次重试 · Xs 后重试"）与输出层每次重问都以进度事件回显到导入面板——无回显用户会误判卡死。退避事件只携带截止时刻（`RetryAt`），剩余秒数由渲染层逐 tick 计算形成实时倒计时（与创作台事件面板共用同一机制）；面板运行期常驻顶部 spinner + 已用时，日志尾部另有流式同款星标光标。
- 错误回显不许空泛：网关 message 常常只有一句 "Provider returned error"；回显与失败文案统一附带适配器的结构化事实（错误分类/HTTP 状态/provider/模型，`modelErrDetail` 经 errors.As 从 litellm 错误链提取），事实前置，截断时优先保住。
- 长时阶段不许静默：切分逐块、综合逐区间在函数内部调用模型（单块可达数分钟），经 `callProfile.step` 逐块/逐区间回显推进（"切分第 N/M 块，已识别 K 个边界"）。事件 Key 只给请求退避（同一调用内的瞬态，原地跳动）；校验重问是跨调用语义事件，各自成行保留历史——共用 Key 会让后块覆盖前块，排查线索全丢。
- 全量日志转录：每条进度事件（含被面板原地覆盖的重试行）写入**导入专属日志** `<书根>/logs/import.log`（不与 tui.log 混流，一次导入一个文件看完整转录）；请求退避与语义重问另以完整错误链落同一日志。
- 回显模型语义而非只有机械计数：切分逐块回显模型识别出的标题（"模型识别出：第十二章 风雪夜 / …（共 N 处）"），分析逐章回显核心事件（"第 12 章〈风雪夜〉：……"），综合完成回显全书概括（premise 摘要）——用户应看见模型读懂了什么。
- 容量错误：`StopReasonLength` 不原样重试，也不进入语义自修复循环；analysis batch 在部分文本可解析时按 §9.5 保存连续合法前缀，否则记录 `prefix_salvage=unavailable` 并缩小重组批；其余语义函数直接显式失败并保留原始响应。

鉴权、权限、模型不支持和状态冲突立即失败。没有模拟成功、空对象兜底或跳过失败章节。

### 13.4 输入与输出预算

每种语义函数有独立 schema、输入预算、推理预留和可见输出预算：

- 分段输出只包含当前 owned range 的边界；
- analysis batch 同时受 context window 和 completion 上限约束，输出是有限连续范围的逐章事实；
- RangeDigest 只包含一个连续区间；
- BookSynthesis 只包含全局事实和卷弧范围，不重复章节对象。

每次请求在发送前都记录估算输入、推理预留、申请的 max tokens 和预计可见输出。估算只决定分块/组批，不删除正文或事实字段。因此不存在“总章数越多，某一次响应必然越长”的结构，也不能仅因输入放得下就忽略输出截断风险。

## 14. 事件、日志和诊断

### 14.1 事件阶段

```go
const (
	StageIngesting            Stage = "ingesting"
	StageSegmenting           Stage = "segmenting"
	StageAwaitingConfirmation Stage = "awaiting_confirmation"
	StageAnalyzing            Stage = "analyzing"
	StageSynthesizing         Stage = "synthesizing"
	StageAwaitingStoryStatus  Stage = "awaiting_story_status"
	StageValidating           Stage = "validating"
	StagePublishing           Stage = "publishing"
	StageDone                 Stage = "done"
	StageError                Stage = "error"
)
```

每个事件包含 action、当前章节/区间、总数、耗时和可选错误。analysis batch 事件额外包含批次范围、预算估算、StopReason 和已提交前缀范围。Event 是投影，不参与恢复。

### 14.2 错误必须同时到达三处

1. TUI 导入面板：自动换行，保留完整错误链；
2. `tui.log`：结构化记录 stage、chapter/range、model、attempt 和 error；
3. `meta/import/failures/`：保存最后一次失败元数据和未经裁剪的模型响应。

原始小说正文不写普通日志，也不进入默认脱敏诊断导出。失败响应位于用户自己的书目录，错误信息明确给出路径。

### 14.3 Session 与 Usage

每次语义调用记录：

- 稳定 task 名，如 `import/segment/0003`、`import/analyze/0054-0061`；
- assistant 原始响应；
- provider/model 与 usage；
- structured mode、thinking level 和输出校验结果。

用量统一归入 architect 角色，预算对导入成本可见。

## 15. 生命周期与并发

- 导入与 Engine、阶段共创、simulation 的写操作互斥；
- 导入期间同一本书只允许一个 Runner；
- 用户取消会取消在途模型调用，已原子落盘的工作区事实保留；
- 确认前取消不会修改正式 Store；
- 发布开始后取消不做猜测性回滚，下一次只能精确恢复发布；
- 第一版 analysis batch 之间串行，批次内由一次模型调用按章顺序返回事实；正式发布仍按章节串行；
- `Host.New/Resume` 在未完成导入时执行 §12.5 门禁，互斥语义跨进程重启仍成立；
- 导出是否允许并发保持现有只读语义，但它只能看到已经正式发布的章节。

## 16. 核心不变量

1. 每份工作区工件都由 `SchemaVersion + InputDigest + Payload` 标识；只有能从当前真实语义输入重建相同 `InputDigest` 才可复用。
2. Manifest 对应唯一归一化源快照；每段非空源文本必须有且只有一个归属。
3. 模型只能引用 Host 提供的 SourceUnit、原文锚点和章节号；Go 只接受能唯一映射回源字节的坐标。
4. analysis batch 只能提交完整响应，或 `StopReasonLength` 下从首章开始的最大连续合法前缀；任一缺章都会阻止后续分析和综合。
5. 卷弧范围必须连续、无重叠并完整覆盖 `1..N`；正式 Foundation 只能从通过完整验证的 Synthesis 发布。
6. 正式章节只能按顺序通过 `commit_chapter` 发布；已存在正式工件只能在内容 digest 相同时幂等复用，不同则冲突失败。
7. 任一模型失败都不得被解释为“无内容”或“继续下一章”，不得修复半个 JSON 或跳过失败章节。
8. `done` 必须由工作区工件、正式工件、Progress、PendingCommit 和 checkpoint 共同证明；`done` 前普通 Engine 不得启动。

## 17. 包结构与窄接口

保留 `internal/host/imp`，按职责拆分：

```text
imp/
├── types.go       公开 Options/Event 与语义 DTO
├── source.go      读取、解码、归一化、SourceUnit/anchor
├── workspace.go   meta/import 原子工件与 InputDigest
├── call.go        import 专用 typed LLM 调用
├── segment.go     结构投影、边界语义函数、覆盖验证
├── analyze.go     双预算连续批次、逐章事实与截断前缀
├── synthesize.go  RangeDigest 与 BookSynthesis
├── publish.go     Foundation 对账与 commit_chapter 发布
└── runner.go      LoadState → NextAction → 执行
```

不新增 `ImportEngine`、`Task`、`WorkflowInstance`、通用 Repository 或插件注册表。

Host 注入的依赖保持窄：

```go
type Deps struct {
	Store         *store.Store
	CommitChapter ChapterCommitter
	Model         agentcore.ChatModel
	Runtime       ModelRuntime
	Prompts       Prompts
	Emit          func(Event)
}
```

`ModelRuntime` 只携带 context window、completion 上限、thinking、session/usage 回调等调用事实，并为每个语义函数预留模型档位选择位（默认 architect）；不让 `imp` 反向依赖整个 Host，也不把单一角色焊死为架构前提。

## 18. 用户接口

### 18.1 新导入

```text
/import <path> [--yes] [--story=open|closed] [--continue] [--guide=<切分指导>]
```

默认行为：创建源快照、语义切分并打开确认预览，完成发布后设置一次导入专用 Hold。删除 `from=N`。

前三个选项是彼此独立的显式授权，并写入 `intent.json`：

- `--yes`：覆盖校验通过后自动接受切分；不决定 uncertain 故事状态，不跳过完成 Hold；
- `--story=open|closed`：仅在 synthesis 返回 uncertain 时预先提供用户选择；模型已明确判断 open/closed 时不覆盖模型事实；
- `--continue`：不创建导入专用 Hold；不绕过正常 advance mode，`review` 下仍需 `/next`。

`--guide` 与前三者不同：它不是启动授权而是切分的语义输入，落盘为工作区 `guidance.txt`（可含空格，须置于命令最后）。见 §18.3。

因此 `/import book.txt --yes` 仍会在导入完成后停下；只有额外传入 `--continue` 才授权创作流程在正常门禁允许时接力。

### 18.2 恢复

同一本书存在活动工作区时执行无参数 `/import`，直接从事实和已保存 intent 推导下一步；源文件路径和启动参数都不是恢复必需项。带新路径的 `/import <path>` 不得覆盖活动工作区。

未完成导入必须主动可见，不能等用户创作被门禁拒绝才暴露。实现为三道递进提示：

1. 启动时 TUI 检测一次（`imp.ResumeSummary`，按 `NextAction` 生成阶段化描述），欢迎界面醒目显示"发现未完成的导入（已分析 N/M 章），输入 /import 从断点恢复"；
2. 用户无视提示尝试创作时，跨重启门禁（§12.5）拒绝引擎启动并发事件警示；
3. 恢复运行中，导入面板实时显示当前阶段与进度。

### 18.3 重新切分

用户核对预览后用 `/import --guide=<自然语言说明>` 重新识别，例如 `--guide=幕间·X 也是独立章节`。指导写入工作区 `guidance.txt` 并纳入 segmentation 的 `InputDigest`：指导变化使旧切分及旧 confirmation、分析和 synthesis 无法重建相同 `InputDigest`，自然全部重做；不提供正则编辑器。

### 18.4 取消

确认前取消只保留工作区；发布前可以显式丢弃整个工作区。发布开始后不提供“假装什么都没发生”的丢弃操作，只允许恢复完成或由用户另行处理正式书籍。

## 19. 实施顺序

### 阶段一：工作区和纯状态推导

- Manifest、Intent、源快照、`Artifact/InputDigest`、原子读写；
- `LoadState/NextAction`；
- 空书和同源恢复前置校验；
- 删除 `ResumeFrom` 设计依赖。

这一阶段不调用模型，先证明恢复事实没有歧义。

### 阶段二：语义切分与确认

- SourceUnit、超长行虚拟分片、原文锚点和上下文预算分块；
- BoundaryDecision typed call；
- 全文覆盖验证；
- TUI 预览、自然语言重识别、`--yes` 和 confirmation 工件。

先用非标准标题、卷标题、前言和尾注验证“不漏一字”。

### 阶段三：连续批次的逐章事实

- `ImportedChapterFacts`；
- context/completion 双预算批次规划；
- 批次间顺序分析和紧凑连续性 ledger；
- 每章 `InputDigest` 工件恢复；
- 截断即「失败 + 缩小重组批」，并记录部分文本是否可用；
- session、usage、failover、thinking、容量错误和结构反馈重试接线。

### 阶段三·补：截断前缀打捞（效率优化，可后置）

- `StopReasonLength` 连续合法前缀解析（§9.5）；
- 仅在部分文本可解析时启用，不改恢复正确性；单独开关、单独验收。

### 阶段四：分层综合与 Foundation

- context-aware RangeDigest；
- BookSynthesis；
- 范围式卷弧结构；
- StoryStatus；
- Foundation 完整组装和校验。

### 阶段五：发布与交接

- Foundation 逐工件 digest 对账；
- 复用 `commit_chapter` 发布；
- 取消/崩溃恢复；
- 跨重启 Engine 门禁；
- 默认导入完成 AdvanceHold 与显式 `--continue`；
- 完整 TUI/log/failure artifact。

### 阶段六：删除旧实现

- 删除 `splitter.go` 的章节格式裁决；
- 删除 tagged envelope；
- 删除 `ReverseFoundation` 整书调用；
- 删除 `pickScale` 章数阈值；
- 删除 `ResumeFrom/from=N`；
- 删除“固定一卷、1～3 弧、强制 open threads”的 prompt 约束；
- 实现完成后再更新 README 和 architecture 的旧流程描述。

## 20. 测试与验收

### 20.1 纯函数和属性测试

- 任意合法 segmentation 都满足全文范围无重叠、无缺口；
- 非法 SourceUnit、非唯一原文锚点、倒序和重复边界必定拒绝；
- 边界顺序按 `(Line, Part)` 数值序判断；构造「字典序与数值序结论相反」的 unit 集，断言以数值序通过；
- 正常行与虚拟分片都能无损映射回同一份归一化源字节；
- 任意合法卷弧 ranges 恰好覆盖 `1..N`；
- 相同语义输入稳定生成相同 `InputDigest`，任一真实输入变化都会使对应工件失配；
- 双预算组批不会超过给定 context/completion 约束；
- NextAction 对同一事实快照恒定。

对坐标映射、范围组装、批次预算和 `InputDigest` 做 fuzz/property test，不断言模型会输出某个固定标题。

### 20.2 模型契约测试

- 非标准章名和混合卷章结构；
- 序章/楔子/番外被模型语义判断为章节；
- front/back matter 明确展示而非丢弃；
- 整本单行、单行多章节和超预算行通过 SourceUnit + anchor 精确切分；
- 安静章节允许空 characters；
- 非法 JSON、缺字段、越界范围进入反馈重试；
- analysis batch 返回连续逐章对象，不能跳号或重复；
- `StopReasonLength` 只保存最大连续合法前缀，半个对象和后续非连续对象不保存；
- 结构化模式不产出可解析部分文本时，断言走「失败 + 缩小重组批」且日志标注 `prefix_salvage=unavailable`；
- 普通 `StopReasonStop` 下的损坏 JSON 不进入截断前缀路径；
- 单章仍截断时显式失败，不生成空事实；
- Prompt Contract 的解析/业务错误持续反馈自修复，直到成功或上下文取消；原生 Schema 契约违约、拒答与截断立即保留原始响应并终止；
- 不支持 thinking/JSON Schema 的模型不收到非法参数。

模型测试断言契约和不变量，不断言精确文学判断。

### 20.3 崩溃矩阵

至少覆盖：

- 源快照后；
- segmentation 后、确认前；
- analysis batch 第 N 个对象落盘前后；
- 长度截断前缀最后一章落盘前后；
- RangeDigest 中间；
- Synthesis 后、Foundation 前；
- 每个 Foundation 工件前后；
- draft/StartChapter/PendingCommit/progress/checkpoint 各窗口；
- 最后一章提交后、AdvanceHold 前后；
- Foundation/章节部分发布后重启并尝试普通 Host.Resume。

每个窗口重启后只能继续当前动作，不能重复消费成功的模型调用，也不能越过失败工件。`NextAction != done` 时普通 Engine 必须被门禁阻止，直到导入恢复完成。

### 20.4 #83 回归形状

构造 54 章及更长输入，验证：

1. 没有单次调用要求输出 54 章详细大纲；
2. analysis batch 同时根据输入 context 和可见输出 completion 预算组批，不因输入放得下就塞入过多章节；
3. 阶段三·补：模拟“前 13 章完整、第 14 章截断”的 `StopReasonLength`，只提交前 13 章，下一动作从第 14 章开始；未实现前缀打捞时，整批失败后从批次首章重组批；
4. 模拟无完整对象的截断响应，错误完整显示、写 log、保存原始响应且不落分析工件；
5. 模拟普通损坏 JSON，走结构反馈重试而不是前缀打捞；
6. 修正后只重跑第一份缺失动作，不重做已完成章节；
7. 非标准标题通过语义切分进入预览，不通过新增正则修复。

### 20.5 最终验收标准

1. 默认交互模式让用户在正式写盘前看到并确认全部章节边界；`--yes` 能显式自动接受且留下同等审计工件。
2. 任意非空源文本都能从 segmentation 找到唯一归属。
3. 200～500 章不会形成一次读取全书正文并输出全部章节对象的模型调用；每个分析批次同时受输入和输出预算约束，全局输出只表达全局事实与卷弧范围。
4. 任一阶段崩溃后无需 `from=N` 即可精确恢复。
5. 正式状态在完整语义验证前保持不变。
6. 发布中断后现有 commit saga 能恢复，且不会重复提交章节。
7. 未完成导入跨重启后不能启动普通 Engine；只能查看、诊断或恢复导入。
8. `--yes` 不跳过完成 Hold；只有独立 `--continue` 才跳过，且不会绕过 review 门禁。
9. 模型和 provider 的能力、用量、StopReason、预算估算和错误均可观测。
10. 更换更强模型即可改善切分、分析和综合质量，并自然扩大安全批次、减少调用次数，不修改 Go 文学规则。

## 21. 面向未来的扩展性

本方案的扩展性来自稳定边界，而不是预建抽象：

- 模型理解增强：Boundary/Chapter/Synthesis 三类语义函数直接变准；
- 上下文或输出窗口扩大：双预算器自动扩大安全 analysis batch，并减少分块和 Reduce 层数；
- 结构化输出增强：typed-call 自动选择更强的 provider 约束；
- 廉价档位模型变强：机械性更强的 segment 可切到更便宜档位，成本红利随之进入，不改语义契约；
- 新输入格式：只需把 EPUB 等转换为同一归一化文本和 SourceUnit 坐标；
- 新的全书语义：在 `ImportedChapterFacts` 或 `BookSynthesis` 增加有明确消费者的字段，不改恢复和发布协议；
- 用户共创增强：在确认边界增加自然语言修正，不把格式知识写进代码。

不变的部分是全文覆盖、`InputDigest` 身份、范围校验和幂等发布。这些是模型再强也不值得交给模型的簿记；可变的语义全部留在模型函数中，因此模型升级的红利可以穿透到产品结果。

## 22. 最终决策

采用**分阶段语义导入管线**，拒绝两条方向：

1. 继续扩大章节正则和章数/弧数阈值；
2. 用一个自由长循环 Agent 接管全部导入。

最终边界是：

> **模型决定文本意味着什么；代码保证每个字去了哪里、每个结果对应哪份输入、每次调用的输入输出都装得下、失败后从哪里继续，以及什么时候有资格成为正式事实。**

这既保留模型自主能力和未来红利，也保持 ainovel-cli 当前 Engine + 类型化语义函数 + 文件事实层的简洁架构。
