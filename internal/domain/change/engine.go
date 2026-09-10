package change

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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

func validateTargetDocuments(target model.AuthorityTarget, patches []model.Patch) error {
	allowed := make(map[model.DocumentKind]struct{})
	for _, kind := range model.DocumentKindsFor(target.Kind) {
		allowed[kind] = struct{}{}
	}
	for _, patch := range patches {
		if _, ok := allowed[patch.Document.Kind]; !ok {
			return fmt.Errorf("document %s cannot be written to %s authority: %w", patch.Document.Kind, target.Kind, model.ErrInvalid)
		}
	}
	return nil
}

func validateProjectedState(target model.AuthorityTarget, state documentState) error {
	if target.Kind != model.AuthorityProject {
		return validateAssetState(target, state)
	}

	planNodes := make(map[string]model.PlanNode)
	manuscriptIDs := make(map[string]struct{})
	for _, document := range state {
		switch document.Document.Kind {
		case model.DocumentPlan:
			var node model.PlanNode
			if err := json.Unmarshal(document.Content, &node); err != nil {
				return fmt.Errorf("decode plan node %q: %w", document.Document.ID, err)
			}
			planNodes[node.ID] = node
		case model.DocumentManuscript:
			manuscriptIDs[document.Document.ID] = struct{}{}
		}
	}

	for _, node := range planNodes {
		if node.ParentID == "" {
			continue
		}
		parent, ok := planNodes[node.ParentID]
		if !ok {
			return fmt.Errorf("plan node %q references missing parent %q: %w", node.ID, node.ParentID, ErrStructuralConflict)
		}
		if !validPlanParent(node.Kind, parent.Kind) {
			return fmt.Errorf("plan node %q of kind %s cannot have %s parent: %w", node.ID, node.Kind, parent.Kind, ErrStructuralConflict)
		}
	}
	if err := detectPlanCycles(planNodes); err != nil {
		return err
	}

	for _, document := range state {
		if document.Document.Kind == model.DocumentManuscript {
			var chapter model.ManuscriptChapter
			if err := json.Unmarshal(document.Content, &chapter); err != nil {
				return fmt.Errorf("decode chapter %q: %w", document.Document.ID, err)
			}
			plan, ok := planNodes[chapter.PlanNodeID]
			if !ok || plan.Kind != model.PlanChapter {
				return fmt.Errorf("chapter %q references missing chapter plan %q: %w", chapter.ID, chapter.PlanNodeID, ErrStructuralConflict)
			}
		}
		if document.Document.Kind == model.DocumentCanon {
			var fact model.CanonFact
			if err := json.Unmarshal(document.Content, &fact); err != nil {
				return fmt.Errorf("decode canon %q: %w", document.Document.ID, err)
			}
			for _, chapterID := range []string{fact.SourceChapterID, fact.EffectiveChapterID} {
				if _, ok := manuscriptIDs[chapterID]; chapterID != "" && !ok {
					return fmt.Errorf("canon %q references missing chapter %q: %w", fact.ID, chapterID, ErrStructuralConflict)
				}
			}
		}
		dependencies, err := model.DocumentDependencies(document.Document, document.Content)
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if _, ok := state[dependency.Key()]; !ok {
				return fmt.Errorf("document %q depends on missing %q: %w", document.Document.Key(), dependency.Key(), ErrStructuralConflict)
			}
		}
	}
	return detectDependencyCycles(state)
}

func validateCanonContinuity(base documentState, patches []model.Patch) error {
	for _, patch := range patches {
		if patch.Document.Kind != model.DocumentCanon || patch.Operation != model.PatchPut {
			continue
		}
		var next model.CanonFact
		if err := json.Unmarshal(patch.Content, &next); err != nil {
			return fmt.Errorf("decode canon continuity candidate %q: %w", patch.Document.ID, err)
		}
		previousDocument, exists := base[patch.Document.Key()]
		if !exists {
			if len(next.PreviousValue) != 0 {
				return fmt.Errorf("new canon %q cannot declare old_value: %w", next.ID, ErrStructuralConflict)
			}
			continue
		}
		var previous model.CanonFact
		if err := json.Unmarshal(previousDocument.Content, &previous); err != nil {
			return fmt.Errorf("decode previous canon %q: %w", next.ID, err)
		}
		if next.Kind != previous.Kind || next.SubjectID != previous.SubjectID || next.Predicate != previous.Predicate {
			return fmt.Errorf("canon %q identity cannot change; create a new stable fact instead: %w", next.ID, ErrStructuralConflict)
		}
		if len(next.PreviousValue) == 0 {
			return fmt.Errorf("updated canon %q requires old_value: %w", next.ID, ErrStructuralConflict)
		}
		same, err := sameJSONValue(next.PreviousValue, previous.Value)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("canon %q old_value does not match the previous new_value: %w", next.ID, ErrStructuralConflict)
		}
	}
	return nil
}

// validateAICanon 落实 AI/扩展提案的 Canon 规则（§4.4 D41）：
//  1. 带正文的提案里，每条事实 put 的来源章必须是本提案的正文之一；
//  2. 每个正文 put 至少一条来源事实，且必须重申报 base 中全部来源于该章的事实（put 或 delete）；
//  3. 带正文的提案只能改动来源章在本提案正文集合内的事件——事件跨章只追加；
//  4. 状态类事实更新的生效位置不得早于现值——插叙不覆盖当前状态，应记录为事件。
//
// 用户提案不受此限，由用户为内容背书。
func validateAICanon(base, projected documentState, change model.Proposal) error {
	if change.Author.Kind != model.AuthorAI && change.Author.Kind != model.AuthorExtension {
		return nil
	}
	chapters := make(map[string]struct{})
	for _, patch := range change.Patches {
		if patch.Document.Kind == model.DocumentManuscript && patch.Operation == model.PatchPut {
			chapters[patch.Document.ID] = struct{}{}
		}
	}
	numbers, err := chapterNumbers(projected)
	if err != nil {
		return err
	}
	covered := make(map[string]struct{})
	touched := make(map[string]struct{})
	for _, patch := range change.Patches {
		if patch.Document.Kind != model.DocumentCanon {
			continue
		}
		touched[patch.Document.Key()] = struct{}{}
		var previous *model.CanonFact
		if document, exists := base[patch.Document.Key()]; exists {
			previous = new(model.CanonFact)
			if err := json.Unmarshal(document.Content, previous); err != nil {
				return fmt.Errorf("decode previous canon %q: %w", patch.Document.ID, err)
			}
			if _, own := chapters[previous.SourceChapterID]; len(chapters) > 0 && previous.IsEvent() && !own {
				return fmt.Errorf("event %q belongs to chapter %q outside this proposal; events are append-only across chapters: %w",
					previous.ID, previous.SourceChapterID, ErrStructuralConflict)
			}
		}
		if patch.Operation != model.PatchPut {
			continue
		}
		var next model.CanonFact
		if err := json.Unmarshal(patch.Content, &next); err != nil {
			return fmt.Errorf("decode canon %q: %w", patch.Document.ID, err)
		}
		if _, own := chapters[next.SourceChapterID]; len(chapters) > 0 && !own {
			return fmt.Errorf("canon %q must be sourced from a chapter in the same proposal: %w", next.ID, ErrStructuralConflict)
		}
		covered[next.SourceChapterID] = struct{}{}
		if previous != nil && !next.IsEvent() && numbers[next.EffectiveChapter()] < numbers[previous.EffectiveChapter()] {
			return fmt.Errorf("canon %q cannot move its effective position back from %q to %q; record flashbacks as events: %w",
				next.ID, previous.EffectiveChapter(), next.EffectiveChapter(), ErrStructuralConflict)
		}
	}
	for chapterID := range chapters {
		if _, ok := covered[chapterID]; !ok {
			return fmt.Errorf("AI manuscript %q requires a Canon Delta in the same Proposal: %w", chapterID, ErrStructuralConflict)
		}
	}
	for key, document := range base {
		if document.Document.Kind != model.DocumentCanon {
			continue
		}
		var fact model.CanonFact
		if err := json.Unmarshal(document.Content, &fact); err != nil {
			return fmt.Errorf("decode canon %q: %w", document.Document.ID, err)
		}
		if _, rewritten := chapters[fact.SourceChapterID]; rewritten {
			if _, ok := touched[key]; !ok {
				return fmt.Errorf("rewritten chapter %q must redeclare canon %q (confirm, update or delete): %w",
					fact.SourceChapterID, fact.ID, ErrStructuralConflict)
			}
		}
	}
	return nil
}

// chapterNumbers 取投影状态里各正文的章号：故事内位置按它比较。
func chapterNumbers(state documentState) (map[string]int, error) {
	numbers := make(map[string]int)
	for _, document := range state {
		if document.Document.Kind != model.DocumentManuscript {
			continue
		}
		var chapter model.ManuscriptChapter
		if err := json.Unmarshal(document.Content, &chapter); err != nil {
			return nil, fmt.Errorf("decode chapter %q: %w", document.Document.ID, err)
		}
		numbers[chapter.ID] = chapter.Number
	}
	return numbers, nil
}

// validateChapterAuthors 落实 D34：AI 与扩展提交的章节必须署自己的名，不得冒认
// 用户或他方；用户提交（含导入、回滚）可以携带任何作者，由用户为内容背书。
func validateChapterAuthors(change model.Proposal) error {
	if change.Author.Kind != model.AuthorAI && change.Author.Kind != model.AuthorExtension {
		return nil
	}
	for _, patch := range change.Patches {
		if patch.Document.Kind != model.DocumentManuscript || patch.Operation != model.PatchPut {
			continue
		}
		var chapter model.ManuscriptChapter
		if err := json.Unmarshal(patch.Content, &chapter); err != nil {
			return fmt.Errorf("decode chapter author %q: %w", patch.Document.ID, err)
		}
		if chapter.Author != change.Author.Kind {
			return fmt.Errorf("chapter %q author %s does not match proposal author %s: %w",
				chapter.ID, chapter.Author, change.Author.Kind, ErrStructuralConflict)
		}
	}
	return nil
}

// validateAppendOnly 落实只追加文档（D43 裁决）：不得覆盖已存在的记录，也不得删除。
func validateAppendOnly(base documentState, patches []model.Patch) error {
	for _, patch := range patches {
		spec, err := model.DocumentType(patch.Document.Kind)
		if err != nil {
			return err
		}
		if !spec.AppendOnly {
			continue
		}
		if _, exists := base[patch.Document.Key()]; exists || patch.Operation == model.PatchDelete {
			return fmt.Errorf("%s is append-only: %w", patch.Document.Key(), ErrStructuralConflict)
		}
	}
	return nil
}

func sameJSONValue(left, right json.RawMessage) (bool, error) {
	canonical := func(raw json.RawMessage) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	leftValue, err := canonical(left)
	if err != nil {
		return false, fmt.Errorf("decode canon old_value: %w", err)
	}
	rightValue, err := canonical(right)
	if err != nil {
		return false, fmt.Errorf("decode previous canon new_value: %w", err)
	}
	return bytes.Equal(leftValue, rightValue), nil
}

func validateAssetState(target model.AuthorityTarget, state documentState) error {
	if len(state) > 1 {
		return fmt.Errorf("%s authority may contain only one root document: %w", target.Kind, ErrStructuralConflict)
	}
	for _, document := range state {
		switch target.Kind {
		case model.AuthorityProfile:
			var profile model.CreatorProfile
			if err := json.Unmarshal(document.Content, &profile); err != nil {
				return err
			}
			if profile.ID != target.ID || profile.Scope != target.Scope {
				return fmt.Errorf("creator profile does not match authority target: %w", ErrStructuralConflict)
			}
		case model.AuthorityPack:
			var pack model.PackManifest
			if err := json.Unmarshal(document.Content, &pack); err != nil {
				return err
			}
			if pack.ID != target.ID {
				return fmt.Errorf("pack does not match authority target: %w", ErrStructuralConflict)
			}
		}
	}
	return nil
}

func validPlanParent(child, parent model.PlanNodeKind) bool {
	return child == model.PlanArc && parent == model.PlanVolume ||
		child == model.PlanChapter && parent == model.PlanArc ||
		child == model.PlanBeat && parent == model.PlanChapter
}

func detectPlanCycles(nodes map[string]model.PlanNode) error {
	for id := range nodes {
		seen := make(map[string]struct{})
		current := id
		for current != "" {
			if _, ok := seen[current]; ok {
				return fmt.Errorf("plan parent cycle at %q: %w", current, ErrStructuralConflict)
			}
			seen[current] = struct{}{}
			current = nodes[current].ParentID
		}
	}
	return nil
}

func detectDependencyCycles(state documentState) error {
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("document dependency cycle at %q: %w", key, ErrStructuralConflict)
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		document := state[key]
		dependencies, err := model.DocumentDependencies(document.Document, document.Content)
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if err := visit(dependency.Key()); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		return nil
	}
	for key := range state {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

func buildStructuralImpact(state documentState, direct []model.DocumentRef) (StructuralImpact, error) {
	slices.SortFunc(direct, compareDocumentRef)
	direct = slices.CompactFunc(direct, func(a, b model.DocumentRef) bool { return a.Key() == b.Key() })
	directSet := make(map[string]struct{}, len(direct))
	for _, ref := range direct {
		directSet[ref.Key()] = struct{}{}
	}

	reverse := make(map[string][]model.DocumentRef)
	for _, document := range state {
		dependencies, err := model.DocumentDependencies(document.Document, document.Content)
		if err != nil {
			return StructuralImpact{}, err
		}
		for _, dependency := range dependencies {
			reverse[dependency.Key()] = append(reverse[dependency.Key()], document.Document)
		}
	}

	queue := append([]model.DocumentRef(nil), direct...)
	affected := make(map[string]model.DocumentRef)
	seen := make(map[string]struct{}, len(direct))
	for _, ref := range direct {
		seen[ref.Key()] = struct{}{}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependent := range reverse[current.Key()] {
			if _, ok := seen[dependent.Key()]; ok {
				continue
			}
			seen[dependent.Key()] = struct{}{}
			queue = append(queue, dependent)
			if _, direct := directSet[dependent.Key()]; !direct {
				affected[dependent.Key()] = dependent
			}
		}
	}
	affectedRefs := make([]model.DocumentRef, 0, len(affected))
	for _, ref := range affected {
		affectedRefs = append(affectedRefs, ref)
	}
	slices.SortFunc(affectedRefs, compareDocumentRef)
	return StructuralImpact{Direct: direct, Affected: affectedRefs}, nil
}

func authorize(change model.Proposal, base documentState) error {
	decider := change.DecidedBy
	if decider == nil {
		return fmt.Errorf("approval decider is missing: %w", ErrUnauthorized)
	}
	if change.Author.Kind == model.AuthorUser && decider.Kind != model.AuthorUser {
		return fmt.Errorf("user-authored change requires user decision: %w", ErrUnauthorized)
	}
	if change.Target.Kind == model.AuthorityProfile || change.Target.Kind == model.AuthorityPack {
		if change.Author.Kind != model.AuthorUser && decider.Kind != model.AuthorUser {
			return fmt.Errorf("AI changes to creator assets require user approval: %w", ErrUnauthorized)
		}
	}
	for _, patch := range change.Patches {
		// D25/D31/D33：用户专属文档是创作边界，AI 可以提出 Proposal，但任何作者的
		// 变更都必须由用户裁决。
		spec, err := model.DocumentType(patch.Document.Kind)
		if err != nil {
			return err
		}
		if spec.UserOnly && decider.Kind != model.AuthorUser {
			return fmt.Errorf("creative boundary changes require user approval: %w", ErrUnauthorized)
		}
		control, err := controlLevel(base, patch.Document)
		if err != nil {
			return err
		}
		if change.Author.Kind == model.AuthorUser {
			continue
		}
		// D25/D28：Intent 变更无条件用户确认；唯一例外是初始化事务
		// （BaseRevision == InitialRevision，即项目的第一个 Revision）。
		if patch.Document.Kind == model.DocumentIntent &&
			change.BaseRevision != model.InitialRevision && decider.Kind != model.AuthorUser {
			return fmt.Errorf("intent changes after initialization require user approval: %w", ErrUnauthorized)
		}
		switch control {
		case model.ControlLocked:
			if decider.Kind != model.AuthorUser {
				return fmt.Errorf("locked document %q requires user approval: %w", patch.Document.Key(), ErrUnauthorized)
			}
		case model.ControlGuided:
			if decider.Kind != model.AuthorUser {
				if err := compliancePasses(change.Impact.Compliance); err != nil {
					return fmt.Errorf("guided document %q: %w", patch.Document.Key(), err)
				}
			}
		}
	}
	return nil
}

// compliancePasses 兑现 D14：只有独立合规裁定明确返回 pass 才允许自动提交；
// 证据缺失、不合法或任何非 pass 状态都不得放行。
func compliancePasses(compliance json.RawMessage) error {
	if len(compliance) == 0 {
		return fmt.Errorf("semantic compliance evidence is required: %w", ErrUnauthorized)
	}
	var report model.SemanticComplianceReport
	if err := json.Unmarshal(compliance, &report); err != nil {
		return fmt.Errorf("decode semantic compliance report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return err
	}
	if report.Status != model.SemanticCompliancePass {
		return fmt.Errorf("semantic compliance is %s: %w", report.Status, ErrUnauthorized)
	}
	return nil
}

// controlLevel 取文档的控制级别：有 Ownership 规则以规则为准；没有规则时，用户
// 亲笔的章节默认 locked（D34），其余 open。
func controlLevel(state documentState, ref model.DocumentRef) (model.ControlLevel, error) {
	ownershipRef := model.DocumentRef{Kind: model.DocumentOwnership, ID: ref.Key()}
	document, ok := state[ownershipRef.Key()]
	if !ok {
		if base, exists := state[ref.Key()]; exists && ref.Kind == model.DocumentManuscript {
			var chapter model.ManuscriptChapter
			if err := json.Unmarshal(base.Content, &chapter); err != nil {
				return "", fmt.Errorf("decode chapter author for %q: %w", ref.Key(), err)
			}
			if chapter.Author == model.AuthorUser {
				return model.ControlLocked, nil
			}
		}
		return model.ControlOpen, nil
	}
	var rule model.OwnershipRule
	if err := json.Unmarshal(document.Content, &rule); err != nil {
		return "", fmt.Errorf("decode ownership for %q: %w", ref.Key(), err)
	}
	if err := rule.Validate(); err != nil {
		return "", err
	}
	return rule.Control, nil
}

func cloneState(source documentState) documentState {
	result := make(documentState, len(source))
	for key, value := range source {
		value.Content = append(json.RawMessage(nil), value.Content...)
		result[key] = value
	}
	return result
}

func compareDocumentRef(a, b model.DocumentRef) int {
	return strings.Compare(a.Key(), b.Key())
}
