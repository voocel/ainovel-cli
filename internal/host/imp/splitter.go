package imp

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/voocel/ainovel-cli/internal/utils"
)

// Mặc địnhChươngTiêu đề正则。覆盖常见中文（第N章/回/话/卷/节/幕、卷N、序章/楔子/尾声/番外/外传 等）
// 与英文（Chapter N、Prologue、Epilogue）Tiêu đề，兼容 Markdown Tiêu đề前缀（# / ##）、
// 起点系 txt 的「Chính văn 第N章」前缀、以及【】〖〗包裹的Tiêu đề。
//
// 命名分组：副Tiêu đề组优先于关键词组（提取时按 priority 顺序回退）：
//   - cn    编号Chương副Tiêu đề（第X章/回/话/卷/节/幕 之后的文字）
//   - vol   独立卷副Tiêu đề（卷X 之后的文字）
//   - sp    特殊单元副Tiêu đề（序章/楔子/尾声/番外 之后的文字）
//   - en    英文Chương副Tiêu đề（Chapter X / Prologue / Epilogue 之后的文字）
//   - spkw  特殊单元关键词本身（Không có副Tiêu đề时作Tiêu đề，如「楔子」「番外」）
//   - enkw  英文特殊单元关键词本身（Không có副Tiêu đề时作Tiêu đề，如「Prologue」）

// ws 是字符类内容：ASCII Rỗng白 + 全角Rỗng格。Go RE2 的 \s 只含 ASCII Rỗng白，
// 而中文排版的Tiêu đề分隔常用 U+3000（「第一章　风起」）。
const ws = `\s\x{3000}`

// cnNum 是Chương编号可用的数字字符：阿拉伯 / 全角 / 中文小写 / 中文大写繁体（壹贰叁…萬）。
const cnNum = `零〇○Ｏ０一二三四五六七八九十百千万两壹贰貳叁參肆伍陆陸柒捌玖拾佰仟萬兩\d`

// sub 是副Tiêu đề捕获：取到行尾，但不吞掉右包裹符（】〗），留给结尾的可选闭括号。
const sub = `[^】〗\n]*`

var defaultChapterRegex = regexp.MustCompile(
	`(?im)^#{0,2}[` + ws + `]*(?:Chính văn[` + ws + `]*)?[【〖]?[` + ws + `]*(?:` +
		`第\s*(?:[` + cnNum + `]+)\s*(?:章|回|话|卷|节|幕)` +
		`(?:[:：．\.` + ws + `]+(?P<cn>` + sub + `))?` +
		`|` +
		`卷\s*(?:[` + cnNum + `]+)` +
		`(?:[:：．\.` + ws + `]+(?P<vol>` + sub + `))?` +
		`|` +
		`(?P<spkw>序章|序幕|楔子|引子|前言|序言|尾声|终章|后记|番外|外传)` +
		`(?:[:：．\.` + ws + `]+(?P<sp>` + sub + `))?` +
		`|` +
		`(?:Chapter\s+(?:\d+|[IVXLCDM]+)|(?P<enkw>Prologue|Epilogue))` +
		`(?:[:：．\.` + ws + `]+(?P<en>` + sub + `))?` +
		`)[` + ws + `]*[】〗]?[` + ws + `]*$`,
)

// SplitFile 把单个文本Tập tin切分成Chương列表。
func SplitFile(path string) ([]Chapter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	text := utils.DecodeText(data)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("source file is empty: %s", path)
	}
	return splitText(text, defaultChapterRegex), nil
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

// extractTitle 从匹配行提取ChươngTiêu đề；优先取命名捕获，否则回退Chương号占位。
func extractTitle(line string, pattern *regexp.Regexp, loc []int, fallbackNum int) string {
	subnames := pattern.SubexpNames()
	priority := []string{"cn", "vol", "sp", "en", "spkw", "enkw"}
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
	// 兜底：取第一个非Rỗng捕获组（防御性，Mặc định正则的命名组已覆盖各分支）
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
