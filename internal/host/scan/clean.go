package scan

import (
	"fmt"
	"strings"
)

// Entry 是一条榜单条目（清洗后的结构化数据）。
type Entry struct {
	Rank        int    `json:"rank"`                  // 排名
	Title       string `json:"title"`                 // 书名
	Author      string `json:"author"`                // 作者
	Category    string `json:"category,omitempty"`    // 分类/标签
	Description string `json:"description,omitempty"` // 简介（截断到 100 字）
	Stats       string `json:"stats,omitempty"`       // 数据（点击/收藏等）
}

// QualityReport 是数据质量报告。
type QualityReport struct {
	TotalRaw      int      // 原始条目数
	ValidEntries  int      // 有效条目数
	Sparse        bool     // 数据稀疏标记
	Issues        []string // 问题列表
}

// 质量门阈值
const (
	minEntriesNormal = 15 // 正常平台最低有效条目数
	minEntriesSmall  = 10 // 小平台最低有效条目数
	descMaxRunes     = 100 // 简介最大字数
)

// smallPlatforms 小平台列表（阈值更宽松）
var smallPlatforms = map[string]bool{
	"qimao":    true,
	"ciweimao": true,
	"other":    true,
}

// CleanEntries 清洗榜单条目：剔除无效条目、截断简介、标记稀疏数据。
func CleanEntries(raw []Entry, platform string) ([]Entry, QualityReport) {
	report := QualityReport{
		TotalRaw: len(raw),
		Issues:   make([]string, 0),
	}

	valid := make([]Entry, 0, len(raw))
	var invalidCount int

	for _, e := range raw {
		// 必填字段缺失即剔除
		if e.Rank <= 0 || strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Author) == "" {
			invalidCount++
			continue
		}

		// 简介截断到 100 字（按句号）
		if e.Description != "" {
			e.Description = truncateDescription(e.Description, descMaxRunes)
		}

		// 空值标记为 [待补]
		if e.Category == "" {
			e.Category = "[待补]"
		}
		if e.Description == "" {
			e.Description = "[待补]"
		}
		if e.Stats == "" {
			e.Stats = "[待补]"
		}

		valid = append(valid, e)
	}

	report.ValidEntries = len(valid)

	if invalidCount > 0 {
		report.Issues = append(report.Issues, fmt.Sprintf("剔除 %d 条无效条目（排名/书名/作者缺失）", invalidCount))
	}

	// 判断数据稀疏
	minEntries := minEntriesNormal
	if smallPlatforms[strings.ToLower(platform)] {
		minEntries = minEntriesSmall
	}

	if report.ValidEntries < minEntries {
		report.Sparse = true
		report.Issues = append(report.Issues, fmt.Sprintf("有效条目不足 %d 条，数据稀疏", minEntries))
	}

	return valid, report
}

// truncateDescription 按句号截断简介到指定字数。
func truncateDescription(desc string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(desc))
	if len(runes) <= maxRunes {
		return string(runes)
	}

	// 在 maxRunes 之前找最后一个句号
	for i := maxRunes; i >= 0; i-- {
		if i < len(runes) && (runes[i] == '。' || runes[i] == '.' || runes[i] == '！' || runes[i] == '？') {
			return string(runes[:i+1])
		}
	}

	// 没找到句号，直接截断
	return string(runes[:maxRunes]) + "…"
}

// RenderQualityHeader 渲染质量报告头部（写入产物文件顶部）。
func RenderQualityHeader(report QualityReport) string {
	var sb strings.Builder
	sb.WriteString("# 数据质量\n\n")
	if report.Sparse {
		sb.WriteString("⚠️ **[数据稀疏]** - 样本不足，趋势分析与选题可行性结论参考价值有限。\n\n")
	} else {
		sb.WriteString("✓ 数据质量正常\n\n")
	}
	fmt.Fprintf(&sb, "- 原始条目：%d\n", report.TotalRaw)
	fmt.Fprintf(&sb, "- 有效条目：%d\n", report.ValidEntries)
	if len(report.Issues) > 0 {
		sb.WriteString("\n## 问题摘要\n\n")
		for _, issue := range report.Issues {
			fmt.Fprintf(&sb, "- %s\n", issue)
		}
	}
	sb.WriteString("\n---\n\n")
	return sb.String()
}
