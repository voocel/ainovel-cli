package prompt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type Slot string

const (
	SlotArchitectStoryDesign Slot = "architect.story_design"
	SlotArchitectArcExpand   Slot = "architect.arc_expand"
	SlotWriterChapterPlan    Slot = "writer.chapter_plan"
	SlotWriterChapterDraft   Slot = "writer.chapter_draft"
	SlotWriterRewrite        Slot = "writer.rewrite"
	SlotEditorStoryReview    Slot = "editor.story_review"
	SlotEditorStyleReview    Slot = "editor.style_review"
)

type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type WorkerProfile struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	PromptSlots    []Slot          `json:"prompt_slots"`
	Tools          []ToolSchema    `json:"ordered_tool_schemas"`
	ModelRole      string          `json:"model_role"`
	InputContract  json.RawMessage `json:"input_contract"`
	OutputContract json.RawMessage `json:"output_contract"`
	StopCondition  string          `json:"stop_condition"`
}

// ModelRoles 是 worker profile 可声明的模型角色；配置的角色映射按这些键覆盖默认模型。
var ModelRoles = []string{"architect", "writer", "editor"}

func KnownModelRole(role string) bool {
	for _, known := range ModelRoles {
		if known == role {
			return true
		}
	}
	return false
}

func (p WorkerProfile) Validate() error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Version) == "" || strings.TrimSpace(p.StopCondition) == "" {
		return fmt.Errorf("worker profile identity and stop condition are required: %w", model.ErrInvalid)
	}
	if !KnownModelRole(p.ModelRole) {
		return fmt.Errorf("unknown model role %q: %w", p.ModelRole, model.ErrInvalid)
	}
	if len(p.PromptSlots) == 0 || len(p.Tools) == 0 {
		return fmt.Errorf("worker profile requires prompt slots and tools: %w", model.ErrInvalid)
	}
	if len(p.InputContract) == 0 || !json.Valid(p.InputContract) || len(p.OutputContract) == 0 || !json.Valid(p.OutputContract) {
		return fmt.Errorf("worker profile contracts must be valid JSON: %w", model.ErrInvalid)
	}
	seenSlots := make(map[Slot]struct{}, len(p.PromptSlots))
	for _, slot := range p.PromptSlots {
		if !validSlot(slot) {
			return fmt.Errorf("unknown prompt slot %q: %w", slot, model.ErrInvalid)
		}
		if _, ok := seenSlots[slot]; ok {
			return fmt.Errorf("duplicate prompt slot %q: %w", slot, model.ErrInvalid)
		}
		seenSlots[slot] = struct{}{}
	}
	seenTools := make(map[string]struct{}, len(p.Tools))
	for _, tool := range p.Tools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Description) == "" ||
			len(tool.InputSchema) == 0 || !json.Valid(tool.InputSchema) {
			return fmt.Errorf("tool schema is invalid: %w", model.ErrInvalid)
		}
		if _, ok := seenTools[tool.Name]; ok {
			return fmt.Errorf("duplicate tool %q: %w", tool.Name, model.ErrInvalid)
		}
		seenTools[tool.Name] = struct{}{}
	}
	return nil
}

type VersionedPack struct {
	Revision model.Revision     `json:"revision"`
	Manifest model.PackManifest `json:"manifest"`
}

type VersionedCreatorProfile struct {
	Revision model.Revision       `json:"revision"`
	Profile  model.CreatorProfile `json:"profile"`
}

type CompileRequest struct {
	ProjectID              string
	CoreProtocolVersion    string
	Worker                 WorkerProfile
	Packs                  []VersionedPack
	CreatorProfiles        []VersionedCreatorProfile
	Intent                 model.Intent
	Ownership              []model.OwnershipRule
	OverlayRules           []string
	StoryContext           json.RawMessage
	Task                   json.RawMessage
	BaseRevision           model.Revision
	ProjectOverlayRevision model.Revision
}

type SourceKind string

const (
	SourceInstruction SourceKind = "instruction"
	SourceData        SourceKind = "data"
)

type Source struct {
	Layer    string         `json:"layer"`
	ID       string         `json:"id"`
	Revision model.Revision `json:"revision,omitempty"`
	Kind     SourceKind     `json:"kind"`
	Slots    []Slot         `json:"slots,omitempty"`
}

// Compiled 是编译好的 Execution Profile：ProfileDigest 是其持久化记录的内容
// 身份，Operation 以 Snapshot.ConfigDigest 引用它。
type Compiled struct {
	ProjectID           string       `json:"project_id"`
	WorkerProfile       string       `json:"worker_profile"`
	CoreProtocolVersion string       `json:"core_protocol_version"`
	StablePrefix        string       `json:"stable_prefix"`
	DynamicTail         string       `json:"dynamic_tail"`
	Tools               []ToolSchema `json:"ordered_tool_schemas"`
	Sources             []Source     `json:"sources"`
	PromptDigest        string       `json:"prompt_digest"`
	ToolSchemaDigest    string       `json:"tool_schema_digest"`
	ProfileDigest       string       `json:"execution_profile_digest"`
}

// record 是 Compiled 的持久化形态；Digest 由记录内容推导，与 ProfileDigest 一致。
func (c Compiled) record(createdAt time.Time) (model.ExecutionProfileRecord, error) {
	tools, err := json.Marshal(c.Tools)
	if err != nil {
		return model.ExecutionProfileRecord{}, fmt.Errorf("encode compiled tools: %w", err)
	}
	sources, err := json.Marshal(c.Sources)
	if err != nil {
		return model.ExecutionProfileRecord{}, fmt.Errorf("encode prompt sources: %w", err)
	}
	record := model.ExecutionProfileRecord{
		ProjectID: c.ProjectID, WorkerProfile: c.WorkerProfile,
		CoreProtocolVersion: c.CoreProtocolVersion,
		PromptDigest:        c.PromptDigest, ToolSchemaDigest: c.ToolSchemaDigest,
		StablePrefix: c.StablePrefix, DynamicTail: c.DynamicTail,
		Tools: tools, Sources: sources, CreatedAt: createdAt,
	}
	if record.Digest, err = record.Identity(); err != nil {
		return model.ExecutionProfileRecord{}, fmt.Errorf("digest execution profile: %w", err)
	}
	return record, nil
}

// ExecutorIdentity 是 LLM 执行族的身份（D45）：agent 循环的族与版本。模型不在
// 身份里——它是运行时绑定（D57），每次尝试以 agent.run_started 记录实际使用的模型。
const ExecutorIdentity = "llm.agent@1"

func validSlot(slot Slot) bool {
	switch slot {
	case SlotArchitectStoryDesign, SlotArchitectArcExpand, SlotWriterChapterPlan,
		SlotWriterChapterDraft, SlotWriterRewrite, SlotEditorStoryReview, SlotEditorStyleReview:
		return true
	default:
		return false
	}
}
