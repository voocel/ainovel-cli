package imp

import (
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func nullableString(description string) map[string]any {
	return llmcontract.Nullable(schema.String(description))
}

func stringList(description string) map[string]any {
	return schema.Array(description, schema.String(description))
}

var segmentContract = llmcontract.Contract{
	Name:        "import_segment",
	Description: "识别导入文本中的章节、卷篇与附属文本边界",
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

var analysisContract = llmcontract.Contract{
	Name:        "import_chapter_analysis",
	Description: "提取连续章节的可追溯故事事实",
	Schema: schema.Object(
		schema.Property("chapters", schema.Array("与输入章号顺序一致的逐章事实", chapterFactsSchema())).Required(),
	),
}

func chapterFactsSchema() map[string]any {
	characterEvidence := schema.Object(
		schema.Property("chapter", schema.Int("证据所在章")).Required(),
		schema.Property("name", schema.String("人物名")).Required(),
		schema.Property("note", nullableString("人物事实；无则为 null")).Required(),
	)
	worldEvidence := schema.Object(
		schema.Property("chapter", schema.Int("证据所在章")).Required(),
		schema.Property("category", nullableString("世界事实类别；无法归类时为 null")).Required(),
		schema.Property("fact", schema.String("正文明确揭示的世界事实")).Required(),
	)
	timelineEvent := schema.Object(
		schema.Property("chapter", schema.Int("章号")).Required(),
		schema.Property("time", schema.String("故事内时间")).Required(),
		schema.Property("event", schema.String("事件")).Required(),
		schema.Property("characters", stringList("相关人物")).Required(),
	)
	foreshadow := schema.Object(
		schema.Property("id", schema.String("复用 ledger 中的伏笔 ID")).Required(),
		schema.Property("action", schema.Enum("伏笔动作", "plant", "advance", "resolve")).Required(),
		schema.Property("description", nullableString("plant 时的伏笔说明；其他情况可为 null")).Required(),
	)
	relationship := schema.Object(
		schema.Property("character_a", schema.String("人物 A")).Required(),
		schema.Property("character_b", schema.String("人物 B")).Required(),
		schema.Property("relation", schema.String("关系变化")).Required(),
		schema.Property("chapter", schema.Int("章号")).Required(),
	)
	stateChange := schema.Object(
		schema.Property("chapter", schema.Int("章号")).Required(),
		schema.Property("entity", schema.String("角色或实体")).Required(),
		schema.Property("field", schema.String("发生变化的属性")).Required(),
		schema.Property("old_value", nullableString("变化前状态；首次出现时为 null")).Required(),
		schema.Property("new_value", schema.String("变化后状态")).Required(),
		schema.Property("reason", nullableString("变化原因；正文未说明时为 null")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("章号")).Required(),
		schema.Property("title", schema.String("章节标题")).Required(),
		schema.Property("summary", schema.String("本章概要")).Required(),
		schema.Property("key_events", stringList("关键事件")).Required(),
		schema.Property("core_event", schema.String("本章最关键的一件事")).Required(),
		schema.Property("hook", nullableString("章末钩子；无则为 null")).Required(),
		schema.Property("scenes", stringList("场景序列")).Required(),
		schema.Property("characters", stringList("出场人物")).Required(),
		schema.Property("character_evidence", schema.Array("人物证据", characterEvidence)).Required(),
		schema.Property("world_evidence", schema.Array("世界事实证据", worldEvidence)).Required(),
		schema.Property("timeline_events", schema.Array("时间线事件", timelineEvent)).Required(),
		schema.Property("foreshadow_updates", schema.Array("伏笔增量", foreshadow)).Required(),
		schema.Property("relationship_changes", schema.Array("关系变化", relationship)).Required(),
		schema.Property("state_changes", schema.Array("状态变化", stateChange)).Required(),
		schema.Property("hook_type", schema.Enum("章末钩子类型", "crisis", "mystery", "desire", "emotion", "choice")).Required(),
		schema.Property("dominant_strand", schema.Enum("主导叙事线", "quest", "fire", "constellation")).Required(),
	)
}

var rangeContract = llmcontract.Contract{
	Name:        "import_range_digest",
	Description: "归纳一个连续章节区间的剧情与事实",
	Schema: schema.Object(
		schema.Property("start_chapter", schema.Int("区间首章")).Required(),
		schema.Property("end_chapter", schema.Int("区间末章")).Required(),
		schema.Property("plot", schema.String("跨章主线剧情推进")).Required(),
		schema.Property("characters", stringList("有实质进展的人物")).Required(),
		schema.Property("world_facts", stringList("已确立的世界事实")).Required(),
		schema.Property("opened_threads", stringList("本区间新开的长线")).Required(),
		schema.Property("resolved_threads", stringList("本区间收束的长线")).Required(),
	),
}

var synthesisContract = llmcontract.Contract{
	Name:        "import_book_synthesis",
	Description: "综合全书事实并给出连续完整的卷弧范围",
	Schema: schema.Object(
		schema.Property("premise", schema.String("故事前提的 Markdown 描述")).Required(),
		schema.Property("characters", schema.Array("主要人物", schema.Object(
			schema.Property("name", schema.String("人物名")).Required(),
			schema.Property("aliases", stringList("别名与称号")).Required(),
			schema.Property("role", schema.String("叙事角色")).Required(),
			schema.Property("description", schema.String("人物描述")).Required(),
			schema.Property("arc", schema.String("人物弧")).Required(),
			schema.Property("traits", stringList("人物特质")).Required(),
			schema.Property("tier", nullableString("人物层级；无法判断时为 null")).Required(),
		))).Required(),
		schema.Property("world_rules", schema.Array("正文确立的世界规则", schema.Object(
			schema.Property("category", schema.String("规则类别")).Required(),
			schema.Property("rule", schema.String("规则描述")).Required(),
			schema.Property("boundary", schema.String("不可违反的边界")).Required(),
		))).Required(),
		schema.Property("structure", schema.Array("卷与弧的连续章节范围", schema.Object(
			schema.Property("title", schema.String("卷标题")).Required(),
			schema.Property("theme", schema.String("卷核心冲突或主题")).Required(),
			schema.Property("arcs", schema.Array("卷内故事弧", schema.Object(
				schema.Property("title", schema.String("弧标题")).Required(),
				schema.Property("goal", schema.String("弧目标")).Required(),
				schema.Property("start_chapter", schema.Int("起始章")).Required(),
				schema.Property("end_chapter", schema.Int("结束章")).Required(),
			))).Required(),
		))).Required(),
		schema.Property("compass", schema.Object(
			schema.Property("ending_direction", schema.String("终局方向")).Required(),
			schema.Property("open_threads", stringList("仍未收束的长线")).Required(),
			schema.Property("estimated_scale", nullableString("模糊规模；无法判断时为 null")).Required(),
			schema.Property("last_updated", llmcontract.Nullable(schema.Int("依据的最新章号；无需填写时为 null"))).Required(),
		)).Required(),
		schema.Property("planning_tier", schema.Enum("规划层级", "short", "mid", "long")).Required(),
		schema.Property("story_status", schema.Enum("故事是否完结", storyOpen, storyClosed, storyUncertain)).Required(),
		schema.Property("status_reason", nullableString("状态判断理由")).Required(),
	),
}
