package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/prosecheck"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCheckConsistencyReturnsProseFindings(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveDraft(1, "他不是害怕，而是在等门外的人先动。"); err != nil {
		t.Fatal(err)
	}

	raw, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ProseFindings []prosecheck.Finding `json:"prose_findings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !hasProseRule(out.ProseFindings, "not_is_comparison") {
		t.Fatalf("check_consistency 未返回章节检测结果: %+v", out.ProseFindings)
	}
}

func TestCommitChapterReturnsProseFindingsForNormalAndRewrite(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName: "检测测试", Genre: domain.GenreNovel, Phase: domain.PhaseWriting, TotalChapters: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveDraft(1, "# 门外\n\n他不是害怕，而是在等门外的人先动。"); err != nil {
		t.Fatal(err)
	}

	tool := NewCommitChapterTool(st)
	args, _ := json.Marshal(map[string]any{
		"chapter": 1, "title": "门外", "summary": "等待门外来客", "characters": []string{"陈砚"}, "key_events": []string{"来客抵达"},
	})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("检测结果不应阻断普通提交: %v", err)
	}
	if findings := decodeProseFindings(t, raw); !hasProseRule(findings, "not_is_comparison") {
		t.Fatalf("普通提交未返回 prose_findings: %+v", findings)
	}

	if err := st.Progress.SetPendingRewrites([]int{1}, "清理模板句"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.SetFlow(domain.FlowPolishing); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveDraft(1, "# 门外\n\n声音不高，却让门外的脚步停了下来。"); err != nil {
		t.Fatal(err)
	}
	rewriteRaw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("检测结果不应阻断返工提交: %v", err)
	}
	if findings := decodeProseFindings(t, rewriteRaw); !hasProseRule(findings, "voice_contrast") {
		t.Fatalf("返工提交未返回 prose_findings: %+v", findings)
	}
}

func TestContextToolDynamicallyDetectsCurrentAndPreviousChapter(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{
		NovelName: "检测测试", Genre: domain.GenreNovel, Phase: domain.PhaseWriting, TotalChapters: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "他不是迟疑，而是在听院外的雨声。"); err != nil {
		t.Fatal(err)
	}

	tool := NewContextTool(st, References{}, "default")
	currentRaw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var current struct {
		Findings []prosecheck.Finding `json:"prose_findings"`
		Summary  string               `json:"_loading_summary"`
	}
	if err := json.Unmarshal(currentRaw, &current); err != nil {
		t.Fatal(err)
	}
	if !hasProseRule(current.Findings, "not_is_comparison") {
		t.Fatalf("已完成章节未动态检测: %+v", current.Findings)
	}
	if current.Summary == "" || !strings.Contains(current.Summary, "AI味候选:1") {
		t.Fatalf("加载摘要未显示章节候选数量: %q", current.Summary)
	}

	previousRaw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var previous struct {
		Findings []prosecheck.Finding `json:"previous_prose_findings"`
	}
	if err := json.Unmarshal(previousRaw, &previous); err != nil {
		t.Fatal(err)
	}
	if !hasProseRule(previous.Findings, "not_is_comparison") {
		t.Fatalf("写新章时未注入上一章候选: %+v", previous.Findings)
	}

	if err := st.Drafts.SaveFinalChapter(1, "雨水落进石槽。陈砚收起斗笠，推门进屋。"); err != nil {
		t.Fatal(err)
	}
	clearedRaw, err := tool.Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var cleared struct {
		Findings []prosecheck.Finding `json:"prose_findings"`
	}
	if err := json.Unmarshal(clearedRaw, &cleared); err != nil {
		t.Fatal(err)
	}
	if len(cleared.Findings) != 0 {
		t.Fatalf("正文修正后不应保留旧检测结果: %+v", cleared.Findings)
	}
}

func decodeProseFindings(t *testing.T, raw json.RawMessage) []prosecheck.Finding {
	t.Helper()
	var out struct {
		Findings []prosecheck.Finding `json:"prose_findings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Findings
}

func hasProseRule(findings []prosecheck.Finding, rule string) bool {
	for _, finding := range findings {
		if finding.Rule == rule {
			return true
		}
	}
	return false
}
