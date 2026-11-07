package prompt

import (
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

const (
	ToolAuthorityRead         = "authority_read"
	ToolWorkspaceRead         = "workspace_read"
	ToolWorkspacePutChapter   = "workspace_put_chapter"
	ToolWorkspaceReplaceBlock = "workspace_replace_block"
	ToolWorkspacePutCandidate = "workspace_put_candidate"
	ToolWorkspacePutReview    = "workspace_put_review"
	ToolProposalSubmit        = "proposal_submit"
	ToolVerdictSubmit         = "verdict_submit"
)

// 正文段落写给读者，同时是工具参数里的 JSON 字符串：英文双引号既是排版错误，漏转义
// 还会让整章参数作废。
const proseTextSchema = `{"type":"string","description":"段落正文。对白与引语用中文引号“”（嵌套用‘’），不用英文双引号"}`

// 模型只说故事语言（D66）：章用章号，卷与故事弧用序号，实体用名称，事实用主体+谓词；
// 文档身份全部由宿主维护，下列 Schema 与 narrative 的类型逐字段对应。

// StoryReadSchema 是按章号、序号或名称回查故事内容的参数（narrative.Query）：Worker 的
// authority_read 与主编的 book_read 共用。
const StoryReadSchema = `{
	"type": "object",
	"properties": {
		"chapter": {"type": "integer", "minimum": 1, "description": "章号：返回该章大纲；已写的章附正文与来源于它的事实"},
		"arc": {"type": "integer", "minimum": 1, "description": "故事弧序号：返回该弧与其下各章大纲"},
		"volume": {"type": "integer", "minimum": 1, "description": "卷序号：返回该卷与其下各故事弧"},
		"entity": {"type": "string", "minLength": 1, "description": "实体名称或别名：返回该实体与它的事实（事件只给最近的）"}
	},
	"additionalProperties": false
}`

const authorityReadDescription = "回查上下文没有装入的故事内容（任务开始时的版本）：chapter、arc、volume、entity 四选一"

// 审阅发现只经 workspace_put_review 写入，裁定由宿主从记录里取（D60），
// 但最终仍过 ReviewVerdict.Validate，所以约束必须在写入这一侧讲清。
const reviewFindingSchema = `{
	"type": "object",
	"properties": {
		"chapter": {"type": "integer", "minimum": 1,
			"description": "章号，必须是本次审阅范围（任务的 chapters）之一；跨章问题挂到要改的那一章"},
		"severity": {"type": "string", "enum": ["blocking", "note"],
			"description": "blocking=必须修改的问题，违反的是任务 requirements 里的一项时用 requirement 填它的 id，其他问题不填；note=仅供参考的观察，禁止带 requirement。任务 reviewed 列出的章只作上下文，只有违反 requirements 才能记 blocking"},
		"note": {"type": "string", "description": "结论与依据"},
		"requirement": {"type": "string", "description": "仅 blocking 可用：逐字取自任务输入 requirements 的 id（不是问题类别），且该项在裁定 checks 中声明为 violated；任务没有 requirements 时不填"}
	},
	"required": ["chapter", "severity", "note"],
	"additionalProperties": false
}`

const verdictSubmitSchema = `{
	"type": "object",
	"properties": {
		"review_key": {"type": "string", "minLength": 1, "description": "workspace_put_review 用过的 key"},
		"checks": {"type": "array",
			"description": "逐项声明任务输入 requirements 的核验结论，不多不少；任务没带 requirements 就省略本字段，不要自拟",
			"items": {"type": "object",
				"properties": {
					"id": {"type": "string", "description": "逐字取自任务输入的 requirements"},
					"status": {"type": "string", "enum": ["satisfied", "violated", "pending"],
						"description": "satisfied=本窗口正文已兑现；violated=被违反，审阅记录里必须有 blocking 发现用 requirement 链接它；pending=仅凭本窗口正文还无法判断（如尚未到期），settle 为 true 的项不得使用"},
					"note": {"type": "string", "description": "判断依据"}
				},
				"required": ["id", "status"],
				"additionalProperties": false}}
	},
	"required": ["review_key"],
	"additionalProperties": false
}`

// CapabilityDefinition 是内置故事能力的唯一静态定义。Operation Kind、Worker
// Profile、Prompt slots 与工具 Schema 在这里共同注册，调用层不再维护平行映射。
type CapabilityDefinition struct {
	OperationKinds []model.OperationKind
	Worker         WorkerProfile
}

// builtinCapabilities 是全部内置故事能力；结构由测试守护（每种任务只归一个 Worker，槽位、
// 工具都已登记）。
var builtinCapabilities = []CapabilityDefinition{
	{
		OperationKinds: []model.OperationKind{
			model.OperationDevelopPlan,
			model.OperationRevisePlan,
			model.OperationReviseCanon,
		},
		Worker: WorkerProfile{
			ID: "architect.design", Version: "1", ModelRole: "architect",
			PromptSlots: []Slot{SlotArchitectStoryDesign, SlotArchitectArcExpand},
			Tools: []ToolSchema{
				tool(ToolAuthorityRead, authorityReadDescription, StoryReadSchema),
				tool(ToolWorkspacePutCandidate, "把结构化草案写入当前任务工作区", `{"type":"object","properties":{"key":{"type":"string"},"content":{"type":"object"}},"required":["key","content"],"additionalProperties":false}`),
				proposalTool(submission{redeclare: true, planning: true}),
			},
			StopCondition: "已提交合法的候选，或返回明确错误",
		},
	},
	{
		OperationKinds: []model.OperationKind{model.OperationWriteChapter},
		Worker: WorkerProfile{
			ID: "writer.compose", Version: "1", ModelRole: "writer",
			PromptSlots:   []Slot{SlotWriterChapterPlan, SlotWriterChapterDraft},
			Tools:         writerTools(submission{prose: true}),
			StopCondition: "章节工作稿通过本地校验并提交候选，或返回明确错误",
		},
	},
	{
		OperationKinds: []model.OperationKind{model.OperationRewriteChapter},
		Worker: WorkerProfile{
			ID: "writer.revise", Version: "1", ModelRole: "writer",
			// 重写与新写共用写作标准，重写方法另成一段。
			PromptSlots: []Slot{SlotWriterChapterDraft, SlotWriterRewrite}, Tools: writerTools(submission{prose: true, redeclare: true}),
			StopCondition: "修订工作稿通过本地校验，并重申报本章全部既有事实（确认、更新或删除）后提交候选，或返回明确错误",
		},
	},
	{
		OperationKinds: []model.OperationKind{model.OperationReworkChapter},
		Worker: WorkerProfile{
			ID: "writer.rework", Version: "1", ModelRole: "writer",
			// 返工与修订共用重写方法；只有返工能随正文改本章的章题、章纲与转折（D87、D92）。
			PromptSlots: []Slot{SlotWriterChapterDraft, SlotWriterRewrite}, Tools: writerTools(submission{prose: true, redeclare: true, outline: true}),
			StopCondition: "返工稿通过本地校验，并重申报本章全部既有事实（确认、更新或删除）与章题章纲后提交候选，或返回明确错误",
		},
	},
	{
		OperationKinds: []model.OperationKind{model.OperationReviewRange},
		Worker: WorkerProfile{
			ID: "editor.review", Version: "1", ModelRole: "editor",
			PromptSlots: []Slot{SlotEditorStoryReview, SlotEditorStyleReview},
			Tools: []ToolSchema{
				tool(ToolAuthorityRead, authorityReadDescription, StoryReadSchema),
				tool(ToolWorkspacePutReview, "把审阅记录写入当前任务工作区。findings 只记真正的问题；"+
					"逐项核验结论（含「已满足」「待定」）走 verdict_submit 的 checks，不要写成 note 发现",
					`{"type":"object","properties":{"key":{"type":"string"},`+
						`"findings":{"type":"array","items":`+reviewFindingSchema+`}},`+
						`"required":["key","findings"],"additionalProperties":false}`),
				tool(ToolVerdictSubmit, "提交审阅裁定：引用 workspace_put_review 写好的审阅记录。章节范围、发现与结论由宿主按任务和记录填入，记录里有 blocking 发现即未通过。"+
					"每个 violated 的要求，都要在审阅记录里有一条 blocking 发现通过 requirement 链接到它", verdictSubmitSchema),
			},
			StopCondition: "已提交覆盖请求范围的结构化裁定（verdict_submit），或返回明确错误",
		},
	},
}

func BuiltinCapabilities() []CapabilityDefinition { return builtinCapabilities }

func BuiltinCapability(kind model.OperationKind) (CapabilityDefinition, error) {
	for _, definition := range builtinCapabilities {
		for _, candidate := range definition.OperationKinds {
			if candidate == kind {
				return definition, nil
			}
		}
	}
	return CapabilityDefinition{}, fmt.Errorf("operation %s is not backed by a story capability: %w", kind, model.ErrInvalid)
}

func BuiltinWorkerProfile(id string) (WorkerProfile, error) {
	for _, definition := range builtinCapabilities {
		if definition.Worker.ID == id {
			return definition.Worker, nil
		}
	}
	return WorkerProfile{}, fmt.Errorf("worker profile %q does not exist: %w", id, model.ErrInvalid)
}

// writerTools 是写作类 Worker 的工具，can 是它能提交的内容。一次任务只写一章、只有一份
// 章节工作稿（D86），稿件键由宿主定，不做成模型参数（D60）。
func writerTools(can submission) []ToolSchema {
	return []ToolSchema{
		tool(ToolAuthorityRead, authorityReadDescription, StoryReadSchema),
		tool(ToolWorkspaceRead, "读回本章工作稿的当前版本：段落编号与正文。任务开始时工作稿是空的，直接用 workspace_put_chapter 写入整章；接着上次的尝试做时任务里会说明。工作稿是照更早的作品写的（前任留下的稿）时，changed_since 列出这之后书里的修改，照现在的书核对着改", `{"type":"object","properties":{},"additionalProperties":false}`),
		putChapterTool(),
		tool(ToolWorkspaceReplaceBlock, "按段落编号修改本章工作稿的一个段落；改几段就调用几次，返回改后的版本", `{"type":"object","properties":{"block_id":{"type":"string"},"text":`+proseTextSchema+`},"required":["block_id","text"],"additionalProperties":false}`),
		proposalTool(can),
	}
}

// putChapterTool 只收正文：章的身份（哪一章、署名）由宿主按任务确定，标题在章节计划上。
func putChapterTool() ToolSchema {
	return tool(ToolWorkspacePutChapter, "写入本章工作稿：整篇覆盖", mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"chapter": map[string]any{"type": "object", "properties": map[string]any{
				"blocks": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":   map[string]any{"type": "string", "description": "段落编号，本稿内唯一，按段修改时用"},
						"text": json.RawMessage(proseTextSchema),
					},
					"required": []string{"id", "text"}, "additionalProperties": false,
				}},
			}, "required": []string{"blocks"}, "additionalProperties": false},
		},
		"required": []string{"chapter"}, "additionalProperties": false,
	}))
}

const predicateDescription = "受控谓词，前缀决定种类：event.（事件，跨章只追加）、state.（状态）、relation.（关系，对象写进 value）、" +
	"rule.（世界规则）、foreshadow.（伏笔，命名具体线索）；前缀后用小写字母、数字、下划线"

// factSchema 是一条事实；来源章由宿主按任务填。
func factSchema() map[string]any {
	properties := map[string]any{
		"subject":   map[string]any{"type": "string", "description": "主体的名称或别名：已有实体，或本次 entities 里新建的"},
		"predicate": map[string]any{"type": "string", "description": predicateDescription},
		"value":     map[string]any{"type": "string", "description": "事实内容，一句客观陈述"},
		"effective_chapter": map[string]any{"type": "integer", "minimum": 1,
			"description": "只在插叙、回忆时填：状态在故事里生效的章号，缺省即来源章；生效位置不得早于现值，倒叙记为事件"},
		"resolved": map[string]any{"type": "boolean", "description": "只用于 foreshadow：回收伏笔时按同一主体+谓词提交 true"},
	}
	return map[string]any{"type": "object", "properties": properties, "required": []string{"subject", "predicate", "value"}, "additionalProperties": false}
}

func factRefSchema() map[string]any {
	properties := map[string]any{
		"subject":   map[string]any{"type": "string", "description": "主体的名称或别名"},
		"predicate": map[string]any{"type": "string"},
	}
	return map[string]any{"type": "object", "properties": properties, "required": []string{"subject", "predicate"}, "additionalProperties": false}
}

func entitiesSchema(description string) map[string]any {
	return map[string]any{"type": "array", "description": description,
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"name":    map[string]any{"type": "string", "description": "本名，不得与其他实体的名称或别名相同"},
			"kind":    map[string]any{"type": "string", "enum": []string{"character", "location", "item", "organization"}},
			"aliases": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "required": []string{"name", "kind"}, "additionalProperties": false}}
}

func planEditSchema(number, parent, parentDescription string) map[string]any {
	properties := map[string]any{
		number:    map[string]any{"type": "integer", "minimum": 1},
		"title":   map[string]any{"type": "string"},
		"summary": map[string]any{"type": "string"},
	}
	if parent != "" {
		properties[parent] = map[string]any{"type": "integer", "minimum": 1, "description": parentDescription}
	}
	if number == "chapter" {
		properties["turn"] = map[string]any{"type": "string", "description": turnDescription + "；新增的章必填"}
		properties["threads"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"},
			"description": "本章推进的长线（罗盘里长线的 name）：可以没有；给出即整体替换，省略保持原样"}
	}
	return map[string]any{"type": "array", "items": map[string]any{
		"type": "object", "properties": properties, "required": []string{number}, "additionalProperties": false,
	}}
}

// turnDescription 是章的转折（D92）：每章一个硬进展，写手照它写成场景，审阅照它核对。
const turnDescription = "本章的转折：读完这一章，读者在它推进的长线上多知道了哪件具体的事，或局面变成了什么；写成一件会发生的事，不写气氛与感悟"

const compassSchema = `{"type":"object","description":"故事罗盘的终局与篇幅（长线另在 threads 里编辑），只有规划任务可写。ending 是终局方向：故事最终要回答的问题或抵达的状态，` +
	`首次规划必须给出，之后只在有变化时提交，给出时 ending 与 final 一起替换。final 是收官承诺（全书章数，未收官时省略）：故事到了收束点才声明，` +
	`给出后蓝图不超过第 final 章，可分次规划到它。任务有 fixed_chapters 时篇幅由用户固定，final 与 scale_max 都不写。` +
	`scale_max 是篇幅上限，平时不写：未收官的蓝图触及上限时提交会被退回并说明上限，故事确实需要更长才给出更高的上限，需用户同意；省略保持原样",` +
	`"properties":{"scale_max":{"type":"integer","minimum":1},"ending":{"type":"string"},"final":{"type":"integer","minimum":1}},` +
	`"required":["ending"],"additionalProperties":false}`

// threadsSchema 是长线编辑（D91）：按 name 定位，已有的可改问题、写上作答章，新的要给问题。
func threadsSchema() map[string]any {
	return map[string]any{"type": "array",
		"description": "长线：全书在追问、终局要回答的几个大问题，可以分属不同的人物。它们与伏笔不同：伏笔是埋下待回收的具体线索。" +
			"按 name 定位：已有的可以改 question、写上 answer，没有的就是新立，要给 question；长线只能作答，不能删",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"name":     map[string]any{"type": "string", "description": "短名，章节的 threads 按它引用"},
			"question": map[string]any{"type": "string", "description": "这条长线追问的问题，一句话"},
			"answer":   map[string]any{"type": "integer", "minimum": 1, "description": "回答它的章号，须是已规划或这次规划的章：作答章还没规划到就先不写；已写成的章作答后不再改"},
		}, "required": []string{"name"}, "additionalProperties": false}}
}

// submission 是一种任务能提交的内容。prose 是写章任务：正文由宿主取本章工作稿装配；
// redeclare 公开确认与删除，只给要重申报已有事实的任务——写新章时本章还没有事实；planning
// 公开罗盘与卷弧章编辑；outline 要求重申报本章的章题、章纲与转折，只给返工（D87、D92）。
type submission struct{ prose, redeclare, planning, outline bool }

// proposalTool 按任务公开提交参数：没有合法用途的参数不出现，模型就不会误用。创作意图只装
// 用户的原话，AI 不写（D70）。
func proposalTool(can submission) ToolSchema {
	required := []string{"reason"}
	facts := map[string]any{"type": "array", "items": factSchema(),
		"description": "只写本次新确立或改变的事实；其他章节的既有事实原样保留，不要重复提交"}
	properties := map[string]any{
		"reason":   map[string]any{"type": "string"},
		"entities": entitiesSchema("新登场的人物、地点、物品、组织；与已有实体同名即修改它（合并别名）"),
		"facts":    facts,
	}
	description := `把结果提交为候选，不直接修改作品。实体按名称、事实按主体+谓词定位：state/rule/foreshadow 的同一主体同一谓词就是同一事实，` +
		`再次提交即更新；事件与关系按来源章区分，同一章里同一主体的同一谓词只记一条。`
	if can.prose {
		description += `正文由宿主取本章工作稿的当前版本装配。` +
			`例：{"reason":"完成本章","facts":[{"subject":"<实体名>","predicate":"event.oath","value":"在山门前立誓入宗"}]}。`
	}
	if can.prose && !can.redeclare {
		facts["minItems"] = 1
		required = append(required, "facts")
	}
	if can.redeclare {
		properties["confirm_facts"] = map[string]any{"type": "array", "items": factRefSchema(),
			"description": "重申报时原样保留的既有事实，宿主复制原值"}
		properties["remove_facts"] = map[string]any{"type": "array", "items": factRefSchema(),
			"description": "不再成立的既有事实；想撤销对某状态的改动就把值改回原值，不要删除整个事实"}
		description += `重申报所改章节的全部既有事实：不变的列进 confirm_facts，要改的在 facts 里提交新值，不再成立的列进 remove_facts；其他章节的事实不要动。`
	}
	if can.outline {
		properties["outline"] = map[string]any{"type": "object",
			"description": "重申报本章的章题、章纲与转折：照重写后的正文给出，返工改了本章写的事就改，没变就原样给出",
			"properties": map[string]any{
				"title":   map[string]any{"type": "string", "minLength": 1},
				"summary": map[string]any{"type": "string", "minLength": 1, "description": "一两句话说本章写了什么"},
				"turn":    map[string]any{"type": "string", "minLength": 1, "description": turnDescription},
			}, "required": []string{"title", "summary", "turn"}, "additionalProperties": false}
		required = append(required, "outline")
	}
	if can.planning {
		properties["compass"] = json.RawMessage(compassSchema)
		properties["threads"] = threadsSchema()
		properties["volumes"] = planEditSchema("volume", "", "")
		properties["arcs"] = planEditSchema("arc", "volume", "所属卷序号，新增时必填")
		chapters := planEditSchema("chapter", "arc", "所属故事弧序号，新增时必填")
		chapters["description"] = "章节大纲。只有规划任务能新增章节，追加多少章由你按故事走向决定：至少一章，除非以现有章数声明收官；篇幅上界见 fixed_chapters 与 compass"
		properties["chapters"] = chapters
		description += `卷（volumes）、故事弧（arcs）、章（chapters）按序号编辑：已有序号是修改，省略的字段保持原值；` +
			`新增的紧接 story_context.totals 的现有总数连续编号，并给出 title、summary、上级序号，章还要给 turn；只能在末尾追加，不能插入或删除；已写的章不改。` +
			`长线（threads）至少一条，章节用 threads 引用它们；story_context.compass 里每条长线带着最近推进它的已写章（advanced）、此后停了几章（quiet）与下一次的安排（next）。`
	}
	return tool(ToolProposalSubmit, description, mustSchema(map[string]any{
		"type": "object", "properties": properties, "required": required, "additionalProperties": false,
	}))
}

func mustSchema(schema map[string]any) string {
	payload, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func tool(name, description, schema string) ToolSchema {
	return ToolSchema{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}
