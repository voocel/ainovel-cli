package prompt

import (
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

const (
	ToolAuthorityRead         = "authority_read"
	ToolWorkspaceList         = "workspace_list"
	ToolWorkspaceRead         = "workspace_read"
	ToolWorkspacePutChapter   = "workspace_put_chapter"
	ToolWorkspaceReplaceBlock = "workspace_replace_block"
	ToolWorkspacePutCandidate = "workspace_put_candidate"
	ToolWorkspacePutReview    = "workspace_put_review"
	ToolProposalSubmit        = "proposal_submit"
	ToolVerdictSubmit         = "verdict_submit"
)

// 模型能读写的故事文档种类只有这一份枚举。此前 proposal_submit 写了枚举、authority_read
// 是裸 string，模型就去猜 story_context / project_rules 这类不存在的种类。
const storyDocumentKinds = `["intent", "plan", "entity", "canon", "manuscript"]`

// authority_read 三个 Worker 各抄一份，且三份都没写取值域。集中一处并补齐说明。
const authorityReadSchema = `{
	"type": "object",
	"properties": {
		"kind": {"type": "string", "enum": ` + storyDocumentKinds + `,
			"description": "故事文档种类，只能取列出的这几种"},
		"id": {"type": "string", "description": "文档 ID；intent 用 root，其余用该文档自己的 id（如 ch-001）"},
		"revision": {"type": "integer", "minimum": 1,
			"description": "必填。要读的版本，不能超过任务基线 Revision；任务输入里给了基线就用它"}
	},
	"required": ["kind", "id", "revision"],
	"additionalProperties": false
}`

// 审阅发现只经 workspace_put_review 写入，裁定由宿主从记录里取（D60），
// 但最终仍过 ReviewVerdict.Validate，所以约束必须在写入这一侧讲清。
const reviewFindingSchema = `{
	"type": "object",
	"properties": {
		"chapter_id": {"type": "string",
			"description": "必须是本次请求范围内的章节之一（任务输入的 chapter_ids）；跨章问题挂到最相关的那一章，没有 all 这种写法"},
		"severity": {"type": "string", "enum": ["blocking", "note"],
			"description": "blocking=有要求未满足，必须用 directive_id 或 intent 指出是哪一项；note=仅供参考的观察，禁止带 directive_id 或 intent"},
		"note": {"type": "string", "description": "结论与依据"},
		"directive_id": {"type": "string", "description": "仅 blocking 可用，且与 intent 二选一，不能同时给"},
		"intent": {"type": "string", "enum": ["required_present", "forbidden_absent", "ending_consistent"],
			"description": "仅 blocking 可用，且与 directive_id 二选一，不能同时给"}
	},
	"required": ["chapter_id", "severity", "note"],
	"additionalProperties": false
}`

const verdictSubmitSchema = `{
	"type": "object",
	"properties": {
		"status": {"type": "string", "enum": ["pass", "blocked"],
			"description": "pass=审阅记录里没有 blocking 发现；blocked=至少一条"},
		"review_key": {"type": "string", "minLength": 1, "description": "workspace_put_review 用过的 key"},
		"intent": {"type": "object",
			"description": "任务要求核验意图（verify_intent）时必填：三个维度各自是否满足",
			"properties": {
				"required_present": {"type": "boolean"},
				"forbidden_absent": {"type": "boolean"},
				"ending_consistent": {"type": "boolean"}
			},
			"required": ["required_present", "forbidden_absent", "ending_consistent"],
			"additionalProperties": false},
		"directives": {"type": "array",
			"description": "逐条声明任务输入 directives 的核验结果，不多不少；任务没带 directives 就省略本字段，不要自拟",
			"items": {"type": "object",
				"properties": {
					"directive_id": {"type": "string", "description": "逐字取自任务输入的 directives"},
					"satisfied": {"type": "boolean"},
					"note": {"type": "string", "description": "判断依据"}
				},
				"required": ["directive_id", "satisfied"],
				"additionalProperties": false}}
	},
	"required": ["status", "review_key"],
	"additionalProperties": false
}`

// CapabilityDefinition 是内置故事能力的唯一静态定义。Operation Kind、Worker
// Profile、Prompt slots 与工具 Schema 在这里共同注册，调用层不再维护平行映射。
type CapabilityDefinition struct {
	OperationKinds []model.OperationKind
	Worker         WorkerProfile
}

func BuiltinCapabilities() ([]CapabilityDefinition, error) {
	definitions := []CapabilityDefinition{
		{
			OperationKinds: []model.OperationKind{
				model.OperationInitializeProject,
				model.OperationDevelopPlan,
				model.OperationRevisePlan,
				model.OperationReviseCanon,
			},
			Worker: WorkerProfile{
				ID: "architect.design", Version: "1", ModelRole: "architect",
				PromptSlots: []Slot{SlotArchitectStoryDesign, SlotArchitectArcExpand},
				Tools: []ToolSchema{
					tool(ToolAuthorityRead, "读取指定 Revision 的权威故事文档", authorityReadSchema),
					tool(ToolWorkspacePutCandidate, "把结构化候选写入当前 Operation Workspace", `{"type":"object","properties":{"key":{"type":"string"},"content":{"type":"object"}},"required":["key","content"],"additionalProperties":false}`),
					proposalTool(nil),
				},
				InputContract: json.RawMessage(`{"type":"object","required":["intent"]}`), OutputContract: json.RawMessage(`{"type":"object","required":["proposal_id"]}`),
				StopCondition: "已提交结构合法的 Proposal，或返回明确错误",
			},
		},
		{
			OperationKinds: []model.OperationKind{model.OperationWriteChapter},
			Worker: WorkerProfile{
				ID: "writer.compose", Version: "1", ModelRole: "writer",
				PromptSlots:   []Slot{SlotWriterChapterPlan, SlotWriterChapterDraft},
				Tools:         writerTools(),
				InputContract: json.RawMessage(`{"type":"object","required":["chapter_plan_id"]}`), OutputContract: json.RawMessage(`{"type":"object","required":["proposal_id","workspace_key"]}`),
				StopCondition: "章节工作稿通过本地校验并提交 Proposal，或返回明确错误",
			},
		},
		{
			OperationKinds: []model.OperationKind{model.OperationRewriteChapter},
			Worker: WorkerProfile{
				ID: "writer.revise", Version: "1", ModelRole: "writer",
				PromptSlots: []Slot{SlotWriterRewrite}, Tools: writerTools(),
				InputContract: json.RawMessage(`{"type":"object","required":["chapter_id"]}`), OutputContract: json.RawMessage(`{"type":"object","required":["proposal_id","workspace_key"]}`),
				StopCondition: "修订工作稿通过本地校验，并重申报本章全部既有事实（确认、更新或删除）后提交 Proposal，或返回明确错误",
			},
		},
		{
			OperationKinds: []model.OperationKind{model.OperationRewriteAffected},
			Worker: WorkerProfile{
				ID: "writer.revise_affected", Version: "1", ModelRole: "writer",
				PromptSlots: []Slot{SlotWriterRewrite}, Tools: writerRangeTools(),
				InputContract:  json.RawMessage(`{"type":"object","required":["chapter_ids","base_revision","resolution_proposal_id"]}`),
				OutputContract: json.RawMessage(`{"type":"object","required":["proposal_id","workspace_keys"]}`),
				StopCondition:  "所有受影响章节工作稿通过本地校验，并在一个 Proposal 中原子提交正文与各章 Canon Delta，或返回明确错误",
			},
		},
		{
			OperationKinds: []model.OperationKind{model.OperationReviewRange},
			Worker: WorkerProfile{
				ID: "editor.review", Version: "1", ModelRole: "editor",
				PromptSlots: []Slot{SlotEditorStoryReview, SlotEditorStyleReview},
				Tools: []ToolSchema{
					tool(ToolAuthorityRead, "读取指定 Revision 的权威故事文档", authorityReadSchema),
					tool(ToolWorkspacePutReview, "把审阅过程记录写入当前 Operation Workspace。findings 只记真正的问题；"+
						"逐项核验结论（含「已满足」）走 verdict_submit 的 directives / intent，不要写成 note 发现",
						`{"type":"object","properties":{"key":{"type":"string"},`+
							`"findings":{"type":"array","items":`+reviewFindingSchema+`}},`+
							`"required":["key","findings"],"additionalProperties":false}`),
					tool(ToolVerdictSubmit, "提交审阅裁定：引用 workspace_put_review 写好的审阅记录。章节范围与发现由宿主按任务输入和记录填入。"+
						"每个未满足的要求或意图维度，都要在审阅记录里有一条 blocking 发现通过 directive_id / intent 链接到它", verdictSubmitSchema),
				},
				InputContract: json.RawMessage(`{"type":"object","required":["chapter_ids"]}`), OutputContract: json.RawMessage(`{"type":"object","required":["verdict"]}`),
				StopCondition: "已提交覆盖请求范围的结构化裁定（verdict_submit），或返回明确错误",
			},
		},
	}
	if err := validateBuiltinCapabilities(definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

func BuiltinCapability(kind model.OperationKind) (CapabilityDefinition, error) {
	definitions, err := BuiltinCapabilities()
	if err != nil {
		return CapabilityDefinition{}, err
	}
	for _, definition := range definitions {
		for _, candidate := range definition.OperationKinds {
			if candidate == kind {
				return definition, nil
			}
		}
	}
	return CapabilityDefinition{}, fmt.Errorf("operation %s is not backed by a story capability: %w", kind, model.ErrInvalid)
}

func BuiltinWorkerProfile(id string) (WorkerProfile, error) {
	definitions, err := BuiltinCapabilities()
	if err != nil {
		return WorkerProfile{}, err
	}
	for _, definition := range definitions {
		if definition.Worker.ID == id {
			return definition.Worker, nil
		}
	}
	return WorkerProfile{}, fmt.Errorf("worker profile %q does not exist: %w", id, model.ErrInvalid)
}

func validateBuiltinCapabilities(definitions []CapabilityDefinition) error {
	seenKinds := make(map[model.OperationKind]string)
	seenWorkers := make(map[string]struct{})
	for _, definition := range definitions {
		if len(definition.OperationKinds) == 0 {
			return fmt.Errorf("worker profile %q has no operation kinds: %w", definition.Worker.ID, model.ErrInvalid)
		}
		if err := definition.Worker.Validate(); err != nil {
			return err
		}
		if _, exists := seenWorkers[definition.Worker.ID]; exists {
			return fmt.Errorf("duplicate worker profile %q: %w", definition.Worker.ID, model.ErrInvalid)
		}
		seenWorkers[definition.Worker.ID] = struct{}{}
		for _, kind := range definition.OperationKinds {
			if owner, exists := seenKinds[kind]; exists {
				return fmt.Errorf("operation %s is registered by both %s and %s: %w", kind, owner, definition.Worker.ID, model.ErrInvalid)
			}
			seenKinds[kind] = definition.Worker.ID
		}
	}
	return nil
}

func writerRangeTools() []ToolSchema {
	tools := writerTools()
	tools[len(tools)-1] = proposalTool([]string{"workspace_keys"})
	return tools
}

func writerTools() []ToolSchema {
	return []ToolSchema{
		tool(ToolAuthorityRead, "读取指定 Revision 的权威故事文档", authorityReadSchema),
		tool(ToolWorkspaceList, "列出当前 Operation Workspace 的持久化工件和版本", `{"type":"object","properties":{},"additionalProperties":false}`),
		tool(ToolWorkspaceRead, "读取当前 Operation Workspace 的工件", `{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`),
		tool(ToolWorkspacePutChapter, "写入章节工作稿：整篇覆盖，同一章全程用同一个 key", `{"type":"object","properties":{"key":{"type":"string","description":"工作稿键，一章一个，全程不要改"},"chapter":{"type":"object","properties":{"id":{"type":"string"},"plan_node_id":{"type":"string"},"number":{"type":"integer"},"title":{"type":"string"},"author":{"type":"string","enum":["ai"]},"blocks":{"type":"array","minItems":1,"items":{"type":"object","properties":{"id":{"type":"string"},"text":{"type":"string"}},"required":["id","text"],"additionalProperties":false}},"depends_on":{"type":"array","description":"本章依赖的权威文档（实体、既有事实等），照抄读到的形状即可","items":{"type":"object","properties":{"kind":{"type":"string","enum":`+storyDocumentKinds+`},"id":{"type":"string"}},"required":["kind","id"],"additionalProperties":false}}},"required":["id","plan_node_id","number","title","author","blocks"],"additionalProperties":false}},"required":["key","chapter"],"additionalProperties":false}`),
		tool(ToolWorkspaceReplaceBlock, "按稳定 block_id 和版本前提修改一个章节块", `{"type":"object","properties":{"key":{"type":"string"},"block_id":{"type":"string"},"text":{"type":"string"},"expected_version":{"type":"integer","minimum":0,"description":"必须等于该 key 的当前版本（workspace_list 返回）"}},"required":["key","block_id","text","expected_version"],"additionalProperties":false}`),
		proposalTool([]string{"workspace_key"}),
	}
}

// patchesSchema 精确公开 model.Patch 的提交形状。content 按 document.kind 严格
// 解码（多余字段会被拒绝），模型只能从这里学会如何构造合法补丁，必须与 domain
// 结构体的 json 标签逐字段一致。
const patchesSchema = `{
	"type": "array",
	"minItems": 0,
	"description": "文档补丁列表。content 的形状由 document.kind 决定，禁止未列出的字段——plan: {id,kind,parent_id,order,title,summary}，kind 取 volume/arc/chapter/beat，volume 必须省略 parent_id、其余必填，document.id 必须等于 content.id，每个计划节点单独一个 patch，章节目标数按 kind=chapter 的节点个数统计；entity: {id,kind,name,aliases}，kind 取 character/location/item/organization，角色、地点、物品、组织都是实体；manuscript: {id,plan_node_id,number,title,author,blocks:[{id,text}]}，author 固定为 ai；canon: {id,kind,subject_id,predicate,new_value,old_value,source_chapter_id,effective_chapter_id}，subject_id 必须是已存在或同一 Proposal 中新建的 entity 的 id，kind 取 event/state/relationship/world_rule/foreshadow，predicate 必须落在对应受控前缀（event./state./relation./rule./foreshadow.）内，更新已有事实沿用原 id 并省略 old_value，宿主从冻结任务基线补齐；显式提供的 old_value 仍须逐字精确匹配，包括标点。不得为同一事实另起新 id，新事实不得带 old_value；随正文提交时 source_chapter_id 必须是本次提交的正文章节，重写章节必须重申报该章全部既有事实（不变的事实通过顶层 confirm_canon 按 ID 确认，其余更新或删除），event 类事实跨章只追加、不得改动其他章的事件；effective_chapter_id 是状态类事实在故事中的生效章节，只在插叙/回忆时填写、缺省等于来源章，状态的生效位置不得早于现值，倒叙内容记为 event；intent: 完整 Intent 文档。",
	"items": {
		"type": "object",
		"properties": {
			"document": {
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ` + storyDocumentKinds + `},
					"id": {"type": "string"}
				},
				"required": ["kind", "id"],
				"additionalProperties": false
			},
			"operation": {"type": "string", "enum": ["put", "delete"]},
			"content": {"type": "object", "description": "operation=put 时必填，形状见 patches 描述"}
		},
		"required": ["document", "operation"],
		"additionalProperties": false
	}
}`

func proposalTool(extraRequired []string) ToolSchema {
	required := append([]string{"reason", "patches"}, extraRequired...)
	properties := map[string]any{
		"reason":        map[string]any{"type": "string"},
		"patches":       json.RawMessage(patchesSchema),
		"confirm_canon": map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "uniqueItems": true, "description": "明确确认不变的既有事实 ID；宿主从任务基线复制原值，与 patches 中的事实不得重复。"},
	}
	description := `把最终候选提交为 Proposal，不直接修改权威状态。规划类提交每个计划节点一个 plan patch。更新既有 canon 时省略 old_value，由宿主从冻结任务基线补齐；若显式提供则必须精确匹配（包括标点）。未改变的事实用 confirm_canon:["事实ID"] 确认，宿主复制基线原文，无需重抄；不要同时在 patches 重复这些事实。`
	for _, field := range extraRequired {
		switch field {
		case "workspace_key":
			properties[field] = map[string]any{"type": "string", "minLength": 1}
			properties["workspace_version"] = map[string]any{"type": "integer", "minimum": 1}
			description += ` 单章提交优先使用 workspace_key 与 workspace_version（采用 workspace_put_chapter/workspace_read 返回的 key 和 version），宿主自动装配该版本正文。patches 仅提供 Canon Delta、实体等附带变更，不重复输出 manuscript 正文。例：{"reason":"完成章节","workspace_key":"chapter-1-draft","workspace_version":1,"patches":[...]}。不传版本时为旧式完整正文提交，正文必须与工作稿完全一致。`
		case "workspace_keys":
			properties[field] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "minItems": 1, "uniqueItems": true}
			properties["workspace_versions"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer", "minimum": 1}}
			description += ` 多章提交优先提供 workspace_keys 与 workspace_versions（key 到 version 的映射，必须覆盖每个 key）；宿主自动装配正文，patches 只提供 Canon Delta、实体等附带变更。不传版本映射时为旧式完整正文提交。`
		}
	}
	schema, err := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	})
	if err != nil {
		panic(err)
	}
	return tool(ToolProposalSubmit, description, string(schema))
}

func tool(name, description, schema string) ToolSchema {
	return ToolSchema{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}
