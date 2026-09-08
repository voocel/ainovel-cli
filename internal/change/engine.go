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

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Engine struct {
	store    *store.Store
	semantic SemanticAnalyzer
}

type SemanticAnalyzer interface {
	Analyze(context.Context, domain.Proposal, StructuralImpact) (json.RawMessage, error)
}

type StructuralImpact struct {
	Direct   []domain.DocumentRef `json:"direct"`
	Affected []domain.DocumentRef `json:"affected"`
}

func New(authorityStore *store.Store) *Engine {
	return &Engine{store: authorityStore}
}

func NewWithSemanticAnalyzer(authorityStore *store.Store, analyzer SemanticAnalyzer) *Engine {
	return &Engine{store: authorityStore, semantic: analyzer}
}

func (e *Engine) Prepare(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	return e.prepare(ctx, proposal, false)
}

func (e *Engine) PrepareWithSemantic(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	return e.prepare(ctx, proposal, true)
}

func (e *Engine) prepare(ctx context.Context, proposal domain.Proposal, analyzeSemantic bool) (domain.Proposal, error) {
	if proposal.ApprovalState != domain.ApprovalPending || proposal.DecidedBy != nil || proposal.DecidedAt != nil {
		return domain.Proposal{}, fmt.Errorf("prepare requires a pending proposal: %w", ErrInvalidState)
	}
	if err := proposal.Validate(); err != nil {
		return domain.Proposal{}, err
	}
	impact, baseState, err := e.validateAndAnalyze(ctx, proposal)
	if err != nil {
		return domain.Proposal{}, err
	}
	payload, err := json.Marshal(impact)
	if err != nil {
		return domain.Proposal{}, fmt.Errorf("marshal structural impact: %w", err)
	}
	proposal.Impact.Structural = payload
	if analyzeSemantic {
		if e.semantic == nil {
			return domain.Proposal{}, ErrSemanticUnavailable
		}
		semantic, err := e.semantic.Analyze(ctx, proposal, impact)
		if err != nil {
			return domain.Proposal{}, fmt.Errorf("analyze semantic impact: %w", err)
		}
		if len(semantic) == 0 || !json.Valid(semantic) {
			return domain.Proposal{}, fmt.Errorf("semantic analyzer returned invalid JSON: %w", domain.ErrInvalid)
		}
		var report SemanticImpactReport
		if err := json.Unmarshal(semantic, &report); err != nil {
			return domain.Proposal{}, fmt.Errorf("decode semantic impact: %w", err)
		}
		if err := report.Validate(); err != nil {
			return domain.Proposal{}, fmt.Errorf("validate semantic impact: %w", err)
		}
		if err := validateSemanticReferences(report, baseState); err != nil {
			return domain.Proposal{}, err
		}
		proposal.Impact.Semantic = append(json.RawMessage(nil), semantic...)
	}
	return e.store.SaveProposal(ctx, proposal)
}

func validateSemanticReferences(report SemanticImpactReport, base documentState) error {
	for _, option := range report.Options {
		for _, chapterID := range option.ChapterIDs {
			key := (domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapterID}).Key()
			if _, exists := base[key]; !exists {
				return fmt.Errorf("semantic resolution references missing chapter %q: %w", chapterID, ErrStructuralConflict)
			}
		}
	}
	return nil
}

func Decide(proposal domain.Proposal, state domain.ApprovalState, decider domain.Author, at time.Time) (domain.Proposal, error) {
	if proposal.ApprovalState != domain.ApprovalPending || proposal.DecidedBy != nil || proposal.DecidedAt != nil {
		return domain.Proposal{}, fmt.Errorf("proposal is not pending: %w", ErrInvalidState)
	}
	if state != domain.ApprovalApproved && state != domain.ApprovalRejected {
		return domain.Proposal{}, fmt.Errorf("decision must approve or reject: %w", ErrInvalidState)
	}
	if at.IsZero() {
		return domain.Proposal{}, fmt.Errorf("decision time is required: %w", domain.ErrInvalid)
	}
	proposal.ApprovalState = state
	proposal.DecidedBy = &decider
	proposal.DecidedAt = &at
	if err := proposal.Validate(); err != nil {
		return domain.Proposal{}, err
	}
	return proposal, nil
}

func (e *Engine) Commit(ctx context.Context, approved domain.Proposal) (domain.ChangeSet, error) {
	ready, err := e.prepareCommit(ctx, approved)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	return e.store.CommitProposal(ctx, ready)
}

// CommitExecution 提交执行实例自动批准的提案：校验与授权同 Commit，落库时在同一事务内
// 确认 attempt 仍是 approved.OperationID 的当前执行（D42）。
func (e *Engine) CommitExecution(ctx context.Context, approved domain.Proposal, attempt int) (domain.ChangeSet, error) {
	ready, err := e.prepareCommit(ctx, approved)
	if err != nil {
		return domain.ChangeSet{}, err
	}
	return e.store.CommitExecutionProposal(ctx, ready, attempt)
}

func (e *Engine) prepareCommit(ctx context.Context, approved domain.Proposal) (domain.Proposal, error) {
	if approved.ApprovalState != domain.ApprovalApproved {
		return domain.Proposal{}, store.ErrNotApproved
	}
	if err := approved.Validate(); err != nil {
		return domain.Proposal{}, err
	}
	impact, baseState, err := e.validateAndAnalyze(ctx, approved)
	if err != nil {
		return domain.Proposal{}, err
	}
	if err := authorize(approved, baseState); err != nil {
		return domain.Proposal{}, err
	}
	payload, err := json.Marshal(impact)
	if err != nil {
		return domain.Proposal{}, fmt.Errorf("marshal structural impact: %w", err)
	}
	approved.Impact.Structural = payload
	return approved, nil
}

func (e *Engine) Reject(ctx context.Context, rejected domain.Proposal) (domain.Proposal, error) {
	if rejected.ApprovalState != domain.ApprovalRejected {
		return domain.Proposal{}, fmt.Errorf("reject requires a rejected proposal: %w", ErrInvalidState)
	}
	return e.store.RejectProposal(ctx, rejected)
}

// PrepareRevert creates a new Proposal that restores an earlier authority
// snapshot. History remains append-only; committing it creates a new revision.
func (e *Engine) PrepareRevert(
	ctx context.Context,
	id string,
	target domain.AuthorityTarget,
	to domain.Revision,
	author domain.Author,
	reason string,
	createdAt time.Time,
) (domain.Proposal, error) {
	current, err := e.currentRevision(ctx, target)
	if err != nil {
		return domain.Proposal{}, err
	}
	if to < domain.InitialRevision || to >= current {
		return domain.Proposal{}, fmt.Errorf("revert revision %d must be before current revision %d: %w", to, current, domain.ErrInvalid)
	}
	currentState, err := e.loadState(ctx, target, current)
	if err != nil {
		return domain.Proposal{}, err
	}
	targetState, err := e.loadState(ctx, target, to)
	if err != nil {
		return domain.Proposal{}, err
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
	patches := make([]domain.Patch, 0, len(keys))
	for _, key := range keys {
		currentDocument, inCurrent := currentState[key]
		targetDocument, inTarget := targetState[key]
		switch {
		case !inTarget:
			patches = append(patches, domain.Patch{Document: currentDocument.Document, Operation: domain.PatchDelete})
		case !inCurrent || !bytes.Equal(currentDocument.Content, targetDocument.Content):
			content := append(json.RawMessage(nil), targetDocument.Content...)
			if inCurrent && targetDocument.Document.Kind == domain.DocumentCanon {
				var currentFact, targetFact domain.CanonFact
				if err := json.Unmarshal(currentDocument.Content, &currentFact); err != nil {
					return domain.Proposal{}, fmt.Errorf("decode current canon for revert: %w", err)
				}
				if err := json.Unmarshal(targetDocument.Content, &targetFact); err != nil {
					return domain.Proposal{}, fmt.Errorf("decode target canon for revert: %w", err)
				}
				targetFact.PreviousValue = append(json.RawMessage(nil), currentFact.Value...)
				content, err = json.Marshal(targetFact)
				if err != nil {
					return domain.Proposal{}, fmt.Errorf("encode canon revert: %w", err)
				}
			}
			patches = append(patches, domain.Patch{
				Document:  targetDocument.Document,
				Operation: domain.PatchPut,
				Content:   content,
			})
		}
	}
	if len(patches) == 0 {
		return domain.Proposal{}, fmt.Errorf("revision %d already matches current state: %w", to, ErrInvalidState)
	}
	return e.Prepare(ctx, domain.Proposal{
		ID:            id,
		Target:        target,
		BaseRevision:  current,
		Author:        author,
		Reason:        reason,
		Patches:       patches,
		ApprovalState: domain.ApprovalPending,
		CreatedAt:     createdAt,
	})
}

type documentState map[string]domain.DocumentVersion

func (e *Engine) validateAndAnalyze(ctx context.Context, change domain.Proposal) (StructuralImpact, documentState, error) {
	current, err := e.currentRevision(ctx, change.Target)
	if err != nil {
		return StructuralImpact{}, nil, err
	}
	if current != change.BaseRevision {
		return StructuralImpact{}, nil, fmt.Errorf("base revision %d, current revision %d: %w", change.BaseRevision, current, store.ErrRevisionConflict)
	}
	if err := validateTargetDocuments(change.Target, change.Patches); err != nil {
		return StructuralImpact{}, nil, err
	}
	baseState, err := e.loadState(ctx, change.Target, change.BaseRevision)
	if err != nil {
		return StructuralImpact{}, nil, err
	}
	projected := cloneState(baseState)
	direct := make([]domain.DocumentRef, 0, len(change.Patches))
	for i, patch := range change.Patches {
		key := patch.Document.Key()
		direct = append(direct, patch.Document)
		switch patch.Operation {
		case domain.PatchPut:
			if err := domain.ValidateDocumentContent(patch.Document, patch.Content); err != nil {
				return StructuralImpact{}, nil, fmt.Errorf("patch %d content: %w", i, err)
			}
			projected[key] = domain.DocumentVersion{
				Target: change.Target, Document: patch.Document, Revision: change.BaseRevision + 1,
				Content: append(json.RawMessage(nil), patch.Content...), ChangeSetID: change.ID,
			}
		case domain.PatchDelete:
			if _, ok := projected[key]; !ok {
				return StructuralImpact{}, nil, fmt.Errorf("delete missing document %q: %w", key, store.ErrNotFound)
			}
			delete(projected, key)
		}
	}
	if err := validateCanonContinuity(baseState, change.Patches); err != nil {
		return StructuralImpact{}, nil, err
	}
	if err := validateAICanonDelta(change); err != nil {
		return StructuralImpact{}, nil, err
	}
	if err := validateProjectedState(change.Target, projected); err != nil {
		return StructuralImpact{}, nil, err
	}
	impact, err := buildStructuralImpact(projected, direct)
	if err != nil {
		return StructuralImpact{}, nil, err
	}
	return impact, baseState, nil
}

func (e *Engine) currentRevision(ctx context.Context, target domain.AuthorityTarget) (domain.Revision, error) {
	revision, err := e.store.CurrentRevision(ctx, target)
	if errors.Is(err, store.ErrNotFound) {
		return domain.InitialRevision, nil
	}
	return revision, err
}

func (e *Engine) loadState(ctx context.Context, target domain.AuthorityTarget, at domain.Revision) (documentState, error) {
	state := make(documentState)
	if at == domain.InitialRevision {
		return state, nil
	}
	for _, kind := range allowedDocumentKinds(target.Kind) {
		documents, err := e.store.ListDocuments(ctx, target, kind, at)
		if err != nil {
			return nil, err
		}
		for _, document := range documents {
			if err := domain.ValidateDocumentContent(document.Document, document.Content); err != nil {
				return nil, fmt.Errorf("stored document %q is invalid: %w", document.Document.Key(), err)
			}
			state[document.Document.Key()] = document
		}
	}
	return state, nil
}

func validateTargetDocuments(target domain.AuthorityTarget, patches []domain.Patch) error {
	allowed := make(map[domain.DocumentKind]struct{})
	for _, kind := range allowedDocumentKinds(target.Kind) {
		allowed[kind] = struct{}{}
	}
	for _, patch := range patches {
		if _, ok := allowed[patch.Document.Kind]; !ok {
			return fmt.Errorf("document %s cannot be written to %s authority: %w", patch.Document.Kind, target.Kind, domain.ErrInvalid)
		}
	}
	return nil
}

func allowedDocumentKinds(kind domain.AuthorityKind) []domain.DocumentKind {
	switch kind {
	case domain.AuthorityProject:
		return []domain.DocumentKind{
			domain.DocumentIntent, domain.DocumentPlan, domain.DocumentCanon,
			domain.DocumentManuscript, domain.DocumentOwnership, domain.DocumentApproval,
			domain.DocumentOverlay, domain.DocumentAssets, domain.DocumentDirective,
		}
	case domain.AuthorityProfile:
		return []domain.DocumentKind{domain.DocumentCreatorProfile}
	case domain.AuthorityPack:
		return []domain.DocumentKind{domain.DocumentPack}
	default:
		return nil
	}
}

func validateProjectedState(target domain.AuthorityTarget, state documentState) error {
	if target.Kind != domain.AuthorityProject {
		return validateAssetState(target, state)
	}

	planNodes := make(map[string]domain.PlanNode)
	manuscriptIDs := make(map[string]struct{})
	for _, document := range state {
		switch document.Document.Kind {
		case domain.DocumentIntent:
			if document.Document.ID != "root" {
				return fmt.Errorf("project intent document id must be root: %w", ErrStructuralConflict)
			}
		case domain.DocumentPlan:
			var node domain.PlanNode
			if err := json.Unmarshal(document.Content, &node); err != nil {
				return fmt.Errorf("decode plan node %q: %w", document.Document.ID, err)
			}
			planNodes[node.ID] = node
		case domain.DocumentManuscript:
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
		if document.Document.Kind == domain.DocumentManuscript {
			var chapter domain.ManuscriptChapter
			if err := json.Unmarshal(document.Content, &chapter); err != nil {
				return fmt.Errorf("decode chapter %q: %w", document.Document.ID, err)
			}
			plan, ok := planNodes[chapter.PlanNodeID]
			if !ok || plan.Kind != domain.PlanChapter {
				return fmt.Errorf("chapter %q references missing chapter plan %q: %w", chapter.ID, chapter.PlanNodeID, ErrStructuralConflict)
			}
		}
		if document.Document.Kind == domain.DocumentCanon {
			var fact domain.CanonFact
			if err := json.Unmarshal(document.Content, &fact); err != nil {
				return fmt.Errorf("decode canon %q: %w", document.Document.ID, err)
			}
			if fact.SourceChapterID != "" {
				if _, ok := manuscriptIDs[fact.SourceChapterID]; !ok {
					return fmt.Errorf("canon %q references missing source chapter %q: %w", fact.ID, fact.SourceChapterID, ErrStructuralConflict)
				}
			}
		}
		dependencies, err := domain.DocumentDependencies(document.Document, document.Content)
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

func validateCanonContinuity(base documentState, patches []domain.Patch) error {
	for _, patch := range patches {
		if patch.Document.Kind != domain.DocumentCanon || patch.Operation != domain.PatchPut {
			continue
		}
		var next domain.CanonFact
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
		var previous domain.CanonFact
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

func validateAICanonDelta(change domain.Proposal) error {
	if change.Author.Kind != domain.AuthorAI && change.Author.Kind != domain.AuthorExtension {
		return nil
	}
	chapters := make(map[string]struct{})
	for _, patch := range change.Patches {
		if patch.Document.Kind == domain.DocumentManuscript && patch.Operation == domain.PatchPut {
			chapters[patch.Document.ID] = struct{}{}
		}
	}
	if len(chapters) == 0 {
		return nil
	}
	covered := make(map[string]struct{})
	for _, patch := range change.Patches {
		if patch.Document.Kind != domain.DocumentCanon || patch.Operation != domain.PatchPut {
			continue
		}
		var fact domain.CanonFact
		if err := json.Unmarshal(patch.Content, &fact); err != nil {
			return fmt.Errorf("decode writer canon delta %q: %w", patch.Document.ID, err)
		}
		if _, ok := chapters[fact.SourceChapterID]; ok {
			covered[fact.SourceChapterID] = struct{}{}
		}
	}
	for chapterID := range chapters {
		if _, ok := covered[chapterID]; !ok {
			return fmt.Errorf("AI manuscript %q requires a Canon Delta in the same Proposal: %w", chapterID, ErrStructuralConflict)
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

func validateAssetState(target domain.AuthorityTarget, state documentState) error {
	if len(state) > 1 {
		return fmt.Errorf("%s authority may contain only one root document: %w", target.Kind, ErrStructuralConflict)
	}
	for _, document := range state {
		switch target.Kind {
		case domain.AuthorityProfile:
			var profile domain.CreatorProfile
			if err := json.Unmarshal(document.Content, &profile); err != nil {
				return err
			}
			if profile.ID != target.ID || profile.Scope != target.Scope {
				return fmt.Errorf("creator profile does not match authority target: %w", ErrStructuralConflict)
			}
		case domain.AuthorityPack:
			var pack domain.PackManifest
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

func validPlanParent(child, parent domain.PlanNodeKind) bool {
	return child == domain.PlanArc && parent == domain.PlanVolume ||
		child == domain.PlanChapter && parent == domain.PlanArc ||
		child == domain.PlanBeat && parent == domain.PlanChapter
}

func detectPlanCycles(nodes map[string]domain.PlanNode) error {
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
		dependencies, err := domain.DocumentDependencies(document.Document, document.Content)
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

func buildStructuralImpact(state documentState, direct []domain.DocumentRef) (StructuralImpact, error) {
	slices.SortFunc(direct, compareDocumentRef)
	direct = slices.CompactFunc(direct, func(a, b domain.DocumentRef) bool { return a.Key() == b.Key() })
	directSet := make(map[string]struct{}, len(direct))
	for _, ref := range direct {
		directSet[ref.Key()] = struct{}{}
	}

	reverse := make(map[string][]domain.DocumentRef)
	for _, document := range state {
		dependencies, err := domain.DocumentDependencies(document.Document, document.Content)
		if err != nil {
			return StructuralImpact{}, err
		}
		for _, dependency := range dependencies {
			reverse[dependency.Key()] = append(reverse[dependency.Key()], document.Document)
		}
	}

	queue := append([]domain.DocumentRef(nil), direct...)
	affected := make(map[string]domain.DocumentRef)
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
	affectedRefs := make([]domain.DocumentRef, 0, len(affected))
	for _, ref := range affected {
		affectedRefs = append(affectedRefs, ref)
	}
	slices.SortFunc(affectedRefs, compareDocumentRef)
	return StructuralImpact{Direct: direct, Affected: affectedRefs}, nil
}

func authorize(change domain.Proposal, base documentState) error {
	decider := change.DecidedBy
	if decider == nil {
		return fmt.Errorf("approval decider is missing: %w", ErrUnauthorized)
	}
	if change.Author.Kind == domain.AuthorUser && decider.Kind != domain.AuthorUser {
		return fmt.Errorf("user-authored change requires user decision: %w", ErrUnauthorized)
	}
	if change.Target.Kind == domain.AuthorityProfile || change.Target.Kind == domain.AuthorityPack {
		if change.Author.Kind != domain.AuthorUser && decider.Kind != domain.AuthorUser {
			return fmt.Errorf("AI changes to creator assets require user approval: %w", ErrUnauthorized)
		}
	}
	for _, patch := range change.Patches {
		// D25/D31/D33：所有权、审批策略、Project Overlay、资产固定引用与 Directive 都是
		// 创作边界，AI 可以提出 Proposal，但任何作者的变更都必须由用户裁决。
		switch patch.Document.Kind {
		case domain.DocumentOwnership, domain.DocumentApproval, domain.DocumentOverlay,
			domain.DocumentAssets, domain.DocumentDirective:
			if decider.Kind != domain.AuthorUser {
				return fmt.Errorf("creative boundary changes require user approval: %w", ErrUnauthorized)
			}
		}
		control, err := controlLevel(base, patch.Document)
		if err != nil {
			return err
		}
		if change.Author.Kind == domain.AuthorUser {
			continue
		}
		// D25/D28：Intent 变更无条件用户确认；唯一例外是初始化事务
		// （BaseRevision == InitialRevision，即项目的第一个 Revision）。
		if patch.Document.Kind == domain.DocumentIntent &&
			change.BaseRevision != domain.InitialRevision && decider.Kind != domain.AuthorUser {
			return fmt.Errorf("intent changes after initialization require user approval: %w", ErrUnauthorized)
		}
		switch control {
		case domain.ControlLocked:
			if decider.Kind != domain.AuthorUser {
				return fmt.Errorf("locked document %q requires user approval: %w", patch.Document.Key(), ErrUnauthorized)
			}
		case domain.ControlGuided:
			if decider.Kind != domain.AuthorUser {
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
	var report domain.SemanticComplianceReport
	if err := json.Unmarshal(compliance, &report); err != nil {
		return fmt.Errorf("decode semantic compliance report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return err
	}
	if report.Status != domain.SemanticCompliancePass {
		return fmt.Errorf("semantic compliance is %s: %w", report.Status, ErrUnauthorized)
	}
	return nil
}

func controlLevel(state documentState, ref domain.DocumentRef) (domain.ControlLevel, error) {
	ownershipRef := domain.DocumentRef{Kind: domain.DocumentOwnership, ID: ref.Key()}
	document, ok := state[ownershipRef.Key()]
	if !ok {
		return domain.ControlOpen, nil
	}
	var rule domain.OwnershipRule
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

func compareDocumentRef(a, b domain.DocumentRef) int {
	return strings.Compare(a.Key(), b.Key())
}
