package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func (r *Runtime) AnalyzePreference(ctx context.Context, input domain.PreferenceLearningInput) (domain.PreferenceCandidate, error) {
	if r.model == nil {
		return domain.PreferenceCandidate{}, fmt.Errorf("preference analysis model is required: %w", domain.ErrInvalid)
	}
	if err := input.Validate(); err != nil {
		return domain.PreferenceCandidate{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return domain.PreferenceCandidate{}, fmt.Errorf("encode preference learning input: %w", err)
	}
	cacheKey, err := prompt.CacheKey(input.ProjectID, "preference.learn@1", domain.Digest(payload), input.CandidateID)
	if err != nil {
		return domain.PreferenceCandidate{}, err
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":        map[string]any{"type": "string"},
			"evidence":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"proposed_rules": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"proposed_style_preferences": map[string]any{
				"type": "object", "additionalProperties": map[string]any{"type": "string"},
			},
		},
		"required":             []string{"summary", "evidence", "proposed_rules", "proposed_style_preferences"},
		"additionalProperties": false,
	}
	response, err := r.model.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg("你是写作偏好分析器。只从用户实际 before/after 修改中归纳可复用偏好；证据不足就明确报错，不得猜测人格或题材偏好。规则要短、可执行，并引用具体修改证据。"),
		agentcore.UserMsg(string(payload)),
	}, nil,
		agentcore.WithJSONSchema("creator_preference_candidate", "从用户正文修改中提出可确认的写作偏好", schema, true),
		agentcore.WithCallPromptCacheKey(cacheKey),
		agentcore.WithCallSessionID(input.CandidateID),
	)
	if err != nil {
		return domain.PreferenceCandidate{}, err
	}
	if response == nil {
		return domain.PreferenceCandidate{}, fmt.Errorf("preference analysis model returned no response")
	}
	var candidate domain.PreferenceCandidate
	if err := decodeToolArgs(json.RawMessage(response.Message.TextContent()), &candidate); err != nil {
		return domain.PreferenceCandidate{}, fmt.Errorf("decode preference candidate: %w", err)
	}
	candidate.ID = input.CandidateID
	candidate.SourceProjectID = input.ProjectID
	candidate.SourceRevision = input.ToRevision
	if err := candidate.Validate(); err != nil {
		return domain.PreferenceCandidate{}, err
	}
	return candidate, nil
}

func (r *Runtime) Analyze(
	ctx context.Context,
	proposal domain.Proposal,
	structural change.StructuralImpact,
) (json.RawMessage, error) {
	if r.model == nil {
		return nil, fmt.Errorf("semantic impact model is required: %w", domain.ErrInvalid)
	}
	type authorityDocument struct {
		Ref     domain.DocumentRef `json:"ref"`
		Content json.RawMessage    `json:"content"`
	}
	documents := make([]authorityDocument, 0, len(structural.Direct)+len(structural.Affected))
	refs := append(append([]domain.DocumentRef(nil), structural.Direct...), structural.Affected...)
	for _, ref := range refs {
		document, err := r.store.GetDocument(ctx, proposal.Target, ref, proposal.BaseRevision)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		documents = append(documents, authorityDocument{Ref: ref, Content: document.Content})
	}
	input, err := json.Marshal(struct {
		Proposal   domain.Proposal         `json:"proposal"`
		Structural change.StructuralImpact `json:"structural_impact"`
		Documents  []authorityDocument     `json:"base_documents"`
	}{Proposal: proposal, Structural: structural, Documents: documents})
	if err != nil {
		return nil, fmt.Errorf("encode semantic impact input: %w", err)
	}
	cacheKey, err := prompt.CacheKey(proposal.Target.ID, "semantic.impact@1", domain.Digest(input), proposal.ID)
	if err != nil {
		return nil, err
	}
	documentSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"kind": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}},
		"required":   []string{"kind", "id"}, "additionalProperties": false,
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []string{"consistent", "conflict", "uncertain"}},
			"findings": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"document": documentSchema, "explanation": map[string]any{"type": "string"},
				}, "required": []string{"explanation"}, "additionalProperties": false,
			}},
			"options": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"strategy":    map[string]any{"type": "string", "enum": []string{"rewrite_affected", "reinterpret_future", "abandon"}},
					"chapter_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"explanation": map[string]any{"type": "string"},
				}, "required": []string{"strategy", "explanation"}, "additionalProperties": false,
			}},
		},
		"required": []string{"status", "findings", "options"}, "additionalProperties": false,
	}
	response, err := r.model.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg("你是小说变更影响分析器。判断候选变更与已经发生的故事事实、人物动机和因果链是否冲突。consistent 时 findings/options 必须为空；conflict 或 uncertain 时必须给出 rewrite_affected、reinterpret_future、abandon 三种明确选项。不得替用户作决定。"),
		agentcore.UserMsg(string(input)),
	}, nil,
		agentcore.WithJSONSchema("story_semantic_impact", "分析故事变更的语义影响和可选处理方案", schema, true),
		agentcore.WithCallPromptCacheKey(cacheKey),
		agentcore.WithCallSessionID(proposal.ID+":semantic-impact"),
	)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("semantic impact model returned no response")
	}
	var report change.SemanticImpactReport
	if err := decodeToolArgs(json.RawMessage(response.Message.TextContent()), &report); err != nil {
		return nil, fmt.Errorf("decode semantic impact report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(report)
}

func (r *Runtime) AnalyzeSemanticCompliance(
	ctx context.Context,
	operation domain.Operation,
	proposal domain.Proposal,
	constraints []domain.OwnershipRule,
) (domain.SemanticComplianceReport, error) {
	if r.model == nil {
		return domain.SemanticComplianceReport{}, fmt.Errorf("semantic compliance model is required: %w", domain.ErrInvalid)
	}
	if operation.State != domain.OperationRunning || operation.Snapshot.ModelConfigDigest != r.modelDigest {
		return domain.SemanticComplianceReport{}, fmt.Errorf("semantic compliance operation context is invalid: %w", store.ErrStateConflict)
	}
	type constraintDocument struct {
		Rule    domain.OwnershipRule `json:"rule"`
		Content json.RawMessage      `json:"authoritative_content"`
	}
	constraintDocuments := make([]constraintDocument, 0, len(constraints))
	for _, rule := range constraints {
		document, err := r.store.GetDocument(ctx, operation.Target, rule.Target, operation.Snapshot.BaseRevision)
		if err != nil {
			return domain.SemanticComplianceReport{}, err
		}
		constraintDocuments = append(constraintDocuments, constraintDocument{
			Rule: rule, Content: append(json.RawMessage(nil), document.Content...),
		})
	}
	input, err := json.Marshal(struct {
		Constraints []constraintDocument `json:"constraints"`
		Patches     []domain.Patch       `json:"candidate_patches"`
	}{Constraints: constraintDocuments, Patches: proposal.Patches})
	if err != nil {
		return domain.SemanticComplianceReport{}, fmt.Errorf("encode semantic compliance input: %w", err)
	}
	cacheKey, err := prompt.CacheKey(
		operation.Target.ID, "semantic.compliance@1", operation.Snapshot.ExecutionProfileDigest,
		operation.ID+":semantic-compliance",
	)
	if err != nil {
		return domain.SemanticComplianceReport{}, err
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []string{"pass", "conflict", "uncertain"}},
			"findings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"constraint": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"kind": map[string]any{"type": "string"},
								"id":   map[string]any{"type": "string"},
							},
							"required": []string{"kind", "id"}, "additionalProperties": false,
						},
						"explanation": map[string]any{"type": "string"},
					},
					"required": []string{"constraint", "explanation"}, "additionalProperties": false,
				},
			},
		},
		"required": []string{"status", "findings"}, "additionalProperties": false,
	}
	response, err := r.model.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg("你是独立的小说事实合规检查器。只判断候选正文是否违背用户 locked/guided 约束；不得改写正文。证据不足必须返回 uncertain。pass 时 findings 必须为空。"),
		agentcore.UserMsg(string(input)),
	}, nil,
		agentcore.WithJSONSchema("story_semantic_compliance", "检查候选正文是否符合用户故事约束", schema, true),
		agentcore.WithCallPromptCacheKey(cacheKey),
		agentcore.WithCallSessionID(operation.ID+":semantic-compliance"),
	)
	if err != nil {
		return domain.SemanticComplianceReport{}, r.recordSemanticFailure(ctx, operation, err)
	}
	if response == nil {
		err := fmt.Errorf("semantic compliance model returned no response")
		return domain.SemanticComplianceReport{}, r.recordSemanticFailure(ctx, operation, err)
	}
	var report domain.SemanticComplianceReport
	if err := domain.DecodeStrict([]byte(response.Message.TextContent()), &report); err != nil {
		return domain.SemanticComplianceReport{}, r.recordSemanticFailure(
			ctx, operation, fmt.Errorf("decode semantic compliance response: %w", err),
		)
	}
	if err := report.Validate(); err != nil {
		return domain.SemanticComplianceReport{}, r.recordSemanticFailure(ctx, operation, err)
	}
	eventPayload, err := json.Marshal(struct {
		Report domain.SemanticComplianceReport `json:"report"`
		Usage  *agentcore.Usage                `json:"usage,omitempty"`
	}{Report: report, Usage: response.Message.Usage})
	if err != nil {
		return domain.SemanticComplianceReport{}, fmt.Errorf("encode semantic compliance event: %w", err)
	}
	if _, err := r.store.AppendOperationEvent(ctx, domain.OperationEvent{
		OperationID: operation.ID, StepID: "semantic.compliance", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("semantic-compliance:%d", operation.Attempt),
		Kind:           "semantic.compliance_checked", Payload: eventPayload, CreatedAt: r.now(),
	}); err != nil {
		return domain.SemanticComplianceReport{}, err
	}
	return report, nil
}

func (r *Runtime) recordSemanticFailure(ctx context.Context, operation domain.Operation, cause error) error {
	payload, err := json.Marshal(map[string]string{"error": cause.Error()})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("encode semantic compliance failure: %w", err))
	}
	if _, err := r.store.AppendOperationEvent(ctx, domain.OperationEvent{
		OperationID: operation.ID, StepID: "semantic.compliance", Attempt: operation.Attempt,
		IdempotencyKey: fmt.Sprintf("semantic-compliance:%d", operation.Attempt),
		Kind:           "semantic.compliance_failed", Payload: payload, CreatedAt: r.now(),
	}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
