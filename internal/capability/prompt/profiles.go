package prompt

import (
	"encoding/json"
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
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
	OperationKinds []domain.OperationKind
	Worker         WorkerProfile
}

func BuiltinCapabilities() ([]CapabilityDefinition, error) {
	definitions := []CapabilityDefinition{
		{
			OperationKinds: []domain.OperationKind{
				domain.OperationInitializeProject,
				domain.OperationDevelopPlan,
				domain.OperationRevisePlan,
				domain.OperationReviseCanon,
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
			OperationKinds: []domain.OperationKind{domain.OperationWriteChapter},
			Worker: WorkerProfile{
				ID: "writer.compose", Version: "1", ModelRole: "writer",
				PromptSlots:   []Slot{SlotWriterChapterPlan, SlotWriterChapterDraft},
				Tools:         writerTools(),
				InputContract: json.RawMessage(`{"type":"object","required":["chapter_plan_id"]}`), OutputContract: json.RawMessage(`{"type":"object","required":["proposal_id","workspace_key"]}`),
				StopCondition: "章节工作稿通过本地校验并提交 Proposal，或返回明确错误",
			},
		},
		{
			OperationKinds: []domain.OperationKind{domain.OperationRewriteChapter},
			Worker: WorkerProfile{
				ID: "writer.revise", Version: "1", ModelRole: "writer",
				PromptSlots: []Slot{SlotWriterRewrite}, Tools: writerTools(),
				InputContract: json.RawMessage(`{"type":"object","required":["chapter_id","base_revision"]}`), OutputContract: json.RawMessage(`{"type":"object","required":["proposal_id","workspace_key"]}`),
				StopCondition: "修订工作稿通过本地校验并提交 Proposal，或返回明确错误",
			},
		},
		{
			OperationKinds: []domain.OperationKind{domain.OperationRewriteAffected},
			Worker: WorkerProfile{
				ID: "writer.revise_affected", Version: "1", ModelRole: "writer",
				PromptSlots: []Slot{SlotWriterRewrite}, Tools: writerRangeTools(),
				InputContract:  json.RawMessage(`{"type":"object","required":["chapter_ids","base_revision","resolution_proposal_id"]}`),
				OutputContract: json.RawMessage(`{"type":"object","required":["proposal_id","workspace_keys"]}`),
				StopCondition:  "所有受影响章节工作稿通过本地校验，并在一个 Proposal 中原子提交正文与各章 Canon Delta，或返回明确错误",
			},
		},
		{
			OperationKinds: []domain.OperationKind{domain.OperationReviewRange},
			Worker: WorkerProfile{
				ID: "editor.review", Version: "1", ModelRole: "editor",
				PromptSlots: []Slot{SlotEditorStoryReview, SlotEditorStyleReview},
				Tools: []ToolSchema{
					tool(ToolAuthorityRead, "读取指定 Revision 的权威故事文档", `{"type":"object","properties":{"kind":{"type":"string"},"id":{"type":"string"},"revision":{"type":"integer"}},"required":["kind","id","revision"],"additionalProperties":false}`),
					tool(ToolWorkspacePutReview, "把审阅过程记录写入当前 Operation Workspace", `{"type":"object","properties":{"key":{"type":"string"},"expected_version":{"type":"integer"},"findings":{"type":"array","items":{"type":"object"}}},"required":["key","expected_version","findings"],"additionalProperties":false}`),
					tool(ToolVerdictSubmit, "引用审阅工作区记录并提交覆盖请求范围的结构化裁定（D30）：pass 表示无阻塞发现；任务要求核验 Intent（verify_intent）时 pass 必须附逐项意图核验声明，意图未满足应给出阻塞发现；任务输入携带 directives 时必须逐条声明核验结果（directives），未满足应给出阻塞发现；blocked 必须给出阻塞发现", `{"type":"object","properties":{"status":{"type":"string","enum":["pass","blocked"]},"chapter_ids":{"type":"array","items":{"type":"string"},"minItems":1},"review_key":{"type":"string","minLength":1},"intent":{"type":"object","properties":{"required_present":{"type":"boolean"},"forbidden_absent":{"type":"boolean"},"ending_consistent":{"type":"boolean"}},"required":["required_present","forbidden_absent","ending_consistent"],"additionalProperties":false},"directives":{"type":"array","items":{"type":"object","properties":{"directive_id":{"type":"string"},"satisfied":{"type":"boolean"},"note":{"type":"string"}},"required":["directive_id","satisfied"],"additionalProperties":false}},"findings":{"type":"array","items":{"type":"object","properties":{"chapter_id":{"type":"string"},"severity":{"type":"string","enum":["blocking","note"]},"note":{"type":"string"}},"required":["chapter_id","severity","note"],"additionalProperties":false}}},"required":["status","chapter_ids","review_key","findings"],"additionalProperties":false}`),
				},
				InputContract: json.RawMessage(`{"type":"object","required":["range"]}`), OutputContract: json.RawMessage(`{"type":"object","required":["verdict"]}`),
				StopCondition: "已提交覆盖请求范围的结构化裁定（verdict_submit），或返回明确错误",
			},
		},
	}
	if err := validateBuiltinCapabilities(definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

func BuiltinCapability(kind domain.OperationKind) (CapabilityDefinition, error) {
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
	return CapabilityDefinition{}, fmt.Errorf("operation %s is not backed by a story capability: %w", kind, domain.ErrInvalid)
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
	return WorkerProfile{}, fmt.Errorf("worker profile %q does not exist: %w", id, domain.ErrInvalid)
}

func validateBuiltinCapabilities(definitions []CapabilityDefinition) error {
	seenKinds := make(map[domain.OperationKind]string)
	seenWorkers := make(map[string]struct{})
	for _, definition := range definitions {
		if len(definition.OperationKinds) == 0 {
			return fmt.Errorf("worker profile %q has no operation kinds: %w", definition.Worker.ID, domain.ErrInvalid)
		}
		if err := definition.Worker.Validate(); err != nil {
			return err
		}
		if _, exists := seenWorkers[definition.Worker.ID]; exists {
			return fmt.Errorf("duplicate worker profile %q: %w", definition.Worker.ID, domain.ErrInvalid)
		}
		seenWorkers[definition.Worker.ID] = struct{}{}
		for _, kind := range definition.OperationKinds {
			if owner, exists := seenKinds[kind]; exists {
				return fmt.Errorf("operation %s is registered by both %s and %s: %w", kind, owner, definition.Worker.ID, domain.ErrInvalid)
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
		tool(ToolWorkspacePutChapter, "以版本前提写入章节工作稿", `{"type":"object","properties":{"key":{"type":"string"},"expected_version":{"type":"integer"},"chapter":{"type":"object","properties":{"id":{"type":"string"},"plan_node_id":{"type":"string"},"number":{"type":"integer"},"title":{"type":"string"},"blocks":{"type":"array","minItems":1,"items":{"type":"object","properties":{"id":{"type":"string"},"text":{"type":"string"}},"required":["id","text"],"additionalProperties":false}}},"required":["id","plan_node_id","number","title","blocks"],"additionalProperties":false}},"required":["key","expected_version","chapter"],"additionalProperties":false}`),
		tool(ToolWorkspaceReplaceBlock, "按稳定 block_id 和版本前提修改一个章节块", `{"type":"object","properties":{"key":{"type":"string"},"block_id":{"type":"string"},"text":{"type":"string"},"expected_version":{"type":"integer"}},"required":["key","block_id","text","expected_version"],"additionalProperties":false}`),
		proposalTool([]string{"workspace_key"}),
	}
}

// patchesSchema 精确公开 domain.Patch 的提交形状。content 按 document.kind 严格
// 解码（多余字段会被拒绝），模型只能从这里学会如何构造合法补丁，必须与 domain
// 结构体的 json 标签逐字段一致。
const patchesSchema = `{
	"type": "array",
	"minItems": 1,
	"description": "文档补丁列表。content 的形状由 document.kind 决定，禁止未列出的字段——plan: {id,kind,parent_id,order,title,summary}，kind 取 volume/arc/chapter/beat，volume 必须省略 parent_id、其余必填，document.id 必须等于 content.id，每个计划节点单独一个 patch，章节目标数按 kind=chapter 的节点个数统计；manuscript: {id,plan_node_id,number,title,blocks:[{id,text}]}；canon: {id,kind,subject_id,predicate,new_value,old_value,source_chapter_id}，kind 取 event/state/relationship/world_rule/foreshadow，predicate 必须落在对应受控前缀（event./state./relation./rule./foreshadow.）内，old_value 仅在更新已有事实时必填且须与上一版本一致；intent: 完整 Intent 文档。",
	"items": {
		"type": "object",
		"properties": {
			"document": {
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["intent", "plan", "canon", "manuscript"]},
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
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason":        map[string]any{"type": "string"},
			"patches":       json.RawMessage(patchesSchema),
			"workspace_key": map[string]any{"type": "string"},
			"workspace_keys": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1,
			},
			"review_key": map[string]any{"type": "string"},
		},
		"required":             required,
		"additionalProperties": false,
	})
	if err != nil {
		panic(err)
	}
	return tool(ToolProposalSubmit, `把最终候选提交为 Proposal，不直接修改权威状态。规划类提交＝每个计划节点一个 plan patch，例：{"document":{"kind":"plan","id":"chapter-1"},"operation":"put","content":{"id":"chapter-1","kind":"chapter","parent_id":"arc-1","order":1,"title":"章节标题","summary":"章节概要"}}`, string(schema))
}

func tool(name, description, schema string) ToolSchema {
	return ToolSchema{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}
