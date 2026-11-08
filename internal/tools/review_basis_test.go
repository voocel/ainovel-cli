package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

func basisReviewArgs(t *testing.T, scope string, chapter int) []byte {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"chapter": chapter, "scope": scope, "verdict": "accept", "summary": "当前正文一致",
		"dimensions": []domain.DimensionScore{{Dimension: "consistency", Score: 90, Comment: "已核对原文"}},
		"issues":     []domain.ConsistencyIssue{}, "contract_misses": []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func TestSaveReviewCapturesBasisForItsScope(t *testing.T) {
	for _, tc := range []struct {
		scope    string
		chapter  int
		chapters []int
	}{
		{"chapter", 3, []int{3}},
		{"arc", 4, []int{3, 4}},
		{"global", 5, []int{1, 2, 3, 4, 5}},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			var s *store.Store
			if tc.scope == "arc" {
				s = setupArcReviewStore(t)
			} else {
				s = store.NewStore(t.TempDir())
				if err := s.Init(); err != nil {
					t.Fatal(err)
				}
				if err := s.Progress.Init(8); err != nil {
					t.Fatal(err)
				}
				for ch := 1; ch <= 6; ch++ {
					if err := s.Progress.MarkChapterComplete(ch, 100, "", ""); err != nil {
						t.Fatal(err)
					}
					saveReviewTestChapter(t, s, ch)
				}
			}
			load := func() *domain.ReviewEntry {
				t.Helper()
				var r *domain.ReviewEntry
				var err error
				if tc.scope == "global" {
					r, err = s.World.LoadGlobalReview(tc.chapter)
				} else {
					r, err = s.World.LoadReview(tc.chapter)
				}
				if err != nil || r == nil {
					t.Fatalf("load review: r=%+v err=%v", r, err)
				}
				return r
			}
			tool := NewSaveReviewTool(s)
			args := basisReviewArgs(t, tc.scope, tc.chapter)
			if raw, err := tool.Execute(t.Context(), args); err != nil {
				t.Fatal(err)
			} else {
				var result map[string]json.RawMessage
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				if _, exists := result["basis"]; exists {
					t.Fatal("审计哈希不得进入模型工具结果")
				}
				if string(result["affected_chapters"]) != "[]" {
					t.Fatalf("无返工章节的返回值仍应为空数组: %s", raw)
				}
			}
			r := load()
			var expected []domain.ChapterVersion
			for _, chapter := range tc.chapters {
				record, err := s.ChapterRecords.Load(chapter)
				if err != nil {
					t.Fatal(err)
				}
				expected = append(expected, domain.ChapterVersion{Chapter: chapter, Revision: record.Revision, ContentSHA256: record.ContentSHA256})
			}
			if !reflect.DeepEqual(r.Basis, expected) {
				t.Fatalf("快照应只包含已审区间: got=%+v want=%+v", r.Basis, expected)
			}
			// 正文换版后，保存重试不得把旧评审重新绑定到新正文。
			chapter := tc.chapters[0]
			record, err := s.ChapterRecords.Load(chapter)
			if err != nil {
				t.Fatal(err)
			}
			updated := record.Content + "\n主角采取了新的行动。"
			if err := s.Drafts.SaveFinalChapter(chapter, updated); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ChapterRecords.Accept(chapter, record.Origin, updated, record.Facts, record.StyleDelta); err != nil {
				t.Fatal(err)
			}
			if _, err := tool.Execute(t.Context(), args); err != nil {
				t.Fatalf("相同评审重放不得因正文换版而失败: %v", err)
			}
			if got := load().Basis; !reflect.DeepEqual(got, expected) {
				t.Fatalf("原评审版本不可回写: got=%+v want=%+v", got, expected)
			}
			state, err := flow.LoadState(s)
			if err != nil || len(state.Progress.PendingRewrites) != 0 || state.ReviewRecheck != nil {
				t.Fatalf("版本差异不得新增返工或复审: state=%+v err=%v", state, err)
			}
			if tc.scope == "arc" && (!state.HasArcReview || state.AggregateRefresh == nil || state.AggregateRefresh.Kind != flow.AggregateArcSummary) {
				t.Fatalf("版本快照不改变已有评审的路由语义: %+v", state)
			}
		})
	}
}

func TestSaveReviewRejectsSubmittedVersionMetadata(t *testing.T) {
	for _, field := range []string{"basis", "revised_chapters"} {
		t.Run(field, func(t *testing.T) {
			s := setupArcReviewStore(t)
			args, err := json.Marshal(map[string]any{"chapter": 4, "scope": "arc", "verdict": "accept", "summary": "伪造版本", field: []domain.ChapterVersion{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewSaveReviewTool(s).Execute(t.Context(), args); !errors.Is(err, errs.ErrToolArgs) {
				t.Fatalf("版本须由系统采集，不可由模型指定: %v", err)
			}
			if review, err := s.World.LoadReview(4); err != nil || review != nil {
				t.Fatalf("伪造来源不得落盘: review=%+v err=%v", review, err)
			}
		})
	}
}

func TestLegacyReviewReplayKeepsMissingBasis(t *testing.T) {
	s := setupArcReviewStore(t)
	args := basisReviewArgs(t, "arc", 4)
	tool := NewSaveReviewTool(s)
	var legacy domain.ReviewEntry
	if err := json.Unmarshal(args, &legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.normalizeReviewEntry(&legacy); err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveReview(legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.Dir(), store.ChapterRecordPath(3))); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(t.Context(), args); err != nil {
		t.Fatalf("旧评审重放不能要求补造版本: %v", err)
	}
	r, err := s.World.LoadReview(4)
	if err != nil || r.Basis != nil {
		t.Fatalf("旧评审仍须保留未知版本: r=%+v err=%v", r, err)
	}
}

func TestSaveReviewDoesNotInventBasisOnReadFailure(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		s := setupArcReviewStore(t)
		path := filepath.Join(s.Dir(), store.ChapterRecordPath(3))
		if corrupt {
			if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSaveReviewTool(s).Execute(t.Context(), arcReviewArgs(t, 3)); !errors.Is(err, errs.ErrStoreRead) {
			t.Fatalf("缺失或损坏记录不得伪造快照: corrupt=%v err=%v", corrupt, err)
		}
		if r, err := s.World.LoadReview(4); err != nil || r != nil {
			t.Fatalf("读取失败不得保存无来源的新评审: r=%+v err=%v", r, err)
		}
		if p, err := s.Progress.Load(); err != nil || len(p.PendingRewrites) != 0 {
			t.Fatalf("读取失败不得修改返工队列: p=%+v err=%v", p, err)
		}
	}
}
