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
	executor   operationengine.Executor
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
		now:        func() time.Time { return time.Now().UTC() },
	}
}

func NewWithExecutor(authorityStore *store.Store, executor operationengine.Executor) *Service {
	service := New(authorityStore)
	service.executor = executor
	if analyzer, ok := executor.(change.SemanticAnalyzer); ok {
		service.changes = change.NewWithSemanticAnalyzer(authorityStore, analyzer)
	}
	return service
}

type ProjectDraft struct {
	Intent    domain.Intent          `json:"intent"`
	Plan      []domain.PlanNode      `json:"plan"`
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
	ID         string                     `json:"id"`
	Revision   domain.Revision            `json:"revision"`
	Intent     domain.Intent              `json:"intent"`
	Plan       []domain.PlanNode          `json:"plan"`
	Canon      []domain.CanonFact         `json:"canon"`
	Manuscript []domain.ManuscriptChapter `json:"manuscript"`
	Ownership  []domain.OwnershipRule     `json:"ownership"`
	Directives []domain.Directive         `json:"directives,omitempty"`
	Approval   domain.ApprovalPolicy      `json:"approval,omitempty"`
	Overlay    []string                   `json:"overlay,omitempty"`
	Assets     *domain.ProjectAssetRefs   `json:"assets,omitempty"`
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
	result := ProjectSnapshot{ID: projectID, Revision: revision}
	intent, err := s.store.GetDocument(ctx, target, domain.DocumentRef{Kind: domain.DocumentIntent, ID: "root"}, revision)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if err := json.Unmarshal(intent.Content, &result.Intent); err != nil {
		return ProjectSnapshot{}, fmt.Errorf("decode project intent: %w", err)
	}
	if result.Plan, err = loadDocuments[domain.PlanNode](ctx, s.store, target, domain.DocumentPlan, revision); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Canon, err = loadDocuments[domain.CanonFact](ctx, s.store, target, domain.DocumentCanon, revision); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Manuscript, err = loadDocuments[domain.ManuscriptChapter](ctx, s.store, target, domain.DocumentManuscript, revision); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Ownership, err = loadDocuments[domain.OwnershipRule](ctx, s.store, target, domain.DocumentOwnership, revision); err != nil {
		return ProjectSnapshot{}, err
	}
	if result.Directives, err = loadDocuments[domain.Directive](ctx, s.store, target, domain.DocumentDirective, revision); err != nil {
		return ProjectSnapshot{}, err
	}
	settings, err := loadDocuments[domain.ApprovalSetting](ctx, s.store, target, domain.DocumentApproval, revision)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if len(settings) > 0 {
		result.Approval = settings[0].Policy
	}
	overlays, err := loadDocuments[domain.ProjectOverlay](ctx, s.store, target, domain.DocumentOverlay, revision)
	if err != nil {
		return ProjectSnapshot{}, err
	}
	if len(overlays) > 0 {
		result.Overlay = overlays[0].Rules
	}
	assets, err := loadDocuments[domain.ProjectAssetRefs](ctx, s.store, target, domain.DocumentAssets, revision)
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
		values = append(values, value)
	}
	return values, nil
}
