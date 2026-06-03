// internal/store/contest_test.go
package store

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// 注：store 包内已有 NewStore(dir) *Store（单返回值，无 error），见 cast_test.go:11。

func TestContest_CandidateRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Contest.SaveCandidate(3, "wuzei", "乌贼的第三章"); err != nil {
		t.Fatalf("SaveCandidate: %v", err)
	}
	got, err := st.Contest.LoadCandidate(3, "wuzei")
	if err != nil {
		t.Fatalf("LoadCandidate: %v", err)
	}
	if got != "乌贼的第三章" {
		t.Fatalf("LoadCandidate = %q", got)
	}
}

func TestContest_ListCandidates(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Contest.SaveCandidate(5, "wuzei", "a")
	_ = st.Contest.SaveCandidate(5, "tudou", "b")
	got, err := st.Contest.ListCandidates(5, []string{"wuzei", "tudou", "maibao"})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("presence map size = %d, want 3", len(got))
	}
	if !got["wuzei"] || !got["tudou"] || got["maibao"] {
		t.Fatalf("presence map wrong: %v", got)
	}
}

func TestContest_VerdictRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	v := domain.Verdict{Chapter: 7, Winner: "wuzei", RevisionNotes: "加钩子"}
	if err := st.Contest.SaveVerdict(v); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}
	got, err := st.Contest.LoadVerdict(7)
	if err != nil || got == nil {
		t.Fatalf("LoadVerdict: %v / nil", err)
	}
	if got.Winner != "wuzei" || got.Promoted {
		t.Fatalf("verdict = %+v", got)
	}
}

func TestContest_LoadVerdict_Missing(t *testing.T) {
	st := NewStore(t.TempDir())
	got, err := st.Contest.LoadVerdict(99)
	if err != nil {
		t.Fatalf("LoadVerdict missing should not error: %v", err)
	}
	if got != nil {
		t.Fatalf("missing verdict should be nil, got %+v", got)
	}
}

func TestContest_PromoteCandidate(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Contest.SaveCandidate(9, "wuzei", "中选正文")
	_ = st.Contest.SaveVerdict(domain.Verdict{Chapter: 9, Winner: "wuzei"})

	if st.Contest.IsPromoted(9) {
		t.Fatal("提升前 IsPromoted 应为 false")
	}
	if err := st.Contest.PromoteCandidate(9, "wuzei"); err != nil {
		t.Fatalf("PromoteCandidate: %v", err)
	}
	draft, _ := st.Drafts.LoadDraft(9)
	if draft != "中选正文" {
		t.Fatalf("draft after promote = %q", draft)
	}
	if !st.Contest.IsPromoted(9) {
		t.Fatal("提升后 IsPromoted 应为 true")
	}
	// 幂等：再提升一次不报错
	if err := st.Contest.PromoteCandidate(9, "wuzei"); err != nil {
		t.Fatalf("PromoteCandidate 二次: %v", err)
	}
}
