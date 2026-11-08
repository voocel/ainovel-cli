package agents

// 端到端验证 save_review 硬停 + 任务感知 StopGuard 的组合行为（build.go editor
// 配置的真实接线：StopAfterToolResult 命中 save_review/save_*_summary、
// StopGuardFactory 用真 guard、工具落真 checkpoint）。
//
// 场景一（摘要任务先复核）：editor 被派生成弧摘要，却先调了 save_review——
// 硬停触发但 guard 否决，注入催促后 editor 走到 save_arc_summary 才真正退出。
// 这是恢复 save_review 硬停的安全前提，防止弧摘要永不落盘的死循环回归。
//
// 场景二（评审任务一步收尾）：editor 被派评审，save_review 落盘即硬停放行，
// 不再多跑一轮 LLM 收尾。

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/internal/agents/guard"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// editorStopAfterToolResult 与 build.go 中 editor 的配置保持同一判据。
func editorStopAfterToolResult(toolName string, _ json.RawMessage) bool {
	return toolName == "save_review" || toolName == "save_review_check" || toolName == "save_arc_summary" || toolName == "save_volume_summary"
}

func checkpointTool(t *testing.T, st *store.Store, name, step string) agentcore.Tool {
	t.Helper()
	return agentcore.NewFuncTool(name, "fake "+name, map[string]any{"type": "object"},
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if _, err := st.Checkpoints.Append(domain.ArcScope(1, 1), step, "artifact", "digest"); err != nil {
				t.Fatalf("append checkpoint %s: %v", step, err)
			}
			return json.RawMessage(`"saved"`), nil
		})
}

func runEditorLike(t *testing.T, st *store.Store, task string, model agentcore.ChatModel, tools []agentcore.Tool) {
	t.Helper()
	cfg := subagent.Config{
		Name:                "editor",
		Description:         "test editor",
		Model:               model,
		SystemPrompt:        "test",
		Tools:               tools,
		MaxTurns:            10,
		StopAfterToolResult: editorStopAfterToolResult,
		StopGuardFactory: func(_, task string) agentcore.StopGuard {
			return guard.NewEditorStopGuard(st, task, nil)
		},
	}
	tool := subagent.NewRunner(cfg).AsTool()
	args, _ := json.Marshal(map[string]string{"agent": "editor", "task": task})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("subagent execute: %v", err)
	}
}

func TestEditorFlow_SummaryTaskSurvivesEarlyReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}

	var calls atomic.Int32
	model := &contractModel{fn: func(i int, _ []agentcore.Message) (*agentcore.LLMResponse, error) {
		switch i {
		case 0:
			// 跑偏：摘要任务却先复核。
			return &agentcore.LLMResponse{Message: assistantToolCall("save_review", `{}`)}, nil
		default:
			// guard 否决硬停并注入催促后，本轮才产出摘要。
			calls.Add(1)
			return &agentcore.LLMResponse{Message: assistantToolCall("save_arc_summary", `{}`)}, nil
		}
	}}

	runEditorLike(t, st, "生成第 1 卷第 1 弧摘要（save_arc_summary）", model, []agentcore.Tool{
		checkpointTool(t, st, "save_review", "review"),
		checkpointTool(t, st, "save_arc_summary", "arc_summary"),
	})

	if calls.Load() == 0 {
		t.Fatal("save_review 硬停被 guard 否决后，editor 应继续走到 save_arc_summary——若 run 在复核后直接结束，说明终态退出绕过了 guard，弧摘要死循环会回归")
	}
	all := st.Checkpoints.All()
	var hasSummary bool
	for _, cp := range all {
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

	model := &contractModel{fn: func(i int, _ []agentcore.Message) (*agentcore.LLMResponse, error) {
		if i == 0 {
			return &agentcore.LLMResponse{Message: assistantToolCall("save_review", `{}`)}, nil
		}
		t.Fatal("评审任务 save_review 落盘后应硬停，模型不应获得额外轮次")
		return nil, nil
	}}

	runEditorLike(t, st, "对第 1 卷第 1 弧做弧级评审（scope=arc）", model, []agentcore.Tool{
		checkpointTool(t, st, "save_review", "review"),
	})

	if got := model.calls(); got != 1 {
		t.Fatalf("评审任务应恰好一次模型调用后收尾，got %d", got)
	}
}

func TestEditorFlow_ReviewCheckStopsWithoutExtraTurn(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	model := &contractModel{fn: func(i int, _ []agentcore.Message) (*agentcore.LLMResponse, error) {
		if i > 0 {
			t.Fatal("定向复核落盘后不得再增加模型轮次")
		}
		return &agentcore.LLMResponse{Message: assistantToolCall("save_review_check", `{}`)}, nil
	}}
	runEditorLike(t, st, "复核原问题并用 save_review_check 落盘", model, []agentcore.Tool{
		checkpointTool(t, st, "save_review_check", "review_check"),
	})
}
