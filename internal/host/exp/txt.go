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

// chapterLocation 是某章在分层大纲中的归属。
type chapterLocation struct {
	VolumeIdx       int
	VolumeTitle     string
	ArcIdx          int
	ArcTitle        string
	IsFirstOfVolume bool
	IsFirstOfArc    bool
}

// buildLocations 按分层大纲的全局章节顺序构造 {chapter -> location}。
// 章号按 FlattenOutline 同样的规则重建（卷内弧内顺序累加），
// 以保持与 Progress.CompletedChapters 的章号一致。
func buildLocations(volumes []domain.VolumeOutline) map[int]chapterLocation {
	if len(volumes) == 0 {
		return nil
	}
	locs := make(map[int]chapterLocation)
	ch := 0
	for _, v := range volumes {
		firstOfVol := true
		for _, a := range v.Arcs {
			firstOfArc := true
			for range a.Chapters {
				ch++
				locs[ch] = chapterLocation{
					VolumeIdx:       v.Index,
					VolumeTitle:     v.Title,
					ArcIdx:          a.Index,
					ArcTitle:        a.Title,
					IsFirstOfVolume: firstOfVol,
					IsFirstOfArc:    firstOfArc,
				}
				firstOfVol = false
				firstOfArc = false
			}
		}
	}
	return locs
}

// chapterHeaderRe 匹配 Markdown 章节标题首行（# 第N章 / ## 第 12 章 ...）。
// 用于剥离 LLM 写入终稿时偶尔带的标题，避免与导出器统一标题重复。
var chapterHeaderRe = regexp.MustCompile(`^#+\s+第.+?章`)

// stripChapterTitleHeader 若首行是章节标题（# 第N章 ...）则剥掉。
// 调用方负责先 TrimSpace，因此前导空行不在考虑范围内。
func stripChapterTitleHeader(content string) string {
	first, rest, hasNewline := strings.Cut(content, "\n")
	if !chapterHeaderRe.MatchString(first) {
		return content
	}
	if !hasNewline {
		return ""
	}
	return strings.TrimLeft(rest, "\n")
}

// renderTXT 拼接最终文本。
//
// 章节顺序由 chapters 决定（调用方已按章号升序去重）。bodies/titleIdx/locations
// 都按"缺失即降级"处理：标题缺失只输出 "第 N 章"；分层定位缺失就当扁平大纲。
func renderTXT(
	novelName, premise string,
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
	if pm := strings.TrimSpace(premise); pm != "" {
		b.WriteString(pm)
		b.WriteString("\n\n")
	}

	useLayered := len(locations) > 0

	for i, ch := range chapters {
		if useLayered {
			if loc, ok := locations[ch]; ok {
				if loc.IsFirstOfVolume {
					b.WriteString("\n═══════════════════════════════════════════\n")
					fmt.Fprintf(&b, "           第 %d 卷  %s\n", loc.VolumeIdx, strings.TrimSpace(loc.VolumeTitle))
					b.WriteString("═══════════════════════════════════════════\n\n")
				}
				if loc.IsFirstOfArc {
					fmt.Fprintf(&b, "──────  第 %d 弧  %s  ──────\n\n", loc.ArcIdx, strings.TrimSpace(loc.ArcTitle))
				}
			}
		}

		title := strings.TrimSpace(titleIdx[ch])
		if title != "" {
			fmt.Fprintf(&b, "第 %d 章  %s\n\n", ch, title)
		} else {
			fmt.Fprintf(&b, "第 %d 章\n\n", ch)
		}

		body := stripChapterTitleHeader(strings.TrimSpace(bodies[ch]))
		b.WriteString(body)
		b.WriteString("\n")
		if i < len(chapters)-1 {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}
