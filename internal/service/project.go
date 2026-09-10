package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/activity"
	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/change"
	"github.com/voocel/ainovel-cli/internal/domain"
	operationengine "github.com/voocel/ainovel-cli/internal/operation"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Service struct {
	store      *store.Store
	changes    *change.Engine
	prompts    *prompt.Registry
	operations *operationengine.Engine
	executors  ExecutorSet
	// derivers 按目标种类登记推导器（D49）：小说是唯一的生产实现，测试在同包内直接赋值。
	derivers map[domain.GoalKind]goalDeriver
	// activity 是可选的实时活动通道（组合根注入，只依赖接口——页面设计 §4）：
	// 易失投影，权威语义不经过它。
	activity ActivityFeed
	// now 是协调器时间源：租约有效期按真实时间对账，必须用真实时钟；测试注入假时钟保证确定性。
	now func() time.Time
}

// ActivityFeed 是实时活动通道的读取契约，由 activity.Hub 实现。
type ActivityFeed interface {
	Subscribe(projectID string) (<-chan struct{}, func())
	Snapshot(projectID string) (activity.Snapshot, bool)
}

// AttachActivityFeed 装配实时活动通道；nil 表示无实时通道，订阅方得到 ok=false。
func (s *Service) AttachActivityFeed(feed ActivityFeed) { s.activity = feed }

// IsNotFound 报告错误是否为"目标不存在"。入口层不接触 store 哨兵错误，
// 通过这里区分可宽容的瞬态（建书起步作品尚未落库）与必须呈现的真实错误。
func IsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// SubscribeRunActivity 订阅作品的实时创作活动：返回合并唤醒信号与取消函数。
// 取消只结束订阅，绝不取消创作（页面设计 §4 交付契约 2）。
func (s *Service) SubscribeRunActivity(projectID string) (<-chan struct{}, func(), bool) {
	if s.activity == nil {
		return nil, func() {}, false
	}
	wake, cancel := s.activity.Subscribe(projectID)
	return wake, cancel, true
}

// RunActivity 整读作品当前活动快照；ok=false 表示无活动或未装配通道。
func (s *Service) RunActivity(projectID string) (activity.Snapshot, bool) {
	if s.activity == nil {
		return activity.Snapshot{}, false
	}
	return s.activity.Snapshot(projectID)
}

func New(authorityStore *store.Store) *Service {
	return &Service{
		store: authorityStore, changes: change.New(authorityStore),
		prompts:    prompt.NewRegistry(authorityStore),
		operations: operationengine.NewEngine(authorityStore),
		derivers:   map[domain.GoalKind]goalDeriver{domain.GoalNovel: novelDeriver{}},
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// ExecutorSet is the static composition of the supported execution families.
// Each operation freezes its executor identity; changing this set never reroutes old work.
type ExecutorSet struct {
	LLM      operationengine.Executor
	External operationengine.Executor
}

func (e ExecutorSet) forFamily(family domain.ExecutorFamily) operationengine.Executor {
	switch family {
	case domain.ExecutorLLM:
		return e.LLM
	case domain.ExecutorExternal:
		return e.External
	}
	return nil
}

func (e ExecutorSet) all() []operationengine.Executor {
	var result []operationengine.Executor
	for _, executor := range []operationengine.Executor{e.LLM, e.External} {
		if executor != nil {
			result = append(result, executor)
		}
	}
	return result
}

// NewWithExecutor is the single LLM runtime convenience constructor.
func NewWithExecutor(authorityStore *store.Store, executor operationengine.Executor) *Service {
	return NewWithExecutors(authorityStore, ExecutorSet{LLM: executor})
}

func NewWithExecutors(authorityStore *store.Store, executors ExecutorSet, contracts ...operationengine.VerdictContract) *Service {
	service := New(authorityStore)
	service.operations = operationengine.NewEngine(authorityStore, contracts...)
	service.executors = executors
	if analyzer, ok := executors.LLM.(change.SemanticAnalyzer); ok {
		service.changes = change.NewWithSemanticAnalyzer(authorityStore, analyzer)
	}
	return service
}

type ProjectDraft struct {
	Intent    domain.Intent          `json:"intent"`
	Plan      []domain.PlanNode      `json:"plan"`
	Entities  []domain.Entity        `json:"entities,omitempty"`
	Canon     []domain.CanonFact     `json:"canon"`
	Ownership []domain.OwnershipRule `json:"ownership"`
	// Approval 是初始化事务写入权威的审批策略（§6.3）；留空表示 auto。
	Approval domain.ApprovalPolicy `json:"approval,omitempty"`
}

type CreateProjectCommand struct {
	ProjectID string
	ChangeID  string
	UserID    string
	Reason    string
	Draft     ProjectDraft
	CreatedAt time.Time
}

type ProjectSnapshot struct {
	ID       string             `json:"id"`
	Revision domain.Revision    `json:"revision"`
	Intent   domain.Intent      `json:"intent"`
	Plan     []domain.PlanNode  `json:"plan"`
	Entities []domain.Entity    `json:"entities,omitempty"`
	Canon    []domain.CanonFact `json:"canon"`
	// CanonRecorded 是按历史来源记录重建的最近入账版本；状态转移到后章不会抹除旧章记录。
	CanonRecorded map[string]domain.Revision `json:"-"`
	Manuscript    []domain.ManuscriptChapter `json:"manuscript"`
	Ownership     []domain.OwnershipRule     `json:"ownership"`
	Directives    []domain.Directive         `json:"directives,omitempty"`
	Attachments   []domain.Attachment        `json:"attachments,omitempty"`
	// Adjudications 是全部用户裁决记录（含撤回，D43）；有效性由证据层按基线判定。
	Adjudications []domain.Adjudication    `json:"adjudications,omitempty"`
	Approval      domain.ApprovalPolicy    `json:"approval,omitempty"`
	Overlay       []string                 `json:"overlay,omitempty"`
	Assets        *domain.ProjectAssetRefs `json:"assets,omitempty"`
	// Index 是快照内每份文档的最后变化 revision 与结构依赖，供证据基线构造与失效判定（D48）。
	Index DocumentIndex `json:"-"`
}

type DocumentIndex map[string]DocumentEntry

type DocumentEntry struct {
	Ref          domain.DocumentRef
	Revision     domain.Revision
	Dependencies []domain.DocumentRef
}

func (index DocumentIndex) add(document domain.DocumentVersion) error {
	dependencies, err := domain.DocumentDependencies(document.Document, document.Content)
	if err != nil {
		return fmt.Errorf("index %s: %w", document.Document.Key(), err)
	}
	index[document.Document.Key()] = DocumentEntry{
		Ref: document.Document, Revision: document.Revision, Dependencies: dependencies,
	}
	return nil
}

// ListProjects 列举作品库中的全部作品。
func (s *Service) ListProjects(ctx context.Context) ([]store.ProjectRecord, error) {
	return s.store.ListProjects(ctx)
}

// DeleteProject 永久删除一部作品的全部数据；进行中（running）的创作需先取消。
func (s *Service) DeleteProject(ctx context.Context, projectID string) error {
	return s.store.DeleteProject(ctx, projectID)
}

func (s *Service) CreateProject(ctx context.Context, command CreateProjectCommand) (ProjectSnapshot, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: command.ProjectID}
	patches, err := projectDraftPatches(command.Draft)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	proposal := domain.Proposal{
		ID: command.ChangeID, Target: target, BaseRevision: domain.InitialRevision,
		Author: domain.Author{Kind: domain.AuthorUser, ID: command.UserID},
		Reason: command.Reason, Patches: patches,
		ApprovalState: domain.ApprovalPending, CreatedAt: command.CreatedAt,
	}
	committed, err := s.commitUserProposal(ctx, proposal, command.CreatedAt)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	return s.Project(ctx, command.ProjectID, committed.NewRevision)
}

func (s *Service) Project(ctx context.Context, projectID string, revision domain.Revision) (ProjectSnapshot, error) {
	target := domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: projectID}
	if revision == domain.InitialRevision {
		var err error
		revision, err = s.store.CurrentRevision(ctx, target)
		if err != nil {
			return ProjectSnapshot{}, err
		}
	}
	result := ProjectSnapshot{ID: projectID, Revision: revision, Index: DocumentIndex{}}
	intent, err := s.store.GetDocument(ctx, target, domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, revision)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if err := json.Unmarshal(intent.Content, &result.Intent); err != nil {
		return ProjectSnapshot{}, fmt.Errorf("decode project intent: %w", err)
	}
	if err := result.Index.add(intent); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Plan, err = loadIndexedDocuments[domain.PlanNode](ctx, s.store, target, domain.DocumentPlan, revision, result.Index); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Entities, err = loadIndexedDocuments[domain.Entity](ctx, s.store, target, domain.DocumentEntity, revision, result.Index); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Canon, err = loadIndexedDocuments[domain.CanonFact](ctx, s.store, target, domain.DocumentCanon, revision, result.Index); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.CanonRecorded, err = s.canonRecorded(ctx, target, revision); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Manuscript, err = loadIndexedDocuments[domain.ManuscriptChapter](ctx, s.store, target, domain.DocumentManuscript, revision, result.Index); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Ownership, err = loadIndexedDocuments[domain.OwnershipRule](ctx, s.store, target, domain.DocumentOwnership, revision, result.Index); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Directives, err = loadIndexedDocuments[domain.Directive](ctx, s.store, target, domain.DocumentDirective, revision, result.Index); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Attachments, err = loadIndexedDocuments[domain.Attachment](ctx, s.store, target, domain.DocumentAttachment, revision, result.Index); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Adjudications, err = loadIndexedDocuments[domain.Adjudication](ctx, s.store, target, domain.DocumentAdjudication, revision, result.Index); err != nil {
		return ProjectSnapshot{}, err
	}
	settings, err := loadIndexedDocuments[domain.ApprovalSetting](ctx, s.store, target, domain.DocumentApproval, revision, result.Index)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if len(settings) > 0 {
		result.Approval = settings[0].Policy
	}
	overlays, err := loadIndexedDocuments[domain.ProjectOverlay](ctx, s.store, target, domain.DocumentOverlay, revision, result.Index)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if len(overlays) > 0 {
		result.Overlay = overlays[0].Rules
	}
	assets, err := loadIndexedDocuments[domain.ProjectAssetRefs](ctx, s.store, target, domain.DocumentAssets, revision, result.Index)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if len(assets) > 0 {
		refs := assets[0]
		result.Assets = &refs
	}
	return result, nil
}

func (s *Service) DerivedDocuments(
	ctx context.Context,
	projectID string,
	revision domain.Revision,
) ([]domain.DerivedDocument, error) {
	if revision == domain.InitialRevision {
		var err error
		revision, err = s.store.CurrentRevision(ctx, domain.AuthorityTarget{Kind: domain.AuthorityProject, ID: projectID})
		if err != nil {
			return nil, err
		}
	}
	return s.store.ListDerivedDocuments(ctx, projectID, revision)
}

func projectDraftPatches(draft ProjectDraft) ([]domain.Patch, error) {
	patches := make([]domain.Patch, 0, 1+len(draft.Plan)+len(draft.Canon)+len(draft.Ownership))
	appendPut := func(ref domain.DocumentRef, value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		patches = append(patches, domain.Patch{Document: ref, Operation: domain.PatchPut, Content: payload})
		return nil
	}
	if err := appendPut(domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, draft.Intent); err != nil {
		return nil, err
	}
	for _, node := range draft.Plan {
		if err := appendPut(domain.DocumentRef{Kind: domain.DocumentPlan, ID: node.ID}, node); err != nil {
			return nil, err
		}
	}
	for _, entity := range draft.Entities {
		if err := appendPut(domain.DocumentRef{Kind: domain.DocumentEntity, ID: entity.ID}, entity); err != nil {
			return nil, err
		}
	}
	for _, fact := range draft.Canon {
		if err := appendPut(domain.DocumentRef{Kind: domain.DocumentCanon, ID: fact.ID}, fact); err != nil {
			return nil, err
		}
	}
	for _, rule := range draft.Ownership {
		if err := appendPut(domain.DocumentRef{Kind: domain.DocumentOwnership, ID: rule.Target.Key()}, rule); err != nil {
			return nil, err
		}
	}
	if draft.Approval != "" {
		setting := domain.ApprovalSetting{Policy: draft.Approval}
		if err := appendPut(domain.DocumentRef{Kind: domain.DocumentApproval, ID: "root"}, setting); err != nil {
			return nil, err
		}
	}
	return patches, nil
}

func loadDocuments[T any](
	ctx context.Context,
	authorityStore *store.Store,
	target domain.AuthorityTarget,
	kind domain.DocumentKind,
	revision domain.Revision,
) ([]T, error) {
	return loadIndexedDocuments[T](ctx, authorityStore, target, kind, revision, nil)
}

// loadIndexedDocuments 解码一类文档并把版本与依赖登记进 index（nil 表示不登记）。
func loadIndexedDocuments[T any](
	ctx context.Context,
	authorityStore *store.Store,
	target domain.AuthorityTarget,
	kind domain.DocumentKind,
	revision domain.Revision,
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
			if err := index.add(document); err != nil {
				return nil, err
			}
		}
		values = append(values, value)
	}
	return values, nil
}
