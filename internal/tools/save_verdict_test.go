package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

func TestSaveVerdict_Execute(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewSaveVerdictTool(st)
	args := json.RawMessage(`{
		"chapter": 4,
		"winner": "wuzei",
		"scores": [{"persona":"wuzei","score":9,"comment":"好"},{"persona":"tudou","score":7,"comment":"平"}],
		"revision_notes": "强化结尾钩子"
	}`)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal(out, &res)
	if res["saved"] != true || res["winner"] != "wuzei" {
		t.Fatalf("result = %v", res)
	}
	if res["winner_score"] != float64(9) {
		t.Fatalf("winner_score = %v", res["winner_score"])
	}
	v, _ := st.Contest.LoadVerdict(4)
	if v == nil || v.Winner != "wuzei" || len(v.Scores) != 2 {
		t.Fatalf("stored verdict = %+v", v)
	}
}

func TestSaveVerdict_RejectWinnerNotInScores(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewSaveVerdictTool(st)
	args := json.RawMessage(`{"chapter":1,"winner":"ghost","scores":[{"persona":"wuzei","score":8,"comment":"ok"}],"revision_notes":"x"}`)
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("winner 不在 scores 中应报错")
	}
}

func TestSaveVerdict_RejectScoreOutOfRange(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewSaveVerdictTool(st)
	args := json.RawMessage(`{"chapter":1,"winner":"wuzei","scores":[{"persona":"wuzei","score":11,"comment":"ok"}],"revision_notes":"x"}`)
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("score 越界应报错")
	}
}

func TestSaveVerdict_RejectEmptyRevisionNotes(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewSaveVerdictTool(st)
	args := json.RawMessage(`{"chapter":1,"winner":"wuzei","scores":[{"persona":"wuzei","score":8,"comment":"ok"}],"revision_notes":""}`)
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("revision_notes 为空应报错")
	}
}
