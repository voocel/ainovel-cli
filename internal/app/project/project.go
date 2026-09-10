package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

type Repository struct {
	store   *store.Store
	changes *change.Engine
}

func New(s *store.Store, changes *change.Engine) *Repository {
	return &Repository{store: s, changes: changes}
}

type ProjectDraft struct {
	Intent    model.Intent          `json:"intent"`
	Plan      []model.PlanNode      `json:"plan"`
	Entities  []model.Entity        `json:"entities,omitempty"`
	Canon     []model.CanonFact     `json:"canon"`
	Ownership []model.OwnershipRule `json:"ownership"`
	// Approval 是初始化事务写入权威的审批策略（§6.3）；留空表示 auto。
	Approval model.ApprovalPolicy `json:"approval,omitempty"`
}

type CreateProjectCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	Reason    string
	Draft     ProjectDraft
	CreatedAt time.Time
}

type Snapshot struct {
	ID       string            `json:"id"`
	Revision model.Revision    `json:"revision"`
	Intent   model.Intent      `json:"intent"`
	Plan     []model.PlanNode  `json:"plan"`
	Entities []model.Entity    `json:"entities,omitempty"`
	Canon    []model.CanonFact `json:"canon"`
	// CanonRecorded 是按历史来源记录重建的最近入账版本；状态转移到后章不会抹除旧章记录。
	CanonRecorded map[string]model.Revision `json:"-"`
	Manuscript    []model.ManuscriptChapter `json:"manuscript"`
	Ownership     []model.OwnershipRule     `json:"ownership"`
	Directives    []model.Directive         `json:"directives,omitempty"`
	Attachments   []model.Attachment        `json:"attachments,omitempty"`
	// Adjudications 是全部用户裁决记录（含撤回，D43）；有效性由证据层按基线判定。
	Adjudications []model.Adjudication    `json:"adjudications,omitempty"`
	Approval      model.ApprovalPolicy    `json:"approval,omitempty"`
	Overlay       []string                `json:"overlay,omitempty"`
	Assets        *model.ProjectAssetRefs `json:"assets,omitempty"`
	// Index 是快照内每份文档的最后变化 revision 与结构依赖，供证据基线构造与失效判定（D48）。
	Index DocumentIndex `json:"-"`
}

type DocumentIndex map[string]DocumentEntry

type DocumentEntry struct {
	Ref          model.DocumentRef
	Revision     model.Revision
	Dependencies []model.DocumentRef
}

func (index DocumentIndex) Add(document model.DocumentVersion) error {
	dependencies, err := model.DocumentDependencies(document.Document, document.Content)
	if err != nil {
		return fmt.Errorf("index %s: %w", document.Document.Key(), err)
	}
	index[document.Document.Key()] = DocumentEntry{
		Ref: document.Document, Revision: document.Revision, Dependencies: dependencies,
	}
	return nil
}

// ListProjects 列举作品库中的全部作品。
func (s *Repository) ListProjects(ctx context.Context) ([]model.ProjectRecord, error) {
	return s.store.ListProjects(ctx)
}

// DeleteProject 永久删除一部作品的全部数据；进行中（running）的创作需先取消。
func (s *Repository) DeleteProject(ctx context.Context, projectID string) error {
	return s.store.DeleteProject(ctx, projectID)
}

func (s *Repository) CreateProject(ctx context.Context, command CreateProjectCommand) (Snapshot, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: command.ProjectID}
	patches, err := projectDraftPatches(command.Draft)
	if err != nil {
		return Snapshot{}, err
	}
	proposal := model.Proposal{
		ID: command.ChangeID, Target: target, BaseRevision: model.InitialRevision,
		Author: model.Author{Kind: model.AuthorUser, ID: command.UserID},
		Reason: command.Reason, Patches: patches,
		ApprovalState: model.ApprovalPending, CreatedAt: command.CreatedAt,
	}
	committed, err := s.changes.CommitUser(ctx, proposal, command.CreatedAt)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Project(ctx, command.ProjectID, committed.NewRevision)
}

func (s *Repository) Project(ctx context.Context, projectID string, revision model.Revision) (Snapshot, error) {
	target := model.AuthorityTarget{Kind: model.AuthorityProject, ID: projectID}
	if revision == model.InitialRevision {
		var err error
		revision, err = s.store.CurrentRevision(ctx, target)
		if err != nil {
			return Snapshot{}, err
		}
	}
	result := Snapshot{ID: projectID, Revision: revision, Index: DocumentIndex{}}
	intent, err := s.store.GetDocument(ctx, target, model.DocumentRef{Kind: model.DocumentIntent, ID: "root"}, revision)
	if err != nil {
		return Snapshot{}, err
	}
	if err := json.Unmarshal(intent.Content, &result.Intent); err != nil {
		return Snapshot{}, fmt.Errorf("decode project intent: %w", err)
	}
	if err := result.Index.Add(intent); err != nil {
		return Snapshot{}, err
	}
	if result.Plan, err = loadIndexedDocuments[model.PlanNode](ctx, s.store, target, model.DocumentPlan, revision, result.Index); err != nil {
		return Snapshot{}, err
	}
	if result.Entities, err = loadIndexedDocuments[model.Entity](ctx, s.store, target, model.DocumentEntity, revision, result.Index); err != nil {
		return Snapshot{}, err
	}
	if result.Canon, err = loadIndexedDocuments[model.CanonFact](ctx, s.store, target, model.DocumentCanon, revision, result.Index); err != nil {
		return Snapshot{}, err
	}
	if result.CanonRecorded, err = s.canonRecorded(ctx, target, revision); err != nil {
		return Snapshot{}, err
	}
	if result.Manuscript, err = loadIndexedDocuments[model.ManuscriptChapter](ctx, s.store, target, model.DocumentManuscript, revision, result.Index); err != nil {
		return Snapshot{}, err
	}
	if result.Ownership, err = loadIndexedDocuments[model.OwnershipRule](ctx, s.store, target, model.DocumentOwnership, revision, result.Index); err != nil {
		return Snapshot{}, err
	}
	if result.Directives, err = loadIndexedDocuments[model.Directive](ctx, s.store, target, model.DocumentDirective, revision, result.Index); err != nil {
		return Snapshot{}, err
	}
	if result.Attachments, err = loadIndexedDocuments[model.Attachment](ctx, s.store, target, model.DocumentAttachment, revision, result.Index); err != nil {
		return Snapshot{}, err
	}
	if result.Adjudications, err = loadIndexedDocuments[model.Adjudication](ctx, s.store, target, model.DocumentAdjudication, revision, result.Index); err != nil {
		return Snapshot{}, err
	}
	settings, err := loadIndexedDocuments[model.ApprovalSetting](ctx, s.store, target, model.DocumentApproval, revision, result.Index)
	if err != nil {
		return Snapshot{}, err
	}
	if len(settings) > 0 {
		result.Approval = settings[0].Policy
	}
	overlays, err := loadIndexedDocuments[model.ProjectOverlay](ctx, s.store, target, model.DocumentOverlay, revision, result.Index)
	if err != nil {
		return Snapshot{}, err
	}
	if len(overlays) > 0 {
		result.Overlay = overlays[0].Rules
	}
	assets, err := loadIndexedDocuments[model.ProjectAssetRefs](ctx, s.store, target, model.DocumentAssets, revision, result.Index)
	if err != nil {
		return Snapshot{}, err
	}
	if len(assets) > 0 {
		refs := assets[0]
		result.Assets = &refs
	}
	return result, nil
}

func (s *Repository) DerivedDocuments(
	ctx context.Context,
	projectID string,
	revision model.Revision,
) ([]model.DerivedDocument, error) {
	if revision == model.InitialRevision {
		var err error
		revision, err = s.store.CurrentRevision(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: projectID})
		if err != nil {
			return nil, err
		}
	}
	return s.store.ListDerivedDocuments(ctx, projectID, revision)
}

func projectDraftPatches(draft ProjectDraft) ([]model.Patch, error) {
	patches := make([]model.Patch, 0, 1+len(draft.Plan)+len(draft.Canon)+len(draft.Ownership))
	appendPut := func(ref model.DocumentRef, value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		patches = append(patches, model.Patch{Document: ref, Operation: model.PatchPut, Content: payload})
		return nil
	}
	if err := appendPut(model.DocumentRef{Kind: model.DocumentIntent, ID: "root"}, draft.Intent); err != nil {
		return nil, err
	}
	for _, node := range draft.Plan {
		if err := appendPut(model.DocumentRef{Kind: model.DocumentPlan, ID: node.ID}, node); err != nil {
			return nil, err
		}
	}
	for _, entity := range draft.Entities {
		if err := appendPut(model.DocumentRef{Kind: model.DocumentEntity, ID: entity.ID}, entity); err != nil {
			return nil, err
		}
	}
	for _, fact := range draft.Canon {
		if err := appendPut(model.DocumentRef{Kind: model.DocumentCanon, ID: fact.ID}, fact); err != nil {
			return nil, err
		}
	}
	for _, rule := range draft.Ownership {
		if err := appendPut(model.DocumentRef{Kind: model.DocumentOwnership, ID: rule.Target.Key()}, rule); err != nil {
			return nil, err
		}
	}
	if draft.Approval != "" {
		setting := model.ApprovalSetting{Policy: draft.Approval}
		if err := appendPut(model.DocumentRef{Kind: model.DocumentApproval, ID: "root"}, setting); err != nil {
			return nil, err
		}
	}
	return patches, nil
}

func LoadDocuments[T any](
	ctx context.Context,
	authorityStore *store.Store,
	target model.AuthorityTarget,
	kind model.DocumentKind,
	revision model.Revision,
) ([]T, error) {
	return loadIndexedDocuments[T](ctx, authorityStore, target, kind, revision, nil)
}

// loadIndexedDocuments 解码一类文档并把版本与依赖登记进 index（nil 表示不登记）。
func loadIndexedDocuments[T any](
	ctx context.Context,
	authorityStore *store.Store,
	target model.AuthorityTarget,
	kind model.DocumentKind,
	revision model.Revision,
	index DocumentIndex,
) ([]T, error) {
	documents, err := authorityStore.ListDocuments(ctx, target, kind, revision)
	if err != nil {
		return nil, err
	}
	values := make([]T, 0, len(documents))
	for _, document := range documents {
		var value T
		if err := json.Unmarshal(document.Content, &value); err != nil {
			return nil, fmt.Errorf("decode %s document %q: %w", kind, document.Document.ID, err)
		}
		if index != nil {
			if err := index.Add(document); err != nil {
				return nil, err
			}
		}
		values = append(values, value)
	}
	return values, nil
}

func IsNotFound(err error) bool { return errors.Is(err, model.ErrNotFound) }
