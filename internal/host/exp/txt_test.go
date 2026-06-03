package exp

import (
	"strings"
	"testing"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

func TestStripChapterTitleHeader(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain body untouched", "他望着窗外。", "他望着窗外。"},
		{"strip h1 chinese title", "# 第 1 章  雨夜归人\n\n他望着窗外。", "他望着窗外。"},
		{"strip h2 with chapter token", "## 第二章\n\n他望着窗外。", "他望着窗外。"},
		{"keep body even if no header", "正文第一句。\n第二句。", "正文第一句。\n第二句。"},
		{"do not strip non-chapter heading", "# 序章\n他望着窗外。", "# 序章\n他望着窗外。"},
		{"single line header only", "# 第 1 章", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripChapterTitleHeader(c.in)
			if got != c.want {
				t.Fatalf("stripChapterTitleHeader\nin   = %q\nwant = %q\ngot  = %q", c.in, c.want, got)
			}
		})
	}
}

func TestBuildTitleIndex(t *testing.T) {
	outline := []domain.OutlineEntry{
		{Chapter: 1, Title: "雨夜归人"},
		{Chapter: 2, Title: ""}, // 空标题应被过滤
		{Chapter: 3, Title: "破晓"},
	}
	idx := buildTitleIndex(outline)
	if got := idx[1]; got != "雨夜归人" {
		t.Errorf("ch1 title: got %q want 雨夜归人", got)
	}
	if _, ok := idx[2]; ok {
		t.Errorf("ch2 should be absent (empty title)")
	}
	if got := idx[3]; got != "破晓" {
		t.Errorf("ch3 title: got %q want 破晓", got)
	}
}

func TestBuildLocations(t *testing.T) {
	volumes := []domain.VolumeOutline{
		{Index: 1, Title: "起源", Arcs: []domain.ArcOutline{
			{Index: 1, Title: "少年初登场", Chapters: []domain.OutlineEntry{{}, {}}}, // 2 章
			{Index: 2, Title: "宗门试炼", Chapters: []domain.OutlineEntry{{}}},      // 1 章
		}},
		{Index: 2, Title: "崛起", Arcs: []domain.ArcOutline{
			{Index: 1, Title: "初战", Chapters: []domain.OutlineEntry{{}}},
		}},
	}
	locs := buildLocations(volumes)

	if loc := locs[1]; !loc.IsFirstOfVolume || !loc.IsFirstOfArc || loc.VolumeIdx != 1 || loc.ArcIdx != 1 {
		t.Errorf("ch1 loc = %+v", loc)
	}
	if loc := locs[2]; loc.IsFirstOfVolume || loc.IsFirstOfArc {
		t.Errorf("ch2 should not be first of vol/arc: %+v", loc)
	}
	if loc := locs[3]; loc.IsFirstOfVolume || !loc.IsFirstOfArc || loc.ArcIdx != 2 {
		t.Errorf("ch3 should start arc 2 only: %+v", loc)
	}
	if loc := locs[4]; !loc.IsFirstOfVolume || !loc.IsFirstOfArc || loc.VolumeIdx != 2 {
		t.Errorf("ch4 should start volume 2 + arc 1: %+v", loc)
	}
}

func TestRenderTXT_FrontMatterAndChapter(t *testing.T) {
	got := renderTXT(
		"光斑",
		"  这是一个关于光与影的故事。  ",
		[]int{1, 2},
		chapterTitleIndex{1: "雨夜归人", 2: "破晓"},
		nil,
		map[int]string{
			1: "# 第 1 章 雨夜归人\n\n他望着窗外。",
			2: "她推开门。",
		},
	)
	if !strings.HasPrefix(got, "《光斑》\n\n") {
		t.Errorf("missing book title at start:\n%s", got)
	}
	if !strings.Contains(got, "这是一个关于光与影的故事。") {
		t.Errorf("missing premise body")
	}
	if !strings.Contains(got, "第 1 章  雨夜归人") {
		t.Errorf("missing ch1 header")
	}
	if !strings.Contains(got, "他望着窗外。") {
		t.Errorf("missing ch1 body")
	}
	if strings.Contains(got, "# 第 1 章") {
		t.Errorf("body markdown header not stripped:\n%s", got)
	}
	if !strings.Contains(got, "第 2 章  破晓") {
		t.Errorf("missing ch2 header")
	}
}

func TestRenderTXT_EmptyNovelNameAndPremiseNoFrontMatter(t *testing.T) {
	got := renderTXT(
		"", "",
		[]int{1},
		chapterTitleIndex{1: "雨夜归人"},
		nil,
		map[int]string{1: "正文。"},
	)
	if strings.Contains(got, "《") {
		t.Errorf("should not contain book title brackets: %s", got)
	}
	if !strings.HasPrefix(got, "第 1 章  雨夜归人") {
		t.Errorf("expect chapter header at very start: %s", got)
	}
}

func TestRenderTXT_LayeredVolumeArc(t *testing.T) {
	locs := map[int]chapterLocation{
		1: {VolumeIdx: 1, VolumeTitle: "起源", ArcIdx: 1, ArcTitle: "登场", IsFirstOfVolume: true, IsFirstOfArc: true},
		2: {VolumeIdx: 1, VolumeTitle: "起源", ArcIdx: 1, ArcTitle: "登场"},
	}
	got := renderTXT(
		"X", "", []int{1, 2},
		chapterTitleIndex{1: "A", 2: "B"},
		locs,
		map[int]string{1: "正文一。", 2: "正文二。"},
	)
	if !strings.Contains(got, "第 1 卷  起源") {
		t.Errorf("missing volume header: %s", got)
	}
	if !strings.Contains(got, "第 1 弧  登场") {
		t.Errorf("missing arc header: %s", got)
	}
	// 卷/弧标题只在第一章前出现一次
	if strings.Count(got, "第 1 卷") != 1 {
		t.Errorf("volume header should appear exactly once: %s", got)
	}
}

func TestRenderTXT_ChapterWithoutTitleFallsBackToNumberOnly(t *testing.T) {
	got := renderTXT(
		"", "", []int{5},
		chapterTitleIndex{}, // 没有标题
		nil,
		map[int]string{5: "正文。"},
	)
	if !strings.Contains(got, "第 5 章\n\n") {
		t.Errorf("expect 'first 5 章' fallback header: %s", got)
	}
}
