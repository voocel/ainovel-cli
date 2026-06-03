package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/store"
)

func TestReadCandidates_ReturnsAllPresent(t *testing.T) {
	st := store.NewStore(t.TempDir())
	_ = st.Contest.SaveCandidate(5, "wuzei", "乌贼稿")
	_ = st.Contest.SaveCandidate(5, "tudou", "土豆稿")
	tool := NewReadCandidatesTool(st, []string{"wuzei", "tudou", "maibao"})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":5}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res struct {
		Chapter    int `json:"chapter"`
		Candidates []struct {
			Persona   string `json:"persona"`
			Content   string `json:"content"`
			WordCount int    `json:"word_count"`
		} `json:"candidates"`
	}
	_ = json.Unmarshal(out, &res)
	// 只返回已落盘的候选（maibao 缺席不计入）
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2 (%s)", len(res.Candidates), string(out))
	}
}

func TestReadCandidates_RequiresChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	tool := NewReadCandidatesTool(st, []string{"wuzei"})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":0}`)); err == nil {
		t.Fatal("chapter<=0 应报错")
	}
}
