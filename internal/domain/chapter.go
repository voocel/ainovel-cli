package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ReviewInterval 全局审阅间隔（每 N 章触发一次）。
const ReviewInterval = 5

// ShouldReview 根据已完成章节数判断是否需要全局审阅（短篇/中篇模式）。
func ShouldReview(completedCount int) (bool, string) {
	if completedCount > 0 && completedCount%ReviewInterval == 0 {
		return true, fmt.Sprintf("已完成 %d 章，触发全局审阅", completedCount)
	}
	return false, ""
}

// ShouldArcReview 长篇模式下判断是否需要弧级/卷级评审。
func ShouldArcReview(isArcEnd, isVolumeEnd bool, volume, arc int) (bool, string) {
	if isVolumeEnd {
		return true, fmt.Sprintf("第 %d 卷第 %d 弧结束（卷结束），触发弧级+卷级评审", volume, arc)
	}
	if isArcEnd {
		return true, fmt.Sprintf("第 %d 卷第 %d 弧结束，触发弧级评审", volume, arc)
	}
	return false, ""
}

// WordCount 按 rune 计算字数。
func WordCount(content string) int {
	return utf8.RuneCountInString(content)
}

// chapterHeadingRe 匹配以「第 N 章」开头的标题行，可带 Markdown 井号。
var chapterHeadingRe = regexp.MustCompile(`^(#{1,6}\s*)?第\s*[0-9零〇一二三四五六七八九十百千两]+\s*章`)

// ChapterBody 去掉正文开头的章节标题行。writer 写进正文的标题格式不一（「第 N 章 标题」、
// 「第N章《标题》」「# 标题」或只有标题），章号与标题以提交的 title 为准由系统统一生成。
// 首行是「第 N 章…」（可带 #），或去掉 # 与书名号后恰是本章标题时才剥掉；其他首行（如 # 序章）属正文。
func ChapterBody(content, title string) string {
	content = strings.TrimLeft(content, "\r\n")
	first, rest, _ := strings.Cut(content, "\n")
	first = strings.TrimSpace(first)
	bare := strings.Trim(strings.TrimSpace(strings.TrimLeft(first, "#")), "《》")
	if !chapterHeadingRe.MatchString(first) && (title == "" || bare != strings.TrimSpace(title)) {
		return content
	}
	return strings.TrimLeft(rest, "\r\n")
}

// ChapterText 是终稿的统一格式：「# 第 N 章 标题」加去掉原标题行的正文。
func ChapterText(chapter int, title, content string) string {
	return fmt.Sprintf("# 第 %d 章 %s\n\n%s", chapter, strings.TrimSpace(title), ChapterBody(content, title))
}
