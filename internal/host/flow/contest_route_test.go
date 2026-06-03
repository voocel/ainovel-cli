package flow

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
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
