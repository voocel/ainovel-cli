package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func setupLayered(t *testing.T, volumes []domain.VolumeOutline) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init("test", 0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatalf("SetLayered: %v", err)
	}
	return s
}

func TestCheckArcBoundaryNeedsNewVolume(t *testing.T) {
	// 只有 1 卷 1 弧 1 章，且非 Final → 应触发 NeedsNewVolume
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "首弧", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "第一章", CoreEvent: "开局", Hook: "继续"}},
		}},
	}})

	b, err := s.Outline.CheckArcBoundary(1) // 第 1 章 = 弧/卷最后一章
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if b == nil {
		t.Fatal("expected boundary, got nil")
	}
	if !b.IsArcEnd || !b.IsVolumeEnd {
		t.Fatalf("expected arc+volume end, got arc=%v vol=%v", b.IsArcEnd, b.IsVolumeEnd)
	}
	if !b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=true")
	}
	if b.NextVolume != 0 || b.NextArc != 0 {
		t.Fatalf("expected no next, got vol=%d arc=%d", b.NextVolume, b.NextArc)
	}
}

func TestCheckArcBoundaryLastVolumeRequiresDecision(t *testing.T) {
	// 单卷最后一章 → 触发 NeedsNewVolume，让 Router 让架构师二选一：
	// append_volume 续写 / complete_book 收尾。
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "唯一卷", Theme: "主题",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "唯一弧", Goal: "收束",
			Chapters: []domain.OutlineEntry{{Title: "终章", CoreEvent: "结局", Hook: "无"}},
		}},
	}})

	b, err := s.Outline.CheckArcBoundary(1)
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if !b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=true at last expanded chapter")
	}
	if b.HasNextArc() {
		t.Fatal("expected no next arc")
	}
}

func TestCheckArcBoundaryNextArcInSameVolume(t *testing.T) {
	// 2 弧：第 1 弧结束应指向第 2 弧，不触发 NeedsNewVolume
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "首弧", Goal: "目标", Chapters: []domain.OutlineEntry{{Title: "章一", CoreEvent: "事件", Hook: "钩子"}}},
			{Index: 2, Title: "次弧", Goal: "目标2", EstimatedChapters: 10},
		},
	}})

	b, err := s.Outline.CheckArcBoundary(1)
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if !b.IsArcEnd {
		t.Fatal("expected arc end")
	}
	if b.IsVolumeEnd {
		t.Fatal("expected not volume end (second arc exists)")
	}
	if b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=false")
	}
	if b.NextVolume != 1 || b.NextArc != 2 {
		t.Fatalf("expected next vol=1 arc=2, got vol=%d arc=%d", b.NextVolume, b.NextArc)
	}
	if !b.NeedsExpansion {
		t.Fatal("expected NeedsExpansion=true for skeleton arc")
	}
}

func TestExpandArcCalibratesUnwrittenPlan(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "旧弧", Goal: "造成计划外的决裂", Chapters: []domain.OutlineEntry{{Title: "决裂", CoreEvent: "同伴离队", Hook: "去向不明"}}},
			{Index: 2, Title: "原骨架", Goal: "按原计划同行", EstimatedChapters: 8},
		},
	}})

	expansion := domain.ArcExpansion{
		Title: "分途追索",
		Goal:  "承认决裂已经发生，让两条行动线分别逼近同一真相",
		Chapters: []domain.OutlineEntry{
			{Title: "两张地图", CoreEvent: "两队从不同线索出发", Hook: "线索指向同一地点"},
			{Title: "隔墙回声", CoreEvent: "双方隔空影响彼此选择", Hook: "重逢代价浮现"},
		},
	}
	if err := s.ExpandArc(1, 2, expansion); err != nil {
		t.Fatalf("ExpandArc: %v", err)
	}

	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	got := volumes[0].Arcs[1]
	if got.Title != expansion.Title || got.Goal != expansion.Goal {
		t.Fatalf("expected calibrated title/goal, got title=%q goal=%q", got.Title, got.Goal)
	}
	if got.EstimatedChapters != 0 || len(got.Chapters) != 2 {
		t.Fatalf("expected expanded arc, got estimated=%d chapters=%d", got.EstimatedChapters, len(got.Chapters))
	}
	flat, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(flat) != 3 || flat[1].Chapter != 2 || flat[2].Chapter != 3 {
		t.Fatalf("expected continuous flattened outline, got %+v", flat)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.TotalChapters != 3 {
		t.Fatalf("expected total chapters 3, got %d", progress.TotalChapters)
	}

	if err := s.ExpandArc(1, 2, expansion); err != nil {
		t.Fatalf("same expansion must be idempotent: %v", err)
	}
	// 模拟上次只写完 layered JSON、派生 flat outline 与 Progress 尚未补齐。
	if err := os.Remove(filepath.Join(s.Dir(), "outline.json")); err != nil {
		t.Fatalf("remove flat outline: %v", err)
	}
	if err := s.Progress.SetTotalChapters(1); err != nil {
		t.Fatalf("set stale total: %v", err)
	}
	if err := s.ExpandArc(1, 2, expansion); err != nil {
		t.Fatalf("idempotent retry should repair derived state: %v", err)
	}
	flat, err = s.Outline.LoadOutline()
	if err != nil || len(flat) != 3 {
		t.Fatalf("flat outline was not repaired: len=%d err=%v", len(flat), err)
	}
	progress, err = s.Progress.Load()
	if err != nil || progress.TotalChapters != 3 {
		t.Fatalf("progress total was not repaired: progress=%+v err=%v", progress, err)
	}
	changed := expansion
	changed.Goal = "事后改写已展开弧"
	if err := s.ExpandArc(1, 2, changed); err == nil {
		t.Fatal("expected a different expansion to reject overwriting the expanded arc")
	}
}

func TestAppendVolumeValidation(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "首弧", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "章", CoreEvent: "事件", Hook: "钩子"}},
		}},
	}})

	validVol := domain.VolumeOutline{
		Index: 2, Title: "第二卷", Theme: "升级",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "弧一", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "新章", CoreEvent: "推进", Hook: "钩子"}},
		}},
	}

	// 正常追加应成功
	if err := s.AppendVolume(validVol); err != nil {
		t.Fatalf("AppendVolume valid: %v", err)
	}

	// Index 不递增 → 失败
	if err := s.AppendVolume(domain.VolumeOutline{
		Index: 1, Title: "重复", Theme: "x",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "弧", Goal: "g", Chapters: []domain.OutlineEntry{{Title: "ch", CoreEvent: "e", Hook: "h"}}}},
	}); err == nil {
		t.Fatal("expected error for non-increasing index")
	}

	// 无弧 → 失败
	if err := s.AppendVolume(domain.VolumeOutline{Index: 3, Title: "空", Theme: "x"}); err == nil {
		t.Fatal("expected error for volume with no arcs")
	}

	// 首弧无章节 → 失败
	if err := s.AppendVolume(domain.VolumeOutline{
		Index: 3, Title: "骨架", Theme: "x",
		Arcs: []domain.ArcOutline{{Index: 1, Title: "弧", Goal: "g", EstimatedChapters: 10}},
	}); err == nil {
		t.Fatal("expected error for first arc without chapters")
	}
}

// 注：原先用 Final 卷拒绝 append 的语义已下沉到 save_foundation 层（Phase=Complete 拒绝），
// 见 save_foundation_test.go::TestSaveFoundationAppendVolumeRejectsAfterComplete。
// store 层只保留结构性校验（Index 递增 / 首弧含章节等）。

func TestSaveAndLoadCompass(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// 空 direction 应失败
	if err := s.Outline.SaveCompass(domain.StoryCompass{EstimatedScale: "3 卷"}); err == nil {
		t.Fatal("expected error for empty ending_direction")
	}

	// 正常保存
	compass := domain.StoryCompass{
		EndingDirection: "主角面对最终抉择",
		OpenThreads:     []string{"线索A", "关系B"},
		EstimatedScale:  "预计 4-6 卷",
		LastUpdated:     12,
	}
	if err := s.Outline.SaveCompass(compass); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}

	loaded, err := s.Outline.LoadCompass()
	if err != nil {
		t.Fatalf("LoadCompass: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected compass, got nil")
	}
	if loaded.EndingDirection != "主角面对最终抉择" {
		t.Fatalf("expected direction %q, got %q", "主角面对最终抉择", loaded.EndingDirection)
	}
	if len(loaded.OpenThreads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(loaded.OpenThreads))
	}
}

// TestOutlineFeedbackPool 反馈池闭环:commit 落盘 → 跨重启可读 → 结构操作消费清空。
func TestOutlineFeedbackPool(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Outline.AppendOutlineFeedback(ChapterFeedback{Chapter: 3, Deviation: "支线膨胀", Suggestion: "下一弧收线"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Outline.AppendOutlineFeedback(ChapterFeedback{Chapter: 3, Deviation: "支线膨胀", Suggestion: "下一弧收线"}); err != nil {
		t.Fatalf("append duplicate: %v", err)
	}
	if err := s.Outline.AppendOutlineFeedback(ChapterFeedback{Chapter: 4, Suggestion: "反派提前登场"}); err != nil {
		t.Fatalf("append2: %v", err)
	}

	// 跨重启(新 Store 实例)可读——不是内存态
	s2 := NewStore(dir)
	fbs, err := s2.Outline.LoadPendingOutlineFeedback()
	if err != nil {
		t.Fatalf("load feedback: %v", err)
	}
	if len(fbs) != 2 || fbs[0].Chapter != 3 || fbs[1].Suggestion != "反派提前登场" {
		t.Fatalf("跨重启读取失败: %+v", fbs)
	}
	for _, fb := range fbs {
		if fb.At == "" {
			t.Fatal("At 应自动补齐")
		}
	}

	if err := s2.Outline.ClearOutlineFeedback(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if left, err := s2.Outline.LoadPendingOutlineFeedback(); err != nil || len(left) != 0 {
		t.Fatalf("消费后应为空: %+v", left)
	}
	// 幂等清空
	if err := s2.Outline.ClearOutlineFeedback(); err != nil {
		t.Fatalf("clear idempotent: %v", err)
	}
}

func TestOutlineFeedbackCorruptionIsNotSilentlyConsumed(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	path := filepath.Join(dir, outlineFeedbackFile)
	if err := os.WriteFile(path, []byte("{\n"), 0o644); err != nil {
		t.Fatalf("write corrupt feedback: %v", err)
	}
	if _, err := s.Outline.LoadPendingOutlineFeedback(); err == nil {
		t.Fatal("corrupt feedback must return a read error")
	}
	if err := s.Outline.ClearOutlineFeedback(); err == nil {
		t.Fatal("corrupt feedback must not be cleared as if it had been consumed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corrupt feedback should remain for diagnosis: %v", err)
	}
}
