package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

type ProjectProjection struct {
	ProjectID    string           `json:"project_id"`
	BaseRevision model.Revision   `json:"base_revision"`
	Intent       model.Intent     `json:"intent"`
	Plan         []model.PlanNode `json:"plan"`
	Entities     []model.Entity   `json:"entities,omitempty"`
	// 附件只携带本作品库内的不可变引用；投影不是包含媒体文件的备份包。
	Attachments []model.Attachment        `json:"attachments,omitempty"`
	Canon       []model.CanonFact         `json:"canon"`
	Manuscript  []model.ManuscriptChapter `json:"manuscript"`
	Ownership   []model.OwnershipRule     `json:"ownership"`
	Directives  []model.Directive         `json:"directives,omitempty"`
	Approval    model.ApprovalPolicy      `json:"approval,omitempty"`
	Overlay     []string                  `json:"overlay,omitempty"`
	Assets      *model.ProjectAssetRefs   `json:"assets,omitempty"`
}

func (s *Repository) ExportProject(ctx context.Context, projectID string, revision model.Revision) (ProjectProjection, error) {
	project, err := s.Project(ctx, projectID, revision)
	if err != nil {
		return ProjectProjection{}, err
	}
	return ProjectProjection{
		ProjectID: project.ID, BaseRevision: project.Revision,
		Intent: project.Intent, Plan: project.Plan, Entities: project.Entities, Attachments: project.Attachments, Canon: project.Canon,
		Manuscript: project.Manuscript, Ownership: project.Ownership, Directives: project.Directives,
		Approval: project.Approval, Overlay: project.Overlay, Assets: project.Assets,
	}, nil
}

func (s *Repository) ImportProject(
	ctx context.Context,
	changeID, userID, reason string,
	projection ProjectProjection,
	createdAt time.Time,
) (model.Proposal, error) {
	return s.importProject(ctx, changeID, userID, reason, projection, createdAt, false)
}

func (s *Repository) ImportProjectWithSemantic(
	ctx context.Context,
	changeID, userID, reason string,
	projection ProjectProjection,
	createdAt time.Time,
) (model.Proposal, error) {
	return s.importProject(ctx, changeID, userID, reason, projection, createdAt, true)
}

// ImportNewProject 从投影建立全新作品：先以投影的 Intent/Approval 初始化
// 权威流，再把其余内容作为一份待批准草案导入，用户批准后成书。
func (s *Repository) ImportNewProject(
	ctx context.Context,
	changeID, userID, reason string,
	projection ProjectProjection,
	createdAt time.Time,
) (model.Proposal, error) {
	if err := s.validateProjectionArtifacts(ctx, projection); err != nil {
		return model.Proposal{}, err
	}
	approval := projection.Approval
	if approval == "" {
		approval = model.ApprovalAuto
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
		return model.Proposal{}, err
	}
	projection.BaseRevision = project.Revision
	return s.importProject(ctx, changeID, userID, reason, projection, createdAt, false)
}

func (s *Repository) importProject(
	ctx context.Context,
	changeID, userID, reason string,
	projection ProjectProjection,
	createdAt time.Time,
	analyzeSemantic bool,
) (model.Proposal, error) {
	if strings.TrimSpace(projection.ProjectID) == "" || projection.BaseRevision <= model.InitialRevision {
		return model.Proposal{}, fmt.Errorf("project projection identity and base revision are required: %w", model.ErrInvalid)
	}
	if err := s.validateProjectionArtifacts(ctx, projection); err != nil {
		return model.Proposal{}, err
	}
	base, err := s.Project(ctx, projection.ProjectID, projection.BaseRevision)
	if err != nil {
		return model.Proposal{}, err
	}
	currentDocuments, err := snapshotDocuments(base)
	if err != nil {
		return model.Proposal{}, err
	}
	desiredDocuments, err := projectionDocuments(projection)
	if err != nil {
		return model.Proposal{}, err
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
	patches := make([]model.Patch, 0, len(keys))
	for _, key := range keys {
		current, inCurrent := currentDocuments[key]
		desired, inDesired := desiredDocuments[key]
		if inDesired && desired.ref.Kind == model.DocumentCanon {
			desired, err = normalizeProjectedCanon(current, inCurrent, desired)
			if err != nil {
				return model.Proposal{}, err
			}
		}
		switch {
		case !inDesired:
			patches = append(patches, model.Patch{Document: current.ref, Operation: model.PatchDelete})
		case !inCurrent || !bytes.Equal(current.content, desired.content):
			patches = append(patches, model.Patch{
				Document: desired.ref, Operation: model.PatchPut,
				Content: append(json.RawMessage(nil), desired.content...),
			})
		}
	}
	if len(patches) == 0 {
		return model.Proposal{}, fmt.Errorf("project projection contains no changes: %w", change.ErrInvalidState)
	}
	proposal := model.Proposal{
		ID:           changeID,
		Target:       model.AuthorityTarget{Kind: model.AuthorityProject, ID: projection.ProjectID},
		BaseRevision: projection.BaseRevision,
		Author:       model.Author{Kind: model.AuthorUser, ID: userID},
		Reason:       reason, Patches: patches,
		ApprovalState: model.ApprovalPending, CreatedAt: createdAt,
	}
	if analyzeSemantic {
		return s.changes.PrepareWithSemantic(ctx, proposal)
	}
	return s.changes.Prepare(ctx, proposal)
}

func (s *Repository) validateProjectionArtifacts(ctx context.Context, projection ProjectProjection) error {
	for _, attachment := range projection.Attachments {
		if err := attachment.Validate(); err != nil {
			return err
		}
		artifact, err := s.store.GetArtifact(ctx, attachment.Artifact.ID)
		if err != nil {
			return fmt.Errorf("attachment %q requires artifact %q in this project library; projection import does not transfer media files: %w", attachment.ID, attachment.Artifact.ID, err)
		}
		if artifact.ProjectID != projection.ProjectID || artifact.Digest != attachment.Artifact.Digest {
			return fmt.Errorf("attachment %q does not match an artifact in this project library: %w", attachment.ID, model.ErrInvalid)
		}
	}
	return nil
}

func normalizeProjectedCanon(current projectionDocument, inCurrent bool, desired projectionDocument) (projectionDocument, error) {
	var fact model.CanonFact
	if err := json.Unmarshal(desired.content, &fact); err != nil {
		return projectionDocument{}, fmt.Errorf("decode projected canon %q: %w", desired.ref.ID, err)
	}
	fact.PreviousValue = nil
	if inCurrent {
		var previous model.CanonFact
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
	ref     model.DocumentRef
	content json.RawMessage
}

func snapshotDocuments(project Snapshot) (map[string]projectionDocument, error) {
	return projectionDocuments(ProjectProjection{
		ProjectID: project.ID, BaseRevision: project.Revision,
		Intent: project.Intent, Plan: project.Plan, Entities: project.Entities, Attachments: project.Attachments, Canon: project.Canon,
		Manuscript: project.Manuscript, Ownership: project.Ownership, Directives: project.Directives,
		Approval: project.Approval, Overlay: project.Overlay, Assets: project.Assets,
	})
}

func projectionDocuments(projection ProjectProjection) (map[string]projectionDocument, error) {
	documents := make(map[string]projectionDocument)
	add := func(ref model.DocumentRef, value any) error {
		if _, exists := documents[ref.Key()]; exists {
			return fmt.Errorf("duplicate projected document %q: %w", ref.Key(), model.ErrInvalid)
		}
		content, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode projected document %q: %w", ref.Key(), err)
		}
		documents[ref.Key()] = projectionDocument{ref: ref, content: content}
		return nil
	}
	if err := add(model.DocumentRef{Kind: model.DocumentIntent, ID: "root"}, projection.Intent); err != nil {
		return nil, err
	}
	for _, node := range projection.Plan {
		if err := add(model.DocumentRef{Kind: model.DocumentPlan, ID: node.ID}, node); err != nil {
			return nil, err
		}
	}
	for _, entity := range projection.Entities {
		if err := add(model.DocumentRef{Kind: model.DocumentEntity, ID: entity.ID}, entity); err != nil {
			return nil, err
		}
	}
	for _, attachment := range projection.Attachments {
		if err := add(model.DocumentRef{Kind: model.DocumentAttachment, ID: attachment.ID}, attachment); err != nil {
			return nil, err
		}
	}
	for _, fact := range projection.Canon {
		if err := add(model.DocumentRef{Kind: model.DocumentCanon, ID: fact.ID}, fact); err != nil {
			return nil, err
		}
	}
	for _, chapter := range projection.Manuscript {
		if err := add(model.DocumentRef{Kind: model.DocumentManuscript, ID: chapter.ID}, chapter); err != nil {
			return nil, err
		}
	}
	for _, rule := range projection.Ownership {
		if err := add(model.DocumentRef{Kind: model.DocumentOwnership, ID: rule.Target.Key()}, rule); err != nil {
			return nil, err
		}
	}
	for _, directive := range projection.Directives {
		if err := add(model.DocumentRef{Kind: model.DocumentDirective, ID: directive.ID}, directive); err != nil {
			return nil, err
		}
	}
	if projection.Approval != "" {
		setting := model.ApprovalSetting{Policy: projection.Approval}
		if err := add(model.DocumentRef{Kind: model.DocumentApproval, ID: "root"}, setting); err != nil {
			return nil, err
		}
	}
	if len(projection.Overlay) > 0 {
		overlay := model.ProjectOverlay{Rules: projection.Overlay}
		if err := add(model.DocumentRef{Kind: model.DocumentOverlay, ID: "root"}, overlay); err != nil {
			return nil, err
		}
	}
	if projection.Assets != nil {
		if err := add(model.DocumentRef{Kind: model.DocumentAssets, ID: "root"}, *projection.Assets); err != nil {
			return nil, err
		}
	}
	return documents, nil
}
