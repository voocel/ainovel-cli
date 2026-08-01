package rip

import (
	"testing"
)

// TestValidateCountsThresholds 验证结构计数阈值校验，含「无反转」carve-out。
func TestValidateCountsThresholds(t *testing.T) {
	tests := []struct {
		name    string
		counts  StructureCounts
		wantErr bool
		errMsg  string
	}{
		{
			name: "正常：所有计数达标，有反转",
			counts: StructureCounts{
				Beats:               4,
				Hooks:               3,
				SetupClues:          3,
				CharacterArchetypes: 2,
				ReusableStructures:  3,
				ResonanceLayers:     3,
				ReversalType:        "视角反转",
			},
			wantErr: false,
		},
		{
			name: "正常：无反转时跳过 SetupClues 检查 (carve-out)",
			counts: StructureCounts{
				Beats:               4,
				Hooks:               3,
				SetupClues:          0, // 无反转时允许为 0
				CharacterArchetypes: 2,
				ReusableStructures:  3,
				ResonanceLayers:     3,
				ReversalType:        reversalNone,
			},
			wantErr: false,
		},
		{
			name: "错误：Beats 不足",
			counts: StructureCounts{
				Beats:               2, // 少于 minBeats=4
				Hooks:               3,
				SetupClues:          3,
				CharacterArchetypes: 2,
				ReusableStructures:  3,
				ResonanceLayers:     3,
				ReversalType:        "视角反转",
			},
			wantErr: true,
			errMsg:  "功能分段只有 2 段",
		},
		{
			name: "错误：Hooks 不足",
			counts: StructureCounts{
				Beats:               4,
				Hooks:               1, // 少于 minHooks=3
				SetupClues:          3,
				CharacterArchetypes: 2,
				ReusableStructures:  3,
				ResonanceLayers:     3,
				ReversalType:        "视角反转",
			},
			wantErr: true,
			errMsg:  "钩子只有 1 条",
		},
		{
			name: "错误：有反转但 SetupClues 不足",
			counts: StructureCounts{
				Beats:               4,
				Hooks:               3,
				SetupClues:          1, // 少于 minSetupClues=3，且不是无反转
				CharacterArchetypes: 2,
				ReusableStructures:  3,
				ResonanceLayers:     3,
				ReversalType:        "身份反转",
			},
			wantErr: true,
			errMsg:  "反转铺垫线索只有 1 条",
		},
		{
			name: "错误：CharacterArchetypes 不足",
			counts: StructureCounts{
				Beats:               4,
				Hooks:               3,
				SetupClues:          3,
				CharacterArchetypes: 1, // 少于 minCharacterArchetypes=2
				ReusableStructures:  3,
				ResonanceLayers:     3,
				ReversalType:        "视角反转",
			},
			wantErr: true,
			errMsg:  "有反差的人物原型只有 1 个",
		},
		{
			name: "错误：ReusableStructures 不足",
			counts: StructureCounts{
				Beats:               4,
				Hooks:               3,
				SetupClues:          3,
				CharacterArchetypes: 2,
				ReusableStructures:  2, // 少于 minReusableStructures=3
				ResonanceLayers:     3,
				ReversalType:        "视角反转",
			},
			wantErr: true,
			errMsg:  "可复用结构只有 2 条",
		},
		{
			name: "错误：ResonanceLayers 不足",
			counts: StructureCounts{
				Beats:               4,
				Hooks:               3,
				SetupClues:          3,
				CharacterArchetypes: 2,
				ReusableStructures:  3,
				ResonanceLayers:     1, // 少于 minResonanceLayers=3
				ReversalType:        "视角反转",
			},
			wantErr: true,
			errMsg:  "共鸣层次只有 1 层",
		},
		{
			name: "错误：非法 ReversalType",
			counts: StructureCounts{
				Beats:               4,
				Hooks:               3,
				SetupClues:          3,
				CharacterArchetypes: 2,
				ReusableStructures:  3,
				ResonanceLayers:     3,
				ReversalType:        "未知反转", // 不在 reversalTypes 词表中
			},
			wantErr: true,
			errMsg:  "反转类型",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCounts(tt.counts)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCounts() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("validateCounts() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

// TestDeriveCountsFromReport 验证从报告内容确定性算出结构计数。
func TestDeriveCountsFromReport(t *testing.T) {
	report := &Report{
		Beats: []Beat{
			{Name: "开端", StartChapter: 1, EndChapter: 2},
			{Name: "发展", StartChapter: 3, EndChapter: 5},
			{Name: "高潮", StartChapter: 6, EndChapter: 7},
			{Name: "结局", StartChapter: 8, EndChapter: 10},
		},
		Hooks: []string{"钩子1", "钩子2", "", "钩子3", "  "}, // 空值应被过滤
		Reversal: Reversal{
			Type:       "视角反转",
			SetupClues: []string{"线索1", "线索2", "线索3", ""},
		},
		CharacterArchetypes: []string{"反差型主角", "成长型配角", ""},
		ReusableStructures: []ReusableStructure{
			{Name: "结构1"},
			{Name: "结构2"},
			{Name: "结构3"},
		},
		Resonance: []string{"共鸣1", "共鸣2", "", "共鸣3"},
	}

	counts := deriveCounts(report)

	if counts.Beats != 4 {
		t.Errorf("Beats = %d, want 4", counts.Beats)
	}
	if counts.Hooks != 3 {
		t.Errorf("Hooks = %d, want 3 (empty strings filtered)", counts.Hooks)
	}
	if counts.SetupClues != 3 {
		t.Errorf("SetupClues = %d, want 3 (empty strings filtered)", counts.SetupClues)
	}
	if counts.CharacterArchetypes != 2 {
		t.Errorf("CharacterArchetypes = %d, want 2 (empty strings filtered)", counts.CharacterArchetypes)
	}
	if counts.ReusableStructures != 3 {
		t.Errorf("ReusableStructures = %d, want 3", counts.ReusableStructures)
	}
	if counts.ResonanceLayers != 3 {
		t.Errorf("ResonanceLayers = %d, want 3 (empty strings filtered)", counts.ResonanceLayers)
	}
	if counts.ReversalType != "视角反转" {
		t.Errorf("ReversalType = %q, want %q", counts.ReversalType, "视角反转")
	}
}

// TestValidateReportBasic 验证报告的基本校验：必填字段、分段范围。
func TestValidateReportBasic(t *testing.T) {
	tests := []struct {
		name    string
		report  *Report
		total   int
		wantErr bool
		errMsg  string
	}{
		{
			name: "正常：完整报告",
			report: &Report{
				StoryCore: "故事核",
				Synopsis:  "梗概",
				Beats: []Beat{
					{Name: "开端", StartChapter: 1, EndChapter: 3},
					{Name: "发展", StartChapter: 4, EndChapter: 7},
					{Name: "高潮", StartChapter: 8, EndChapter: 9},
					{Name: "结局", StartChapter: 10, EndChapter: 10},
				},
				Hooks:               []string{"钩子1", "钩子2", "钩子3"},
				Reversal:            Reversal{Type: reversalNone},
				CharacterArchetypes: []string{"原型1", "原型2"},
				ReusableStructures: []ReusableStructure{
					{Name: "结构1", How: "用法1", FailMode: "失败模式1", Example: "示例1"},
					{Name: "结构2", How: "用法2", FailMode: "失败模式2", Example: "示例2"},
					{Name: "结构3", How: "用法3", FailMode: "失败模式3", Example: "示例3"},
				},
				Resonance: []string{"共鸣1", "共鸣2", "共鸣3"},
				Scores: []Score{
					{Dimension: "情节", Value: 4, Reason: "理由"},
				},
			},
			total:   10,
			wantErr: false,
		},
		{
			name: "错误：缺故事核",
			report: &Report{
				StoryCore: "",
				Synopsis:  "梗概",
			},
			total:   10,
			wantErr: true,
			errMsg:  "缺故事核",
		},
		{
			name: "错误：功能分段范围越界",
			report: &Report{
				StoryCore: "故事核",
				Synopsis:  "梗概",
				Beats: []Beat{
					{Name: "开端", StartChapter: 1, EndChapter: 15}, // 超过全书章数
				},
				Hooks:               []string{"钩子1", "钩子2", "钩子3"},
				Reversal:            Reversal{Type: reversalNone},
				CharacterArchetypes: []string{"原型1", "原型2"},
				ReusableStructures: []ReusableStructure{
					{Name: "结构1", How: "用法1", FailMode: "失败模式1", Example: "示例1"},
					{Name: "结构2", How: "用法2", FailMode: "失败模式2", Example: "示例2"},
					{Name: "结构3", How: "用法3", FailMode: "失败模式3", Example: "示例3"},
				},
				Resonance: []string{"共鸣1", "共鸣2", "共鸣3"},
			},
			total:   10,
			wantErr: true,
			errMsg:  "非法",
		},
		{
			name: "错误：评分越界",
			report: &Report{
				StoryCore: "故事核",
				Synopsis:  "梗概",
				Beats: []Beat{
					{Name: "开端", StartChapter: 1, EndChapter: 3},
					{Name: "发展", StartChapter: 4, EndChapter: 7},
					{Name: "高潮", StartChapter: 8, EndChapter: 9},
					{Name: "结局", StartChapter: 10, EndChapter: 10},
				},
				Hooks:               []string{"钩子1", "钩子2", "钩子3"},
				Reversal:            Reversal{Type: reversalNone},
				CharacterArchetypes: []string{"原型1", "原型2"},
				ReusableStructures: []ReusableStructure{
					{Name: "结构1", How: "用法1", FailMode: "失败模式1", Example: "示例1"},
					{Name: "结构2", How: "用法2", FailMode: "失败模式2", Example: "示例2"},
					{Name: "结构3", How: "用法3", FailMode: "失败模式3", Example: "示例3"},
				},
				Resonance: []string{"共鸣1", "共鸣2", "共鸣3"},
				Scores: []Score{
					{Dimension: "情节", Value: 6, Reason: "理由"}, // 评分超过 5
				},
			},
			total:   10,
			wantErr: true,
			errMsg:  "越界",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateReport(tt.report, tt.total)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateReport() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("validateReport() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}
