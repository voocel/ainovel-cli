package capability

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/activity"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
	"github.com/voocel/ainovel-cli/internal/infra/store"
	"github.com/voocel/ainovel-cli/internal/infra/workspace"
)

var (
	ErrNoProposal = errors.New("capability run ended without a proposal")
	ErrNoVerdict  = errors.New("capability run ended without a verdict")
)

// Runtime 是 Capability 执行与语义分析的稳定门面。具体职责按执行、工具、
// 提交校验和分析拆在同包文件中，共享的权威边界只保留在这里。
//
// 模型绑定是运行时属性（D57）：Runtime 常驻，随时可重绑；每次尝试开始时读一次
// 绑定并沿用到结束，在途尝试不受切换影响。
type Runtime struct {
	bindings  atomic.Pointer[models.Bindings]
	store     runtimeStore
	changes   *change.Engine // 只做工具边界的只读结构校验；提交仍由 Operation Engine 经 change 完成
	prompts   *prompt.Registry
	workspace *workspace.Service
	now       func() time.Time
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

// ActivityHub 返回装配的活动 Hub（订阅端用）；装的不是 Hub 时为 nil。
func (r *Runtime) ActivityHub() *activity.Hub {
	hub, _ := r.activity.(*activity.Hub)
	return hub
}

type runtimeStore interface {
	GetDocument(context.Context, model.AuthorityTarget, model.DocumentRef, model.Revision) (model.DocumentVersion, error)
	ListPlanNodes(context.Context, model.AuthorityTarget, model.Revision) ([]model.PlanNode, error)
	GetWorkspaceArtifact(context.Context, string, string) (model.WorkspaceArtifact, error)
	ListWorkspaceArtifacts(context.Context, string) ([]model.WorkspaceArtifact, error)
	PutWorkspaceArtifact(context.Context, model.WorkspaceArtifact, int64, int) (model.WorkspaceArtifact, error)
	AppendOperationEvent(context.Context, model.OperationEvent) (model.OperationEvent, error)
	ListOperationEvents(context.Context, string) ([]model.OperationEvent, error)
}

func NewRuntime(authorityStore *store.Store) *Runtime {
	return &Runtime{
		store: authorityStore, changes: change.New(authorityStore),
		prompts:   prompt.NewRegistry(authorityStore),
		workspace: workspace.New(authorityStore),
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// Identity 是本 Runtime 的执行器身份（D45）：只领取并执行按它冻结的任务。
func (r *Runtime) Identity() string { return prompt.ExecutorIdentity }

// Bind 替换整套模型绑定；下一次开始的尝试起生效。
func (r *Runtime) Bind(bindings models.Bindings) { r.bindings.Store(&bindings) }

// Bound 报告是否已有可执行的模型：未绑定的 Runtime 不领取 LLM 任务。
func (r *Runtime) Bound() bool { return r.bindings.Load() != nil }

// bindingFor 取角色的绑定，未覆盖的角色落到默认；未绑定返回 false。
func (r *Runtime) bindingFor(role string) (models.Binding, bool) {
	bindings := r.bindings.Load()
	if bindings == nil {
		return models.Binding{}, false
	}
	return bindings.For(role), true
}
