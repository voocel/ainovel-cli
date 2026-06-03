// internal/tools/draft_persona_test.go
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestDraftPersona_CandidatePhase(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewDraftPersonaTool(st, "wuzei")
	if tool.Name() != "draft_chapter" {
		t.Fatalf("Name = %q, want draft_chapter", tool.Name())
	}
	args := json.RawMessage(`{"chapter":2,"content":"乌贼候选","mode":"write"}`)
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	cand, _ := st.Contest.LoadCandidate(2, "wuzei")
	if cand != "乌贼候选" {
		t.Fatalf("candidate = %q", cand)
	}
	draft, _ := st.Drafts.LoadDraft(2)
	if draft != "" {
		t.Fatalf("候选阶段不应写 draft.md, got %q", draft)
	}
}

func TestDraftPersona_PolishPhase(t *testing.T) {
	st := store.NewStore(t.TempDir())
	_ = st.Contest.SaveCandidate(3, "wuzei", "初稿")
	_ = st.Contest.SaveVerdict(domain.Verdict{Chapter: 3, Winner: "wuzei", Promoted: true})

	tool := NewDraftPersonaTool(st, "wuzei")
	args := json.RawMessage(`{"chapter":3,"content":"润色后正文","mode":"write"}`)
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	draft, _ := st.Drafts.LoadDraft(3)
	if draft != "润色后正文" {
		t.Fatalf("draft.md = %q, want 润色后正文", draft)
	}
}

func TestDraftPersona_NonWinnerStaysCandidate(t *testing.T) {
	st := store.NewStore(t.TempDir())
	_ = st.Contest.SaveVerdict(domain.Verdict{Chapter: 4, Winner: "wuzei", Promoted: true})
	tool := NewDraftPersonaTool(st, "tudou")
	args := json.RawMessage(`{"chapter":4,"content":"土豆稿","mode":"write"}`)
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if d, _ := st.Drafts.LoadDraft(4); d != "" {
		t.Fatalf("非 winner 不应写 draft.md, got %q", d)
	}
	if c, _ := st.Contest.LoadCandidate(4, "tudou"); c != "土豆稿" {
		t.Fatalf("tudou candidate = %q", c)
	}
}
