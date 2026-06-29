package rules

import (
	"regexp"
	"strings"
)

// Lint 内置产品底线Kiểm tra：扫描Chính văn中的机制残留，与用户规则Không có关，commit 时始终执行。
// 与 Check 同契约——仅返事实（铁律一），不阻断流程，由评审/用户裁定。
//
// Hiện tại三类（Tất cả来自真实长跑产物的实证缺陷）：
//   - markdown_residue：Chính văn残留 ** 加粗、首行之外的 # Tiêu đề行（Xuất txt 会裸露符号）
//   - non_cjk_fragments：连续拉丁字母片段（Mô hình语言混杂，如中文Chính văn裸混 "pattern"）
func Lint(text string) []Violation {
	var vs []Violation
	vs = appendMarkdownResidue(vs, text)
	vs = appendNonCJKFragments(vs, text)
	return vs
}

func appendMarkdownResidue(vs []Violation, text string) []Violation {
	if n := strings.Count(text, "**"); n > 0 {
		vs = append(vs, Violation{
			Rule:     "markdown_residue",
			Target:   "**",
			Actual:   n,
			Severity: SeverityWarning,
		})
	}
	headings := 0
	seenContent := false
	for line := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		// 第一个非Rỗng行的 # Tiêu đề是章Tập tin的合法格式（不按行号写死，容忍前导Rỗng行）
		first := !seenContent
		seenContent = true
		if !first && strings.HasPrefix(t, "#") {
			headings++
		}
	}
	if headings > 0 {
		vs = append(vs, Violation{
			Rule:     "markdown_residue",
			Target:   "#",
			Actual:   headings,
			Severity: SeverityWarning,
		})
	}
	return vs
}

var latinFragmentRe = regexp.MustCompile(`[A-Za-z]{2,}`)

// appendNonCJKFragments 报告拉丁字母片段的总次数与去重示例。
// 现代题材的合法英文（品牌名/缩写）也会命中——warning 级事实，由评审按题材裁定。
func appendNonCJKFragments(vs []Violation, text string) []Violation {
	matches := latinFragmentRe.FindAllString(text, -1)
	if len(matches) == 0 {
		return vs
	}
	seen := make(map[string]struct{})
	var examples []string
	for _, m := range matches {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		if len(examples) < 3 {
			examples = append(examples, m)
		}
	}
	return append(vs, Violation{
		Rule:     "non_cjk_fragments",
		Target:   strings.Join(examples, "、"),
		Actual:   len(matches),
		Severity: SeverityWarning,
	})
}
