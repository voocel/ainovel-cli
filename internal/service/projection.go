package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
)

type ProjectProjection struct {
	ProjectID    string                     `json:"project_id"`
	BaseRevision domain.Revision            `json:"base_revision"`
	Intent       domain.Intent              `json:"intent"`
	Plan         []domain.PlanNode          `json:"plan"`
	Canon        []domain.CanonFact         `json:"canon"`
	Manuscript   []domain.ManuscriptChapter `json:"manuscript"`
	Ownership    []domain.OwnershipRule     `json:"ownership"`
	Directives   []domain.Directive         `json:"directives,omitempty"`
	Approval     domain.ApprovalPolicy      `json:"approval,omitempty"`
	Overlay      []string                   `json:"overlay,omitempty"`
	Assets       *domain.ProjectAssetRefs   `json:"assets,omitempty"`
}

func (s *Service) ExportProject(ctx context.Context, projectID string, revision domain.Revision) (ProjectProjection, error) {
	project, err := s.Project(ctx, projectID, revision)
	if err != nil {
		return ProjectProjection{}, err
	}
	return ProjectProjection{
		ProjectID: project.ID, BaseRevision: project.Revision,
		Intent: project.Intent, Plan: project.Plan, Canon: project.Canon,
		Manuscript: project.Manuscript, Ownership: project.Ownership, Directives: project.Directives,
		Approval: project.Approval, Overlay: project.Overlay, Assets: project.Assets,
	}, nil
}

func (s *Service) ImportProject(
	ctx context.Context,
	changeID, userID, reason string,
	projection ProjectProjection,
	createdAt time.Time,
) (domain.Proposal, error) {
	return s.importProject(ctx, changeID, userID, reason, projection, createdAt, false)
}

func (s *Service) ImportProjectWithSemantic(
	ctx context.Context,
	changeID, userID, reason string,
	projection ProjectProjection,
	createdAt time.Time,
) (domain.Proposal, error) {
	return s.importProject(ctx, changeID, userID, reason, projection, createdAt, true)
}

// ImportNewProject 从投影建立全新作品：先以投影的 Intent/Approval 初始化
// 权威流，再把其余内容作为一份待批准草案导入，用户批准后成书。
func (s *Service) ImportNewProject(
	ctx context.Context,
	changeID, userID, reason string,
	projection ProjectProjection,
	createdAt time.Time,
) (domain.Proposal, error) {
	approval := projection.Approval
	if approval == "" {
		approval = domain.ApprovalAuto
	}
	project, err := s.CreateProject(ctx, CreateProjectCommand{
		ProjectID: projection.ProjectID,
		ChangeID:  changeID + ":create",
		UserID:    userID,
		Reason:    reason,
		Draft:     ProjectDraft{Intent: projection.Intent, Approval: approval},
		CreatedAt: createdAt,
	})
	if err != nil {
		return domain.Proposal{}, err
	}
	projection.BaseRevision = project.Revision
	return s.importProject(ctx, changeID, userID, reason, projection, createdAt, false)
}

func (s *Service) importProject(
	ctx context.Context,
	changeID, userID, reason string,
	projection ProjectProjection,
	createdAt time.Time,
	analyzeSemantic bool,
) (domain.Proposal, error) {
	if strings.TrimSpace(projection.ProjectID) == "" || projection.BaseRevision <= domain.InitialRevision {
		return domain.Proposal{}, fmt.Errorf("project projection identity and base revision are required: %w", domain.ErrInvalid)
	}
	base, err := s.Project(ctx, projection.ProjectID, projection.BaseRevision)
	if err != nil {
		return domain.Proposal{}, err
	}
	currentDocuments, err := snapshotDocuments(base)
	if err != nil {
		return domain.Proposal{}, err
	}
	desiredDocuments, err := projectionDocuments(projection)
	if err != nil {
		return domain.Proposal{}, err
	}
	keys := make([]string, 0, len(currentDocuments)+len(desiredDocuments))
	seen := make(map[string]struct{}, len(currentDocuments)+len(desiredDocuments))
	for key := range currentDocuments {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range desiredDocuments {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	patches := make([]domain.Patch, 0, len(keys))
	for _, key := range keys {
		current, inCurrent := currentDocuments[key]
		desired, inDesired := desiredDocuments[key]
		if inDesired && desired.ref.Kind == domain.DocumentCanon {
			desired, err = normalizeProjectedCanon(current, inCurrent, desired)
			if err != nil {
				return domain.Proposal{}, err
			}
		}
		switch {
		case !inDesired:
			patches = append(patches, domain.Patch{Document: current.ref, Operation: domain.PatchDelete})
		case !inCurrent || !bytes.Equal(current.content, desired.content):
			patches = append(patches, domain.Patch{
				Document: desired.ref, Operation: domain.PatchPut,
				Content: append(json.RawMessage(nil), desired.content...),
			})
		}
	}
	if len(patches) == 0 {
		return domain.Proposal{}, fmt.Errorf("project projection contains no changes: %w", change.ErrInvalidState)
	}
	proposal := domain.Proposal{
		ID:           changeID,
		Target:       domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: projection.ProjectID},
		BaseRevision: projection.BaseRevision,
		Author:       domain.Author{Kind: domain.AuthorUser, ID: userID},
		Reason:       reason, Patches: patches,
		ApprovalState: domain.ApprovalPending, CreatedAt: createdAt,
	}
	if analyzeSemantic {
		return s.changes.PrepareWithSemantic(ctx, proposal)
	}
	return s.changes.Prepare(ctx, proposal)
}

func normalizeProjectedCanon(current projectionDocument, inCurrent bool, desired projectionDocument) (projectionDocument, error) {
	var fact domain.CanonFact
	if err := json.Unmarshal(desired.content, &fact); err != nil {
		return projectionDocument{}, fmt.Errorf("decode projected canon %q: %w", desired.ref.ID, err)
	}
	fact.PreviousValue = nil
	if inCurrent {
		var previous domain.CanonFact
		if err := json.Unmarshal(current.content, &previous); err != nil {
			return projectionDocument{}, fmt.Errorf("decode base canon %q: %w", desired.ref.ID, err)
		}
		previous.PreviousValue = nil
		desiredComparable, err := json.Marshal(fact)
		if err != nil {
			return projectionDocument{}, fmt.Errorf("encode projected canon %q for comparison: %w", desired.ref.ID, err)
		}
		currentComparable, err := json.Marshal(previous)
		if err != nil {
			return projectionDocument{}, fmt.Errorf("encode base canon %q for comparison: %w", desired.ref.ID, err)
		}
		if bytes.Equal(desiredComparable, currentComparable) {
			desired.content = append(json.RawMessage(nil), current.content...)
			return desired, nil
		}
		fact.PreviousValue = append(json.RawMessage(nil), previous.Value...)
	}
	content, err := json.Marshal(fact)
	if err != nil {
		return projectionDocument{}, fmt.Errorf("encode projected canon %q: %w", desired.ref.ID, err)
	}
	desired.content = content
	return desired, nil
}

type projectionDocument struct {
	ref     domain.DocumentRef
	content json.RawMessage
}

func snapshotDocuments(project ProjectSnapshot) (map[string]projectionDocument, error) {
	return projectionDocuments(ProjectProjection{
		ProjectID: project.ID, BaseRevision: project.Revision,
		Intent: project.Intent, Plan: project.Plan, Canon: project.Canon,
		Manuscript: project.Manuscript, Ownership: project.Ownership, Directives: project.Directives,
		Approval: project.Approval, Overlay: project.Overlay, Assets: project.Assets,
	})
}

func projectionDocuments(projection ProjectProjection) (map[string]projectionDocument, error) {
	documents := make(map[string]projectionDocument)
	add := func(ref domain.DocumentRef, value any) error {
		if _, exists := documents[ref.Key()]; exists {
			return fmt.Errorf("duplicate projected document %q: %w", ref.Key(), domain.ErrInvalid)
		}
		content, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode projected document %q: %w", ref.Key(), err)
		}
		documents[ref.Key()] = projectionDocument{ref: ref, content: content}
		return nil
	}
	if err := add(domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, projection.Intent); err != nil {
		return nil, err
	}
	for _, node := range projection.Plan {
		if err := add(domain.DocumentRef{Kind: domain.DocumentPlan, ID: node.ID}, node); err != nil {
			return nil, err
		}
	}
	for _, fact := range projection.Canon {
		if err := add(domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}, fact); err != nil {
			return nil, err
		}
	}
	for _, chapter := range projection.Manuscript {
		if err := add(domain.DocumentRef{Kind: domain.DocumentManuscript, ID: chapter.ID}, chapter); err != nil {
			return nil, err
		}
	}
	for _, rule := range projection.Ownership {
		if err := add(domain.DocumentRef{Kind: domain.DocumentOwnership, ID: rule.Target.Key()}, rule); err != nil {
			return nil, err
		}
	}
	for _, directive := range projection.Directives {
		if err := add(domain.DocumentRef{Kind: domain.DocumentDirective, ID: directive.ID}, directive); err != nil {
			return nil, err
		}
	}
	if projection.Approval != "" {
		setting := domain.ApprovalSetting{Policy: projection.Approval}
		if err := add(domain.DocumentRef{Kind: domain.DocumentApproval, ID: "root"}, setting); err != nil {
			return nil, err
		}
	}
	if len(projection.Overlay) > 0 {
		overlay := domain.ProjectOverlay{Rules: projection.Overlay}
		if err := add(domain.DocumentRef{Kind: domain.DocumentOverlay, ID: "root"}, overlay); err != nil {
			return nil, err
		}
	}
	if projection.Assets != nil {
		if err := add(domain.DocumentRef{Kind: domain.DocumentAssets, ID: "root"}, *projection.Assets); err != nil {
			return nil, err
		}
	}
	return documents, nil
}
