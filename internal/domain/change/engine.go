package change

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type Engine struct {
	store    Store
	semantic SemanticAnalyzer
}

type SemanticAnalyzer interface {
	Analyze(context.Context, model.Proposal, StructuralImpact) (json.RawMessage, error)
}

type StructuralImpact struct {
	Direct   []model.DocumentRef `json:"direct"`
	Affected []model.DocumentRef `json:"affected"`
}

func New(authorityStore Store) *Engine {
	return &Engine{store: authorityStore}
}

func NewWithSemanticAnalyzer(authorityStore Store, analyzer SemanticAnalyzer) *Engine {
	return &Engine{store: authorityStore, semantic: analyzer}
}

func (e *Engine) Prepare(ctx context.Context, proposal model.Proposal) (model.Proposal, error) {
	return e.prepare(ctx, proposal, false, 0)
}

// Validate 只做确定性结构校验、不落库：执行器在工具边界调用它，把冲突当场回给模型
// 自纠（§11 第 7 条），收尾的 PrepareExecution 仍是兜底。基线按提案自己的 BaseRevision
// 读取而不核对当前 Revision——漂移留给收尾时的重定位（D51）处理。
func (e *Engine) Validate(ctx context.Context, proposal model.Proposal) error {
	if err := proposal.Validate(); err != nil {
		return err
	}
	_, _, err := e.analyze(ctx, proposal)
	return err
}

func (e *Engine) PrepareExecution(ctx context.Context, proposal model.Proposal, attempt int) (model.Proposal, error) {
	if attempt <= 0 {
		return model.Proposal{}, fmt.Errorf("execution proposal requires an attempt: %w", model.ErrInvalid)
	}
	return e.prepare(ctx, proposal, false, attempt)
}

func (e *Engine) PrepareWithSemantic(ctx context.Context, proposal model.Proposal) (model.Proposal, error) {
	return e.prepare(ctx, proposal, true, 0)
}

func (e *Engine) prepare(ctx context.Context, proposal model.Proposal, analyzeSemantic bool, attempt int) (model.Proposal, error) {
	if proposal.ApprovalState != model.ApprovalPending || proposal.DecidedBy != nil || proposal.DecidedAt != nil {
		return model.Proposal{}, fmt.Errorf("prepare requires a pending proposal: %w", ErrInvalidState)
	}
	if err := proposal.Validate(); err != nil {
		return model.Proposal{}, err
	}
	impact, baseState, err := e.validateAndAnalyze(ctx, proposal)
	if err != nil {
		return model.Proposal{}, err
	}
	payload, err := json.Marshal(impact)
	if err != nil {
		return model.Proposal{}, fmt.Errorf("marshal structural impact: %w", err)
	}
	proposal.Impact.Structural = payload
	if analyzeSemantic {
		if e.semantic == nil {
			return model.Proposal{}, ErrSemanticUnavailable
		}
		semantic, err := e.semantic.Analyze(ctx, proposal, impact)
		if err != nil {
			return model.Proposal{}, fmt.Errorf("analyze semantic impact: %w", err)
		}
		if len(semantic) == 0 || !json.Valid(semantic) {
			return model.Proposal{}, fmt.Errorf("semantic analyzer returned invalid JSON: %w", model.ErrInvalid)
		}
		var report SemanticImpactReport
		if err := json.Unmarshal(semantic, &report); err != nil {
			return model.Proposal{}, fmt.Errorf("decode semantic impact: %w", err)
		}
		if err := report.Validate(); err != nil {
			return model.Proposal{}, fmt.Errorf("validate semantic impact: %w", err)
		}
		if err := validateSemanticReferences(report, baseState); err != nil {
			return model.Proposal{}, err
		}
		proposal.Impact.Semantic = append(json.RawMessage(nil), semantic...)
	}
	if attempt > 0 {
		return e.store.SaveExecutionProposal(ctx, proposal, attempt)
	}
	return e.store.SaveProposal(ctx, proposal)
}

func validateSemanticReferences(report SemanticImpactReport, base documentState) error {
	for _, option := range report.Options {
		for _, chapterID := range option.ChapterIDs {
			key := (model.DocumentRef{Kind: model.DocumentManuscript, ID: chapterID}).Key()
			if _, exists := base[key]; !exists {
				return fmt.Errorf("semantic resolution references missing chapter %q: %w", chapterID, ErrStructuralConflict)
			}
		}
	}
	return nil
}

func Decide(proposal model.Proposal, state model.ApprovalState, decider model.Author, at time.Time) (model.Proposal, error) {
	if proposal.ApprovalState != model.ApprovalPending || proposal.DecidedBy != nil || proposal.DecidedAt != nil {
		return model.Proposal{}, fmt.Errorf("proposal is not pending: %w", ErrInvalidState)
	}
	if state != model.ApprovalApproved && state != model.ApprovalRejected {
		return model.Proposal{}, fmt.Errorf("decision must approve or reject: %w", ErrInvalidState)
	}
	if at.IsZero() {
		return model.Proposal{}, fmt.Errorf("decision time is required: %w", model.ErrInvalid)
	}
	proposal.ApprovalState = state
	proposal.DecidedBy = &decider
	proposal.DecidedAt = &at
	if err := proposal.Validate(); err != nil {
		return model.Proposal{}, err
	}
	return proposal, nil
}

func (e *Engine) Commit(ctx context.Context, approved model.Proposal) (model.ChangeSet, error) {
	ready, err := e.prepareCommit(ctx, approved)
	if err != nil {
		return model.ChangeSet{}, err
	}
	return e.store.CommitProposal(ctx, ready)
}

// CommitExecution 提交执行实例自动批准的提案：校验与授权同 Commit，落库时在同一事务内
// 确认 attempt 仍是 approved.OperationID 的当前执行（D42）。
func (e *Engine) CommitExecution(ctx context.Context, approved model.Proposal, attempt int) (model.ChangeSet, error) {
	ready, err := e.prepareCommit(ctx, approved)
	if err != nil {
		return model.ChangeSet{}, err
	}
	return e.store.CommitExecutionProposal(ctx, ready, attempt)
}

func (e *Engine) prepareCommit(ctx context.Context, approved model.Proposal) (model.Proposal, error) {
	if approved.ApprovalState != model.ApprovalApproved {
		return model.Proposal{}, model.ErrNotApproved
	}
	if err := approved.Validate(); err != nil {
		return model.Proposal{}, err
	}
	impact, baseState, err := e.validateAndAnalyze(ctx, approved)
	if err != nil {
		return model.Proposal{}, err
	}
	if err := authorize(approved, baseState); err != nil {
		return model.Proposal{}, err
	}
	payload, err := json.Marshal(impact)
	if err != nil {
		return model.Proposal{}, fmt.Errorf("marshal structural impact: %w", err)
	}
	approved.Impact.Structural = payload
	return approved, nil
}

func (e *Engine) Reject(ctx context.Context, rejected model.Proposal) (model.Proposal, error) {
	if rejected.ApprovalState != model.ApprovalRejected {
		return model.Proposal{}, fmt.Errorf("reject requires a rejected proposal: %w", ErrInvalidState)
	}
	return e.store.RejectProposal(ctx, rejected)
}

// PrepareRevert creates a new Proposal that restores an earlier authority
// snapshot. History remains append-only; committing it creates a new revision.
func (e *Engine) PrepareRevert(
	ctx context.Context,
	id string,
	target model.AuthorityTarget,
	to model.Revision,
	author model.Author,
	reason string,
	createdAt time.Time,
) (model.Proposal, error) {
	current, err := e.currentRevision(ctx, target)
	if err != nil {
		return model.Proposal{}, err
	}
	if to < model.InitialRevision || to >= current {
		return model.Proposal{}, fmt.Errorf("revert revision %d must be before current revision %d: %w", to, current, model.ErrInvalid)
	}
	currentState, err := e.loadState(ctx, target, current)
	if err != nil {
		return model.Proposal{}, err
	}
	targetState, err := e.loadState(ctx, target, to)
	if err != nil {
		return model.Proposal{}, err
	}

	keys := make([]string, 0, len(currentState)+len(targetState))
	seen := make(map[string]struct{}, len(currentState)+len(targetState))
	for key := range currentState {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range targetState {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	patches := make([]model.Patch, 0, len(keys))
	for _, key := range keys {
		currentDocument, inCurrent := currentState[key]
		targetDocument, inTarget := targetState[key]
		ref := currentDocument.Document
		if !inCurrent {
			ref = targetDocument.Document
		}
		// 只追加文档（裁决）不随回滚变化：历史记录不是可回退的内容。
		if spec, _ := model.DocumentType(ref.Kind); spec.AppendOnly {
			continue
		}
		switch {
		case !inTarget:
			patches = append(patches, model.Patch{Document: currentDocument.Document, Operation: model.PatchDelete})
		case !inCurrent || !bytes.Equal(currentDocument.Content, targetDocument.Content):
			content := append(json.RawMessage(nil), targetDocument.Content...)
			if inCurrent && targetDocument.Document.Kind == model.DocumentCanon {
				var currentFact, targetFact model.CanonFact
				if err := json.Unmarshal(currentDocument.Content, &currentFact); err != nil {
					return model.Proposal{}, fmt.Errorf("decode current canon for revert: %w", err)
				}
				if err := json.Unmarshal(targetDocument.Content, &targetFact); err != nil {
					return model.Proposal{}, fmt.Errorf("decode target canon for revert: %w", err)
				}
				targetFact.PreviousValue = append(json.RawMessage(nil), currentFact.Value...)
				content, err = json.Marshal(targetFact)
				if err != nil {
					return model.Proposal{}, fmt.Errorf("encode canon revert: %w", err)
				}
			}
			patches = append(patches, model.Patch{
				Document:  targetDocument.Document,
				Operation: model.PatchPut,
				Content:   content,
			})
		}
	}
	if len(patches) == 0 {
		return model.Proposal{}, fmt.Errorf("revision %d already matches current state: %w", to, ErrInvalidState)
	}
	return e.Prepare(ctx, model.Proposal{
		ID:            id,
		Target:        target,
		BaseRevision:  current,
		Author:        author,
		Reason:        reason,
		Patches:       patches,
		ApprovalState: model.ApprovalPending,
		CreatedAt:     createdAt,
	})
}

type documentState map[string]model.DocumentVersion

func (e *Engine) validateAndAnalyze(ctx context.Context, change model.Proposal) (StructuralImpact, documentState, error) {
	current, err := e.currentRevision(ctx, change.Target)
	if err != nil {
		return StructuralImpact{}, nil, err
	}
	if current != change.BaseRevision {
		return StructuralImpact{}, nil, fmt.Errorf("base revision %d, current revision %d: %w", change.BaseRevision, current, model.ErrRevisionConflict)
	}
	return e.analyze(ctx, change)
}

// analyze 在提案自己的 BaseRevision 上执行全部确定性结构校验并计算结构影响，不落库。
func (e *Engine) analyze(ctx context.Context, change model.Proposal) (StructuralImpact, documentState, error) {
	if err := validateTargetDocuments(change.Target, change.Patches); err != nil {
		return StructuralImpact{}, nil, err
	}
	baseState, err := e.loadState(ctx, change.Target, change.BaseRevision)
	if err != nil {
		return StructuralImpact{}, nil, err
	}
	projected := cloneState(baseState)
	direct := make([]model.DocumentRef, 0, len(change.Patches))
	for i, patch := range change.Patches {
		key := patch.Document.Key()
		direct = append(direct, patch.Document)
		switch patch.Operation {
		case model.PatchPut:
			if err := model.ValidateDocumentContent(patch.Document, patch.Content); err != nil {
				return StructuralImpact{}, nil, fmt.Errorf("patch %d content: %w", i, err)
			}
			projected[key] = model.DocumentVersion{
				Target: change.Target, Document: patch.Document, Revision: change.BaseRevision + 1,
				Content: append(json.RawMessage(nil), patch.Content...), ChangeSetID: change.ID,
			}
		case model.PatchDelete:
			if _, ok := projected[key]; !ok {
				return StructuralImpact{}, nil, fmt.Errorf("delete missing document %q: %w", key, model.ErrNotFound)
			}
			delete(projected, key)
		}
	}
	if err := validateCanonContinuity(baseState, change.Patches); err != nil {
		return StructuralImpact{}, nil, err
	}
	if err := validateChapterAuthors(change); err != nil {
		return StructuralImpact{}, nil, err
	}
	if err := validateAppendOnly(baseState, change.Patches); err != nil {
		return StructuralImpact{}, nil, err
	}
	if err := e.validateAttachments(ctx, change); err != nil {
		return StructuralImpact{}, nil, err
	}
	if err := validateProjectedState(change.Target, projected); err != nil {
		return StructuralImpact{}, nil, err
	}
	if err := validateAICanon(baseState, projected, change); err != nil {
		return StructuralImpact{}, nil, err
	}
	impact, err := buildStructuralImpact(projected, direct)
	if err != nil {
		return StructuralImpact{}, nil, err
	}
	return impact, baseState, nil
}

// validateAttachments 要求附件引用的工件已发布、属于本作品且摘要一致（D47）：
// 权威只能指向不可变内容。
func (e *Engine) validateAttachments(ctx context.Context, change model.Proposal) error {
	for _, patch := range change.Patches {
		if patch.Document.Kind != model.DocumentAttachment || patch.Operation != model.PatchPut {
			continue
		}
		var attachment model.Attachment
		if err := json.Unmarshal(patch.Content, &attachment); err != nil {
			return fmt.Errorf("decode attachment %q: %w", patch.Document.ID, err)
		}
		artifact, err := e.store.GetArtifact(ctx, attachment.Artifact.ID)
		if errors.Is(err, model.ErrNotFound) {
			return fmt.Errorf("attachment %q references unpublished artifact %q: %w", attachment.ID, attachment.Artifact.ID, ErrStructuralConflict)
		}
		if err != nil {
			return err
		}
		if artifact.ProjectID != change.Target.ID || artifact.Digest != attachment.Artifact.Digest {
			return fmt.Errorf("attachment %q artifact %q does not match the published object: %w", attachment.ID, attachment.Artifact.ID, ErrStructuralConflict)
		}
	}
	return nil
}

func (e *Engine) currentRevision(ctx context.Context, target model.AuthorityTarget) (model.Revision, error) {
	revision, err := e.store.CurrentRevision(ctx, target)
	if errors.Is(err, model.ErrNotFound) {
		return model.InitialRevision, nil
	}
	return revision, err
}

func (e *Engine) loadState(ctx context.Context, target model.AuthorityTarget, at model.Revision) (documentState, error) {
	state := make(documentState)
	if at == model.InitialRevision {
		return state, nil
	}
	for _, kind := range model.DocumentKindsFor(target.Kind) {
		documents, err := e.store.ListDocuments(ctx, target, kind, at)
		if err != nil {
			return nil, err
		}
		for _, document := range documents {
			if err := model.ValidateDocumentContent(document.Document, document.Content); err != nil {
				return nil, fmt.Errorf("stored document %q is invalid: %w", document.Document.Key(), err)
			}
			state[document.Document.Key()] = document
		}
	}
	return state, nil
}
