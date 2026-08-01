package rip

import (
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// 契约是输出格式的唯一真源：prompt 只写角色与约束，不重复描述 JSON 结构。
// strict 模式要求全 required，可选值一律表达成 Nullable。

func nullableString(description string) map[string]any {
	return llmcontract.Nullable(schema.String(description))
}

func stringList(description string) map[string]any {
	return schema.Array(description, schema.String(description))
}

var boundContract = llmcontract.Contract{
	Name:        "rip_bound",
	Description: "识别对标原文中的章节、卷篇与附属文本边界",
	Schema: schema.Object(
		schema.Property("boundaries", schema.Array("按原文顺序排列的边界", schema.Object(
			schema.Property("unit_id", schema.String("owned 区间内的 unit id")).Required(),
			schema.Property("anchor", nullableString("同一 unit 多边界时的原文定位片段；否则为 null")).Required(),
			schema.Property("kind", schema.Enum("边界类型", kindChapter, kindGroup, kindFrontMatter, kindBackMatter)).Required(),
			schema.Property("title", nullableString("标题原文；没有标题时为 null")).Required(),
			schema.Property("uncertain", schema.Bool("是否需要用户确认")).Required(),
			schema.Property("reason", nullableString("不确定原因；无需说明时为 null")).Required(),
		))).Required(),
	),
}

var summaryContract = llmcontract.Contract{
	Name:        "rip_chapter_summary",
	Description: "逐章拆解对标原文的情节点、基调、爽点与手法",
	Schema: schema.Object(
		schema.Property("chapters", schema.Array("与输入章号顺序一致的逐章拆解", chapterSummarySchema())).Required(),
	),
}

func chapterSummarySchema() map[string]any {
	plotPoint := schema.Object(
		schema.Property("id", schema.String("情节点编号，从 P1 起按序递增不跳号")).Required(),
		schema.Property("beat", schema.String("该情节点发生的事")).Required(),
		schema.Property("tone", schema.Enum("该情节点的基调", plotTones...)).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("章号")).Required(),
		schema.Property("title", schema.String("章节标题")).Required(),
		schema.Property("summary", schema.String("本章梗概")).Required(),
		schema.Property("plot_points", schema.Array("本章情节点，按发生顺序", plotPoint)).Required(),
		schema.Property("key_facts", stringList("正文明确揭示的关键信息，至少 1 条")).Required(),
		schema.Property("payoffs", stringList("本章的爽点/情绪支付")).Required(),
		schema.Property("hook", nullableString("章末钩子；无则为 null")).Required(),
		schema.Property("hook_type", schema.Enum("章末钩子类型", domain.HookTypes()...)).Required(),
		schema.Property("dominant_strand", schema.Enum("主导叙事线", domain.DominantStrands()...)).Required(),
		schema.Property("characters", stringList("本章出场人物")).Required(),
		schema.Property("emotion_arc", schema.String("本章情绪走向：从哪起、落到哪")).Required(),
		schema.Property("techniques", stringList("本章可见的写作手法（视角/对话/时间处理/信息投放等）")).Required(),
	)
}

var previewContract = llmcontract.Contract{
	Name:        "rip_preview",
	Description: "深读开篇黄金三章，判断入场方式与读者承诺",
	Schema: schema.Object(
		schema.Property("chapters", schema.Array("与输入章号顺序一致的逐章深读", schema.Object(
			schema.Property("chapter", schema.Int("章号")).Required(),
			schema.Property("title", schema.String("章节标题")).Required(),
			schema.Property("opening", schema.String("本章如何入场、怎么钩人")).Required(),
			schema.Property("hooks", stringList("本章埋下的钩子")).Required(),
			schema.Property("conflict", schema.String("本章核心冲突")).Required(),
			schema.Property("promise", schema.String("本章给读者的承诺：读下去能得到什么")).Required(),
			schema.Property("risks", stringList("可能劝退读者的地方")).Required(),
		))).Required(),
		schema.Property("verdict", schema.String("三章整体判断")).Required(),
		schema.Property("strengths", stringList("值得学的地方，至少 1 条")).Required(),
		schema.Property("weaknesses", stringList("明显短板")).Required(),
	),
}

var aggregateContract = llmcontract.Contract{
	Name:        "rip_aggregate",
	Description: "把逐章拆解归纳为剧情单元、节奏与可复用情绪模块",
	Schema: schema.Object(
		schema.Property("plot_units", schema.Array("剧情单元，按章节顺序，必须连续不重叠且完整覆盖全书", schema.Object(
			schema.Property("title", schema.String("单元标题")).Required(),
			schema.Property("function", schema.String("该单元在全书中承担的功能")).Required(),
			schema.Property("start_chapter", schema.Int("起始章号")).Required(),
			schema.Property("end_chapter", schema.Int("结束章号")).Required(),
			schema.Property("summary", schema.String("单元概要")).Required(),
			schema.Property("turns", stringList("单元内的关键转折")).Required(),
		))).Required(),
		schema.Property("pacing", schema.Array("章节区间的节奏观察", schema.Object(
			schema.Property("start_chapter", schema.Int("起始章号")).Required(),
			schema.Property("end_chapter", schema.Int("结束章号")).Required(),
			schema.Property("tempo", schema.Enum("该区间的速度", "快", "中", "慢")).Required(),
			schema.Property("note", schema.String("节奏说明")).Required(),
		))).Required(),
		schema.Property("emotion_modules", schema.Array("可复用的情绪模块", schema.Object(
			schema.Property("name", schema.String("模块名")).Required(),
			schema.Property("trigger", schema.String("如何触发这种情绪")).Required(),
			schema.Property("payoff", schema.String("如何支付这种情绪")).Required(),
			schema.Property("chapters", schema.Array("出现章号", schema.Int("章号"))).Required(),
		))).Required(),
		schema.Property("main_conflict", schema.String("全书主线冲突")).Required(),
		schema.Property("story_core", schema.String("故事核：一句话说清这本书在讲什么")).Required(),
	),
}

var profileContract = llmcontract.Contract{
	Name:        "rip_profile",
	Description: "归纳角色分层、设定与人物关系网",
	Schema: schema.Object(
		schema.Property("characters", schema.Array("角色画像，至少 1 个", schema.Object(
			schema.Property("name", schema.String("角色名，必须使用原文中出现的称呼")).Required(),
			schema.Property("aliases", stringList("原文中的其他称呼")).Required(),
			schema.Property("tier", schema.Enum("角色层级", "主角", "重要", "配角", "功能性")).Required(),
			schema.Property("function", schema.String("该角色在故事中承担的功能")).Required(),
			schema.Property("hard_facts", stringList("正文明确写出的硬事实；引用原文时用「」包裹并逐字复制")).Required(),
			schema.Property("inferred", stringList("你的推断，与硬事实分开")).Required(),
			schema.Property("arc", schema.String("角色在全书中的变化")).Required(),
			schema.Property("first_seen", schema.Int("首次出场章号")).Required(),
			schema.Property("motivation", schema.String("角色动机")).Required(),
		))).Required(),
		schema.Property("settings", schema.Array("世界规则、金手指、组织、地理等设定", schema.Object(
			schema.Property("category", schema.String("设定类别")).Required(),
			schema.Property("name", schema.String("设定名")).Required(),
			schema.Property("rule", schema.String("规则内容")).Required(),
			schema.Property("boundary", nullableString("该设定的限制（不可违反处）；原文未写则为 null")).Required(),
		))).Required(),
		schema.Property("relations", schema.Array("人物关系；两端必须出现在 characters 的名字或别名中", schema.Object(
			schema.Property("from", schema.String("关系起点角色")).Required(),
			schema.Property("to", schema.String("关系终点角色")).Required(),
			schema.Property("kind", schema.String("关系类型")).Required(),
			schema.Property("change", nullableString("全书中的关系变化；无变化则为 null")).Required(),
		))).Required(),
	),
}

var reportContract = llmcontract.Contract{
	Name:        "rip_report",
	Description: "产出完整拆文报告：结构、钩子、反转、评分与可复用手法",
	Schema: schema.Object(
		schema.Property("story_core", schema.String("故事核")).Required(),
		schema.Property("synopsis", schema.String("故事梗概")).Required(),
		schema.Property("beats", schema.Array("功能分段，须含开端/发展/高潮/结局，按章节顺序不重叠", schema.Object(
			schema.Property("name", schema.String("分段名")).Required(),
			schema.Property("start_chapter", schema.Int("起始章号")).Required(),
			schema.Property("end_chapter", schema.Int("结束章号")).Required(),
			schema.Property("function", schema.String("该段承担的功能")).Required(),
		))).Required(),
		schema.Property("hooks", stringList("全书钩子设计")).Required(),
		schema.Property("reversal", schema.Object(
			schema.Property("type", schema.Enum("反转类型；本篇确实没有反转时选「无反转」", reversalTypes...)).Required(),
			schema.Property("mechanism", nullableString("反转机制；无反转时为 null")).Required(),
			schema.Property("setup_clues", stringList("铺垫线索；无反转时可为空数组")).Required(),
			schema.Property("chapter", llmcontract.Nullable(schema.Int("反转引爆章；无反转时为 null"))).Required(),
		)).Required(),
		schema.Property("scores", schema.Array("分维度评分，1-5 分并给理由", schema.Object(
			schema.Property("dimension", schema.String("维度名")).Required(),
			schema.Property("value", schema.Int("分数，1-5")).Required(),
			schema.Property("reason", schema.String("评分理由")).Required(),
		))).Required(),
		schema.Property("resonance", stringList("共鸣层次，由表层到深层")).Required(),
		schema.Property("reusable_structures", schema.Array("可复用结构/手法", schema.Object(
			schema.Property("name", schema.String("结构名")).Required(),
			schema.Property("how", schema.String("怎么用")).Required(),
			schema.Property("fail_mode", nullableString("用错会怎样；无则为 null")).Required(),
			schema.Property("example", nullableString("本书中的落点；无则为 null")).Required(),
		))).Required(),
		schema.Property("character_archetypes", stringList("有反差的人物原型")).Required(),
		schema.Property("opening", schema.String("开头分析")).Required(),
		schema.Property("ending", schema.String("结尾分析")).Required(),
		schema.Property("pacing_brief", schema.String("节奏速报")).Required(),
		schema.Property("verdict", schema.String("一句话总评")).Required(),
	),
}

var styleContract = llmcontract.Contract{
	Name:        "rip_style",
	Description: "裁定文风：语感、句式、视角、对话与叙述配比，并挑出原文锚点",
	Schema: schema.Object(
		schema.Property("voice", schema.String("语感总述")).Required(),
		schema.Property("sentence", schema.String("句式特征")).Required(),
		schema.Property("perspective", schema.String("视角与人称")).Required(),
		schema.Property("dialogue", schema.String("对话风格")).Required(),
		schema.Property("narration", schema.String("叙述与描写配比")).Required(),
		schema.Property("signatures", stringList("标志性手法，至少 1 条")).Required(),
		schema.Property("avoid", stringList("模仿时要避开的陷阱")).Required(),
		schema.Property("anchors", schema.Array("原文锚点，至少 2 条", schema.Object(
			schema.Property("chapter", schema.Int("锚点所在章号")).Required(),
			schema.Property("quote", schema.String("原文片段，逐字复制，至少 8 字")).Required(),
			schema.Property("why", schema.String("这段体现了什么文风特征")).Required(),
		))).Required(),
	),
}
