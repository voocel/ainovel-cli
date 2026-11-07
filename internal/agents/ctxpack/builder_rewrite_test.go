package ctxpack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestWriterCompactionAndRestoreUseTargetChapterHistory(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	var volumes []domain.VolumeOutline
	for volume := 1; volume <= 4; volume++ {
		v := domain.VolumeOutline{Index: volume}
		for arc := 1; arc <= 3; arc++ {
			a := domain.ArcOutline{Index: arc}
			for range 5 {
				a.Chapters = append(a.Chapters, domain.OutlineEntry{Title: "目标章节"})
			}
			v.Arcs = append(v.Arcs, a)
			if err := s.Summaries.SaveArcSummary(domain.ArcSummary{Volume: volume, Arc: arc, Summary: fmt.Sprintf("V%dA%d摘要", volume, arc)}); err != nil {
				t.Fatal(err)
			}
		}
		volumes = append(volumes, v)
		if err := s.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: volume, Summary: fmt.Sprintf("V%d卷摘要", volume)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline(domain.FlattenOutline(volumes)); err != nil {
		t.Fatal(err)
	}
	for _, snap := range []domain.CharacterSnapshot{
		{Volume: 1, Arc: 1, Name: "主角", Status: "前期状态"},
		{Volume: 4, Arc: 3, Name: "主角", Status: "未来终局状态"},
	} {
		if err := s.Characters.SaveSnapshots(snap.Volume, snap.Arc, []domain.CharacterSnapshot{snap}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{Volume: 4, Arc: 3, Prose: []string{"未来终局规则"}}); err != nil {
		t.Fatal(err)
	}
	for _, chapter := range []int{1, 6, 16} {
		t.Run(fmt.Sprint(chapter), func(t *testing.T) {
			if err := s.Progress.Save(&domain.Progress{
				Phase: domain.PhaseWriting, Flow: domain.FlowRewriting, TotalChapters: 60,
				CurrentChapter: 61, CurrentVolume: 4, CurrentArc: 3, Layered: true,
				PendingRewrites: []int{chapter},
			}); err != nil {
				t.Fatal(err)
			}
			if chapter > 1 {
				if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: chapter - 1, Summary: "目标章的前情"}); err != nil {
					t.Fatal(err)
				}
			}
			pack := &WriterRestorePack{}
			pack.Refresh(s)
			if pack.chapter != chapter || strings.Contains(pack.text, "未来终局") {
				t.Fatalf("Writer 预标进行中之前，恢复包也应聚焦返工队列头: chapter=%d text=%s", pack.chapter, pack.text)
			}
			builders := map[string]func(*store.Store, int) (string, bool, error){"restore": buildWriterRestoreText}
			if chapter > 1 {
				builders["compact"] = buildWriterStoreSummaryText
			}
			for name, build := range builders {
				text, ok, err := build(s, 7000)
				if err != nil || !ok {
					t.Fatalf("%s: ok=%v err=%v", name, ok, err)
				}
				for _, future := range []string{"未来终局", "V4A3摘要", "V4卷摘要", `"current_volume":4`, `"current_arc":3`} {
					if strings.Contains(text, future) {
						t.Errorf("%s 注入了未来上下文 %q: %s", name, future, text)
					}
				}
				if chapter > 1 && !strings.Contains(text, "前期状态") {
					t.Errorf("%s 缺少最近的历史快照: %s", name, text)
				}
				if chapter == 1 && strings.Contains(text, "角色快照") {
					t.Errorf("%s 首章不应有弧末快照: %s", name, text)
				}
				if chapter == 6 && !strings.Contains(text, "V1A1摘要") {
					t.Errorf("%s 缺少前弧摘要: %s", name, text)
				}
				if chapter == 16 && (!strings.Contains(text, "V1A3摘要") || !strings.Contains(text, "V1卷摘要")) {
					t.Errorf("%s 缺少前弧或前卷摘要: %s", name, text)
				}
			}
		})
	}
}
