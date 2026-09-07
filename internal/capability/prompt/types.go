package prompt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
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

func (p WorkerProfile) Validate() error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Version) == "" ||
		strings.TrimSpace(p.ModelRole) == "" || strings.TrimSpace(p.StopCondition) == "" {
		return fmt.Errorf("worker profile identity, role and stop condition are required: %w", domain.ErrInvalid)
	}
	if len(p.PromptSlots) == 0 || len(p.Tools) == 0 {
		return fmt.Errorf("worker profile requires prompt slots and tools: %w", domain.ErrInvalid)
	}
	if len(p.InputContract) == 0 || !json.Valid(p.InputContract) || len(p.OutputContract) == 0 || !json.Valid(p.OutputContract) {
		return fmt.Errorf("worker profile contracts must be valid JSON: %w", domain.ErrInvalid)
	}
	seenSlots := make(map[Slot]struct{}, len(p.PromptSlots))
	for _, slot := range p.PromptSlots {
		if !validSlot(slot) {
			return fmt.Errorf("unknown prompt slot %q: %w", slot, domain.ErrInvalid)
		}
		if _, ok := seenSlots[slot]; ok {
			return fmt.Errorf("duplicate prompt slot %q: %w", slot, domain.ErrInvalid)
		}
		seenSlots[slot] = struct{}{}
	}
	seenTools := make(map[string]struct{}, len(p.Tools))
	for _, tool := range p.Tools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Description) == "" ||
			len(tool.InputSchema) == 0 || !json.Valid(tool.InputSchema) {
			return fmt.Errorf("tool schema is invalid: %w", domain.ErrInvalid)
		}
		if _, ok := seenTools[tool.Name]; ok {
			return fmt.Errorf("duplicate tool %q: %w", tool.Name, domain.ErrInvalid)
		}
		seenTools[tool.Name] = struct{}{}
	}
	return nil
}

type VersionedPack struct {
	Revision domain.Revision     `json:"revision"`
	Manifest domain.PackManifest `json:"manifest"`
}

type VersionedCreatorProfile struct {
	Revision domain.Revision       `json:"revision"`
	Profile  domain.CreatorProfile `json:"profile"`
}

type CompileRequest struct {
	ProjectID              string
	CoreProtocolVersion    string
	Worker                 WorkerProfile
	Packs                  []VersionedPack
	CreatorProfiles        []VersionedCreatorProfile
	Intent                 domain.Intent
	Ownership              []domain.OwnershipRule
	OverlayRules           []string
	StoryContext           json.RawMessage
	Task                   json.RawMessage
	BaseRevision           domain.Revision
	ProjectOverlayRevision domain.Revision
	ModelConfigDigest      string
	ApprovalPolicy         domain.ApprovalPolicy
	ApprovalPolicyDigest   string
}

type SourceKind string

const (
	SourceInstruction SourceKind = "instruction"
	SourceData        SourceKind = "data"
)

type Source struct {
	Layer    string          `json:"layer"`
	ID       string          `json:"id"`
	Revision domain.Revision `json:"revision,omitempty"`
	Kind     SourceKind      `json:"kind"`
	Slots    []Slot          `json:"slots,omitempty"`
}

type Compiled struct {
	StablePrefix     string                   `json:"stable_prefix"`
	DynamicTail      string                   `json:"dynamic_tail"`
	Tools            []ToolSchema             `json:"ordered_tool_schemas"`
	Sources          []Source                 `json:"sources"`
	PromptDigest     string                   `json:"prompt_digest"`
	ToolSchemaDigest string                   `json:"tool_schema_digest"`
	ProfileDigest    string                   `json:"execution_profile_digest"`
	Snapshot         domain.ExecutionSnapshot `json:"execution_snapshot"`
}

func validSlot(slot Slot) bool {
	switch slot {
	case SlotArchitectStoryDesign, SlotArchitectArcExpand, SlotWriterChapterPlan,
		SlotWriterChapterDraft, SlotWriterRewrite, SlotEditorStoryReview, SlotEditorStyleReview:
		return true
	default:
		return false
	}
}
