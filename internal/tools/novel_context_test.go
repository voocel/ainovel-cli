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
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/stylestat"
)

func newTestContextTool(st *store.Store, refs References, style string) *ContextTool {
	return NewContextTool(st, refs, style, NewStyleStatsIndex(st))
}

func TestBuildProgressStatusHidesLayeredCapacityEstimate(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{TotalChapters: 66, Layered: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1, Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, EstimatedChapters: 64},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	result := map[string]any{}
	newTestContextTool(st, References{}, "default").buildProgressStatus(result, &contextReads{})
	status, ok := result["progress_status"].(map[string]any)
	if !ok {
		t.Fatalf("progress_status = %#v", result["progress_status"])
	}
	if status["dynamic_planning"] != true || status["outlined_chapters"] != 2 {
		t.Fatalf("动态规划进度错误: %#v", status)
	}
	if _, exists := status["total_chapters"]; exists {
		t.Fatalf("分层容量估算不得作为 total_chapters 暴露: %#v", status)
	}
}

func TestContextToolInjectsStyleStats(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	progress := &domain.Progress{TotalChapters: 10}
	body := "# 第N章\n他不是迟疑，而是恐惧。沉默了几息。像一道光。\n夜色落下。\n他走了。"
	for ch := 1; ch <= 6; ch++ {
		if err := st.Drafts.SaveFinalChapter(ch, body); err != nil {
			t.Fatalf("SaveFinalChapter: %v", err)
		}
		if _, err := st.ChapterRecords.Accept(ch, domain.ChapterOriginGenerated, body, domain.ChapterFacts{
			Title: fmt.Sprintf("第%d章", ch), Summary: "摘要", KeyEvents: []string{"事件"},
		}, domain.StyleDelta{}); err != nil {
			t.Fatalf("Accept: %v", err)
		}
		progress.CompletedChapters = append(progress.CompletedChapters, ch)
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	tool := newTestContextTool(st, References{}, "default")
	args, _ := json.Marshal(map[string]any{"chapter": 7})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Episodic map[string]json.RawMessage `json:"episodic_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	statsRaw, ok := payload.Episodic["style_stats"]
	if !ok {
		t.Fatalf("expected episodic_memory.style_stats, got keys %v", keysOf(payload.Episodic))
	}
	var stats struct {
		Chapters int `json:"chapters"`
		Patterns []struct {
			Name  string `json:"name"`
			Total int    `json:"total"`
		} `json:"patterns"`
	}
	if err := json.Unmarshal(statsRaw, &stats); err != nil {
		t.Fatalf("Unmarshal stats: %v", err)
	}
	if stats.Chapters != 6 || len(stats.Patterns) == 0 {
		t.Errorf("stats content: %+v", stats)
	}
	if usage, ok := payload.Episodic["_usage"]; !ok || len(usage) == 0 {
		t.Error("expected episodic_memory._usage annotation")
	}
}

func TestContextToolStyleStatsTitleSource(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mixedFinals     bool
		unwrittenPrefix bool
		summaryFallback bool
		withPrefix      int
		withoutPrefix   int
	}{
		{name: "committed headings", mixedFinals: true, withPrefix: 3, withoutPrefix: 3},
		{name: "unwritten outline", unwrittenPrefix: true},
		{name: "summary fallback", summaryFallback: true, withPrefix: 1, withoutPrefix: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewStore(t.TempDir())
			if err := st.Init(); err != nil {
				t.Fatal(err)
			}
			progress := &domain.Progress{TotalChapters: 8}
			var outline []domain.OutlineEntry
			for ch := 1; ch <= 8; ch++ {
				title := "风起"
				if tc.unwrittenPrefix && ch > 6 {
					title = "第一章 未写"
				}
				outline = append(outline, domain.OutlineEntry{Chapter: ch, Title: title})
				if ch > 6 {
					continue
				}
				body := "\n  # 风起  \n夜色落下。"
				if tc.mixedFinals && ch%2 == 0 {
					body = "\n  # 第一章 风起  \n夜色落下。"
				}
				summaryTitle := "风起"
				if tc.summaryFallback && ch == 6 {
					body = " \n\t"
					summaryTitle = "第一章 开端"
				}
				if err := st.Drafts.SaveFinalChapter(ch, body); err != nil {
					t.Fatal(err)
				}
				if _, err := st.ChapterRecords.Accept(ch, domain.ChapterOriginGenerated, body, domain.ChapterFacts{
					Title: summaryTitle, Summary: "摘要", KeyEvents: []string{"事件"},
				}, domain.StyleDelta{}); err != nil {
					t.Fatal(err)
				}
				if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: ch, Title: summaryTitle}); err != nil {
					t.Fatal(err)
				}
				progress.CompletedChapters = append(progress.CompletedChapters, ch)
			}
			if err := st.Outline.SaveOutline(outline); err != nil {
				t.Fatal(err)
			}
			if err := st.Progress.Save(progress); err != nil {
				t.Fatal(err)
			}
			raw, err := newTestContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(`{"chapter":7}`))
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Episodic struct {
					StyleStats *struct {
						TitleFormats *struct {
							WithPrefix    int `json:"with_prefix"`
							WithoutPrefix int `json:"without_prefix"`
						} `json:"title_formats"`
					} `json:"style_stats"`
				} `json:"episodic_memory"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Episodic.StyleStats == nil {
				t.Fatal("expected style_stats")
			}
			formats := payload.Episodic.StyleStats.TitleFormats
			if tc.withPrefix == 0 {
				if formats != nil {
					t.Fatalf("expected no title_formats, got %+v", formats)
				}
			} else if formats == nil || formats.WithPrefix != tc.withPrefix || formats.WithoutPrefix != tc.withoutPrefix {
				t.Fatalf("title_formats = %+v, want with_prefix=%d without_prefix=%d", formats, tc.withPrefix, tc.withoutPrefix)
			}
		})
	}
}

func TestStyleStatsTitleBoundsOversizedInput(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	heading := "# 第一章 " + strings.Repeat("甲", 8000)
	body := heading + "\n" + strings.Repeat("。\n", 8000)
	if err := st.Drafts.SaveFinalChapter(1, body); err != nil {
		t.Fatal(err)
	}
	longSummary := strings.Repeat("第", stylestat.TitleScanBytes) + "一章 开端"
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Title: longSummary}); err != nil {
		t.Fatal(err)
	}
	tool := newTestContextTool(st, References{}, "default")
	got := tool.styleStatsTitle(1, &contextReads{})
	if len(got) == 0 || len(got) > stylestat.TitleScanBytes {
		t.Fatalf("final title len=%d, bound=%d", len(got), stylestat.TitleScanBytes)
	}
	if !strings.HasPrefix(got, "# 第一章 ") {
		t.Fatalf("oversized final title = %q", got)
	}
	if strings.Contains(got, "一章 开端") {
		t.Fatal("summary title overrode a non-empty final heading")
	}

	if err := st.Drafts.SaveFinalChapter(2, " \n\t"); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 2, Title: longSummary}); err != nil {
		t.Fatal(err)
	}
	got = tool.styleStatsTitle(2, &contextReads{})
	if len(got) == 0 || len(got) > stylestat.TitleScanBytes {
		t.Fatalf("summary title len=%d, bound=%d", len(got), stylestat.TitleScanBytes)
	}
	if strings.Contains(got, "一章 开端") {
		t.Fatal("oversized summary title kept the tail past the scan bound")
	}
}

func TestContextToolWarnsWhenOptionalDataIsCorrupt(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "simulation_profile.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := newTestContextTool(st, References{}, "default").Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	warnings, _ := got["_warnings"].([]any)
	if len(warnings) == 0 || !strings.Contains(warnings[0].(string), "simulation_profile") {
		t.Fatalf("可选资料损坏必须显式告警: %+v", got["_warnings"])
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestContextToolRejectsCorruptCoreState(t *testing.T) {
	dir := t.TempDir()
	store := store.NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "meta", "progress.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write progress.json: %v", err)
	}

	tool := newTestContextTool(store, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	_, err = tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "progress") {
		t.Fatalf("核心事实损坏必须终止上下文装配: %v", err)
	}
}

func TestContextToolChapterModeIncludesWorkingAndReferenceFields(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SavePremise(`## 题材和基调
少年成长，偏紧张压迫。

## 题材定位
少年升级流

## 核心冲突
主角必须在宗门竞争中活下来。

## 主角目标
进入内门。

## 终局方向
成为真正的执棋者。

## 写作禁区
不提前揭露师尊真相。

## 差异化卖点
弱者逆袭。

## 差异化钩子
每阶段都要用更高代价换成长。

## 核心兑现承诺
持续兑现危机与突破。

## 故事引擎
试炼、资源争夺与身份升级共同推进。

## 中段转折
主角被迫转向另一条修行路线。
`); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "入门", CoreEvent: "主角进入宗门", Scenes: []string{"拜师", "立誓"}},
		{Chapter: 2, Title: "试炼", CoreEvent: "参加外门试炼", Scenes: []string{"集合", "出发"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{
		{Name: "林砚", Role: "主角", Description: "少年修士", Arc: "成长", Traits: []string{"冷静"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{
		{Category: "magic", Rule: "灵气可以炼化", Boundary: "凡人不可直接驾驭"},
	}); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}
	if err := s.Progress.Init(2); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{
		Chapter:    1,
		Summary:    "主角拜入宗门，确立目标。",
		Characters: []string{"林砚"},
		KeyEvents:  []string{"拜师"},
	}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if err := s.Drafts.SaveFinalChapter(1, "第一章正文结尾，留下试炼悬念。"); err != nil {
		t.Fatalf("SaveFinalChapter: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 2,
		Title:   "试炼",
		Goal:    "通过第一关",
		Contract: domain.ChapterContract{
			RequiredBeats:    []string{"必须让主角通过第一关", "必须埋下内门试炼邀请"},
			ForbiddenMoves:   []string{"不能提前揭露师尊真实身份"},
			ContinuityChecks: []string{"主角左臂旧伤仍未痊愈"},
			EvaluationFocus:  []string{"重点检查试炼节奏是否拖沓"},
		},
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{
		Volume: 1,
		Arc:    1,
		Prose:  []string{"叙述保持克制"},
	}); err != nil {
		t.Fatalf("SaveStyleRules: %v", err)
	}
	if err := s.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}

	tool := newTestContextTool(s, References{
		Consistency:      "一致性检查",
		HookTechniques:   "钩子技巧",
		QualityChecklist: "质量清单",
	}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{
		"premise",
		"premise_sections",
		"premise_structure",
		"world_rules",
		"memory_policy",
		"working_memory",
		"episodic_memory",
		"reference_pack",
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in chapter context", key)
		}
	}
	if _, ok := payload["outline"]; ok {
		t.Fatal("chapter context must not include the whole outline")
	}
	working := payload["working_memory"].(map[string]any)
	for _, key := range []string{"current_chapter_outline", "recent_summaries", "chapter_plan", "chapter_contract", "previous_tail"} {
		if _, ok := working[key]; !ok {
			t.Fatalf("expected working_memory.%s", key)
		}
	}
	episodic := payload["episodic_memory"].(map[string]any)
	if _, ok := episodic["planning_tier"]; !ok {
		t.Fatal("expected episodic_memory.planning_tier")
	}
	referencePack := payload["reference_pack"].(map[string]any)
	for _, key := range []string{"style_rules", "references"} {
		if _, ok := referencePack[key]; !ok {
			t.Fatalf("expected reference_pack.%s", key)
		}
	}
	for _, key := range []string{"planning_tier", "current_chapter_outline", "recent_summaries", "chapter_plan", "chapter_contract", "previous_tail", "style_rules", "references"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("unexpected top-level memory field %q", key)
		}
	}
}

func TestContextToolArchitectModeIncludesPlanningAndFoundation(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(6); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatalf("SetLayered: %v", err)
	}
	if err := s.Progress.UpdateVolumeArc(1, 1); err != nil {
		t.Fatalf("UpdateVolumeArc: %v", err)
	}
	if err := s.Outline.SavePremise(`## 题材和基调
群像冒险，偏冷峻史诗。

## 题材定位
群像长篇冒险

## 核心冲突
众人必须在不断失控的旧秩序中寻找新秩序。

## 主角目标
抵达真相核心。

## 终局方向
揭开古老真相并重建秩序。

## 写作禁区
不靠天降设定收尾。

## 差异化卖点
群像关系推进。

## 差异化钩子
每卷都改变队伍关系结构。

## 核心兑现承诺
持续提供发现、牺牲与选择。

## 故事引擎
旅途推进、真相调查与队伍关系共同驱动。

## 关系/成长主线
队伍从互不信任走向分裂再重组。

## 升级路径
从地方事件走向世界级危机。

## 中期转向
真相并非敌人，而是秩序本身有问题。

## 终局命题
秩序应由谁定义。
`); err != nil {
		t.Fatalf("SavePremise: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "起点", CoreEvent: "旅途开始"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Characters.Save([]domain.Character{
		{Name: "沈曜", Role: "主角", Description: "流浪剑客", Arc: "寻找真相", Traits: []string{"敏锐"}},
	}); err != nil {
		t.Fatalf("SaveCharacters: %v", err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{
		{Category: "society", Rule: "城邦林立", Boundary: "皇权不可直辖边地"},
	}); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{
		{
			Index: 1, Title: "第一卷", Theme: "踏上旅途",
			Arcs: []domain.ArcOutline{
				{Index: 1, Title: "启程", Goal: "建立队伍", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "起点"}}},
				{Index: 2, Title: "迷雾", Goal: "逼近秘密", EstimatedChapters: 5},
			},
		},
	}); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Summaries.SaveArcSummary(domain.ArcSummary{
		Volume: 1, Arc: 1, Title: "启程", Summary: "队伍建立，但因真相分歧出现裂痕。", KeyEvents: []string{"队伍建立", "分歧浮现"},
	}); err != nil {
		t.Fatalf("SaveArcSummary: %v", err)
	}
	if err := s.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: "揭开古老真相",
		EstimatedScale:  "预计 3 卷",
	}); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}
	if err := s.World.SaveStyleRules(domain.WritingStyleRules{
		Volume: 1,
		Arc:    1,
		Prose:  []string{"保持冷峻节制"},
	}); err != nil {
		t.Fatalf("SaveStyleRules: %v", err)
	}
	if err := s.RunMeta.SetPlanningTier(domain.PlanningTierLong); err != nil {
		t.Fatalf("SetPlanningTier: %v", err)
	}

	tool := newTestContextTool(s, References{
		OutlineTemplate:   "大纲模板",
		CharacterTemplate: "角色模板",
		LongformPlanning:  "长篇规划",
	}, "default")
	args, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{"memory_policy", "planning_memory", "foundation_memory", "reference_pack"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in architect context", key)
		}
	}
	planning := payload["planning_memory"].(map[string]any)
	for _, key := range []string{"planning_tier", "layered_outline", "skeleton_arcs", "arc_summaries", "compass"} {
		if _, ok := planning[key]; !ok {
			t.Fatalf("expected planning_memory.%s", key)
		}
	}
	foundation := payload["foundation_memory"].(map[string]any)
	for _, key := range []string{"premise", "premise_sections", "premise_structure", "characters", "foundation_status"} {
		if _, ok := foundation[key]; !ok {
			t.Fatalf("expected foundation_memory.%s", key)
		}
	}
	referencePack := payload["reference_pack"].(map[string]any)
	for _, key := range []string{"style_rules", "references"} {
		if _, ok := referencePack[key]; !ok {
			t.Fatalf("expected reference_pack.%s", key)
		}
	}
	for _, key := range []string{"planning_tier", "layered_outline", "skeleton_arcs", "arc_summaries", "compass", "premise", "premise_sections", "premise_structure", "characters", "foundation_status", "style_rules", "references"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("unexpected top-level memory field %q", key)
		}
	}
}

func TestContextToolArchitectModeIncludesFlatOutline(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "开端"}}); err != nil {
		t.Fatal(err)
	}

	raw, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	planning := payload["planning_memory"].(map[string]any)
	if _, ok := planning["outline"]; !ok {
		t.Fatal("expected planning_memory.outline")
	}
	if _, ok := payload["outline"]; ok {
		t.Fatal("unexpected top-level outline")
	}
}

func TestProjectLayeredOutlineKeepsOnlyFocusedArcDetails(t *testing.T) {
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, Chapters: []domain.OutlineEntry{{Title: "三"}, {Title: "四"}}},
		},
	}}

	projected, detailIncluded := projectLayeredOutlineForPlanning(volumes, 2, 1, 2)
	if !detailIncluded {
		t.Fatal("expected focused arc details")
	}
	if got := projected[0].Arcs[0]; got.Status != "completed" || len(got.Chapters) != 0 || !got.ChaptersOmitted || got.StartChapter != 1 || got.EndChapter != 2 {
		t.Fatalf("completed arc projection = %+v", got)
	}
	if got := projected[0].Arcs[1]; got.Status != "expanded" || len(got.Chapters) != 2 || got.StartChapter != 3 || got.EndChapter != 4 {
		t.Fatalf("future arc projection = %+v", got)
	}
}

func TestContextToolLongLayeredPlanningProjectsFocusedArc(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := make([]domain.VolumeOutline, 10)
	chapter := 0
	for vi := range volumes {
		volumes[vi] = domain.VolumeOutline{Index: vi + 1, Title: fmt.Sprintf("卷%d", vi+1), Theme: strings.Repeat("主题", 10)}
		for ai := 0; ai < 10; ai++ {
			arc := domain.ArcOutline{Index: ai + 1, Title: fmt.Sprintf("弧%d", ai+1), Goal: strings.Repeat("目标", 20)}
			for ci := 0; ci < 5; ci++ {
				chapter++
				arc.Chapters = append(arc.Chapters, domain.OutlineEntry{
					Title: fmt.Sprintf("第%d章", chapter), CoreEvent: strings.Repeat("关键事件", 30),
					Hook: strings.Repeat("悬念", 20), Scenes: []string{strings.Repeat("场景", 20)},
				})
			}
			volumes[vi].Arcs = append(volumes[vi].Arcs, arc)
		}
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	completed := make([]int, 297)
	for i := range completed {
		completed[i] = i + 1
	}
	if err := s.Progress.Save(&domain.Progress{
		Phase: domain.PhaseWriting, Layered: true, CompletedChapters: completed,
		CurrentVolume: 6, CurrentArc: 10,
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		PlanningMemory struct {
			LayeredOutline []planningVolumeOutline `json:"layered_outline"`
		} `json:"planning_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	current := payload.PlanningMemory.LayeredOutline[5].Arcs[9]
	if len(current.Chapters) != 5 {
		t.Fatalf("current arc chapters = %d, want 5", len(current.Chapters))
	}
	future := payload.PlanningMemory.LayeredOutline[6].Arcs[0]
	if len(future.Chapters) != 0 || !future.ChaptersOmitted {
		t.Fatalf("future arc projection = %+v", future)
	}

	focusedRaw, err := newTestContextTool(s, References{}, "default").Execute(
		context.Background(),
		json.RawMessage(`{"volume":7,"arc":1}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var focusedPayload struct {
		PlanningMemory struct {
			LayeredOutline []planningVolumeOutline `json:"layered_outline"`
		} `json:"planning_memory"`
	}
	if err := json.Unmarshal(focusedRaw, &focusedPayload); err != nil {
		t.Fatal(err)
	}
	focused := focusedPayload.PlanningMemory.LayeredOutline[6].Arcs[0]
	if len(focused.Chapters) != 5 || focused.ChaptersOmitted {
		t.Fatalf("focused arc projection = %+v", focused)
	}
}

func TestContextToolReadsFocusedPlanningScope(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	tool := newTestContextTool(s, References{}, "default")
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"volume":1}`)); err == nil || !strings.Contains(err.Error(), "provided together") {
		t.Fatalf("expected paired planning scope error, got %v", err)
	}
	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{Index: 1, Arcs: []domain.ArcOutline{
		{Index: 1, Chapters: []domain.OutlineEntry{{Title: "序幕"}}},
		{Index: 2, Title: "暗潮", Goal: "找出幕后势力", EstimatedChapters: 6},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Save(&domain.Progress{CompletedChapters: []int{1}, CurrentVolume: 1, CurrentArc: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"volume":2,"arc":1}`)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing planning scope error, got %v", err)
	}
	skeletonRaw, err := tool.Execute(context.Background(), json.RawMessage(`{"volume":1,"arc":2}`))
	if err != nil {
		t.Fatalf("focused skeleton arc: %v", err)
	}
	var skeletonPayload struct {
		PlanningMemory struct {
			LayeredOutline []planningVolumeOutline `json:"layered_outline"`
		} `json:"planning_memory"`
	}
	if err := json.Unmarshal(skeletonRaw, &skeletonPayload); err != nil {
		t.Fatal(err)
	}
	prior := skeletonPayload.PlanningMemory.LayeredOutline[0].Arcs[0]
	if len(prior.Chapters) != 0 || !prior.ChaptersOmitted {
		t.Fatalf("unfocused prior arc = %+v", prior)
	}
	skeleton := skeletonPayload.PlanningMemory.LayeredOutline[0].Arcs[1]
	if skeleton.Status != "skeleton" || skeleton.Title != "暗潮" || skeleton.Goal != "找出幕后势力" || skeleton.EstimatedChapters != 6 || len(skeleton.Chapters) != 0 {
		t.Fatalf("focused skeleton = %+v", skeleton)
	}

	if err := s.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{{
			Index:    1,
			Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Save(&domain.Progress{CompletedChapters: []int{1, 2}, CurrentVolume: 1, CurrentArc: 1}); err != nil {
		t.Fatal(err)
	}
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"volume":1,"arc":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		PlanningMemory struct {
			LayeredOutline []planningVolumeOutline `json:"layered_outline"`
			OutlineDetail  map[string]int          `json:"outline_detail"`
		} `json:"planning_memory"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	focused := payload.PlanningMemory.LayeredOutline[0].Arcs[0]
	if focused.Status != "completed" || len(focused.Chapters) != 2 || focused.ChaptersOmitted {
		t.Fatalf("completed focused arc projection = %+v", focused)
	}
	if payload.PlanningMemory.OutlineDetail["volume"] != 1 || payload.PlanningMemory.OutlineDetail["arc"] != 1 {
		t.Fatalf("outline detail = %+v", payload.PlanningMemory.OutlineDetail)
	}

	if err := s.Outline.ClearLayeredOutline(); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"volume":1,"arc":1}`)); err == nil || !strings.Contains(err.Error(), "requires a layered outline") {
		t.Fatalf("expected flat outline planning scope error, got %v", err)
	}
}

func TestContextToolWriterDoesNotIncludeWholeOutline(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	outline := make([]domain.OutlineEntry, 200)
	for i := range outline {
		outline[i] = domain.OutlineEntry{
			Chapter: i + 1, Title: fmt.Sprintf("第%d章", i+1),
			CoreEvent: strings.Repeat("事件", 20), Hook: strings.Repeat("悬念", 10),
		}
	}
	if err := s.Outline.SaveOutline(outline); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, TotalChapters: 200}); err != nil {
		t.Fatal(err)
	}

	raw, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), json.RawMessage(`{"chapter":100}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["outline"]; ok {
		t.Fatal("writer payload must not include the whole outline")
	}
	working := payload["working_memory"].(map[string]any)
	if _, ok := working["current_chapter_outline"]; !ok {
		t.Fatal("writer payload must retain the current chapter outline")
	}
	window, ok := working["outline_window"].([]any)
	if !ok || len(window) != domain.ReviewInterval {
		t.Fatalf("writer outline window = %T/%d, want %d entries", working["outline_window"], len(window), domain.ReviewInterval)
	}
}

func TestContextToolSelectedMemoryRecallsStoryThreadsAndReviewLessons(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "邀约", CoreEvent: "长老暗中给出内门试炼邀请", Scenes: []string{"密谈", "留下试炼令"}},
		{Chapter: 2, Title: "试炼前夜", CoreEvent: "林砚准备回应内门试炼邀请", Hook: "谁在背后推动这场试炼", Scenes: []string{"整理线索", "决定赴约"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init(8); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "trial_invite", Description: "内门试炼邀请的真实目的", PlantedAt: 1, Status: "planted"},
		{ID: "trial_mastermind", Description: "谁在背后推动这场试炼", PlantedAt: 1, Status: "planted"},
		{ID: "trial_rules", Description: "试炼规则碑文残卷", PlantedAt: 1, Status: "planted"},
		{ID: "outer_disciple", Description: "外门弟子的旧债纠纷", PlantedAt: 1, Status: "planted"},
		{ID: "elder_token", Description: "长老手中令牌的来历", PlantedAt: 1, Status: "planted"},
		{ID: "hidden_gate", Description: "山门背后的隐藏通道", PlantedAt: 1, Status: "planted"},
		{ID: "trial_bet", Description: "试炼盘口的幕后操盘人", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}
	if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{
		Chapter: 2,
		Title:   "试炼前夜",
		Goal:    "决定是否回应邀请",
		Contract: domain.ChapterContract{
			PayoffPoints: []string{"回应内门试炼邀请"},
			HookGoal:     "抛出谁在背后推动试炼",
		},
	}); err != nil {
		t.Fatalf("SaveChapterPlan: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter:        1,
		Scope:          "chapter",
		Verdict:        "polish",
		Summary:        "主线启动完成，但伏笔不够明确。",
		ContractStatus: "partial",
		ContractMisses: []string{"未明确埋下内门试炼邀请"},
		Issues: []domain.ConsistencyIssue{
			{Type: "hook", Severity: "warning", Description: "章末钩子不够具体"},
		},
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}

	tool := newTestContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			StoryThreads  []domain.RecallItem `json:"story_threads"`
			ReviewLessons []domain.RecallItem `json:"review_lessons"`
		} `json:"selected_memory"`
		Summary string `json:"_loading_summary"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Selected.StoryThreads) == 0 {
		t.Fatal("expected story thread recall items")
	}
	if len(payload.Selected.ReviewLessons) == 0 {
		t.Fatal("expected review lesson recall items")
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "内门试炼邀请") {
		t.Fatalf("expected story thread recall to mention invite, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "推动这场试炼") {
		t.Fatalf("expected story thread recall to mention trial mastermind, got %+v", payload.Selected.StoryThreads)
	}
	if containsRecallSummary(payload.Selected.StoryThreads, "试炼规则碑文残卷") {
		t.Fatalf("expected weak-overlap foreshadow to stay out, got %+v", payload.Selected.StoryThreads)
	}
	if containsRecallSummary(payload.Selected.StoryThreads, "建议回看第") {
		t.Fatalf("expected related_chapters not to be duplicated into story_threads, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.ReviewLessons, "contract 漏项") {
		t.Fatalf("expected review lesson recall to mention contract miss, got %+v", payload.Selected.ReviewLessons)
	}
	if !strings.Contains(payload.Summary, "线索召回:") || !strings.Contains(payload.Summary, "评审召回:") {
		t.Fatalf("expected loading summary to report selected memory, got %q", payload.Summary)
	}
}

// 久挂未回收的伏笔即使与当前章关键词无关，也应被账龄回填进 story_threads——
// 这正是相关性召回的盲区（独自悬挂太久、却没在本章撞上关键词的那根线）。
// 近期埋下的伏笔（账龄 < 阈值）不应被误标为"未回收"。
func TestContextToolSelectedMemorySurfacesAgingForeshadow(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 当前章主题与所有伏笔都不沾边，确保相关性召回为空，只剩账龄回填生效。
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 50, Title: "瘟疫", CoreEvent: "林砚在城南医馆救治瘟疫病患", Scenes: []string{"熬药", "封锁街巷"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init(60); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	// 6 条满足召回阈值；前两条账龄 ≥30（久挂），后四条账龄 <30（近期）。
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "ancient_seal", Description: "上古封印的裂隙", PlantedAt: 3, Status: "planted"},
		{ID: "lost_bloodline", Description: "主角失落的血脉来历", PlantedAt: 5, Status: "advanced"},
		{ID: "market_feud", Description: "昨夜集市的口角", PlantedAt: 47, Status: "planted"},
		{ID: "rumor_a", Description: "近日传闻甲", PlantedAt: 48, Status: "planted"},
		{ID: "rumor_b", Description: "近日传闻乙", PlantedAt: 48, Status: "planted"},
		{ID: "rumor_c", Description: "近日传闻丙", PlantedAt: 49, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := newTestContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 50})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			StoryThreads []domain.RecallItem `json:"story_threads"`
		} `json:"selected_memory"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// 两条久挂伏笔应被回填，且带"未回收"账龄标注。
	if !containsRecallSummary(payload.Selected.StoryThreads, "上古封印的裂隙") {
		t.Fatalf("expected aging foreshadow to surface despite no relevance, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "失落的血脉") {
		t.Fatalf("expected second aging foreshadow to surface, got %+v", payload.Selected.StoryThreads)
	}
	if !containsRecallSummary(payload.Selected.StoryThreads, "未回收") {
		t.Fatalf("expected aging item to carry overdue annotation, got %+v", payload.Selected.StoryThreads)
	}
	// 近期伏笔（账龄 <30 且不相关）不应被回填。
	if containsRecallSummary(payload.Selected.StoryThreads, "昨夜集市的口角") {
		t.Fatalf("recent foreshadow must not be labeled overdue, got %+v", payload.Selected.StoryThreads)
	}
}

func TestContextToolSelectedMemoryIncludesGlobalReviewLessons(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "开端", CoreEvent: "故事开始"},
		{Chapter: 2, Title: "推进", CoreEvent: "主线继续推进"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init(6); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 1,
		Scope:   "global",
		Verdict: "polish",
		Summary: "全局推进合格，但角色目标表达还不够稳定。",
		Issues: []domain.ConsistencyIssue{
			{Type: "character", Severity: "warning", Description: "主角目标表达不够稳定"},
		},
	}); err != nil {
		t.Fatalf("SaveReview(global): %v", err)
	}

	tool := newTestContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload struct {
		Selected struct {
			ReviewLessons []domain.RecallItem `json:"review_lessons"`
		} `json:"selected_memory"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !containsRecallSummary(payload.Selected.ReviewLessons, "主角目标表达不够稳定") {
		t.Fatalf("expected global review lesson to be recalled, got %+v", payload.Selected.ReviewLessons)
	}
}

func TestContextToolKeepsFullForeshadowWhenRecallNotTriggered(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "起势", CoreEvent: "故事起势"},
		{Chapter: 2, Title: "推进", CoreEvent: "继续推进"},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init(4); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "small_1", Description: "第一条小伏笔", PlantedAt: 1, Status: "planted"},
		{ID: "small_2", Description: "第二条小伏笔", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := newTestContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	episodic := payload["episodic_memory"].(map[string]any)
	if _, ok := episodic["foreshadow_ledger"]; !ok {
		t.Fatal("expected full foreshadow ledger to remain when selected recall is not triggered")
	}
	if _, ok := payload["selected_memory"]; ok {
		t.Fatalf("expected no selected_memory for small foreshadow sets, got %+v", payload["selected_memory"])
	}
}

func TestContextToolFallsBackToFullForeshadowWhenSelectionIsTooSparse(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "邀约", CoreEvent: "长老暗中给出内门试炼邀请"},
		{Chapter: 2, Title: "试炼前夜", CoreEvent: "林砚准备回应内门试炼邀请", Scenes: []string{"整理线索", "决定赴约"}},
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	if err := s.Progress.Init(8); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.World.SaveForeshadowLedger([]domain.ForeshadowEntry{
		{ID: "trial_invite", Description: "内门试炼邀请的真实目的", PlantedAt: 1, Status: "planted"},
		{ID: "trial_rules", Description: "试炼规则碑文残卷", PlantedAt: 1, Status: "planted"},
		{ID: "outer_disciple", Description: "外门弟子的旧债纠纷", PlantedAt: 1, Status: "planted"},
		{ID: "elder_token", Description: "长老手中令牌的来历", PlantedAt: 1, Status: "planted"},
		{ID: "hidden_gate", Description: "山门背后的隐藏通道", PlantedAt: 1, Status: "planted"},
		{ID: "trial_bet", Description: "试炼盘口的幕后操盘人", PlantedAt: 1, Status: "planted"},
	}); err != nil {
		t.Fatalf("SaveForeshadowLedger: %v", err)
	}

	tool := newTestContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	episodic := payload["episodic_memory"].(map[string]any)
	if _, ok := episodic["foreshadow_ledger"]; !ok {
		t.Fatal("expected full foreshadow ledger when selection is too sparse")
	}
	if selected, ok := payload["selected_memory"].(map[string]any); ok {
		if _, exists := selected["story_threads"]; exists {
			t.Fatalf("expected sparse story_threads to fall back to full ledger, got %+v", selected["story_threads"])
		}
	}
}

func containsRecallSummary(items []domain.RecallItem, want string) bool {
	for _, item := range items {
		if strings.Contains(item.Summary, want) {
			return true
		}
	}
	return false
}

func TestContextToolInjectsRewriteBriefForPendingRewriteChapter(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Progress.MarkChapterComplete(2, 3000, "", ""); err != nil {
		t.Fatalf("MarkChapterComplete: %v", err)
	}
	if _, err := s.ChapterRecords.Accept(2, domain.ChapterOriginGenerated, "正文", domain.ChapterFacts{
		Title: "第二章", Summary: "摘要", KeyEvents: []string{"事件"},
	}, domain.StyleDelta{}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := s.Progress.SetPendingRewrites([]int{2}, "节奏拖沓，需要压缩前半段"); err != nil {
		t.Fatalf("SetPendingRewrites: %v", err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 2,
		Scope:   "chapter",
		Verdict: "rewrite",
		Summary: "前半段铺垫过长，冲突迟迟不出现。",
		Issues: []domain.ConsistencyIssue{
			{Type: "pacing", Severity: "error", Description: "前 2000 字无推进"},
		},
		ContractMisses: []string{"未兑现试炼开场"},
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}

	tool := newTestContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	working := payload["working_memory"].(map[string]any)
	brief, ok := working["rewrite_brief"].(map[string]any)
	if !ok {
		t.Fatalf("expected working_memory.rewrite_brief, got %T", working["rewrite_brief"])
	}
	if got := brief["reason"]; got != "节奏拖沓，需要压缩前半段" {
		t.Fatalf("expected rewrite reason, got %v", got)
	}
	if got, _ := brief["review_summary"].(string); !strings.Contains(got, "铺垫过长") {
		t.Fatalf("expected review summary from chapter review, got %v", brief["review_summary"])
	}
	if issues, _ := brief["issues"].([]any); len(issues) == 0 {
		t.Fatalf("expected review issues in rewrite_brief, got %v", brief["issues"])
	}
	if misses, _ := brief["contract_misses"].([]any); len(misses) == 0 {
		t.Fatalf("expected contract misses in rewrite_brief, got %v", brief["contract_misses"])
	}
}

func TestContextToolOmitsRewriteBriefForNormalChapter(t *testing.T) {
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := newTestContextTool(s, References{}, "default")
	args, err := json.Marshal(map[string]any{"chapter": 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	working := payload["working_memory"].(map[string]any)
	if _, ok := working["rewrite_brief"]; ok {
		t.Fatal("expected no rewrite_brief for chapter outside PendingRewrites")
	}
}

func TestContextToolLoadsArcReviewAffectingEarlierChapter(t *testing.T) {
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Progress.Init(4); err != nil {
		t.Fatal(err)
	}
	for chapter := 1; chapter <= 4; chapter++ {
		if err := s.Progress.MarkChapterComplete(chapter, 100, "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ChapterRecords.Accept(chapter, domain.ChapterOriginGenerated, "正文", domain.ChapterFacts{
			Title: fmt.Sprintf("第%d章", chapter), Summary: "摘要", KeyEvents: []string{"事件"},
		}, domain.StyleDelta{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Progress.SetPendingRewrites([]int{3}, "弧评审返工"); err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveReview(domain.ReviewEntry{
		Chapter: 4, Scope: "arc", Verdict: "polish", Summary: "第二弧节奏需压缩", AffectedChapters: []int{3},
		Issues: []domain.ConsistencyIssue{{
			Type: "pacing", Severity: "error", Description: "第3章铺垫过长", Evidence: "冲突迟到",
			Chapters: []int{3}, RequiresChange: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := newTestContextTool(s, References{}, "default").Execute(context.Background(), json.RawMessage(`{"chapter":3}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	working := payload["working_memory"].(map[string]any)
	brief, _ := working["rewrite_brief"].(map[string]any)
	if brief == nil || !strings.Contains(fmt.Sprint(brief["review_summary"]), "第二弧") {
		t.Fatalf("expected arc review handoff for chapter 3, got %#v", brief)
	}
}

func TestContextToolDoesNotInjectUserDirectives(t *testing.T) {
	// save_directive 已移除：novel_context 不再注入 working_memory.user_directives，
	// 长期写作要求统一走 user_rules。锁死这条，防止回归。
	dir := t.TempDir()
	s := store.NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(3); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}

	tool := newTestContextTool(s, References{}, "default")
	for name, chapter := range map[string]int{"writer": 1, "architect": 0} {
		args, _ := json.Marshal(map[string]any{"chapter": chapter})
		result, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("[%s] Execute: %v", name, err)
		}
		var payload map[string]any
		if err := json.Unmarshal(result, &payload); err != nil {
			t.Fatalf("[%s] Unmarshal: %v", name, err)
		}
		working, ok := payload["working_memory"].(map[string]any)
		if !ok {
			t.Fatalf("[%s] missing working_memory", name)
		}
		if _, exists := working["user_directives"]; exists {
			t.Errorf("[%s] working_memory 不应再有 user_directives（已统一到 user_rules）", name)
		}
		// user_rules 仍应稳定注入
		if _, ok := working["user_rules"].(map[string]any); !ok {
			t.Errorf("[%s] working_memory.user_rules 应稳定注入", name)
		}
	}
}

func TestContextToolComputesRuleViolationsFromAcceptedContent(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.Progress.Save(&domain.Progress{TotalChapters: 3, Phase: domain.PhaseWriting}); err != nil {
		t.Fatalf("progress: %v", err)
	}
	snap := rules.BuildSnapshot([]rules.Candidate{{
		Source: "test", Structured: rules.Structured{ForbiddenPhrases: []string{"不禁"}},
	}})
	if err := st.UserRules.Save(&snap); err != nil {
		t.Fatalf("save rules: %v", err)
	}
	if _, err := st.ChapterRecords.Accept(2, domain.ChapterOriginGenerated, "他不禁回头。", domain.ChapterFacts{
		Title: "第二章", Summary: "回头", KeyEvents: []string{"回头"},
	}, domain.StyleDelta{}); err != nil {
		t.Fatalf("accept chapter: %v", err)
	}

	tool := newTestContextTool(st, References{}, "default")
	args, _ := json.Marshal(map[string]any{"chapter": 2})
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	vs, ok := result["rule_violations"].([]any)
	if !ok || len(vs) != 1 {
		t.Fatalf("rule_violations 必须注入章节上下文, got %v", result["rule_violations"])
	}

	// 规则改变后，同一接纳正文应立即按新规则重算，不保留旧结果。
	updated := rules.BuildSnapshot([]rules.Candidate{{Source: "test"}})
	if err := st.UserRules.Save(&updated); err != nil {
		t.Fatalf("update rules: %v", err)
	}
	raw3, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute updated rules: %v", err)
	}
	var result3 map[string]any
	_ = json.Unmarshal(raw3, &result3)
	if _, has := result3["rule_violations"]; has {
		t.Fatal("规则更新后不应残留旧的 rule_violations")
	}
}
