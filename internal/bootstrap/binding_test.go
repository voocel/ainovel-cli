package bootstrap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	novelapp "github.com/voocel/ainovel-cli/internal/app/novel"
	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	appconfig "github.com/voocel/ainovel-cli/internal/infra/config"
	"github.com/voocel/ainovel-cli/internal/infra/llm/models"
)

// rebindableExecutor 记录每次执行时生效的默认模型：切换应体现在下一次尝试上。
type rebindableExecutor struct {
	*interruptingExecutor
	bindings *models.Bindings
	seen     []string
}

func (e *rebindableExecutor) Bind(bindings models.Bindings) { e.bindings = &bindings }
func (e *rebindableExecutor) Bound() bool                   { return e.bindings != nil }
func (e *rebindableExecutor) Execute(ctx context.Context, operation model.Operation) (model.OperationOutcome, error) {
	e.seen = append(e.seen, e.bindings.Default.Model)
	return e.interruptingExecutor.Execute(ctx, operation)
}

func bindingConfig(model string) appconfig.Config {
	return appconfig.Config{}.WithProvider("deepseek", model, appconfig.ProviderConfig{APIKey: "k"})
}

// 模型绑定是运行时属性（D57）：未绑定时任务只入队不领取；切换模型后，队列里的
// 任务在下一次尝试直接用新模型，不重建任务也不重建应用。
func TestQueuedOperationsRunUnderTheBindingCurrentAtAttemptStart(t *testing.T) {
	authorityStore := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := &rebindableExecutor{interruptingExecutor: &interruptingExecutor{
		scriptedQuickExecutor: &scriptedQuickExecutor{now: testTime(), authorityStore: authorityStore}, cancel: cancel,
	}}
	configDir := t.TempDir()
	api := newTestApp(authorityStore, bootstrap.Options{Executors: task.ExecutorSet{LLM: executor}, ConfigDir: configDir})
	api.now = testTime
	command := novelapp.QuickWriteCommand{
		ProjectID: "quick-book", UserID: "user-1", Premise: "一个失忆的邮差替亡者送完最后一封信",
		Chapters: 2, WorkerID: "quick-worker", LeaseDuration: time.Minute, CreatedAt: testTime(),
	}
	if _, err := api.Novels.QuickWrite(ctx, command); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("unbound runtime must refuse to start: %v", err)
	}
	if err := api.Models.Apply(bindingConfig("model-a")); err != nil {
		t.Fatal(err)
	}
	result, err := api.Novels.QuickWrite(ctx, command)
	if !errors.Is(err, context.Canceled) || result.RunState != model.RunRunning {
		t.Fatalf("interrupted quick write: state=%s err=%v", result.RunState, err)
	}
	background := context.Background()
	if err := api.Models.Switch(bindingConfig("model-b")); err != nil {
		t.Fatal(err)
	}
	command.CreatedAt = testTime().Add(time.Second)
	if result, err = api.Novels.QuickWrite(background, command); err != nil || result.RunState != model.RunCompleted {
		t.Fatalf("resume after switching models: %+v err=%v", result, err)
	}
	resumed, err := authorityStore.GetOperation(background, executor.interrupted)
	if err != nil || resumed.State != model.OperationSucceeded || resumed.Attempt != 2 || resumed.Snapshot.Executor != "llm.agent@1" {
		t.Fatalf("resumed operation = %+v err=%v", resumed, err)
	}
	// 规划与被中断的第一次写章用 model-a；续跑的尝试以及后续任务全部用 model-b。
	if len(executor.seen) < 3 || executor.seen[0] != "model-a" || executor.seen[1] != "model-a" || executor.seen[2] != "model-b" {
		t.Fatalf("models seen per execution = %v", executor.seen)
	}
	for _, seen := range executor.seen[2:] {
		if seen != "model-b" {
			t.Fatalf("later attempts must use the switched model: %v", executor.seen)
		}
	}
	if saved, err := appconfig.LoadConfig(configDir); err != nil || saved.Model != "model-b" {
		t.Fatalf("Switch must persist the new binding: %#v %v", saved, err)
	}
}
