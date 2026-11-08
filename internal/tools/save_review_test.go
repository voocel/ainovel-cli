package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

func saveReviewTestChapter(t *testing.T, s *store.Store, chapter int) {
	t.Helper()
	content := fmt.Sprintf("第%d章既有正文。", chapter)
	if err := s.Drafts.SaveFinalChapter(chapter, content); err != nil {
		t.Fatal(err)
	}
	saveTestChapterRecord(t, s, chapter, content)
}

func TestSaveReviewPersistsContractAssessment(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	saveReviewTestChapter(t, s, 3)

	tool := NewSaveReviewTool(s)
	if !tool.StrictSchema() {
		t.Fatal("save_review must use strict schema")
	}
	if err := llmcontract.ValidateStrictReady(tool.Schema()); err != nil {
		t.Fatalf("save_review schema is not strict-ready: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"chapter":    3,
		"scope":      "chapter",
		"dimensions": []map[string]any{{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "基本一致"}, {"dimension": "character", "score": 82, "verdict": "pass", "comment": "人设稳定"}, {"dimension": "pacing", "score": 78, "verdict": "warning", "comment": "略慢"}, {"dimension": "continuity", "score": 84, "verdict": "pass", "comment": "连贯"}, {"dimension": "foreshadow", "score": 80, "verdict": "pass", "comment": "正常"}, {"dimension": "hook", "score": 76, "verdict": "warning", "comment": "钩子一般"}, {"dimension": "aesthetic", "score": 81, "verdict": "pass", "comment": "语言基本成立"}},
		"issues": []map[string]any{{
			"type": "contract", "severity": "error", "description": "契约漏项", "evidence": "未出现试炼邀请",
			"suggestion": "补入邀请", "chapters": []int{3}, "requires_change": true,
		}},
		"contract_status": "partial",
		"contract_misses": []string{"未明确埋下内门试炼邀请"},
		"contract_notes":  "主线推进达成，但 contract 中的第二个推进项没有落地。",
		"verdict":         "polish",
		"summary":         "本章基本完成目标，但 contract 仍有漏项。",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	review, err := s.World.LoadReview(3)
	if err != nil {
		t.Fatalf("LoadReview: %v", err)
	}
	if review == nil {
		t.Fatal("expected review saved, got nil")
	}
	if review.ContractStatus != "partial" {
		t.Fatalf("unexpected contract status: %q", review.ContractStatus)
	}
	if len(review.ContractMisses) != 1 || review.ContractMisses[0] != "未明确埋下内门试炼邀请" {
		t.Fatalf("unexpected contract misses: %+v", review.ContractMisses)
	}
	if review.Dimension("aesthetic") == nil {
		t.Fatalf("expected aesthetic dimension persisted, got %+v", review.Dimensions)
	}
}

func TestSaveReviewRejectsMissingDimensions(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter":    3,
		"scope":      "chapter",
		"dimensions": []map[string]any{},
		"issues":     []map[string]any{},
		"verdict":    "accept",
		"summary":    "ok",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "dimensions must contain at least one") {
		t.Fatalf("expected dimensions validation error, got %v", err)
	}
}

func TestSaveReviewRejectsDimensionWithoutComment(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "comment": "基本一致"},
			{"dimension": "character", "score": 82, "comment": "人设稳定"},
			{"dimension": "pacing", "score": 78},
			{"dimension": "continuity", "score": 84, "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "comment": "正常"},
			{"dimension": "hook", "score": 76, "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "comment": "语言基本成立"},
		},
		"issues":  []map[string]any{},
		"verdict": "accept",
		"summary": "ok",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "dimension comment is required: pacing") {
		t.Fatalf("expected dimension comment validation error, got %v", err)
	}
}

func TestSaveReviewRejectsIssueOutsideChapterScope(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(80); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	for ch := 1; ch <= 58; ch++ {
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 58,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "comment": "基本一致"},
			{"dimension": "character", "score": 82, "comment": "人设稳定"},
			{"dimension": "pacing", "score": 58, "comment": "节奏需要重写"},
			{"dimension": "continuity", "score": 84, "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "comment": "正常"},
			{"dimension": "hook", "score": 76, "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "comment": "语言基本成立"},
		},
		"issues": []map[string]any{{
			"type": "pacing", "severity": "error", "description": "节奏问题", "evidence": "第65章",
			"suggestion": "调整", "chapters": []int{65}, "requires_change": true,
		}},
		"contract_status": "partial",
		"verdict":         "polish",
		"summary":         "需要打磨第 58 章，不能把未完成章节入队。",
		"contract_misses": []string{"节奏超出本章职责"},
		"contract_notes":  "应只处理已完成章节。",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "must reference chapter 58") {
		t.Fatalf("expected out-of-scope affected chapter rejection, got %v", err)
	}
	review, err := s.World.LoadReview(58)
	if err != nil {
		t.Fatalf("LoadReview: %v", err)
	}
	if review != nil {
		t.Fatalf("review should not be saved when pending rewrite validation fails: %+v", review)
	}
	p, _ := s.Progress.Load()
	if p.Flow != domain.FlowWriting && p.Flow != "" {
		t.Fatalf("flow should not enter rewrite/polish, got %s", p.Flow)
	}
	if len(p.PendingRewrites) != 0 {
		t.Fatalf("pending_rewrites should remain empty, got %v", p.PendingRewrites)
	}
}

// TestSaveReviewKeepsModelDefinedDimension 验证工具不再把文学评价维度和分数阈值
// 写死在 Go 中；Editor 可以按当前任务补充更准确的评价面。
func TestSaveReviewKeepsModelDefinedDimension(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	saveReviewTestChapter(t, s, 3)

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"scope":   "chapter",
		"dimensions": []map[string]any{{
			"dimension": "dialogue_subtext", "score": 85, "verdict": "warning", "comment": "潜台词仍可加强",
		}},
		"issues":  []map[string]any{},
		"verdict": "accept",
		"summary": "ok",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute should accept model-defined dimension, got %v", err)
	}

	review, err := s.World.LoadReview(3)
	if err != nil || review == nil {
		t.Fatalf("LoadReview: %v", err)
	}
	if d := review.Dimension("dialogue_subtext"); d == nil || d.Verdict != "warning" {
		t.Fatalf("model-defined assessment should be preserved, got %+v", d)
	}
}

func TestSaveReviewRejectsRewriteWithoutActionableIssue(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "基本一致"},
			{"dimension": "character", "score": 82, "verdict": "pass", "comment": "人设稳定"},
			{"dimension": "pacing", "score": 78, "verdict": "warning", "comment": "略慢"},
			{"dimension": "continuity", "score": 84, "verdict": "pass", "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "verdict": "pass", "comment": "正常"},
			{"dimension": "hook", "score": 76, "verdict": "warning", "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "verdict": "pass", "comment": "语言基本成立"},
		},
		"issues":  []map[string]any{},
		"verdict": "rewrite",
		"summary": "需要重写",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "requires at least one issue") {
		t.Fatalf("expected actionable issue validation error, got %v", err)
	}
}

func TestSaveReviewRejectsIssueWithoutEvidence(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 3,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "基本一致"},
			{"dimension": "character", "score": 82, "verdict": "pass", "comment": "人设稳定"},
			{"dimension": "pacing", "score": 78, "verdict": "warning", "comment": "略慢"},
			{"dimension": "continuity", "score": 84, "verdict": "pass", "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "verdict": "pass", "comment": "正常"},
			{"dimension": "hook", "score": 76, "verdict": "warning", "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "verdict": "pass", "comment": "语言基本成立"},
		},
		"issues": []map[string]any{
			{"type": "hook", "severity": "warning", "description": "章末钩子偏弱"},
		},
		"verdict": "polish",
		"summary": "需要补强钩子。",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "issue evidence is required") {
		t.Fatalf("expected issue evidence validation error, got %v", err)
	}
}

// TestSaveReviewDoesNotDirtyQueueOnIllegalFlowTransition 防回归：返工排空中途
// （Flow=rewriting、PendingRewrites=[8,9]）对已重写章复审得到 polish 时，
// Flow=polishing 与 rewriting 构成非法迁移。ApplyReviewOutcome 必须在同一次写锁中
// 完成校验和写入，非法迁移时队列保持不变。
func TestSaveReviewDoesNotDirtyQueueOnIllegalFlowTransition(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(10); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	for _, ch := range []int{8, 9} {
		if err := s.Progress.MarkChapterComplete(ch, 3000, "", ""); err != nil {
			t.Fatalf("MarkChapterComplete(%d): %v", ch, err)
		}
		saveReviewTestChapter(t, s, ch)
	}
	if err := s.Progress.SetPendingRewrites([]int{8, 9}, "返工"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.Progress.SetFlow(domain.FlowRewriting); err != nil {
		t.Fatalf("SetFlow rewriting: %v", err)
	}

	tool := NewSaveReviewTool(s)
	args, err := json.Marshal(map[string]any{
		"chapter": 8,
		"scope":   "chapter",
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "verdict": "pass", "comment": "基本一致"},
			{"dimension": "character", "score": 82, "verdict": "pass", "comment": "人设稳定"},
			{"dimension": "pacing", "score": 78, "verdict": "warning", "comment": "略慢"},
			{"dimension": "continuity", "score": 84, "verdict": "pass", "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "verdict": "pass", "comment": "正常"},
			{"dimension": "hook", "score": 76, "verdict": "warning", "comment": "钩子一般"},
			{"dimension": "aesthetic", "score": 81, "verdict": "pass", "comment": "语言基本成立"},
		},
		"issues": []map[string]any{{
			"type": "contract", "severity": "error", "description": "漏项", "evidence": "契约未完成",
			"suggestion": "补齐", "chapters": []int{8}, "requires_change": true,
		}},
		"contract_status": "partial",
		"contract_misses": []string{"漏项"},
		"contract_notes":  "复审仍有漏项。",
		"verdict":         "polish",
		"summary":         "复审第 8 章需打磨。",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "apply review outcome") {
		t.Fatalf("expected illegal flow transition error, got %v", err)
	}

	p, _ := s.Progress.Load()
	if len(p.PendingRewrites) != 2 || p.PendingRewrites[0] != 8 || p.PendingRewrites[1] != 9 {
		t.Fatalf("PendingRewrites 不应被脏写，期望 [8 9]，got %v", p.PendingRewrites)
	}
	if p.Flow != domain.FlowRewriting {
		t.Fatalf("Flow 应保持 rewriting，got %s", p.Flow)
	}
}

func TestSaveReviewKeepsOutcomeWhenReviewArtifactWriteFails(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(3); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(3, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	saveReviewTestChapter(t, s, 3)
	// 让目标文件路径成为目录，稳定触发原子 rename 失败。
	if err := os.MkdirAll(filepath.Join(dir, "reviews", "03.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	args, err := json.Marshal(map[string]any{
		"chapter": 3, "scope": "chapter", "verdict": "polish", "summary": "需要补足衔接",
		"issues": []map[string]any{{
			"type": "continuity", "severity": "error", "description": "衔接不足", "evidence": "开篇缺少承接",
			"suggestion": "补足衔接", "chapters": []int{3}, "requires_change": true,
		}},
		"dimensions": []map[string]any{
			{"dimension": "consistency", "score": 85, "comment": "一致"},
			{"dimension": "character", "score": 82, "comment": "稳定"},
			{"dimension": "pacing", "score": 78, "comment": "略快"},
			{"dimension": "continuity", "score": 84, "comment": "连贯"},
			{"dimension": "foreshadow", "score": 80, "comment": "正常"},
			{"dimension": "hook", "score": 76, "comment": "可加强"},
			{"dimension": "aesthetic", "score": 81, "comment": "语言成立"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "save review") {
		t.Fatalf("expected review write failure, got %v", err)
	}

	p, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if p.Flow != domain.FlowPolishing || len(p.PendingRewrites) != 1 || p.PendingRewrites[0] != 3 {
		t.Fatalf("审阅工件失败后返工意图必须保持可恢复，got flow=%s queue=%v", p.Flow, p.PendingRewrites)
	}
}

func setupArcReviewStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(4); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, Chapters: []domain.OutlineEntry{{Title: "三"}, {Title: "四"}}},
		},
	}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatal(err)
	}
	for chapter := 1; chapter <= 4; chapter++ {
		if err := s.Progress.MarkChapterComplete(chapter, 100, "", ""); err != nil {
			t.Fatal(err)
		}
		saveReviewTestChapter(t, s, chapter)
	}
	// 第一弧已经完整收尾，因此 Router 当前唯一待补工件是第二弧评审。
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 2, Scope: "arc", Verdict: "accept", Summary: "第一弧评审"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 1, Title: "第一弧", Summary: "完成", KeyEvents: []string{"事件"}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func arcReviewArgs(t *testing.T, issueChapter int) []byte {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"chapter": 4,
		"scope":   "arc",
		"dimensions": []map[string]any{{
			"dimension": "pacing", "score": 70, "comment": "第三章节奏拖沓",
		}},
		"issues": []map[string]any{{
			"type": "pacing", "severity": "error", "description": "冲突进入过晚", "evidence": "第3章前半没有推进",
			"suggestion": "压缩铺垫", "chapters": []int{issueChapter}, "requires_change": true,
		}},
		"verdict": "polish",
		"summary": "第二弧需要压缩一处铺垫",
	})
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func TestSaveReviewRejectsIssueOutsideArcSpan(t *testing.T) {
	s := setupArcReviewStore(t)
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), arcReviewArgs(t, 2)); err == nil || !strings.Contains(err.Error(), "outside 3-4") {
		t.Fatalf("expected arc range rejection, got %v", err)
	}
	if p, _ := s.Progress.Load(); len(p.PendingRewrites) != 0 {
		t.Fatalf("invalid review must not enqueue rewrites: %v", p.PendingRewrites)
	}
}

func TestSaveReviewDerivesAffectedChaptersFromIssues(t *testing.T) {
	s := setupArcReviewStore(t)
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), arcReviewArgs(t, 3)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	review, err := s.World.LoadReview(4)
	if err != nil || review == nil {
		t.Fatalf("LoadReview: %v", err)
	}
	if !slices.Equal(review.AffectedChapters, []int{3}) {
		t.Fatalf("affected chapters must be derived from issues, got %v", review.AffectedChapters)
	}
	if p, _ := s.Progress.Load(); !slices.Equal(p.PendingRewrites, []int{3}) {
		t.Fatalf("rewrite queue = %v, want [3]", p.PendingRewrites)
	}
}

func TestSaveReviewRetriesCheckpointWithoutReapplyingOutcome(t *testing.T) {
	s := setupArcReviewStore(t)
	checkpointPath := filepath.Join(s.Dir(), "meta", "checkpoints.jsonl")
	if err := os.MkdirAll(checkpointPath, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := NewSaveReviewTool(s)
	args := arcReviewArgs(t, 3)

	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "checkpoint review") {
		t.Fatalf("expected checkpoint failure, got %v", err)
	}
	if err := os.Remove(checkpointPath); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("idempotent checkpoint retry: %v", err)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(progress.PendingRewrites, []int{3}) {
		t.Fatalf("review outcome must not be duplicated or changed, queue=%v", progress.PendingRewrites)
	}
	if cp := s.Checkpoints.LatestByStep(domain.ArcScope(1, 2), "review"); cp == nil {
		t.Fatal("checkpoint retry must finish the review write")
	}
}

func TestSaveArcReviewCanReplaceChapterReviewAtEndpoint(t *testing.T) {
	s := setupArcReviewStore(t)
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 4, Scope: "chapter", Verdict: "accept", Summary: "末章单章评审"}); err != nil {
		t.Fatal(err)
	}

	if _, err := NewSaveReviewTool(s).Execute(context.Background(), arcReviewArgs(t, 3)); err != nil {
		t.Fatalf("arc review should replace chapter-scope artifact at the same endpoint: %v", err)
	}
	review, err := s.World.LoadReview(4)
	if err != nil {
		t.Fatal(err)
	}
	if review == nil || review.Scope != "arc" {
		t.Fatalf("stored review = %+v, want scope=arc", review)
	}
}

func TestSaveReviewRejectsFutureArc(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(4); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, Chapters: []domain.OutlineEntry{{Title: "三"}, {Title: "四"}}},
		},
	}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatal(err)
	}
	for chapter := 1; chapter <= 2; chapter++ {
		if err := s.Progress.MarkChapterComplete(chapter, 100, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := NewSaveReviewTool(s).Execute(context.Background(), arcReviewArgs(t, 3)); err == nil || !strings.Contains(err.Error(), "must be completed") {
		t.Fatalf("expected future arc rejection, got %v", err)
	}
	if review, err := s.World.LoadReview(4); err != nil || review != nil {
		t.Fatalf("future review must not be persisted, review=%+v err=%v", review, err)
	}
}
