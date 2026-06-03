package flow

import (
	"testing"

	storepkg "github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

func TestLoadState_ContestFacts(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	s := LoadStateWithContest(st, ContestConfig{Personas: []string{"wuzei", "tudou"}})
	if !s.ContestEnabled {
		t.Fatal("配置两 persona 应 ContestEnabled=true")
	}
	if len(s.Personas) != 2 {
		t.Fatalf("personas = %v", s.Personas)
	}
}

func TestLoadState_NoContestWhenSinglePersona(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	s := LoadStateWithContest(st, ContestConfig{Personas: []string{"wuzei"}})
	if s.ContestEnabled {
		t.Fatal("单 persona 不应启用竞稿")
	}
}

func TestLoadState_NoContestWhenEmpty(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	s := LoadStateWithContest(st, ContestConfig{})
	if s.ContestEnabled {
		t.Fatal("无 persona 不应启用竞稿")
	}
}

func TestLoadState_ContestChapterPopulated(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	// 用真实生命周期 API 构造 PhaseWriting、已完成 1、2 章的 Progress：
	// Init → StartChapter/MarkChapterComplete 各一次，使 NextChapter()=3。
	if err := st.Progress.Init("test", 10); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, ch := range []int{1, 2} {
		if err := st.Progress.StartChapter(ch); err != nil {
			t.Fatalf("StartChapter(%d): %v", ch, err)
		}
		if err := st.Progress.MarkChapterComplete(ch, 5000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}
	// 仅 wuzei 落盘候选稿，tudou 缺席。
	if err := st.Contest.SaveCandidate(3, "wuzei", "稿"); err != nil {
		t.Fatalf("SaveCandidate: %v", err)
	}
	s := LoadStateWithContest(st, ContestConfig{Personas: []string{"wuzei", "tudou"}})
	if s.ContestChapter != 3 {
		t.Fatalf("ContestChapter = %d, want 3", s.ContestChapter)
	}
	if s.CandidatesReady == nil || !s.CandidatesReady["wuzei"] || s.CandidatesReady["tudou"] {
		t.Fatalf("CandidatesReady = %v", s.CandidatesReady)
	}
}
