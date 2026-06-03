package flow

import (
	"strings"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

// contestWritingState 构造一个处于 Writing 阶段、竞稿模式的 State，目标章为 next。
func contestWritingState(next int) State {
	p := &domain.Progress{Phase: domain.PhaseWriting, Flow: domain.FlowWriting, Layered: false}
	for i := 1; i < next; i++ {
		p.CompletedChapters = append(p.CompletedChapters, i)
	}
	return State{
		Progress:       p,
		ContestEnabled: true,
		Personas:       []string{"wuzei", "tudou"},
		ContestChapter: next,
	}
}

func TestRoute_Contest_FirstCandidate(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": false, "tudou": false}
	got := Route(s)
	if got == nil || got.Agent != "writer_wuzei" {
		t.Fatalf("expected writer_wuzei, got %+v", got)
	}
	if got.Chapter != 1 {
		t.Fatalf("chapter = %d", got.Chapter)
	}
}

func TestRoute_Contest_SecondCandidate(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": false}
	got := Route(s)
	if got == nil || got.Agent != "writer_tudou" {
		t.Fatalf("expected writer_tudou, got %+v", got)
	}
}

func TestRoute_Contest_AllReady_Judge(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": true}
	got := Route(s)
	if got == nil || got.Agent != "judge" {
		t.Fatalf("expected judge, got %+v", got)
	}
}

func TestRoute_Contest_VerdictNotPromoted_ReturnsNil(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": true}
	s.HasVerdict = true
	s.VerdictWinner = "wuzei"
	s.IsPromoted = false
	if got := Route(s); got != nil {
		t.Fatalf("未提升时 Route 应返回 nil（交 dispatcher 内联提升），got %+v", got)
	}
}

func TestRoute_Contest_Promoted_Polish(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": true}
	s.HasVerdict = true
	s.VerdictWinner = "wuzei"
	s.IsPromoted = true
	got := Route(s)
	if got == nil || got.Agent != "writer_wuzei" {
		t.Fatalf("expected writer_wuzei polish, got %+v", got)
	}
	if got.Task == "写第 1 章候选稿" {
		t.Fatal("润色 Task 不能与候选 Task 相同")
	}
}

func TestRoute_Contest_PolishTaskCarriesNotes(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": true}
	s.HasVerdict = true
	s.VerdictWinner = "wuzei"
	s.IsPromoted = true
	s.VerdictRevisionNotes = "强化章末钩子"
	got := Route(s)
	if got == nil || !strings.Contains(got.Task, "强化章末钩子") {
		t.Fatalf("润色 task 应携带 revision_notes，got %+v", got)
	}
	if !strings.Contains(got.Task, "润色") {
		t.Fatal("润色 task 必须仍含\"润色\"（StopGuard 依赖）")
	}
}

func TestRoute_Contest_PromotedButEmptyWinner_ReturnsNil(t *testing.T) {
	s := contestWritingState(1)
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": true}
	s.HasVerdict = true
	s.VerdictWinner = ""
	s.IsPromoted = true
	if got := Route(s); got != nil {
		t.Fatalf("winner 为空应返回 nil 降级，got %+v", got)
	}
}

// 并发：候选未齐时返回携带全部 pending 的 Batch。
func TestRoute_Contest_Concurrent_BatchAllPending(t *testing.T) {
	s := contestWritingState(1)
	s.ContestConcurrent = true
	s.CandidatesReady = map[string]bool{"wuzei": false, "tudou": false}
	got := Route(s)
	if got == nil || len(got.Batch) != 2 {
		t.Fatalf("应返回 2 元素 Batch, got %+v", got)
	}
	if got.Agent != "" {
		t.Fatalf("批量指令 Agent 应为空, got %q", got.Agent)
	}
	if got.Batch[0].Agent != "writer_wuzei" || got.Batch[1].Agent != "writer_tudou" {
		t.Fatalf("Batch 顺序/命名错: %+v", got.Batch)
	}
	if got.Chapter != 1 {
		t.Fatalf("Chapter=%d", got.Chapter)
	}
}

// 并发：部分就绪时 Batch 只含剩余 pending。
func TestRoute_Contest_Concurrent_BatchPartial(t *testing.T) {
	s := contestWritingState(1)
	s.ContestConcurrent = true
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": false}
	got := Route(s)
	if got == nil || len(got.Batch) != 1 || got.Batch[0].Agent != "writer_tudou" {
		t.Fatalf("应只补 tudou, got %+v", got)
	}
}

// 并发：弃权的 persona 不进 Batch；其余就绪则进 judge。
func TestRoute_Contest_Concurrent_AbandonedExcluded(t *testing.T) {
	s := contestWritingState(1)
	s.ContestConcurrent = true
	s.CandidatesReady = map[string]bool{"wuzei": true, "tudou": false}
	s.Abandoned = map[string]bool{"tudou": true}
	got := Route(s)
	if got == nil || got.Agent != "judge" {
		t.Fatalf("tudou 弃权 + wuzei 就绪应进 judge, got %+v", got)
	}
}

// 并发：全部弃权 → 降级单 writer 写 draft.md。
func TestRoute_Contest_AllAbandoned_DegradeSingleWriter(t *testing.T) {
	s := contestWritingState(1)
	s.ContestConcurrent = true
	s.CandidatesReady = map[string]bool{"wuzei": false, "tudou": false}
	s.Abandoned = map[string]bool{"wuzei": true, "tudou": true}
	got := Route(s)
	if got == nil || got.Agent != "writer" || len(got.Batch) != 0 {
		t.Fatalf("全弃权应降级单 writer（基础 writer，无 Batch）, got %+v", got)
	}
	if got.Task != "写第 1 章" {
		t.Fatalf("降级 Task 应是普通续写, got %q", got.Task)
	}
}

// FormatMessage 批量渲染：含 tasks=[...] 与单次调用约束。
func TestFormatMessage_Batch(t *testing.T) {
	inst := &Instruction{
		Batch: []SubTask{
			{Agent: "writer_wuzei", Task: "写第 1 章候选稿", Chapter: 1},
			{Agent: "writer_tudou", Task: "写第 1 章候选稿", Chapter: 1},
		},
		Reason:  "竞稿：并行补齐 2 份候选稿",
		Chapter: 1,
	}
	msg := FormatMessage(inst)
	for _, want := range []string{"tasks=[", "writer_wuzei", "writer_tudou", "一次 subagent(tasks=[...])"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("批量话术缺 %q:\n%s", want, msg)
		}
	}
}
