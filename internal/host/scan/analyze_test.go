package scan

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func analysisEntries() []Entry {
	return []Entry{
		{Rank: 1, Title: "剑来无归", Author: "甲", Category: "玄幻"},
		{Rank: 2, Title: "都市修复师", Author: "乙", Category: "都市"},
		{Rank: 3, Title: "星海归途", Author: "丙", Category: "科幻"},
	}
}

func validAnalysis() *Analysis {
	return &Analysis{
		Categories: []CategoryStat{
			{Name: "玄幻", Count: 1, Examples: []string{"剑来无归"}, Note: "老套路"},
		},
		Trends: []Trend{
			{Title: "职业线走强", Evidence: []string{"《都市修复师》靠职业线立住"}, Reading: "读者要具体职业"},
		},
		FreshElements: []FreshElement{
			{Name: "修复师", SeenIn: []string{"都市修复师"}, Why: "给老题材新入口"},
		},
		Saturated: []string{"传统玄幻"},
		Verdict:   "分布仍以玄幻为主",
	}
}

// TestValidateAnalysisHappyPath 确认合法产物通过校验。
func TestValidateAnalysisHappyPath(t *testing.T) {
	if err := validateAnalysis(validAnalysis(), analysisEntries()); err != nil {
		t.Fatalf("合法产物应通过校验，得到 %v", err)
	}
}

// TestValidateAnalysisRejects 验证机械校验拦住模型最常见的几种越界：
// 编造书名、放大题材分母、给不出证据。这些在参考实现里靠 prompt 自觉，这里代码执行。
func TestValidateAnalysisRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Analysis)
		reason string // 期望错误里出现的关键词
	}{
		{
			name:   "趋势证据引用了不存在的书",
			mutate: func(a *Analysis) { a.Trends[0].Evidence = []string{"《根本没这本书》很火"} },
			reason: "不在榜单条目里",
		},
		{
			name:   "题材代表作引用了不存在的书",
			mutate: func(a *Analysis) { a.Categories[0].Examples = []string{"凭空捏造录"} },
			reason: "不在榜单条目里",
		},
		{
			name:   "新元素出处引用了不存在的书",
			mutate: func(a *Analysis) { a.FreshElements[0].SeenIn = []string{"虚构之书"} },
			reason: "不在榜单条目里",
		},
		{
			name:   "题材条目数超过有效条目总数",
			mutate: func(a *Analysis) { a.Categories[0].Count = 99 },
			reason: "越界",
		},
		{
			name: "各题材之和超过总数",
			mutate: func(a *Analysis) {
				a.Categories = []CategoryStat{
					{Name: "玄幻", Count: 3, Examples: []string{"剑来无归"}, Note: "n"},
					{Name: "都市", Count: 2, Examples: []string{"都市修复师"}, Note: "n"},
				}
			},
			reason: "超过有效条目总数",
		},
		{
			name:   "趋势没给证据",
			mutate: func(a *Analysis) { a.Trends[0].Evidence = []string{"", "  "} },
			reason: "没给榜单证据",
		},
		{
			name:   "没有趋势判断",
			mutate: func(a *Analysis) { a.Trends = nil },
			reason: "至少需要 1 条趋势",
		},
		{
			name:   "缺总评",
			mutate: func(a *Analysis) { a.Verdict = "   " },
			reason: "verdict",
		},
		{
			name:   "题材缺名称",
			mutate: func(a *Analysis) { a.Categories[0].Name = "" },
			reason: "缺名称",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := validAnalysis()
			tt.mutate(a)
			err := validateAnalysis(a, analysisEntries())
			if err == nil {
				t.Fatalf("应该被拦下（%s）", tt.reason)
			}
			if !containsSubstr(err.Error(), tt.reason) {
				t.Errorf("错误 %q 应包含 %q", err.Error(), tt.reason)
			}
		})
	}
}

// TestCheckTitlesAllowsSentenceReference 验证证据可以是含书名的整句，
// 不要求与书名全等——模型自然会写「《X》靠…立住」而不是光秃秃一个书名。
func TestCheckTitlesAllowsSentenceReference(t *testing.T) {
	titles := map[string]bool{"都市修复师": true}
	refs := []string{"《都市修复师》 用职业线 立住了开篇"} // 含空白，squash 后应命中
	if err := checkTitles(titles, refs, "证据"); err != nil {
		t.Errorf("含书名的整句应通过，得到 %v", err)
	}
}

// TestValidateParse 验证解析层拦住空结果与全空占位条目。
func TestValidateParse(t *testing.T) {
	rank, title := 1, "书甲"
	author := "作者甲"

	if err := validateParse(&parseResult{}); err == nil {
		t.Error("空条目应被拦下")
	}

	blank := &parseResult{Entries: []rawEntry{{}, {}}}
	if err := validateParse(blank); err == nil {
		t.Error("全空占位条目应被拦下")
	}

	ok := &parseResult{Entries: []rawEntry{
		{Rank: &rank, Title: &title, Author: &author},
		{}, // 部分缺失是允许的：交给清洗层剔除并计入质量报告
	}}
	if err := validateParse(ok); err != nil {
		t.Errorf("有真实条目时应通过，得到 %v", err)
	}
}

// TestRawEntryToEntry 验证 null 字段落成零值而不是字面 "null"。
func TestRawEntryToEntry(t *testing.T) {
	rank := 3
	title := "  书名  "
	got := rawEntry{Rank: &rank, Title: &title}.toEntry()

	if got.Rank != 3 {
		t.Errorf("Rank = %d, want 3", got.Rank)
	}
	if got.Title != "书名" {
		t.Errorf("Title = %q, want %q（应去空白）", got.Title, "书名")
	}
	if got.Author != "" || got.Category != "" || got.Description != "" || got.Stats != "" {
		t.Errorf("null 字段应落零值，得到 %+v", got)
	}
}

func TestSplitTextByBytesRespectsBudgetAndUTF8(t *testing.T) {
	raw := "第一条 榜单内容\n第二条 " + strings.Repeat("很长的中文内容", 30) + "\n第三条"
	chunks := splitTextByBytes(raw, 48)
	if len(chunks) < 3 {
		t.Fatalf("超预算文本应分块，got %d", len(chunks))
	}
	if strings.Join(chunks, "") != raw {
		t.Fatal("分块后重新拼接必须等于原文")
	}
	for i, chunk := range chunks {
		if len(chunk) > 48 || !utf8.ValidString(chunk) {
			t.Fatalf("chunk[%d] 非法: bytes=%d utf8=%v", i, len(chunk), utf8.ValidString(chunk))
		}
	}
}

// TestEntriesInputDigestStability 验证条目身份对顺序不敏感、对内容与日期敏感。
// 这是「同一天同一份数据重跑复用，换数据或跨日重扫失效」的实现保证。
func TestEntriesInputDigestStability(t *testing.T) {
	a := []Entry{
		{Rank: 1, Title: "甲", Author: "A"},
		{Rank: 2, Title: "乙", Author: "B"},
	}
	reversed := []Entry{a[1], a[0]}

	if entriesInputDigest(a, "20260101", "qidian") != entriesInputDigest(reversed, "20260101", "qidian") {
		t.Error("同一批条目换顺序后身份应不变")
	}
	if entriesInputDigest(a, "20260101", "qidian") == entriesInputDigest(a, "20260102", "qidian") {
		t.Error("跨日重扫身份必须变化")
	}
	if entriesInputDigest(a, "20260101", "qidian") == entriesInputDigest(a, "20260101", "fanqie") {
		t.Error("换平台身份必须变化")
	}
	changed := []Entry{a[0], {Rank: 2, Title: "乙改", Author: "B"}}
	if entriesInputDigest(a, "20260101", "qidian") == entriesInputDigest(changed, "20260101", "qidian") {
		t.Error("条目内容变化身份必须变化")
	}
}

// TestRenderReportMarkdown 验证报告把质量头放最前，且明细按排名升序。
func TestRenderReportMarkdown(t *testing.T) {
	entries := []Entry{
		{Rank: 3, Title: "星海归途", Author: "丙", Category: "科幻", Stats: "5万"},
		{Rank: 1, Title: "剑来无归", Author: "甲", Category: "玄幻", Stats: "10万"},
	}
	q := QualityReport{TotalRaw: 4, ValidEntries: 2, Sparse: true, Issues: []string{"剔除 2 条无效条目"}}

	md := RenderReportMarkdown(validAnalysis(), entries, q, "qidian", "月票榜", "20260101")

	if !containsSubstr(md, "[数据稀疏]") {
		t.Error("稀疏标记必须出现在报告里")
	}
	iQuality := indexOf(md, "数据质量")
	iVerdict := indexOf(md, "## 总评")
	if iQuality < 0 || iVerdict < 0 || iQuality > iVerdict {
		t.Errorf("质量头必须在总评之前（quality=%d verdict=%d）", iQuality, iVerdict)
	}
	if indexOf(md, "剑来无归") > indexOf(md, "星海归途") {
		t.Error("条目明细应按排名升序")
	}
	if !containsSubstr(md, "剔除 2 条无效条目") {
		t.Error("问题摘要必须出现在报告里")
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
