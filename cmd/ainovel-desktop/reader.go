package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// ── 章节阅读器 ──
//
// 桌面版此前唯一能看到文字的地方是 ProsePane，而它的数据来自 engine:stream——
// 那条通道是有损的（drop-oldest），只能当"正在写什么"的预览。结果是一本写完的书
// 在软件里读不了，必须导出后用别的工具打开。
//
// 阅读器直读 store 的终稿（chapters/NN.md），与导出走同一个数据源
// （internal/host/exp/exporter.go 用的也是 LoadChapterText），因此所见即成品。
//
// 读取方式沿用 foundation.go：以书目录新建只读 Store。引擎落盘是 temp+fsync+rename
// 原子替换，并发读只会读到某个完整版本，不会读到写坏的中间态。

// ChapterMeta 是目录项（不含正文，供列表渲染）。
type ChapterMeta struct {
	Chapter int    `json:"chapter"`
	Title   string `json:"title"`
	Words   int    `json:"words"`
	// Volume/Arc 仅长篇分层模式有值，用于目录分组。
	Volume      int    `json:"volume"`
	VolumeTitle string `json:"volumeTitle"`
}

// BookContents 是整本书的目录。
type BookContents struct {
	NovelName  string        `json:"novelName"`
	Chapters   []ChapterMeta `json:"chapters"`
	TotalWords int           `json:"totalWords"`
	// Layered 为 true 时前端按卷分组渲染目录。
	Layered bool `json:"layered"`
}

// ChapterText 是单章正文。
type ChapterText struct {
	Chapter int    `json:"chapter"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	Words   int    `json:"words"`
	// HasPrev/HasNext 让前端不必自己推断边界（章号可能不连续：返工中的章会缺）。
	PrevChapter int `json:"prevChapter"`
	NextChapter int `json:"nextChapter"`
}

// GetContents 返回已完成章节的目录。
//
// 只列 CompletedChapters：正在写作或待返工的章节文件可能是半成品，
// 放进阅读器会让用户读到残缺正文并以为是成品质量问题。
func (a *App) GetContents() (BookContents, error) {
	h, err := a.reqHost()
	if err != nil {
		return BookContents{}, err
	}
	st := storepkg.NewStore(h.Dir())

	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		// 新书还没有 progress.json，返回空目录而不是报错——
		// 前端据此显示"还没有已完成的章节"。
		return BookContents{}, nil
	}

	titles := chapterTitles(st)
	volumeOf, volumeTitles := volumeIndex(st)

	out := BookContents{
		NovelName:  strings.TrimSpace(progress.NovelName),
		TotalWords: progress.TotalWordCount,
		Layered:    progress.Layered,
	}

	completed := append([]int(nil), progress.CompletedChapters...)
	sort.Ints(completed)
	for _, ch := range completed {
		meta := ChapterMeta{
			Chapter: ch,
			Title:   titles[ch],
			Words:   progress.ChapterWordCounts[ch],
		}
		if progress.Layered {
			meta.Volume = volumeOf[ch]
			meta.VolumeTitle = volumeTitles[meta.Volume]
		}
		out.Chapters = append(out.Chapters, meta)
	}
	return out, nil
}

// ReadChapter 读取一章终稿正文。
func (a *App) ReadChapter(chapter int) (ChapterText, error) {
	h, err := a.reqHost()
	if err != nil {
		return ChapterText{}, err
	}
	if chapter <= 0 {
		return ChapterText{}, fmt.Errorf("章节号无效: %d", chapter)
	}
	st := storepkg.NewStore(h.Dir())

	text, err := st.Drafts.LoadChapterText(chapter)
	if err != nil {
		return ChapterText{}, fmt.Errorf("读取第 %d 章失败: %w", chapter, err)
	}
	if strings.TrimSpace(text) == "" {
		return ChapterText{}, fmt.Errorf("第 %d 章还没有终稿", chapter)
	}

	out := ChapterText{
		Chapter: chapter,
		Title:   chapterTitles(st)[chapter],
		Words:   utf8.RuneCountInString(text),
	}
	// 正文首行常重复章节标题（导出时 exp.stripChapterTitleHeader 也做同样的事），
	// 阅读器自己渲染标题，去掉重复的一行免得看起来像排版错误。
	out.Text = stripLeadingTitle(text, out.Title)

	// 上下章按"已完成"集合推导，跳过缺口。
	if progress, err := st.Progress.Load(); err == nil && progress != nil {
		completed := append([]int(nil), progress.CompletedChapters...)
		sort.Ints(completed)
		for i, ch := range completed {
			if ch != chapter {
				continue
			}
			if i > 0 {
				out.PrevChapter = completed[i-1]
			}
			if i < len(completed)-1 {
				out.NextChapter = completed[i+1]
			}
			break
		}
	}
	return out, nil
}

// chapterTitles 汇总章节标题。
//
// 三个来源都要读：平铺大纲（outline.json）是短篇/中篇的唯一来源；长篇分层模式下
// 章节明细内嵌在 layered_outline.json 的 arc.chapters 里。分层书也可能同时有平铺
// 大纲（当前弧展开后的投影），所以先铺分层、再让平铺覆盖。已完成章节最后用提交时
// 保存的摘要标题覆盖，使阅读器与工作台快照、TXT/EPUB 导出保持一致。
func chapterTitles(st *storepkg.Store) map[int]string {
	titles := make(map[int]string)
	if volumes, err := st.Outline.LoadLayeredOutline(); err == nil {
		for _, v := range volumes {
			for _, arc := range v.Arcs {
				for _, e := range arc.Chapters {
					if e.Chapter <= 0 {
						continue // 未展开弧的占位条目
					}
					if t := strings.TrimSpace(e.Title); t != "" {
						titles[e.Chapter] = t
					}
				}
			}
		}
	}
	if entries, err := st.Outline.LoadOutline(); err == nil {
		for _, e := range entries {
			if t := strings.TrimSpace(e.Title); t != "" {
				titles[e.Chapter] = t
			}
		}
	}
	if progress, err := st.Progress.Load(); err == nil && progress != nil {
		for _, chapter := range progress.CompletedChapters {
			summary, err := st.Summaries.LoadSummary(chapter)
			if err != nil || summary == nil {
				continue
			}
			if title := strings.TrimSpace(summary.Title); title != "" {
				titles[chapter] = title
			}
		}
	}
	return titles
}

// volumeIndex 建立全局章节号 → 卷的映射（仅长篇分层模式有意义）。
// 分层大纲里的章号以结构位置为准：未展开/预规划的章节经常保存为 chapter:0，
// 不能直接拿条目字段反查。这里与 domain.TotalChapters/OutlineStore.LocateChapter
// 保持同一语义，按弧的章节槽位连续编号。
func volumeIndex(st *storepkg.Store) (map[int]int, map[int]string) {
	volumeOf := make(map[int]int)
	volumeTitles := make(map[int]string)
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return volumeOf, volumeTitles
	}
	chapter := 1
	for _, v := range volumes {
		volumeTitles[v.Index] = strings.TrimSpace(v.Title)
		for _, arc := range v.Arcs {
			span := len(arc.Chapters)
			if span == 0 {
				span = arc.EstimatedChapters
			}
			for i := 0; i < span; i++ {
				volumeOf[chapter] = v.Index
				chapter++
			}
		}
	}
	return volumeOf, volumeTitles
}

// stripLeadingTitle 去掉正文开头重复的标题行（"# 第 3 章 ……" 或纯标题）。
func stripLeadingTitle(text, title string) string {
	// U+FEFF 是 BOM：写成转义而非字面量，字面 BOM 会被 Go 解析器当成非法字节序标记。
	trimmed := strings.TrimLeft(text, "\ufeff \t\r\n")
	first, rest, hasNewline := strings.Cut(trimmed, "\n")
	if !hasNewline {
		return trimmed
	}
	probe := strings.TrimSpace(strings.TrimLeft(first, "#"))
	if probe == "" {
		return trimmed
	}
	// 命中条件必须严格，否则会删掉正文。
	//
	// 曾用"首行包含标题且长度差 ≤12 字"判断，结果 "他想起拆迁区的来电，心里一沉。"
	// （15 字，标题 6 字）被当成题头删掉——正文丢一整段，用户无从察觉。
	// 现在只认两种真正的题头形态，且都要求"整行是题头"而非"包含标题"：
	//   1. 首行（去掉 # 与"第X章"前缀后）恰好等于大纲标题
	//   2. 首行是"第X章"打头的题头行，其后只跟一个短标题
	if title != "" && (probe == title || stripChapterPrefix(probe) == title) {
		return strings.TrimLeft(rest, "\r\n")
	}
	if isChapterHeading(probe) {
		return strings.TrimLeft(rest, "\r\n")
	}
	return trimmed
}

// chapterHeadingRe 匹配"第X章"题头前缀。中文数字与阿拉伯数字都要认：
// 真实产物里 Writer 写的是"# 第一章 拆迁区的来电"，不是"第1章"。
var chapterHeadingRe = regexp.MustCompile(`^第\s*([0-9]+|[〇零一二三四五六七八九十百千两]+)\s*章`)

// stripChapterPrefix 去掉"第X章"前缀，返回其后的标题部分。
func stripChapterPrefix(line string) string {
	loc := chapterHeadingRe.FindStringIndex(line)
	if loc == nil {
		return line
	}
	return strings.TrimSpace(line[loc[1]:])
}

// isChapterHeading 判断首行是否为纯题头行。
//
// 不能只看"第X章"开头：正文完全可能以"第三章的会议记录还在他手里。"起句。
// 题头的判别特征是"短且不成句"，故限制其后长度并排除句末标点。
func isChapterHeading(line string) bool {
	if !chapterHeadingRe.MatchString(line) {
		return false
	}
	rest := stripChapterPrefix(line)
	if utf8.RuneCountInString(rest) > 20 {
		return false
	}
	return !strings.ContainsAny(rest, "。！？，；")
}
