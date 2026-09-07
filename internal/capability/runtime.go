package capability

import (
	"context"
	"errors"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/activity"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/workspace"
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
	GetDocument(context.Context, domain.AuthorityTarget, domain.DocumentRef, domain.Revision) (domain.DocumentVersion, error)
	ListPlanNodes(context.Context, domain.AuthorityTarget, domain.Revision) ([]domain.PlanNode, error)
	GetWorkspaceArtifact(context.Context, string, string) (domain.WorkspaceArtifact, error)
	ListWorkspaceArtifacts(context.Context, string) ([]domain.WorkspaceArtifact, error)
	PutWorkspaceArtifact(context.Context, domain.WorkspaceArtifact, int64) (domain.WorkspaceArtifact, error)
	AppendOperationEvent(context.Context, domain.OperationEvent) (domain.OperationEvent, error)
	ListOperationEvents(context.Context, string) ([]domain.OperationEvent, error)
}

func NewRuntime(model agentcore.ChatModel, modelDigest string, authorityStore *store.Store) *Runtime {
	return &Runtime{
		model: model, store: authorityStore,
		workspace:   workspace.New(authorityStore),
		now:         func() time.Time { return time.Now().UTC() },
		modelDigest: modelDigest,
	}
}

func (r *Runtime) ModelConfigDigest() string {
	return r.modelDigest
}
