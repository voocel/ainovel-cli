package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// stripLeadingTitle 的题头形态取自真实产物：章节正文首行是 "# 第一章 拆迁区的来电"
// （中文数字，不是 "第1章"），所以不能只按阿拉伯数字匹配。
func TestStripLeadingTitle(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		title string
		want  string
	}{
		{
			name:  "markdown 题头带中文数字与标题",
			text:  "# 第一章 拆迁区的来电\n\n水管又冻住了。\n\n第二段。",
			title: "拆迁区的来电",
			want:  "水管又冻住了。\n\n第二段。",
		},
		{
			name:  "纯标题行无 # 前缀",
			text:  "拆迁区的来电\n\n水管又冻住了。",
			title: "拆迁区的来电",
			want:  "水管又冻住了。",
		},
		{
			name:  "阿拉伯数字题头且大纲无标题",
			text:  "# 第12章\n\n正文开始。",
			title: "",
			want:  "正文开始。",
		},
		{
			name:  "正文里出现同名句子不应被删",
			text:  "他想起拆迁区的来电，心里一沉。\n\n下一段。",
			title: "拆迁区的来电",
			want:  "他想起拆迁区的来电，心里一沉。\n\n下一段。",
		},
		{
			name:  "没有题头时原样返回",
			text:  "水管又冻住了。\n\n第二段。",
			title: "拆迁区的来电",
			want:  "水管又冻住了。\n\n第二段。",
		},
		{
			name:  "BOM 与前导空白被清掉",
			text:  "\ufeff\n# 第一章 拆迁区的来电\n\n正文。",
			title: "拆迁区的来电",
			want:  "正文。",
		},
		{
			name:  "单行无换行不截断",
			text:  "就一行正文",
			title: "某标题",
			want:  "就一行正文",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripLeadingTitle(tc.text, tc.title); got != tc.want {
				t.Errorf("stripLeadingTitle()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestReaderAgainstRealBook 用仓库里真实的创作产物验证目录与正文读取。
//
// 这本书是分层模式、35 章已完成，且未展开的弧里带 chapter:0 占位条目——
// 正是容易出错的形态。目录不存在时跳过，不让本地产物成为 CI 的硬依赖。
func TestReaderAgainstRealBook(t *testing.T) {
	const dir = "output/novel"
	if _, err := os.Stat(filepath.Join(dir, "meta", "progress.json")); err != nil {
		t.Skip("无真实产物，跳过")
	}
	st := storepkg.NewStore(dir)

	titles := chapterTitles(st)
	if titles[1] != "拆迁区的来电" {
		t.Errorf("第 1 章标题 = %q，想要 %q", titles[1], "拆迁区的来电")
	}
	if _, ok := titles[0]; ok {
		t.Error("标题表里不应出现第 0 章（未展开弧的占位条目）")
	}

	volumeOf, volumeTitles := volumeIndex(st)
	if _, ok := volumeOf[0]; ok {
		t.Error("卷映射里不应出现第 0 章")
	}
	if volumeOf[1] != 1 {
		t.Errorf("第 1 章应属第 1 卷，实际 %d", volumeOf[1])
	}
	for chapter, wantVolume := range map[int]int{10: 1, 11: 1, 30: 1, 31: 2, 35: 2} {
		if got := volumeOf[chapter]; got != wantVolume {
			t.Errorf("第 %d 章应属第 %d 卷，实际第 %d 卷", chapter, wantVolume, got)
		}
	}
	if volumeTitles[1] != "旧债" {
		t.Errorf("第 1 卷标题 = %q，想要 %q", volumeTitles[1], "旧债")
	}

	// 正文：题头应被剥掉，且不能吃掉首段。
	raw, err := st.Drafts.LoadChapterText(1)
	if err != nil || strings.TrimSpace(raw) == "" {
		t.Fatalf("读第 1 章失败: %v", err)
	}
	body := stripLeadingTitle(raw, titles[1])
	if strings.HasPrefix(body, "#") || strings.Contains(firstLine(body), "第一章") {
		t.Errorf("题头未被剥掉，首行=%q", firstLine(body))
	}
	if !strings.HasPrefix(body, "水管又冻住了") {
		t.Errorf("首段被破坏，首行=%q", firstLine(body))
	}
	// 剥离只应少掉题头那一行，不该丢正文。
	if lost := utf8.RuneCountInString(raw) - utf8.RuneCountInString(body); lost > 30 {
		t.Errorf("剥离题头丢了 %d 字，过多", lost)
	}
}

func TestChapterTitlesUsesCommittedTitleOnlyForCompletedChapters(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("test", 2, domain.GenreNovel); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "计划一"},
		{Chapter: 2, Title: "计划二"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatal(err)
	}
	for chapter, title := range map[int]string{1: "终稿一", 2: "未提交二"} {
		if err := st.Summaries.SaveSummary(domain.ChapterSummary{
			Chapter: chapter,
			Title:   title,
			Summary: "摘要",
		}); err != nil {
			t.Fatal(err)
		}
	}

	titles := chapterTitles(st)
	if titles[1] != "终稿一" {
		t.Fatalf("已完成章节标题 = %q，想要最终提交标题", titles[1])
	}
	if titles[2] != "计划二" {
		t.Fatalf("未完成章节标题 = %q，想要计划标题", titles[2])
	}
}

func TestVolumeIndexUsesStructuralChapterPositions(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{
		{
			Index: 1,
			Title: "第一卷",
			Arcs: []domain.ArcOutline{
				{Index: 1, Chapters: []domain.OutlineEntry{{Chapter: 1}, {Chapter: 2}}},
				// 未展开条目的 chapter 都是 0，但结构位置仍对应第 3、4 章。
				{Index: 2, Chapters: []domain.OutlineEntry{{Chapter: 0}, {Chapter: 0}}},
			},
		},
		{
			Index: 2,
			Title: "第二卷",
			Arcs: []domain.ArcOutline{
				{Index: 1, Chapters: []domain.OutlineEntry{{Chapter: 0}, {Chapter: 0}}},
				{Index: 2, EstimatedChapters: 2},
			},
		},
	}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}

	volumeOf, titles := volumeIndex(st)
	for chapter, wantVolume := range map[int]int{
		1: 1, 2: 1, 3: 1, 4: 1,
		5: 2, 6: 2, 7: 2, 8: 2,
	} {
		if got := volumeOf[chapter]; got != wantVolume {
			t.Errorf("第 %d 章卷号 = %d，想要 %d", chapter, got, wantVolume)
		}
	}
	if _, ok := volumeOf[0]; ok {
		t.Fatal("卷映射不应包含第 0 章")
	}
	if titles[1] != "第一卷" || titles[2] != "第二卷" {
		t.Fatalf("卷标题映射错误: %#v", titles)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
