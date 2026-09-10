package capability

import (
	"context"
	"errors"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/store"
	"github.com/voocel/ainovel-cli/internal/infra/workspace"
)

var (
	ErrNoProposal = errors.New("capability run ended without a proposal")
	ErrNoVerdict  = errors.New("capability run ended without a verdict")
)

// Runtime 是 Capability 执行与语义分析的稳定门面。具体职责按执行、工具、
// 提交校验和分析拆在同包文件中，共享的权威边界只保留在这里。
type Runtime struct {
	model       agentcore.ChatModel
	store       runtimeStore
	prompts     *prompt.Registry
	workspace   *workspace.Service
	now         func() time.Time
	modelDigest string
	// activity 是可选的实时活动通道（页面设计 §4）：nil 表示未装配，零开销。
	activity ActivitySink
}

// ActivitySink 接收带归属的实时活动事件，由组合根注入。实现必须非阻塞：
// agent 事件监听器是同步执行的，任何等待都会拖慢模型循环。
type ActivitySink interface {
	Publish(activity.Event)
}

// SetActivitySink 装配实时活动通道；须在执行开始前完成。
func (r *Runtime) SetActivitySink(sink ActivitySink) { r.activity = sink }

type runtimeStore interface {
	GetDocument(context.Context, model.AuthorityTarget, model.DocumentRef, model.Revision) (model.DocumentVersion, error)
	ListPlanNodes(context.Context, model.AuthorityTarget, model.Revision) ([]model.PlanNode, error)
	ListDocuments(context.Context, model.AuthorityTarget, model.DocumentKind, model.Revision) ([]model.DocumentVersion, error)
	GetWorkspaceArtifact(context.Context, string, string) (model.WorkspaceArtifact, error)
	ListWorkspaceArtifacts(context.Context, string) ([]model.WorkspaceArtifact, error)
	PutWorkspaceArtifact(context.Context, model.WorkspaceArtifact, int64, int) (model.WorkspaceArtifact, error)
	AppendOperationEvent(context.Context, model.OperationEvent) (model.OperationEvent, error)
	ListOperationEvents(context.Context, string) ([]model.OperationEvent, error)
}

func NewRuntime(model agentcore.ChatModel, modelDigest string, authorityStore *store.Store) *Runtime {
	return &Runtime{
		model: model, store: authorityStore, prompts: prompt.NewRegistry(authorityStore),
		workspace:   workspace.New(authorityStore),
		now:         func() time.Time { return time.Now().UTC() },
		modelDigest: modelDigest,
	}
}

// Identity 是本 Runtime 的执行器身份（D45）：只领取并执行按它冻结的任务。
func (r *Runtime) Identity() string {
	return prompt.ExecutorIdentity(r.modelDigest)
}

func (r *Runtime) ModelConfigDigest() string {
	return r.modelDigest
}
