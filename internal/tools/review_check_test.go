package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

func reviewCheckFixture(t *testing.T, chapters []int) (*store.Store, []byte) {
	t.Helper()
	s := setupArcReviewStore(t)
	for ch := 1; ch <= 4; ch++ {
		title := fmt.Sprintf("标题%d", ch)
		content := domain.ChapterText(ch, title, fmt.Sprintf("主角在第%d处等待，没有采取行动。", ch))
		if err := s.Drafts.SaveFinalChapter(ch, content); err != nil {
			t.Fatal(err)
		}
		facts := domain.ChapterFacts{Title: title, Summary: "原摘要", KeyEvents: []string{"等待"}}
		if _, err := s.ChapterRecords.Accept(ch, domain.ChapterOriginGenerated, content, facts, domain.StyleDelta{}); err != nil {
			t.Fatal(err)
		}
		if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: ch, Title: title, Summary: facts.Summary}); err != nil {
			t.Fatal(err)
		}
	}
	r := domain.ReviewEntry{
		Chapter: 4, Scope: "arc", Verdict: "polish", Summary: "本弧缺少行动推进",
		Dimensions: []domain.DimensionScore{{Dimension: "pacing", Score: 60, Comment: "反复等待"}},
		Issues: []domain.ConsistencyIssue{
			{Type: "pacing", Severity: "error", Description: "缺少行动", Evidence: "等待，没有采取行动", Chapters: chapters, RequiresChange: true},
			{Type: "aesthetic", Severity: "warning", Description: "可选的语气调整", Evidence: "等待", Chapters: []int{3}},
		},
	}
	args, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	return s, args
}

func commitReviewChange(t *testing.T, s *store.Store, chapter int, title, body string) {
	t.Helper()
	if err := s.Drafts.SaveDraft(chapter, body); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{
		"chapter": chapter, "title": title, "summary": "修改后摘要", "key_events": []string{"新行动"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newTestCommitChapterTool(s).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
}

func TestReviewCheckBatchesChangesAndDoesNotLoop(t *testing.T) {
	s, originalArgs := reviewCheckFixture(t, []int{3, 4})
	commitReviewChange(t, s, 3, "标题3", "主角离开门槛，找到新的证人。")
	checkArgs := []byte(`{"chapter":4,"scope":"arc","summary":"行动仍缺少代价","unresolved_issues":[{"issue":1,"evidence":"仍然只是在门外等待"}]}`)
	if _, err := NewSaveReviewCheckTool(s).Execute(context.Background(), checkArgs); !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("队列未排空不得提前复核: %v", err)
	}
	s = store.NewStore(s.Dir()) // 重启不能遗失已登记的复核，也不能抢在剩余返工前执行。
	state, err := flow.LoadState(s)
	if err != nil || flow.Route(state).Agent != "writer" {
		t.Fatalf("仍有返工时必须先派 writer: state=%+v err=%v", state, err)
	}
	commitReviewChange(t, s, 4, "标题4", "主角仍然只是在门外等待。")
	s = store.NewStore(s.Dir())
	state, err = flow.LoadState(s)
	if err != nil {
		t.Fatal(err)
	}
	if state.ReviewRecheck == nil || !slices.Equal(state.ReviewRecheck.Recheck.Chapters, []int{3, 4}) {
		t.Fatalf("应合并整批正文改动: %+v", state.ReviewRecheck)
	}
	if inst := flow.Route(state); inst == nil || inst.Agent != "editor" || !strings.Contains(inst.Task, "save_review_check") {
		t.Fatalf("应先复核再生成摘要: %+v", inst)
	}
	ctx, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), []byte(`{"chapter":4}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Working struct {
			Recheck struct {
				Chapters []int             `json:"changed_chapters"`
				Issues   []json.RawMessage `json:"issues"`
			} `json:"review_recheck"`
		} `json:"working_memory"`
	}
	if err := json.Unmarshal(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(payload.Working.Recheck.Chapters, []int{3, 4}) || len(payload.Working.Recheck.Issues) != 1 {
		t.Fatalf("只应提供修改章及相关原问题: %s", ctx)
	}
	// 原保存调用可以幂等重放，但不得重新入队。
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), originalArgs); err != nil {
		t.Fatalf("原评审重放不应与系统复核元数据冲突: %v", err)
	}
	newReview := []byte(`{"chapter":3,"scope":"chapter","verdict":"accept","summary":"新的评审","dimensions":[{"dimension":"pacing","score":80,"comment":"可接受"}],"issues":[]}`)
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), newReview); !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("待复核时不得另起一轮评审: %v", err)
	}
	tool := NewSaveReviewCheckTool(s)
	if err := llmcontract.ValidateStrictReady(tool.Schema()); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"chapter":4,"scope":"arc","summary":"越界问题","unresolved_issues":[{"issue":2,"evidence":"无关的语气问题"}]}`,
		`{"chapter":4,"scope":"arc","summary":"伪造问题","unresolved_issues":[{"issue":9,"evidence":"原评审不存在"}]}`,
		`{"chapter":4,"scope":"arc","summary":"无证据","unresolved_issues":[{"issue":1,"evidence":""}]}`,
	} {
		if _, err := tool.Execute(context.Background(), []byte(bad)); !errors.Is(err, errs.ErrToolArgs) {
			t.Fatalf("应拒绝无效复核结果: %v", err)
		}
	}
	if raw, err := tool.Execute(context.Background(), checkArgs); err != nil || !strings.Contains(string(raw), `"resolved":false`) {
		t.Fatalf("应如实保留未解决问题: raw=%s err=%v", raw, err)
	}
	if _, err := tool.Execute(context.Background(), checkArgs); err != nil {
		t.Fatalf("相同复核结果应幂等: %v", err)
	}
	if _, err := tool.Execute(context.Background(), []byte(`{"chapter":4,"scope":"arc","summary":"改称已解决","unresolved_issues":[]}`)); !errors.Is(err, errs.ErrToolConflict) {
		t.Fatalf("复核结论不得覆盖: %v", err)
	}
	if err := s.World.QueueReviewRecheck(3); err != nil {
		t.Fatal(err)
	}
	s = store.NewStore(s.Dir())
	state, err = flow.LoadState(s)
	if err != nil || state.ReviewRecheck != nil || len(state.Progress.PendingRewrites) != 0 {
		t.Fatalf("未解决项不得自动派生复核或返工: state=%+v err=%v", state, err)
	}
	var count int
	for _, cp := range s.Checkpoints.All() {
		if cp.Step == "review_check" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("整批返工应只产生一次复核: %d", count)
	}
	found := false
	for _, f := range diag.Analyze(s).Findings {
		found = found || f.Rule == "UnresolvedReviewRecheck"
	}
	if !found {
		t.Fatal("未解决项必须可在诊断中看见")
	}
	planning, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var next struct {
		Planning struct {
			Review struct {
				Recheck *domain.ReviewRecheck `json:"recheck"`
			} `json:"previous_review"`
		} `json:"planning_memory"`
	}
	if err := json.Unmarshal(planning, &next); err != nil {
		t.Fatal(err)
	}
	if r := next.Planning.Review.Recheck; r == nil || r.Result == nil || len(r.Result.UnresolvedIssues) != 1 {
		t.Fatalf("自然扩弧时必须收到上一轮未解决问题: %s", planning)
	}
}

func TestReviewCheckSupportsGlobalReview(t *testing.T) {
	s := completedBook(t, 5)
	for ch := 1; ch <= 5; ch++ {
		body := fmt.Sprintf("主角在第%d处等待。", ch)
		if err := s.Drafts.SaveFinalChapter(ch, body); err != nil {
			t.Fatal(err)
		}
		saveTestChapterRecord(t, s, ch, body)
	}
	if err := ReopenBook(s, []int{3}, "补充动机"); err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"chapter":5,"scope":"global","verdict":"rewrite","summary":"第三章缺少动机","dimensions":[{"dimension":"character","score":60,"comment":"行动缺少动机"}],"issues":[{"type":"character","severity":"error","description":"缺少动机","evidence":"等待","chapters":[3],"requires_change":true}]}`)
	if _, err := NewSaveReviewTool(s).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	commitReviewChange(t, s, 3, "第3章", "主角为寻找失踪的亲人出发。")
	checkArgs := []byte(`{"chapter":5,"scope":"global","summary":"动机已补齐","unresolved_issues":[]}`)
	if _, err := NewSaveReviewCheckTool(s).Execute(context.Background(), checkArgs); err != nil {
		t.Fatal(err)
	}
	r, err := s.World.LoadGlobalReview(5)
	if err != nil || r.Recheck == nil || r.Recheck.Result == nil {
		t.Fatalf("全局评审的复核必须保存在原全局工件中: r=%+v err=%v", r, err)
	}
	if other, err := s.World.LoadReview(5); err != nil || other != nil {
		t.Fatalf("不得写成章节评审工件: r=%+v err=%v", other, err)
	}
}

func TestReviewCheckSkipsTitleOnlyChanges(t *testing.T) {
	s, _ := reviewCheckFixture(t, []int{3})
	commitReviewChange(t, s, 3, "新标题", "主角在第3处等待，没有采取行动。\r\n\r\n")
	check, err := s.World.LoadPendingReviewRecheck()
	if err != nil || check != nil {
		t.Fatalf("仅改标题不得增加模型复核: check=%+v err=%v", check, err)
	}
}

func TestReviewCheckBlocksFinaleCompletionUntilResult(t *testing.T) {
	s, _ := reviewCheckFixture(t, []int{3, 4})
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	volumes[0].Final = true
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	// 模拟已有卷末摘要的返工，三项聚合工件齐备也不能越过本次复核。
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: 1, Arc: 2, Title: "末弧", Summary: "已收束"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: 1, Title: "收官卷", Summary: "已完结"}); err != nil {
		t.Fatal(err)
	}
	commitReviewChange(t, s, 3, "标题3", "主角终于离开了门槛。")
	commitReviewChange(t, s, 4, "标题4", "主角付出代价，作出终局选择。")
	if complete, err := ReconcileLayeredCompletion(s); err != nil || complete {
		t.Fatalf("复核前不得完本: complete=%v err=%v", complete, err)
	}
	args := []byte(`{"chapter":4,"scope":"arc","summary":"原问题已解决","unresolved_issues":[]}`)
	if _, err := NewSaveReviewCheckTool(s).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	p, err := s.Progress.Load()
	if err != nil || p.Phase != domain.PhaseComplete {
		t.Fatalf("最后一项复核落盘后应完本: p=%+v err=%v", p, err)
	}
}
