package scan

import (
	"github.com/voocel/agentcore/schema"
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

// parseContract 把一份半结构化榜单文本读成结构化条目。
// 缺失字段一律留 null 而不是编造——清洗层会按必填缺失剔除并计入质量报告，
// 让模型猜排名或作者等于把脏数据洗白成干净数据。
var parseContract = llmcontract.Contract{
	Name:        "scan_parse",
	Description: "把平台榜单的半结构化文本读成结构化条目",
	Schema: schema.Object(
		schema.Property("entries", schema.Array("按原文顺序排列的榜单条目", schema.Object(
			schema.Property("rank", llmcontract.Nullable(schema.Int("榜单排名；原文未给出则为 null，不要推测"))).Required(),
			schema.Property("title", nullableString("书名；原文未给出则为 null")).Required(),
			schema.Property("author", nullableString("作者名；原文未给出则为 null")).Required(),
			schema.Property("category", nullableString("分类或标签；原文未给出则为 null")).Required(),
			schema.Property("description", nullableString("简介原文，逐字摘录不要改写；原文未给出则为 null")).Required(),
			schema.Property("stats", nullableString("热度数据（点击/收藏/在读等），保留原文单位；未给出则为 null")).Required(),
		))).Required(),
		schema.Property("notes", stringList("解析时遇到的可疑之处（疑似平台模板文本、字段错位等）")).Required(),
	),
}

// analyzeContract 归纳市场趋势。
var analyzeContract = llmcontract.Contract{
	Name:        "scan_analyze",
	Description: "从榜单条目归纳题材分布、趋势与新元素",
	Schema: schema.Object(
		schema.Property("categories", schema.Array("题材分布，按条目数从多到少", schema.Object(
			schema.Property("name", schema.String("题材名")).Required(),
			schema.Property("count", schema.Int("该题材的条目数")).Required(),
			schema.Property("examples", stringList("该题材的代表书名，取自输入条目")).Required(),
			schema.Property("note", schema.String("这一类的共性观察")).Required(),
		))).Required(),
		schema.Property("trends", schema.Array("趋势判断，至少 1 条", schema.Object(
			schema.Property("title", schema.String("趋势名")).Required(),
			schema.Property("evidence", stringList("支撑该判断的榜单证据，引用具体书名")).Required(),
			schema.Property("reading", schema.String("这说明了什么")).Required(),
		))).Required(),
		schema.Property("fresh_elements", schema.Array("新元素/新组合", schema.Object(
			schema.Property("name", schema.String("元素名")).Required(),
			schema.Property("seen_in", stringList("出现在哪些书里")).Required(),
			schema.Property("why", schema.String("为什么值得注意")).Required(),
		))).Required(),
		schema.Property("saturated", stringList("已经饱和、不建议再挤的方向")).Required(),
		schema.Property("verdict", schema.String("一句话总结这份榜单说明的市场状态")).Required(),
	),
}

// topicContract 产出选题决策。
// feasibility 由模型给初值，但 Go 侧会按样本量做降级钳制（见 ClampFeasibility）——
// 契约里把「高」的门槛写清楚，钳制才是兜底而不是常态。
var topicContract = llmcontract.Contract{
	Name:        "scan_topic",
	Description: "基于榜单趋势给出可执行的选题方向",
	Schema: schema.Object(
		schema.Property("ideas", schema.Array("选题方向，2-5 个", schema.Object(
			schema.Property("direction", schema.String("方向：一句话说清写什么")).Required(),
			schema.Property("why", schema.String("为什么能爆，须落到榜单证据上")).Required(),
			schema.Property("feasibility", schema.Enum("可行性；仅当榜单样本足够支撑时才可选「高」", FeasibilityHigh, FeasibilityMedium, FeasibilityLow)).Required(),
			schema.Property("risks", stringList("风险，至少 1 条")).Required(),
			schema.Property("next_steps", stringList("下一步具体动作，至少 1 条")).Required(),
		))).Required(),
	),
}
