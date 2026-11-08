package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSaveReviewPreservesReopenedWorkUntilAllChaptersCommit(t *testing.T) {
	s := completedBook(t, 3)
	for chapter := 1; chapter <= 3; chapter++ {
		content := fmt.Sprintf("主角在第 %d 个夜晚踏上旅途。", chapter)
		if err := s.Drafts.SaveFinalChapter(chapter, content); err != nil {
			t.Fatal(err)
		}
		saveTestChapterRecord(t, s, chapter, content)
	}
	if err := ReopenBook(s, []int{3, 1, 2}, "三章都需要修改"); err != nil {
		t.Fatal(err)
	}
	review, err := json.Marshal(domain.ReviewEntry{
		Chapter: 1, Scope: "chapter", Verdict: "rewrite", Summary: "第一章补充动机",
		Dimensions: []domain.DimensionScore{{Dimension: "character", Score: 60, Comment: "动机不足"}},
		Issues: []domain.ConsistencyIssue{{
			Type: "character", Severity: "error", Description: "缺少行动动机", Evidence: "踏上旅途",
			Chapters: []int{1}, RequiresChange: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.PendingRewrites, []int{3, 1, 2}) {
		t.Fatalf("新增局部审阅丢失原返工任务: %v", p.PendingRewrites)
	}

	tool := newTestCommitChapterTool(s)
	for i, chapter := range []int{1, 3, 2} {
		if err := s.Drafts.SaveDraft(chapter, fmt.Sprintf("主角在第 %d 个夜晚为寻找失踪的亲人踏上旅途。", chapter)); err != nil {
			t.Fatal(err)
		}
		args, err := json.Marshal(map[string]any{
			"chapter": chapter, "title": fmt.Sprintf("第%d章", chapter),
			"summary": "返工摘要", "characters": []string{"主角"}, "key_events": []string{"既有事件"},
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		var result domain.CommitResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		p, err := s.Progress.Load()
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && (result.BookComplete || p.Phase != domain.PhaseWriting || len(p.PendingRewrites) != 2-i) {
			t.Fatalf("尚有返工任务时不得完结: result=%+v progress=%+v", result, p)
		}
		if i == 0 {
			for _, pendingChapter := range []int{2, 3} {
				record, err := s.ChapterRecords.Load(pendingChapter)
				if err != nil || record == nil || record.Revision != 1 {
					t.Fatalf("未处理章节不应被视为已返工: record=%+v err=%v", record, err)
				}
			}
		}
		if i == 2 && (result.BookComplete || p.Phase != domain.PhaseWriting || len(p.PendingRewrites) != 0) {
			t.Fatalf("返工排空后应等待原评审的单次复核: result=%+v progress=%+v", result, p)
		}
	}
	args := []byte(`{"chapter":1,"scope":"chapter","summary":"动机已补齐","unresolved_issues":[]}`)
	if _, err := NewSaveReviewCheckTool(s).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	p, err = s.Progress.Load()
	if err != nil || p.Phase != domain.PhaseComplete || len(p.PendingRewrites) != 0 {
		t.Fatalf("返工与复核均完成后应正常完结: p=%+v err=%v", p, err)
	}
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	p, err = s.Progress.Load()
	if err != nil || len(p.PendingRewrites) != 0 {
		t.Fatalf("相同章节评审重放不得重启已复核的返工: p=%+v err=%v", p, err)
	}
}
