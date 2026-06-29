package rules

import (
	"strings"
	"testing"
)

func TestLint_CleanText(t *testing.T) {
	if vs := Lint("# 第一章 风起\n他迈步向前。\n夜色渐深。"); len(vs) != 0 {
		t.Errorf("clean text should pass: %+v", vs)
	}
}

func TestLint_MarkdownResidue(t *testing.T) {
	text := "# 第一章\n这是**重点**内容。\n## 小Tiêu đề\nChính văn。"
	vs := Lint(text)
	bold := findViolation(vs, "markdown_residue", "**")
	if bold == nil || bold.Actual != 2 {
		t.Errorf("expected ** residue x2: %+v", vs)
	}
	heading := findViolation(vs, "markdown_residue", "#")
	if heading == nil || heading.Actual != 1 {
		t.Errorf("expected 1 heading beyond first line: %+v", vs)
	}
}

func TestLint_NonCJKFragments(t *testing.T) {
	text := "# 第一章\n他发现了一个pattern，这个pattern像DNA一样规律。"
	vs := Lint(text)
	var v *Violation
	for i := range vs {
		if vs[i].Rule == "non_cjk_fragments" {
			v = &vs[i]
			break
		}
	}
	if v == nil {
		t.Fatalf("expected non_cjk violation: %+v", vs)
	}
	if v.Actual != 3 {
		t.Errorf("total count: got %v want 3", v.Actual)
	}
	if !strings.Contains(v.Target, "pattern") || !strings.Contains(v.Target, "DNA") {
		t.Errorf("examples should be distinct: %q", v.Target)
	}
	if v.Severity != SeverityWarning {
		t.Errorf("severity: %v", v.Severity)
	}
}
