package rip

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/stylestat"
)

// TestStyleConfidence 验证置信度由样本量判定，不由模型自评。
func TestStyleConfidence(t *testing.T) {
	tests := []struct {
		name            string
		chapters        int
		stats           *stylestat.Stats
		wantConfidence  string
		wantReasonParts []string
	}{
		{
			name:            "高置信度：20+ 章且有统计",
			chapters:        25,
			stats:           &stylestat.Stats{Chapters: 25},
			wantConfidence:  confidenceHigh,
			wantReasonParts: []string{"25 章", "统计稳定"},
		},
		{
			name:            "中置信度：8-19 章且有统计",
			chapters:        12,
			stats:           &stylestat.Stats{Chapters: 12},
			wantConfidence:  confidenceMid,
			wantReasonParts: []string{"12 章", "统计可用"},
		},
		{
			name:            "低置信度：章数虽多但无统计",
			chapters:        50,
			stats:           nil, // 无统计
			wantConfidence:  confidenceLow,
			wantReasonParts: []string{"50 章", "不足以做全书级统计"},
		},
		{
			name:            "低置信度：< 8 章且有统计",
			chapters:        5,
			stats:           &stylestat.Stats{Chapters: 5},
			wantConfidence:  confidenceLow,
			wantReasonParts: []string{"仅 5 章", "参考价值有限"},
		},
		{
			name:            "低置信度：< 8 章且无统计",
			chapters:        3,
			stats:           nil,
			wantConfidence:  confidenceLow,
			wantReasonParts: []string{"仅 3 章", "通读定性"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			confidence, reason := styleConfidence(tt.chapters, tt.stats)
			if confidence != tt.wantConfidence {
				t.Errorf("styleConfidence() confidence = %v, want %v", confidence, tt.wantConfidence)
			}
			for _, part := range tt.wantReasonParts {
				if !contains(reason, part) {
					t.Errorf("styleConfidence() reason = %q, want to contain %q", reason, part)
				}
			}
		})
	}
}

// TestValidateStyleAnchor 验证锚点核对：必须能在原文逐字找到，且长度充分。
func TestValidateStyleAnchor(t *testing.T) {
	boundaries := &Boundaries{
		Chapters: []ChapterSpan{
			{Number: 1, Title: "第一章", Start: 0, End: 100},
			{Number: 2, Title: "第二章", Start: 100, End: 200},
		},
	}
	// 归一化且去空白的原文
	normalizedSquashed := "第一章内容这是一段很长的原文片段用于测试锚点验证第二章有足够长度的内容片段"

	tests := []struct {
		name    string
		style   *Style
		wantErr bool
		errMsg  string
	}{
		{
			name: "正常：2 条锚点都能找到",
			style: &Style{
				Voice:       "语感",
				Sentence:    "句式",
				Perspective: "视角",
				Signatures:  []string{"手法1"},
				Anchors: []StyleAnchor{
					{Chapter: 1, Quote: "这是一段很长的原文片段", Why: "体现语感"},
					{Chapter: 2, Quote: "第二章有足够长度的内容片段", Why: "体现节奏"},
				},
			},
			wantErr: false,
		},
		{
			name: "错误：锚点太短",
			style: &Style{
				Voice:       "语感",
				Sentence:    "句式",
				Perspective: "视角",
				Signatures:  []string{"手法1"},
				Anchors: []StyleAnchor{
					{Chapter: 1, Quote: "这是", Why: "太短"}, // 只有 2 字，少于 styleAnchorMinRunes=8
					{Chapter: 2, Quote: "第二章内容", Why: "正常"},
				},
			},
			wantErr: true,
			errMsg:  "原文片段太短",
		},
		{
			name: "错误：锚点在原文中找不到",
			style: &Style{
				Voice:       "语感",
				Sentence:    "句式",
				Perspective: "视角",
				Signatures:  []string{"手法1"},
				Anchors: []StyleAnchor{
					{Chapter: 1, Quote: "这段文字不存在于原文中", Why: "伪造的"},
					{Chapter: 2, Quote: "第二章内容", Why: "正常"},
				},
			},
			wantErr: true,
			errMsg:  "在原文中找不到",
		},
		{
			name: "错误：锚点数量不足",
			style: &Style{
				Voice:       "语感",
				Sentence:    "句式",
				Perspective: "视角",
				Signatures:  []string{"手法1"},
				Anchors: []StyleAnchor{
					{Chapter: 1, Quote: "这是一段很长的原文片段", Why: "只有 1 条"},
				},
			},
			wantErr: true,
			errMsg:  "至少需要 2 条原文锚点",
		},
		{
			name: "错误：锚点章号越界",
			style: &Style{
				Voice:       "语感",
				Sentence:    "句式",
				Perspective: "视角",
				Signatures:  []string{"手法1"},
				Anchors: []StyleAnchor{
					{Chapter: 1, Quote: "这是一段很长的原文片段", Why: "正常"},
					{Chapter: 5, Quote: "第五章内容", Why: "章号越界"}, // 全书只有 2 章
				},
			},
			wantErr: true,
			errMsg:  "越界",
		},
		{
			name: "错误：缺标志性手法",
			style: &Style{
				Voice:       "语感",
				Sentence:    "句式",
				Perspective: "视角",
				Signatures:  []string{}, // 空数组
				Anchors: []StyleAnchor{
					{Chapter: 1, Quote: "这是一段很长的原文片段", Why: "锚点"},
					{Chapter: 2, Quote: "第二章内容", Why: "锚点"},
				},
			},
			wantErr: true,
			errMsg:  "至少需要 1 条标志性手法",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStyle(tt.style, boundaries, normalizedSquashed)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStyle() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("validateStyle() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

// TestStyleSampleIndexes 验证样本章下标分布：头中尾均匀取点，升序不重复。
func TestStyleSampleIndexes(t *testing.T) {
	tests := []struct {
		name  string
		total int
		want  []int
	}{
		{
			name:  "全书 <= 样本数：取全部章",
			total: 4,
			want:  []int{0, 1, 2, 3},
		},
		{
			name:  "全书 > 样本数：头中尾均匀取 6 章",
			total: 30,
			want:  []int{0, 5, 11, 17, 23, 29}, // 均匀分布，首尾各一章
		},
		{
			name:  "边界：恰好 6 章",
			total: 6,
			want:  []int{0, 1, 2, 3, 4, 5},
		},
		{
			name:  "边界：7 章",
			total: 7,
			want:  []int{0, 1, 2, 3, 4, 6}, // 取 6 章，包含首尾
		},
		{
			name:  "边界：0 章",
			total: 0,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := styleSampleIndexes(tt.total)
			if len(got) != len(tt.want) {
				t.Errorf("styleSampleIndexes(%d) len = %d, want %d", tt.total, len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("styleSampleIndexes(%d) = %v, want %v", tt.total, got, tt.want)
					return
				}
			}
			// 验证升序
			for i := 1; i < len(got); i++ {
				if got[i] <= got[i-1] {
					t.Errorf("styleSampleIndexes(%d) = %v, want ascending order", tt.total, got)
					return
				}
			}
			// 验证首尾
			if len(got) > 0 && tt.total > 0 {
				if got[0] != 0 {
					t.Errorf("styleSampleIndexes(%d) first = %d, want 0", tt.total, got[0])
				}
				if tt.total > styleSampleChapters && got[len(got)-1] != tt.total-1 {
					t.Errorf("styleSampleIndexes(%d) last = %d, want %d", tt.total, got[len(got)-1], tt.total-1)
				}
			}
		})
	}
}
