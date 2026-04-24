package imp

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// 默认章节标题正则。同时识别中文（第N章 / 第N回）和英文（Chapter N），
// 兼容 Markdown 标题前缀（# / ##）。
//
// 命名分组（仅捕获标题主体，章号用非捕获组以免污染 fallback 提取）：
//   - cn   中文标题主体（第X章 之后的文字）
//   - en   英文标题主体（Chapter X 之后的文字）
var defaultChapterRegex = regexp.MustCompile(
	`(?im)^#{0,2}\s*(?:` +
		`第\s*(?:[零〇○Ｏ０一二三四五六七八九十百千万\d]+)\s*(?:章|回)` +
		`(?:[:：．\.\s]+(?P<cn>.*))?` +
		`|` +
		`Chapter\s+(?:\d+|[IVXLCDM]+)` +
		`(?:[:：．\.\s]+(?P<en>.*))?` +
		`)\s*$`,
)

// SplitFile 把单个文本文件切分成章节列表。
// 自定义正则需包含至少一个捕获组用于提取标题；未命中时回退默认正则。
func SplitFile(path string, customRegex string) ([]Chapter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	text := string(data)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("source file is empty: %s", path)
	}

	pattern := defaultChapterRegex
	if strings.TrimSpace(customRegex) != "" {
		re, err := regexp.Compile("(?m)" + customRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid custom regex: %w", err)
		}
		pattern = re
	}
	return splitText(text, pattern), nil
}

// splitText 是纯函数版切分，便于单测。
func splitText(text string, pattern *regexp.Regexp) []Chapter {
	lines := strings.Split(text, "\n")
	type marker struct {
		line  int
		title string
	}
	var marks []marker
	for i, ln := range lines {
		if loc := pattern.FindStringSubmatchIndex(ln); loc != nil {
			marks = append(marks, marker{line: i, title: extractTitle(ln, pattern, loc, len(marks)+1)})
		}
	}
	if len(marks) == 0 {
		return nil
	}

	chapters := make([]Chapter, 0, len(marks))
	for i, m := range marks {
		end := len(lines)
		if i+1 < len(marks) {
			end = marks[i+1].line
		}
		body := strings.Join(lines[m.line+1:end], "\n")
		body = stripTrailingNoise(body)
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		chapters = append(chapters, Chapter{Title: m.title, Content: body})
	}
	return chapters
}

// extractTitle 从匹配行提取章节标题；优先取命名捕获，否则回退章节号占位。
func extractTitle(line string, pattern *regexp.Regexp, loc []int, fallbackNum int) string {
	subnames := pattern.SubexpNames()
	priority := []string{"cn", "en"}
	for _, name := range priority {
		idx := pattern.SubexpIndex(name)
		if idx <= 0 {
			continue
		}
		if loc[2*idx] < 0 {
			continue
		}
		if t := strings.TrimSpace(line[loc[2*idx]:loc[2*idx+1]]); t != "" {
			return t
		}
	}
	// 自定义正则：取第一个非空命名捕获或匿名捕获
	for i := 1; i < len(subnames); i++ {
		if loc[2*i] < 0 {
			continue
		}
		if t := strings.TrimSpace(line[loc[2*i]:loc[2*i+1]]); t != "" {
			return t
		}
	}
	return fmt.Sprintf("第%d章", fallbackNum)
}

// stripTrailingNoise 剥离常见的尾部噪声（Project Gutenberg 等 license trailer）。
var trailerRe = regexp.MustCompile(`(?im)^\s*Project Gutenberg(?:\(TM\)|™)?[\s\S]*$`)

func stripTrailingNoise(content string) string {
	if loc := trailerRe.FindStringIndex(content); loc != nil {
		return strings.TrimRight(content[:loc[0]], " \t\n")
	}
	return content
}
