package sim

import (
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func textList(description string) map[string]any {
	return schema.Array(description, schema.String(description))
}

var sourceReportContract = llmcontract.Contract{
	Name:        "simulation_source_report",
	Description: "从单篇语料提炼可复用且不复制原文的写作方法",
	Schema: schema.Object(
		schema.Property("title", llmcontract.Nullable(schema.String("可选标题；无法确认时为 null"))).Required(),
		schema.Property("summary", schema.String("样本文本的写法价值概括")).Required(),
		schema.Property("style_observations", textList("叙述视角、句式与描写纹理观察")).Required(),
		schema.Property("common_words", textList("高频词、意象与转场词类别")).Required(),
		schema.Property("plot_patterns", textList("情节推进、转折与冲突升级模式")).Required(),
		schema.Property("hook_patterns", textList("开篇、章末与信息差钩子模式")).Required(),
		schema.Property("pacing_notes", textList("场景密度与信息释放节奏")).Required(),
		schema.Property("reader_appeal", textList("吸引读者继续阅读的方法")).Required(),
		schema.Property("reusable_techniques", textList("可借鉴的结构性技巧")).Required(),
		schema.Property("warnings", textList("必须避免的复制与套用风险")).Required(),
	),
}

var synthesisContract = llmcontract.Contract{
	Name:        "simulation_synthesis",
	Description: "把既有画像和语料报告合成为可执行的仿写方法画像",
	Schema: schema.Object(
		schema.Property("style", schema.Object(
			schema.Property("narrative_voice", textList("叙述人称、距离与信息控制")).Required(),
			schema.Property("sentence_rhythm", textList("句式节奏")).Required(),
			schema.Property("prose_texture", textList("描写质感")).Required(),
			schema.Property("perspective", textList("视角规则")).Required(),
			schema.Property("mood", textList("情绪调性")).Required(),
			schema.Property("do_not_copy", textList("禁止复制的内容")).Required(),
		)).Required(),
		schema.Property("lexicon", schema.Object(
			schema.Property("common_words", textList("常用词类别")).Required(),
			schema.Property("emotion_words", textList("情绪词类别")).Required(),
			schema.Property("scene_words", textList("场景词类别")).Required(),
			schema.Property("transition_words", textList("转场词类别")).Required(),
			schema.Property("signature_phrases", textList("抽象后的口吻特征，不含原句")).Required(),
		)).Required(),
		schema.Property("plot_design", schema.Object(
			schema.Property("opening_patterns", textList("开局方式")).Required(),
			schema.Property("escalation_patterns", textList("冲突升级方式")).Required(),
			schema.Property("turning_point_patterns", textList("转折设计")).Required(),
			schema.Property("payoff_patterns", textList("回收与兑现方式")).Required(),
		)).Required(),
		schema.Property("hook_design", schema.Object(
			schema.Property("hook_types", textList("钩子类型")).Required(),
			schema.Property("placement", textList("钩子位置")).Required(),
			schema.Property("cliffhanger_patterns", textList("悬念停顿方式")).Required(),
			schema.Property("payoff_rules", textList("钩子兑现规则")).Required(),
		)).Required(),
		schema.Property("pacing_density", schema.Object(
			schema.Property("scene_density", textList("单场景信息密度")).Required(),
			schema.Property("information_release", textList("信息释放节奏")).Required(),
			schema.Property("dialogue_action_ratio", textList("对白、动作与心理比例")).Required(),
			schema.Property("compression_rules", textList("内容展开与压缩规则")).Required(),
		)).Required(),
		schema.Property("reader_engagement", schema.Object(
			schema.Property("methods", textList("吸引读者的方法")).Required(),
			schema.Property("emotional_drivers", textList("情绪驱动力")).Required(),
			schema.Property("progression_rewards", textList("阶段性进展奖励")).Required(),
			schema.Property("anti_patterns", textList("削弱吸引力的反模式")).Required(),
		)).Required(),
		schema.Property("role_guidance", schema.Object(
			schema.Property("architect", textList("Architect 使用画像的规则")).Required(),
			schema.Property("writer", textList("Writer 借鉴但不复制的规则")).Required(),
			schema.Property("editor", textList("Editor 检查方向与侵权风险的规则")).Required(),
		)).Required(),
	),
}
