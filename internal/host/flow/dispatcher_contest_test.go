package flow

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestPromoteIfNeeded_PromotesAfterVerdict(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	_ = st.Contest.SaveCandidate(1, "wuzei", "中选稿")
	_ = st.Contest.SaveVerdict(domain.Verdict{Chapter: 1, Winner: "wuzei", Promoted: false})

	cfg := ContestConfig{Personas: []string{"wuzei", "tudou"}}
	changed := PromoteIfNeeded(st, cfg, 1)
	if !changed {
		t.Fatal("应执行提升")
	}
	if !st.Contest.IsPromoted(1) {
		t.Fatal("提升标记未置位")
	}
	d, _ := st.Drafts.LoadDraft(1)
	if d != "中选稿" {
		t.Fatalf("draft = %q", d)
	}
	// 幂等：再调一次不应再"改变"
	if PromoteIfNeeded(st, cfg, 1) {
		t.Fatal("已提升后不应再次执行")
	}
}

func TestPromoteIfNeeded_NoVerdict(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	cfg := ContestConfig{Personas: []string{"wuzei", "tudou"}}
	if PromoteIfNeeded(st, cfg, 1) {
		t.Fatal("无 verdict 不应提升")
	}
}

func TestPromoteIfNeeded_Disabled(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	cfg := ContestConfig{Personas: []string{"wuzei"}} // <2 = disabled
	if PromoteIfNeeded(st, cfg, 1) {
		t.Fatal("未启用竞稿不应提升")
	}
}
