package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// helper：构造一个处于 Writing 阶段、分层模式的 Progress。
func writingProgress(completed []int, flow domain.FlowState) *domain.Progress {
	return &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              flow,
		Layered:           true,
		CompletedChapters: completed,
	}
}

func TestRoute_NilProgress(t *testing.T) {
	if got := Route(State{Progress: nil}); got != nil {
		t.Fatalf("expected nil for nil progress, got %+v", got)
	}
}

func TestRoute_PhaseComplete(t *testing.T) {
	s := State{Progress: &domain.Progress{Phase: domain.PhaseComplete}}
	if got := Route(s); got != nil {
		t.Fatalf("expected nil at PhaseComplete, got %+v", got)
	}
}

func TestRoute_NonWritingPhasesDelegateToLLM(t *testing.T) {
	for _, phase := range []domain.Phase{domain.PhaseInit, domain.PhasePremise, domain.PhaseOutline} {
		s := State{Progress: &domain.Progress{Phase: phase}, FoundationMissing: []string{"premise"}}
		if got := Route(s); got != nil {
			t.Fatalf("phase %s should return nil, got %+v", phase, got)
		}
	}
}

func TestRoute_PendingRewritesFirst(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowRewriting)
	p.PendingRewrites = []int{3, 5}
	got := Route(State{Progress: p})
	if got == nil || got.Agent != "writer" {
		t.Fatalf("expected writer for rewrites, got %+v", got)
	}
	if got.Task != "重写第 3 章" {
		t.Errorf("expected '重写第 3 章', got %q", got.Task)
	}
	if got.Chapter != 3 {
		t.Errorf("expected Chapter=3, got %d", got.Chapter)
	}
}

func TestRoute_PendingPolishingVerb(t *testing.T) {
	p := writingProgress([]int{1}, domain.FlowPolishing)
	p.PendingRewrites = []int{2}
	got := Route(State{Progress: p})
	if got == nil || got.Task != "打磨第 2 章" {
		t.Fatalf("expected polish verb, got %+v", got)
	}
}

func TestRoute_ReviewingDelegatesToLLM(t *testing.T) {
	p := writingProgress([]int{1, 2}, domain.FlowReviewing)
	if got := Route(State{Progress: p}); got != nil {
		t.Fatalf("expected nil during reviewing, got %+v", got)
	}
}

func TestRoute_SteeringDelegatesToLLM(t *testing.T) {
	p := writingProgress([]int{1}, domain.FlowSteering)
	if got := Route(State{Progress: p}); got != nil {
		t.Fatalf("expected nil during steering, got %+v", got)
	}
}

func TestRoute_ArcEndNeedsReview(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd: true,
			Volume:   1,
			Arc:      2,
		},
	}
	got := Route(s)
	if got == nil || got.Agent != "editor" {
		t.Fatalf("expected editor for arc review, got %+v", got)
	}
	if got.Reason != "弧末评审未完成" {
		t.Errorf("reason mismatch: %q", got.Reason)
	}
}

func TestRoute_ArcEndHasReviewNeedsSummary(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd: true,
			Volume:   1,
			Arc:      2,
		},
		HasArcReview: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "editor" || got.Reason != "弧摘要未完成" {
		t.Fatalf("expected arc summary editor call, got %+v", got)
	}
}

func TestRoute_VolumeEndNeedsVolumeSummary(t *testing.T) {
	p := writingProgress([]int{20}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 20,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:    true,
			IsVolumeEnd: true,
			Volume:      1,
			Arc:         3,
		},
		HasArcReview:  true,
		HasArcSummary: true,
	}
	got := Route(s)
	if got == nil || got.Reason != "卷摘要未完成" {
		t.Fatalf("expected volume summary request, got %+v", got)
	}
}

func TestRoute_NeedsArcExpansion(t *testing.T) {
	p := writingProgress([]int{10}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			Volume:         1,
			Arc:            2,
			NextVolume:     1,
			NextArc:        3,
			NeedsExpansion: true,
		},
		HasArcReview:  true,
		HasArcSummary: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "architect_long" {
		t.Fatalf("expected architect_long for expansion, got %+v", got)
	}
	if got.Reason != "下一弧骨架待展开" {
		t.Errorf("reason mismatch: %q", got.Reason)
	}
}

func TestRoute_NeedsNewVolume(t *testing.T) {
	p := writingProgress([]int{30}, domain.FlowWriting)
	s := State{
		Progress:      p,
		LastCompleted: 30,
		ArcBoundary: &storepkg.ArcBoundary{
			IsArcEnd:       true,
			IsVolumeEnd:    true,
			Volume:         2,
			Arc:            4,
			NeedsNewVolume: true,
		},
		HasArcReview:     true,
		HasArcSummary:    true,
		HasVolumeSummary: true,
	}
	got := Route(s)
	if got == nil || got.Agent != "architect_long" || got.Reason != "卷末需决定追加新卷、收官卷或结束全书" {
		t.Fatalf("expected append_volume/complete_book dispatch, got %+v", got)
	}
}

func TestRoute_NormalContinue(t *testing.T) {
	p := writingProgress([]int{1, 2, 3}, domain.FlowWriting)
	p.TotalChapters = 20
	got := Route(State{Progress: p, LastCompleted: 3})
	if got == nil || got.Agent != "writer" {
		t.Fatalf("expected writer for next chapter, got %+v", got)
	}
	if got.Task != "写第 4 章" {
		t.Errorf("expected '写第 4 章', got %q", got.Task)
	}
	if got.Chapter != 4 {
		t.Errorf("expected Chapter=4, got %d", got.Chapter)
	}
}

func TestRoute_ArcEndNonLayeredSkipsBoundary(t *testing.T) {
	// 非 Layered 模式即使 ArcBoundary 非 nil 也不走弧末分支
	p := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           false,
		CompletedChapters: []int{10},
		TotalChapters:     20,
	}
	s := State{
		Progress:      p,
		LastCompleted: 10,
		ArcBoundary:   &storepkg.ArcBoundary{IsArcEnd: true, Volume: 1, Arc: 2},
	}
	got := Route(s)
	if got == nil || got.Agent != "writer" {
		t.Fatalf("non-layered should fall through to writer, got %+v", got)
	}
}

func TestFormatMessage(t *testing.T) {
	msg := FormatMessage(&Instruction{Agent: "writer", Task: "写第 5 章", Reason: "续写"})
	for _, want := range []string{"[Host 下达指令]", "subagent(writer, \"写第 5 章\")", "agent: writer", "task: \"写第 5 章\"", "续写", "必须原样使用", "不要改写 task", "不要先调 novel_context"} {
		if !contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDispatcher_TrackRepeat(t *testing.T) {
	// 不需要真实 coordinator / store；trackRepeat 只读自己的缓存。
	d := &Dispatcher{}
	inst := &Instruction{Agent: "writer", Task: "写第 5 章", Reason: "续写"}
	if got := d.trackRepeat(inst); got != 1 {
		t.Fatalf("首次下达应计 1，got %d", got)
	}
	if got := d.trackRepeat(inst); got != 2 {
		t.Fatalf("同 Agent+Task 重复下达应计 2，got %d", got)
	}
	// Reason 不同、Agent+Task 相同时视为同一指令继续累计
	sameTaskDiffReason := &Instruction{Agent: "writer", Task: "写第 5 章", Reason: "弧末后继续"}
	if got := d.trackRepeat(sameTaskDiffReason); got != 3 {
		t.Fatalf("仅 Reason 不同应视为重复累计到 3，got %d", got)
	}
	other := &Instruction{Agent: "writer", Task: "写第 6 章", Reason: "续写"}
	if got := d.trackRepeat(other); got != 1 {
		t.Fatalf("Task 变更后应重置为 1，got %d", got)
	}
	d.ResetRepeat()
	if got := d.trackRepeat(other); got != 1 {
		t.Fatalf("ResetRepeat 后首次应计 1，got %d", got)
	}
}

func TestFormatDispatchMessage_RepeatNotice(t *testing.T) {
	inst := &Instruction{Agent: "writer", Task: "写第 5 章", Reason: "续写"}
	first := formatDispatchMessage(inst, 1)
	if first != FormatMessage(inst) {
		t.Fatalf("首次下达不应附加重复注记: %s", first)
	}
	third := formatDispatchMessage(inst, 3)
	for _, want := range []string{"第 3 次下达", "路由事实未变化", "novel_context", "改派"} {
		if !contains(third, want) {
			t.Errorf("重复注记缺少 %q: %s", want, third)
		}
	}
}

func TestDispatcher_OnRepeatFiresOnceAtThreshold(t *testing.T) {
	d := &Dispatcher{}
	var fired []string
	d.SetOnRepeat(func(agent, task string, n int) {
		fired = append(fired, fmt.Sprintf("%s|%s|%d", agent, task, n))
	})

	inst := &Instruction{Agent: "writer", Task: "写第 5 章"}
	for range 6 {
		d.trackRepeat(inst) // n=1..6：只在 n==3 时回调一次
	}
	if len(fired) != 1 || fired[0] != fmt.Sprintf("writer|写第 5 章|%d", repeatNotifyAt) {
		t.Fatalf("应恰好在第 %d 次触发一次，got %v", repeatNotifyAt, fired)
	}

	// 键变更后重新武装：换任务再连续 3 次 → 再触发一次
	other := &Instruction{Agent: "writer", Task: "写第 6 章"}
	for range 3 {
		d.trackRepeat(other)
	}
	if len(fired) != 2 {
		t.Fatalf("键变更后应重新武装，got %v", fired)
	}
}

func TestDispatcher_SteersAfterSuccessfulBoundaryToolBeforeNextModelCall(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	if err := st.Progress.Init("test", 3); err != nil {
		t.Fatalf("init progress: %v", err)
	}

	var secondReq *agentcore.LLMRequest
	var dispatcher *Dispatcher
	coordinator := agentcore.NewAgent(
		agentcore.WithModel(sequentialFlowTestModel(func(i int, req *agentcore.LLMRequest) (*agentcore.LLMResponse, error) {
			if i == 0 {
				return &agentcore.LLMResponse{Message: flowTestToolCallMsg(agentcore.ToolCall{
					ID:   "tc-subagent",
					Name: "subagent",
					Args: json.RawMessage(`{"agent":"architect_long","task":"plan"}`),
				})}, nil
			}
			secondReq = req
			return &agentcore.LLMResponse{Message: flowTestAssistantMsg("done", agentcore.StopReasonStop)}, nil
		})),
		agentcore.WithTools(agentcore.NewFuncTool("subagent", "fake subagent", map[string]any{
			"type": "object",
		}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
				return nil, err
			}
			return json.RawMessage(`"foundation_ready=true"`), nil
		})),
		agentcore.WithMiddlewares(func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
			out, err := next(ctx, call.Args)
			if err == nil && call.Name == "subagent" {
				dispatcher.Dispatch()
			}
			return out, err
		}),
	)

	dispatcher = NewDispatcher(coordinator, st)
	dispatcher.Enable()

	if err := coordinator.Prompt(context.Background(), "start"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	coordinator.WaitForIdle()

	if secondReq == nil {
		t.Fatal("expected second model request")
	}
	if len(secondReq.Messages) < 4 {
		t.Fatalf("expected tool result and Host instruction in second request, got %d messages", len(secondReq.Messages))
	}
	if result := secondReq.Messages[len(secondReq.Messages)-2]; result.Role != agentcore.RoleTool {
		t.Fatalf("expected tool result immediately before Host instruction, got %q", result.Role)
	}
	got := secondReq.Messages[len(secondReq.Messages)-1].TextContent()
	for _, want := range []string{"[Host 下达指令]", "subagent(writer", "写第 1 章"} {
		if !contains(got, want) {
			t.Fatalf("Host instruction missing %q: %s", want, got)
		}
	}
}

type flowTestSequentialModel struct {
	fn  func(i int, req *agentcore.LLMRequest) (*agentcore.LLMResponse, error)
	idx int64
}

func sequentialFlowTestModel(fn func(i int, req *agentcore.LLMRequest) (*agentcore.LLMResponse, error)) *flowTestSequentialModel {
	return &flowTestSequentialModel{fn: fn}
}

func (m *flowTestSequentialModel) take(msgs []agentcore.Message, tools []agentcore.ToolSpec) (*agentcore.LLMResponse, error) {
	i := int(atomic.AddInt64(&m.idx, 1) - 1)
	return m.fn(i, &agentcore.LLMRequest{Messages: msgs, Tools: tools})
}

func (m *flowTestSequentialModel) Generate(_ context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.take(msgs, tools)
}

func (m *flowTestSequentialModel) GenerateStream(_ context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.take(msgs, tools)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: resp.Message, StopReason: resp.Message.StopReason}
	close(ch)
	return ch, nil
}

func (m *flowTestSequentialModel) SupportsTools() bool { return true }

func flowTestAssistantMsg(text string, stop agentcore.StopReason) agentcore.Message {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: stop,
	}
}

func flowTestToolCallMsg(calls ...agentcore.ToolCall) agentcore.Message {
	blocks := make([]agentcore.ContentBlock, len(calls))
	for i, call := range calls {
		blocks[i] = agentcore.ToolCallBlock(call)
	}
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    blocks,
		StopReason: agentcore.StopReasonToolUse,
	}
}
