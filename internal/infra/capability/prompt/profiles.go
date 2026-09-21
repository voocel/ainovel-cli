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
					tool(ToolAuthorityRead, "读取指定 Revision 的权威故事文档", `{"type":"object","properties":{"kind":{"type":"string"},"id":{"type":"string"},"revision":{"type":"integer"}},"required":["kind","id","revision"],"additionalProperties":false}`),
					tool(ToolWorkspacePutCandidate, "把结构化候选写入当前 Operation Workspace", `{"type":"object","properties":{"key":{"type":"string"},"expected_version":{"type":"integer"},"content":{"type":"object"}},"required":["key","expected_version","content"],"additionalProperties":false}`),
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
					tool(ToolAuthorityRead, "读取指定 Revision 的权威故事文档", `{"type":"object","properties":{"kind":{"type":"string"},"id":{"type":"string"},"revision":{"type":"integer"}},"required":["kind","id","revision"],"additionalProperties":false}`),
					tool(ToolWorkspacePutReview, "把审阅过程记录写入当前 Operation Workspace", `{"type":"object","properties":{"key":{"type":"string"},"expected_version":{"type":"integer"},"findings":{"type":"array","items":{"type":"object"}}},"required":["key","expected_version","findings"],"additionalProperties":false}`),
					tool(ToolVerdictSubmit, "以 review_key + review_version 引用已保存的审阅记录，不要重复传 findings；提交覆盖请求范围的结构化裁定（D30）：pass 表示无阻塞发现；任务要求核验 Intent（verify_intent）时 pass 必须附逐项意图核验声明，意图未满足应给出阻塞发现；任务输入携带 directives 时必须逐条声明核验结果（directives）；每个未满足的要求或意图维度必须至少有一条阻塞发现通过 directive_id / intent 链接到它，用户据此裁决；blocked 必须给出阻塞发现", `{"type":"object","properties":{"status":{"type":"string","enum":["pass","blocked"]},"chapter_ids":{"type":"array","items":{"type":"string"},"minItems":1},"review_key":{"type":"string","minLength":1},"review_version":{"type":"integer","minimum":1},"intent":{"type":"object","properties":{"required_present":{"type":"boolean"},"forbidden_absent":{"type":"boolean"},"ending_consistent":{"type":"boolean"}},"required":["required_present","forbidden_absent","ending_consistent"],"additionalProperties":false},"directives":{"type":"array","items":{"type":"object","properties":{"directive_id":{"type":"string"},"satisfied":{"type":"boolean"},"note":{"type":"string"}},"required":["directive_id","satisfied"],"additionalProperties":false}},"findings":{"type":"array","items":{"type":"object","properties":{"chapter_id":{"type":"string"},"severity":{"type":"string","enum":["blocking","note"]},"note":{"type":"string"},"directive_id":{"type":"string"},"intent":{"type":"string","enum":["required_present","forbidden_absent","ending_consistent"]}},"required":["chapter_id","severity","note"],"additionalProperties":false}}},"required":["status","chapter_ids","review_key","review_version"],"additionalProperties":false}`),
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
		tool(ToolAuthorityRead, "读取指定 Revision 的权威故事文档", `{"type":"object","properties":{"kind":{"type":"string"},"id":{"type":"string"},"revision":{"type":"integer"}},"required":["kind","id","revision"],"additionalProperties":false}`),
		tool(ToolWorkspaceList, "列出当前 Operation Workspace 的持久化工件和版本", `{"type":"object","properties":{},"additionalProperties":false}`),
		tool(ToolWorkspaceRead, "读取当前 Operation Workspace 的工件", `{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`),
		tool(ToolWorkspacePutChapter, "以版本前提写入章节工作稿", `{"type":"object","properties":{"key":{"type":"string"},"expected_version":{"type":"integer"},"chapter":{"type":"object","properties":{"id":{"type":"string"},"plan_node_id":{"type":"string"},"number":{"type":"integer"},"title":{"type":"string"},"author":{"type":"string","enum":["ai"]},"blocks":{"type":"array","minItems":1,"items":{"type":"object","properties":{"id":{"type":"string"},"text":{"type":"string"}},"required":["id","text"],"additionalProperties":false}}},"required":["id","plan_node_id","number","title","author","blocks"],"additionalProperties":false}},"required":["key","expected_version","chapter"],"additionalProperties":false}`),
		tool(ToolWorkspaceReplaceBlock, "按稳定 block_id 和版本前提修改一个章节块", `{"type":"object","properties":{"key":{"type":"string"},"block_id":{"type":"string"},"text":{"type":"string"},"expected_version":{"type":"integer"}},"required":["key","block_id","text","expected_version"],"additionalProperties":false}`),
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
					"kind": {"type": "string", "enum": ["intent", "plan", "entity", "canon", "manuscript"]},
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
