package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func completedLayeredContextBook(t *testing.T) *store.Store {
	t.Helper()
	s := completedBook(t, 60)
	var volumes []domain.VolumeOutline
	for volume := 1; volume <= 4; volume++ {
		v := domain.VolumeOutline{Index: volume, Title: fmt.Sprintf("第%d卷", volume), Final: volume == 4}
		for arc := 1; arc <= 3; arc++ {
			a := domain.ArcOutline{Index: arc, Title: fmt.Sprintf("第%d弧", arc)}
			for range 5 {
				a.Chapters = append(a.Chapters, domain.OutlineEntry{Title: "章节"})
			}
			v.Arcs = append(v.Arcs, a)
		}
		volumes = append(volumes, v)
		if err := s.Summaries.SaveVolumeSummary(domain.VolumeSummary{Volume: volume, Summary: fmt.Sprintf("第%d卷摘要", volume)}); err != nil {
			t.Fatal(err)
		}
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
	if err := s.Progress.UpdateVolumeArc(4, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.Characters.Save([]domain.Character{{Name: "林砚", Role: "主角", Description: "药铺少年"}}); err != nil {
		t.Fatal(err)
	}
	for _, snap := range []domain.CharacterSnapshot{
		{Volume: 1, Arc: 1, Name: "林砚", Status: "前期状态"},
		{Volume: 4, Arc: 3, Name: "林砚", Status: "终局状态"},
	} {
		if err := s.Characters.SaveSnapshots(snap.Volume, snap.Arc, []domain.CharacterSnapshot{snap}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{Volume: 4, Arc: 3, Prose: []string{"终章不升华"}}); err != nil {
		t.Fatal(err)
	}
	for chapter := 1; chapter <= 60; chapter++ {
		saveTestChapterRecord(t, s, chapter, fmt.Sprintf("第%d章正文", chapter))
	}
	if err := ReopenBook(s, []int{1, 31, 32}, "修改前期章节"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestContextToolRewriteUsesTargetChapterHistory(t *testing.T) {
	s := completedLayeredContextBook(t)
	tool := newTestContextTool(s, References{}, "default")
	for _, tc := range []struct {
		chapter, volume, arc, index int
		wantSnapshot                bool
	}{
		{1, 1, 1, 1, false},
		{5, 1, 1, 5, false},
		{6, 1, 2, 1, true},
		{16, 2, 1, 1, true},
		{31, 3, 1, 1, true},
		{60, 4, 3, 5, true},
	} {
		t.Run(fmt.Sprint(tc.chapter), func(t *testing.T) {
			raw, err := tool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"chapter":%d}`, tc.chapter)))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Working struct {
					Current domain.OutlineEntry    `json:"current_chapter_outline"`
					Volumes []domain.VolumeSummary `json:"volume_summaries"`
					Finale  string                 `json:"finale"`
				} `json:"working_memory"`
				Episodic struct {
					Position struct {
						Volume int `json:"volume"`
						Arc    int `json:"arc"`
						Index  int `json:"arc_chapter_index"`
					} `json:"position"`
					Snapshots  []domain.CharacterSnapshot `json:"character_snapshots"`
					Characters []domain.Character         `json:"characters"`
				} `json:"episodic_memory"`
				Reference struct {
					Rules *domain.WritingStyleRules `json:"style_rules"`
				} `json:"reference_pack"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			pos := got.Episodic.Position
			if got.Working.Current.Chapter != tc.chapter || pos.Volume != tc.volume || pos.Arc != tc.arc || pos.Index != tc.index {
				t.Errorf("应按目标章定位: current=%+v position=%+v", got.Working.Current, pos)
			}
			if tc.wantSnapshot {
				if len(got.Episodic.Snapshots) != 1 || got.Episodic.Snapshots[0].Status != "前期状态" {
					t.Errorf("应读取目标章之前的最近快照: %+v", got.Episodic.Snapshots)
				}
			} else if len(got.Episodic.Snapshots) != 0 || len(got.Episodic.Characters) != 1 {
				t.Errorf("无历史快照时应使用初始角色设定: %+v", got.Episodic)
			}
			if got.Reference.Rules != nil {
				t.Errorf("不得把目标弧结束后生成的规则当作当前规则: %+v", got.Reference.Rules)
			}
			if len(got.Working.Volumes) != tc.volume-1 {
				t.Errorf("只能加载目标章之前已结束卷的摘要: %+v", got.Working.Volumes)
			}
			for i, summary := range got.Working.Volumes {
				if summary.Volume != i+1 {
					t.Errorf("卷摘要越界: %+v", summary)
				}
			}
			if (got.Working.Finale != "") != (tc.volume == 4) {
				t.Errorf("收官规则必须按目标卷判断: %q", got.Working.Finale)
			}
		})
	}
}

func TestContextToolRewriteKeepsPriorArcRules(t *testing.T) {
	s := completedLayeredContextBook(t)
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{Volume: 1, Arc: 1, Prose: []string{"保持克制"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), json.RawMessage(`{"chapter":6}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"style_rules":`) || !strings.Contains(string(raw), "保持克制") {
		t.Fatalf("前一弧提炼的规则应继续继承: %s", raw)
	}
}

func TestContextToolRewriteFallbackReadsOnlyPriorProse(t *testing.T) {
	s := completedLayeredContextBook(t)
	for chapter, marker := range map[int]string{1: "前期药铺", 59: "未来终局"} {
		content := strings.Repeat(marker+"的雨声落在檐下。", 6) + "\n\n林砚说：\"" + marker + "这是我的回答。\""
		if err := s.Drafts.SaveFinalChapter(chapter, content); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), json.RawMessage(`{"chapter":6}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Reference map[string]json.RawMessage `json:"reference_pack"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"style_anchors", "voice_samples"} {
		if !strings.Contains(string(got.Reference[key]), "前期药铺") || strings.Contains(string(got.Reference[key]), "未来终局") {
			t.Errorf("%s 应仅使用目标章之前的正文: %s", key, got.Reference[key])
		}
	}
}

func TestContextToolRewriteDoesNotReadFutureAggregates(t *testing.T) {
	s := completedLayeredContextBook(t)
	for _, rel := range []string{"meta/snapshots/v04a03.json", "summaries/vol-v04.json"} {
		if err := os.WriteFile(filepath.Join(s.Dir(), rel), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), json.RawMessage(`{"chapter":6}`)); err != nil {
		t.Fatalf("未来工件不属于当前章上下文，不应参与读取: %v", err)
	}
}
