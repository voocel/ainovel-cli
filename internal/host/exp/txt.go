package exp

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Accelerator-mzq/ainovel-cli/internal/domain"
)

// chapterTitleIndex 给定章号查标题，缺失返回空串。
type chapterTitleIndex map[int]string

func buildTitleIndex(outline []domain.OutlineEntry) chapterTitleIndex {
	idx := make(chapterTitleIndex, len(outline))
	for _, e := range outline {
		if e.Title != "" {
			idx[e.Chapter] = e.Title
		}
	}
	return idx
}

// chapterLocation 是某章在分层大纲中的归属。只保留导出版式需要的卷信息——
// 弧不进导出（读者视角下弧是过细的内部结构）。
type chapterLocation struct {
	VolumeIdx       int
	VolumeTitle     string
	IsFirstOfVolume bool
}

// buildLocations 按分层大纲的全局章节顺序构造 {chapter -> location}。
// 章号按 FlattenOutline 同样的规则重建（卷内弧内顺序累加），
// 以保持与 Progress.CompletedChapters 的章号一致。弧层仍要遍历（算全局章号必经），
// 但不落入 location——导出只在卷首插分隔。
func buildLocations(volumes []domain.VolumeOutline) map[int]chapterLocation {
	if len(volumes) == 0 {
		return nil
	}
	locs := make(map[int]chapterLocation)
	ch := 0
	for _, v := range volumes {
		firstOfVol := true
		for _, a := range v.Arcs {
			for range a.Chapters {
				ch++
				locs[ch] = chapterLocation{
					VolumeIdx:       v.Index,
					VolumeTitle:     v.Title,
					IsFirstOfVolume: firstOfVol,
				}
				firstOfVol = false
			}
		}
	}
	return locs
}

// chapterHeaderRe 匹配带章号的 Markdown 标题首行（# 第N章 / ## 第 12 章 ...）。
var chapterHeaderRe = regexp.MustCompile(`^#+\s+第.+?章`)

// atxTitleRe 提取 ATX 标题（# 标题）的文字部分。
var atxTitleRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)

// stripChapterTitleHeader 若首行是会与导出器统一标题重复的章节标题则剥掉。
// 两种情形：① "# 第N章 …"（带章号）；② markdown 标题且其文字恰是本章标题
// （writer 常把纯章节名当标题写进正文首行，如 "# 边村浮生"，与导出器生成的
// "第 N 章 边村浮生" 重复）。其它 h1（如 "# 序章"）视为正文一部分，保留。
// 调用方负责先 TrimSpace，因此前导空行不在考虑范围内。
func stripChapterTitleHeader(content, title string) string {
	first, rest, hasNewline := strings.Cut(content, "\n")
	if !isChapterTitleLine(first, title) {
		return content
	}
	if !hasNewline {
		return ""
	}
	return strings.TrimLeft(rest, "\n")
}

func isChapterTitleLine(line, title string) bool {
	if chapterHeaderRe.MatchString(line) {
		return true
	}
	if title = strings.TrimSpace(title); title == "" {
		return false
	}
	m := atxTitleRe.FindStringSubmatch(line)
	return len(m) == 2 && strings.TrimSpace(m[1]) == title
}

// renderTXT 拼接最终文本。
//
// 章节顺序由 chapters 决定（调用方已按章号升序去重）。bodies/titleIdx/locations
// 都按"缺失即降级"处理：标题缺失只输出 "第 N 章"；分层定位缺失就当扁平大纲。
func renderTXT(
	novelName string,
	chapters []int,
	titleIdx chapterTitleIndex,
	locations map[int]chapterLocation,
	bodies map[int]string,
) string {
	var b strings.Builder

	if name := strings.TrimSpace(novelName); name != "" {
		b.WriteString("《")
		b.WriteString(name)
		b.WriteString("》\n\n")
	}

	useLayered := len(locations) > 0

	for i, ch := range chapters {
		if useLayered {
			if loc, ok := locations[ch]; ok && loc.IsFirstOfVolume {
				b.WriteString("\n═══════════════════════════════════════════\n")
				fmt.Fprintf(&b, "           第 %d 卷  %s\n", loc.VolumeIdx, strings.TrimSpace(loc.VolumeTitle))
				b.WriteString("═══════════════════════════════════════════\n\n")
			}
		}

		title := strings.TrimSpace(titleIdx[ch])
		if title != "" {
			fmt.Fprintf(&b, "第 %d 章  %s\n\n", ch, title)
		} else {
			fmt.Fprintf(&b, "第 %d 章\n\n", ch)
		}

		body := stripChapterTitleHeader(strings.TrimSpace(bodies[ch]), title)
		b.WriteString(body)
		b.WriteString("\n")
		if i < len(chapters)-1 {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}
