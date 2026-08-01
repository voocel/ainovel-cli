package rip

import (
	"testing"
)

// TestValidateAggregateChapterRanges 验证剧情单元章节范围的机械校验：
// 必须连续、不重叠、不跳空、完整覆盖全书。
func TestValidateAggregateChapterRanges(t *testing.T) {
	tests := []struct {
		name      string
		aggregate *Aggregate
		total     int
		wantErr   bool
		errMsg    string
	}{
		{
			name: "正常：3 个单元完整覆盖 5 章",
			aggregate: &Aggregate{
				PlotUnits: []PlotUnit{
					{Title: "开端", StartChapter: 1, EndChapter: 2},
					{Title: "发展", StartChapter: 3, EndChapter: 4},
					{Title: "结局", StartChapter: 5, EndChapter: 5},
				},
				StoryCore:    "故事核",
				MainConflict: "主线冲突",
			},
			total:   5,
			wantErr: false,
		},
		{
			name: "错误：单元之间跳空",
			aggregate: &Aggregate{
				PlotUnits: []PlotUnit{
					{Title: "开端", StartChapter: 1, EndChapter: 2},
					{Title: "结局", StartChapter: 4, EndChapter: 5}, // 跳过了第 3 章
				},
				StoryCore:    "故事核",
				MainConflict: "主线冲突",
			},
			total:   5,
			wantErr: true,
			errMsg:  "跳空",
		},
		{
			name: "错误：单元之间重叠",
			aggregate: &Aggregate{
				PlotUnits: []PlotUnit{
					{Title: "开端", StartChapter: 1, EndChapter: 3},
					{Title: "发展", StartChapter: 3, EndChapter: 5}, // 第 3 章重复
				},
				StoryCore:    "故事核",
				MainConflict: "主线冲突",
			},
			total:   5,
			wantErr: true,
			errMsg:  "重叠",
		},
		{
			name: "错误：单元未覆盖全书",
			aggregate: &Aggregate{
				PlotUnits: []PlotUnit{
					{Title: "开端", StartChapter: 1, EndChapter: 3},
				},
				StoryCore:    "故事核",
				MainConflict: "主线冲突",
			},
			total:   5,
			wantErr: true,
			errMsg:  "只覆盖到第 3 章",
		},
		{
			name: "错误：单元范围越界",
			aggregate: &Aggregate{
				PlotUnits: []PlotUnit{
					{Title: "开端", StartChapter: 1, EndChapter: 10}, // 超过全书章数
				},
				StoryCore:    "故事核",
				MainConflict: "主线冲突",
			},
			total:   5,
			wantErr: true,
			errMsg:  "越界",
		},
		{
			name: "错误：单元起始章大于结束章",
			aggregate: &Aggregate{
				PlotUnits: []PlotUnit{
					{Title: "错误单元", StartChapter: 3, EndChapter: 1},
				},
				StoryCore:    "故事核",
				MainConflict: "主线冲突",
			},
			total:   5,
			wantErr: true,
			errMsg:  "起始章 3 大于结束章 1",
		},
		{
			name: "错误：缺故事核",
			aggregate: &Aggregate{
				PlotUnits: []PlotUnit{
					{Title: "开端", StartChapter: 1, EndChapter: 5},
				},
				StoryCore:    "", // 缺故事核
				MainConflict: "主线冲突",
			},
			total:   5,
			wantErr: true,
			errMsg:  "缺故事核",
		},
		{
			name: "错误：情绪模块引用不存在的章",
			aggregate: &Aggregate{
				PlotUnits: []PlotUnit{
					{Title: "开端", StartChapter: 1, EndChapter: 5},
				},
				StoryCore:    "故事核",
				MainConflict: "主线冲突",
				EmotionModules: []EmotionModule{
					{Name: "紧张感", Chapters: []int{1, 10}}, // 第 10 章不存在
				},
			},
			total:   5,
			wantErr: true,
			errMsg:  "引用了不存在的第 10 章",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAggregate(tt.aggregate, tt.total)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAggregate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("validateAggregate() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

// contains 检查字符串是否包含子串
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && (s[:len(substr)] == substr || contains(s[1:], substr))))
}
