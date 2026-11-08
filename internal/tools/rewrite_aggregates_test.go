package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

func rewriteAggregatesFixture(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(8); err != nil {
		t.Fatal(err)
	}
	var volumes []domain.VolumeOutline
	for v := 1; v <= 2; v++ {
		volume := domain.VolumeOutline{Index: v, Title: fmt.Sprintf("卷%d", v)}
		for a := 1; a <= 2; a++ {
			volume.Arcs = append(volume.Arcs, domain.ArcOutline{Index: a, Chapters: []domain.OutlineEntry{{Title: "前章"}, {Title: "后章"}}})
		}
		volumes = append(volumes, volume)
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatal(err)
	}
	for ch := 1; ch <= 8; ch++ {
		title := fmt.Sprintf("标题%d", ch)
		content := domain.ChapterText(ch, title, fmt.Sprintf("主角在第%d处等待，没有采取行动。", ch))
		if err := s.Drafts.SaveFinalChapter(ch, content); err != nil {
			t.Fatal(err)
		}
		facts := domain.ChapterFacts{Title: title, Summary: "旧摘要", KeyEvents: []string{"等待"}}
		if _, err := s.ChapterRecords.Accept(ch, domain.ChapterOriginGenerated, content, facts, domain.StyleDelta{}); err != nil {
			t.Fatal(err)
		}
		if err := s.Progress.MarkChapterComplete(ch, domain.WordCount(content), "", ""); err != nil {
			t.Fatal(err)
		}
		if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: ch, Title: title, Summary: facts.Summary}); err != nil {
			t.Fatal(err)
		}
	}
	for v := 1; v <= 2; v++ {
		for a := 1; a <= 2; a++ {
			end := (v-1)*4 + a*2
			basis, err := s.ChapterRecords.LoadVersions([]int{end - 1, end})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.World.SaveReview(domain.ReviewEntry{Chapter: end, Scope: "arc", Verdict: "accept", Summary: "原弧评审", Basis: basis}); err != nil {
				t.Fatal(err)
			}
			if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: v, Arc: a, Summary: "旧弧摘要"}); err != nil {
				t.Fatal(err)
			}
			if err := s.Characters.SaveSnapshots(v, a, []domain.CharacterSnapshot{{Volume: v, Arc: a, Name: "主角", Status: "等待"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.World.SaveArcStyleRules(domain.WritingStyleRules{Volume: v, Arc: a, Prose: []string{"旧规则"}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: v, Summary: "旧卷摘要"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "主角归乡", OpenThreads: []string{"原书未收束的长线"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	return s
}

func rebuiltArcArgs(t *testing.T, volume, arc int) []byte {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal(validArcSummaryArgs(t), &args); err != nil {
		t.Fatal(err)
	}
	args["volume"], args["arc"] = volume, arc
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func saveRebuiltArc(t *testing.T, s *store.Store, volume, arc int) {
	t.Helper()
	if _, err := NewSaveArcSummaryTool(s).Execute(t.Context(), rebuiltArcArgs(t, volume, arc)); err != nil {
		t.Fatal(err)
	}
}

func saveRebuiltVolume(t *testing.T, s *store.Store, volume int) bool {
	t.Helper()
	args, err := json.Marshal(map[string]any{"volume": volume, "title": "重建卷", "summary": "基于改后正文的卷摘要", "key_events": []string{"新的行动"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := NewSaveVolumeSummaryTool(s).Execute(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		BookComplete bool `json:"book_complete"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result.BookComplete
}

func TestInternalRewriteRefreshesOnlyAffectedAggregatesBeforeCompletion(t *testing.T) {
	s := rewriteAggregatesFixture(t)
	original, err := s.World.LoadReview(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{Chapter: 6, Scope: "global", Verdict: "accept", Summary: "前六章旧评审"}); err != nil {
		t.Fatal(err)
	}
	if err := ReopenBook(s, []int{1, 7, 8}, "修改两弧"); err != nil {
		t.Fatal(err)
	}
	commitReviewChange(t, s, 1, "标题1", "主角找到证人，付出代价。")
	if summary, err := s.Summaries.LoadArcSummary(1, 1); err != nil || summary != nil {
		t.Fatalf("修改弧摘要须失效: %+v %v", summary, err)
	}
	if summary, err := s.Summaries.LoadVolumeSummary(1); err != nil || summary != nil {
		t.Fatalf("所属卷摘要须失效: %+v %v", summary, err)
	}
	if snapshots, err := s.Characters.LoadSnapshots(1, 1); err != nil || len(snapshots) != 0 {
		t.Fatalf("修改弧快照须失效: %+v %v", snapshots, err)
	}
	if rules, err := s.World.LoadArcStyleRules(1, 1); err != nil || rules != nil {
		t.Fatalf("修改弧规则须失效: %+v %v", rules, err)
	}
	if rules, err := s.World.LoadStyleRules(); err != nil || rules == nil || rules.Volume != 2 || rules.Arc != 2 {
		t.Fatalf("后续弧规则须保留: %+v %v", rules, err)
	}
	for _, pos := range [][2]int{{1, 2}, {2, 1}, {2, 2}} {
		if summary, err := s.Summaries.LoadArcSummary(pos[0], pos[1]); err != nil || summary == nil {
			t.Fatalf("无关弧须保留: %v %+v %v", pos, summary, err)
		}
		if snapshots, err := s.Characters.LoadSnapshots(pos[0], pos[1]); err != nil || len(snapshots) == 0 {
			t.Fatalf("无关快照须保留: %v %+v %v", pos, snapshots, err)
		}
	}
	commitReviewChange(t, s, 7, "标题7", "主角承担风险，带回关键证据。")
	commitReviewChange(t, s, 8, "标题8", "证据公开，冲突得到解决。")
	if review, err := s.World.LoadGlobalReview(6); err != nil || !slices.Equal(review.UnverifiedChapters(), []int{1}) {
		t.Fatalf("全局评审只记录截止范围内的修改: %+v %v", review, err)
	}
	s = store.NewStore(s.Dir()) // 失效事实须在重启后仍可恢复。
	state, err := flow.LoadState(s)
	if err != nil || state.Progress.Phase != domain.PhaseWriting || len(state.Progress.PendingRewrites) != 0 || state.ReviewRecheck != nil {
		t.Fatalf("返工排空后须等待聚合重建: %+v %v", state, err)
	}
	if state.AggregateRefresh == nil || state.AggregateRefresh.Kind != flow.AggregateArcSummary || state.AggregateRefresh.Volume != 1 || state.AggregateRefresh.Arc != 1 {
		t.Fatalf("只重建摘要，不重启全量评审: %+v", state.AggregateRefresh)
	}
	if complete, err := ReconcileRewrittenBook(s, state.Progress); err != nil || complete {
		t.Fatalf("旧评审不能使缺少摘要的返工书提前完本: %v %v", complete, err)
	}
	if _, err := NewSaveFoundationTool(s).Execute(t.Context(), []byte(`{"type":"complete_book","content":{},"reason":"返工已提交"}`)); !errors.Is(err, errs.ErrToolPrecondition) {
		t.Fatalf("显式完本也不能绕过重建: %v", err)
	}
	review, err := s.World.LoadReview(2)
	if err != nil || review.Summary != original.Summary || !reflect.DeepEqual(review.Basis, original.Basis) || !slices.Equal(review.UnverifiedChapters(), []int{1}) {
		t.Fatalf("保留历史评审并明确当前改动未验收: %+v %v", review, err)
	}
	if review.RevisedChapters[0].Revision != 2 {
		t.Fatalf("应记录改后的版本: %+v", review.RevisedChapters)
	}
	saveRebuiltArc(t, s, 1, 1)
	if saveRebuiltVolume(t, s, 1) {
		t.Fatal("另一卷尚待重建，不得提前完本")
	}
	saveRebuiltArc(t, s, 2, 2)
	if !saveRebuiltVolume(t, s, 2) {
		t.Fatal("全部重建后应按结构完整收尾，不被旧长线阻塞")
	}
	p, err := s.Progress.Load()
	if err != nil || p.Phase != domain.PhaseComplete || p.ReopenedFromComplete {
		t.Fatalf("完结状态未收口: %+v %v", p, err)
	}
	found := false
	for _, finding := range diag.Analyze(s).Findings {
		found = found || finding.Rule == "UnverifiedReviewRevisions"
	}
	if !found {
		t.Fatal("完本不能把未复核的改动伪装为重新验收，诊断须可见")
	}
}

func TestRewriteAggregateInvalidationRecoversAndKeepsLaterRules(t *testing.T) {
	s := rewriteAggregatesFixture(t)
	if err := ReopenBook(s, []int{1}, "补充行动"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir(), "summaries/vol-v01.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(path, "blocker")
	if err := os.WriteFile(blocker, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	const body = "主角离开门槛，找到新的证人。"
	if err := s.Drafts.SaveDraft(1, body); err != nil {
		t.Fatal(err)
	}
	args := []byte(`{"chapter":1,"title":"标题1","summary":"冻结的修改摘要","key_events":["新行动"]}`)
	if _, err := newTestCommitChapterTool(s).Execute(t.Context(), args); !errors.Is(err, errs.ErrStoreWrite) {
		t.Fatalf("失效失败须明确报错: %v", err)
	}
	if record, err := s.ChapterRecords.Load(1); err != nil || record.Revision != 1 {
		t.Fatalf("失效完成前不得接纳新版正文: %+v %v", record, err)
	}
	if pending, err := s.Signals.LoadPendingCommit(); err != nil || pending == nil || pending.Stage != domain.CommitStageStarted {
		t.Fatalf("须保留可恢复提交: %+v %v", pending, err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, "后来覆盖的草稿，不可替代冻结正文。"); err != nil {
		t.Fatal(err)
	}
	s = store.NewStore(s.Dir())
	if _, err := newTestCommitChapterTool(s).Execute(t.Context(), []byte(`{"chapter":1}`)); err != nil {
		t.Fatal(err)
	}
	if record, err := s.ChapterRecords.Load(1); err != nil || record.Revision != 2 || record.Content != domain.ChapterText(1, "标题1", body) {
		t.Fatalf("恢复须沿用同一冻结版本: %+v %v", record, err)
	}
	saveRebuiltArc(t, s, 1, 1)
	if rules, err := s.World.LoadStyleRules(); err != nil || rules == nil || rules.Volume != 2 || rules.Arc != 2 {
		t.Fatalf("重建早期弧不能覆盖后期规则: %+v %v", rules, err)
	}
	tool := NewSaveArcSummaryTool(s)
	if _, err := tool.Execute(t.Context(), rebuiltArcArgs(t, 1, 1)); err != nil {
		t.Fatalf("历史弧也必须能幂等重放: %v", err)
	}
	var different map[string]any
	if err := json.Unmarshal(rebuiltArcArgs(t, 1, 1), &different); err != nil {
		t.Fatal(err)
	}
	different["style_rules"].(map[string]any)["prose"] = []string{"不同规则"}
	changedArgs, err := json.Marshal(different)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(t.Context(), changedArgs); !errors.Is(err, errs.ErrToolConflict) {
		t.Fatalf("新一轮重建完成后仍须保留覆盖保护: %v", err)
	}
	if !saveRebuiltVolume(t, s, 1) {
		t.Fatal("恢复重建完成后应重新完本")
	}
}

func TestTitleOnlyRewriteKeepsAggregates(t *testing.T) {
	s := rewriteAggregatesFixture(t)
	if err := ReopenBook(s, []int{1}, "只改标题"); err != nil {
		t.Fatal(err)
	}
	commitReviewChange(t, s, 1, "新标题", "主角在第1处等待，没有采取行动。\r\n\r\n")
	if summary, err := s.Summaries.LoadArcSummary(1, 1); err != nil || summary == nil || summary.Summary != "旧弧摘要" {
		t.Fatalf("纯标题修改不重建摘要: %+v %v", summary, err)
	}
	if review, err := s.World.LoadReview(2); err != nil || len(review.RevisedChapters) != 0 || review.Recheck != nil {
		t.Fatalf("纯标题修改不派复核: %+v %v", review, err)
	}
	if p, err := s.Progress.Load(); err != nil || p.Phase != domain.PhaseComplete {
		t.Fatalf("纯标题修改可直接收尾: %+v %v", p, err)
	}
}
