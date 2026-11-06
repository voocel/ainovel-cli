package agents

// 端到端验证 save_review 硬停（Result.Terminate）+ 任务感知 OnStop 的组合行为
// （build.go editor 配置的真实接线：terminateOn(stopAfterToolSet(save_review/
// save_arc_summary/save_volume_summary))、stopFactories["editor"] 用真 guard、
// 工具落真 checkpoint）。
//
// 场景一（摘要任务先复核）：editor 被派生成弧摘要，却先调了 save_review——
// 终态退出触发但 guard 否决，注入催促后 editor 走到 save_arc_summary 才真正退出。
// 这是恢复 save_review 硬停的安全前提，防止弧摘要永不落盘的死循环回归。
//
// 场景二（评审任务一步收尾）：editor 被派评审，save_review 落盘即终停放行，
// 不再多跑一轮 LLM 收尾。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/ainovel-cli/internal/agents/guard"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func checkpointTool(t *testing.T, st *store.Store, name, step string) agentcore.Tool {
	t.Helper()
	return agentcore.Tool{
		Name:        name,
		Description: "fake " + name,
		Schema:      map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage) (agentcore.Result, error) {
			if _, err := st.Checkpoints.Append(domain.ArcScope(1, 1), step, "artifact", "digest"); err != nil {
				t.Fatalf("append checkpoint %s: %v", step, err)
			}
			return agentcore.TextResult("saved"), nil
		},
	}
}

// runEditorLike 按 build.go 的 editor 接线组装 WorkerSet 并运行一次。
func runEditorLike(t *testing.T, st *store.Store, task string, model agentcore.Model, tools []agentcore.Tool) error {
	t.Helper()
	cfg := agentcore.Config{
		Model:    model,
		System:   []litellm.Block{litellm.Text("test")},
		Tools:    terminateOn(tools, stopAfterToolSet("save_review", "save_arc_summary", "save_volume_summary")),
		MaxTurns: 10,
	}
	workers := NewWorkerSet(map[string]agentcore.Config{"editor": cfg}, []string{"editor"})
	workers.stopFactories["editor"] = func(task string) StopHook {
		return guard.NewEditorStopGuard(st, task, nil)
	}
	_, err := workers.Run(context.Background(), "editor", task)
	return err
}

func TestEditorFlow_SummaryTaskSurvivesEarlyReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}

	model, provider := contractModel(t,
		// 跑偏：摘要任务却先复核。
		litellmtest.Respond(litellm.ToolUseBlock{ID: "tc-review", Name: "save_review", Arguments: "{}"}),
		// guard 否决硬停并注入催促后，本轮才产出摘要。
		litellmtest.Respond(litellm.ToolUseBlock{ID: "tc-arc", Name: "save_arc_summary", Arguments: "{}"}),
	)

	if err := runEditorLike(t, st, "生成第 1 卷第 1 弧摘要（save_arc_summary）", model, []agentcore.Tool{
		checkpointTool(t, st, "save_review", "review"),
		checkpointTool(t, st, "save_arc_summary", "arc_summary"),
	}); err != nil {
		t.Fatalf("editor run: %v", err)
	}

	if got := len(provider.Requests()); got < 2 {
		t.Fatalf("save_review 终态退出被 guard 否决后，editor 应继续走到 save_arc_summary"+
			"——若 run 在复核后直接结束，说明终态退出绕过了 OnStop，弧摘要死循环会回归（请求数 %d）", got)
	}
	var hasSummary bool
	for _, cp := range st.Checkpoints.All() {
		if cp.Step == "arc_summary" {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Fatal("弧摘要必须最终落盘")
	}
}

func TestEditorFlow_ReviewTaskStopsAtSaveReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}

	model, provider := contractModel(t,
		litellmtest.Respond(litellm.ToolUseBlock{ID: "tc-review", Name: "save_review", Arguments: "{}"}),
	)

	if err := runEditorLike(t, st, "对第 1 卷第 1 弧做弧级评审（scope=arc）", model, []agentcore.Tool{
		checkpointTool(t, st, "save_review", "review"),
	}); err != nil {
		t.Fatalf("editor run: %v", err)
	}

	if got := len(provider.Requests()); got != 1 {
		t.Fatalf("评审任务 save_review 落盘后应终停，模型不应获得额外轮次，got %d 次请求", got)
	}
}
