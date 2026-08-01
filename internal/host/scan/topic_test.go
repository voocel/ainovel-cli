package scan

import (
	"testing"
)

// TestClampFeasibility 验证可行性降级钳制：模型返回「高」但样本不足时，
// 必须被钳成「中」并追加警告，不允许模型通过措辞绕开。
func TestClampFeasibility(t *testing.T) {
	tests := []struct {
		name          string
		ideas         []TopicIdea
		validEntries  int
		sparse        bool
		platform      string
		wantClamped   bool
		checkWarning  bool
	}{
		{
			name: "正常：样本充足，不钳制",
			ideas: []TopicIdea{
				{Direction: "方向1", Feasibility: FeasibilityHigh, Why: "理由"},
			},
			validEntries: 20,
			sparse:       false,
			platform:     "qidian",
			wantClamped:  false,
		},
		{
			name: "钳制：样本不足，高→中",
			ideas: []TopicIdea{
				{Direction: "方向1", Feasibility: FeasibilityHigh, Why: "理由", NextSteps: []string{"原有步骤"}},
			},
			validEntries: 10, // 少于 15
			sparse:       false,
			platform:     "qidian",
			wantClamped:  true,
			checkWarning: true,
		},
		{
			name: "钳制：数据稀疏，高→中",
			ideas: []TopicIdea{
				{Direction: "方向1", Feasibility: FeasibilityHigh, Why: "理由"},
			},
			validEntries: 20,
			sparse:       true, // 稀疏标记
			platform:     "qidian",
			wantClamped:  true,
			checkWarning: true,
		},
		{
			name: "不钳制中和低",
			ideas: []TopicIdea{
				{Direction: "方向1", Feasibility: FeasibilityMedium, Why: "理由"},
				{Direction: "方向2", Feasibility: FeasibilityLow, Why: "理由"},
			},
			validEntries: 10,
			sparse:       false,
			platform:     "qidian",
			wantClamped:  false, // 中和低不被改变
		},
		{
			name: "小平台：10 条达标不钳制",
			ideas: []TopicIdea{
				{Direction: "方向1", Feasibility: FeasibilityHigh, Why: "理由"},
			},
			validEntries: 10,
			sparse:       false,
			platform:     "qimao",
			wantClamped:  false,
		},
		{
			name: "小平台：9 条不足需钳制",
			ideas: []TopicIdea{
				{Direction: "方向1", Feasibility: FeasibilityHigh, Why: "理由"},
			},
			validEntries: 9,
			sparse:       false,
			platform:     "ciweimao",
			wantClamped:  true,
			checkWarning: true,
		},
		{
			name: "多个高可行性都被钳制",
			ideas: []TopicIdea{
				{Direction: "方向1", Feasibility: FeasibilityHigh, Why: "理由1"},
				{Direction: "方向2", Feasibility: FeasibilityHigh, Why: "理由2"},
				{Direction: "方向3", Feasibility: FeasibilityMedium, Why: "理由3"},
			},
			validEntries: 10,
			sparse:       false,
			platform:     "qidian",
			wantClamped:  true,
			checkWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClampFeasibility(tt.ideas, tt.validEntries, tt.sparse, tt.platform)

			if len(result) != len(tt.ideas) {
				t.Fatalf("result length = %d, want %d", len(result), len(tt.ideas))
			}

			for i, original := range tt.ideas {
				if original.Feasibility == FeasibilityHigh && tt.wantClamped {
					// 应该被钳成中
					if result[i].Feasibility != FeasibilityMedium {
						t.Errorf("ideas[%d].Feasibility = %q, want %q (clamped from 高)",
							i, result[i].Feasibility, FeasibilityMedium)
					}

					// 检查是否追加了警告
					if tt.checkWarning {
						found := false
						for _, step := range result[i].NextSteps {
							if containsSubstr(step, "样本不足") || containsSubstr(step, "试水") {
								found = true
								break
							}
						}
						if !found {
							t.Errorf("ideas[%d].NextSteps = %v, want to contain warning about insufficient samples",
								i, result[i].NextSteps)
						}
					}
				} else if original.Feasibility == FeasibilityHigh && !tt.wantClamped {
					// 不应该被钳制
					if result[i].Feasibility != FeasibilityHigh {
						t.Errorf("ideas[%d].Feasibility = %q, want %q (should not be clamped)",
							i, result[i].Feasibility, FeasibilityHigh)
					}
				} else if original.Feasibility != FeasibilityHigh {
					// 中和低不应该被改变
					if result[i].Feasibility != original.Feasibility {
						t.Errorf("ideas[%d].Feasibility = %q, want %q (should not change 中/低)",
							i, result[i].Feasibility, original.Feasibility)
					}
				}
			}
		})
	}
}

// TestValidateTopic 验证选题产物的机械校验。
// 可行性等级刻意不在这里校验：它由 ClampFeasibility 在校验通过后无条件钳制，
// 放进 Validate 会变成「回喂重问直到模型改口」，那是我们要避免的谈判。
func TestValidateTopic(t *testing.T) {
	valid := func() *TopicReport {
		return &TopicReport{Ideas: []TopicIdea{
			{Direction: "方向1", Why: "理由1", Feasibility: FeasibilityMedium,
				Risks: []string{"风险1"}, NextSteps: []string{"动作1"}},
			{Direction: "方向2", Why: "理由2", Feasibility: FeasibilityLow,
				Risks: []string{"风险2"}, NextSteps: []string{"动作2"}},
		}}
	}

	if err := validateTopic(valid()); err != nil {
		t.Fatalf("合法产物应通过校验，得到 %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*TopicReport)
		reason string
	}{
		{
			name:   "只有 1 个方向",
			mutate: func(r *TopicReport) { r.Ideas = r.Ideas[:1] },
			reason: "至少需要 2 个选题方向",
		},
		{
			name:   "缺方向描述",
			mutate: func(r *TopicReport) { r.Ideas[0].Direction = "  " },
			reason: "缺方向描述",
		},
		{
			name:   "缺能爆的原因",
			mutate: func(r *TopicReport) { r.Ideas[0].Why = "" },
			reason: "为什么能爆",
		},
		{
			name:   "风险全是空串",
			mutate: func(r *TopicReport) { r.Ideas[0].Risks = []string{"", " "} },
			reason: "没给风险",
		},
		{
			name:   "下一步为空",
			mutate: func(r *TopicReport) { r.Ideas[0].NextSteps = nil },
			reason: "没给下一步动作",
		},
		{
			// 高可行性本身合法：钳制由 ClampFeasibility 负责，不是校验层的事。
			name:   "高可行性不被校验层拦下",
			mutate: func(r *TopicReport) { r.Ideas[0].Feasibility = FeasibilityHigh },
			reason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := valid()
			tt.mutate(r)
			err := validateTopic(r)
			if tt.reason == "" {
				if err != nil {
					t.Fatalf("不应被拦下，得到 %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应该被拦下（%s）", tt.reason)
			}
			if !containsSubstr(err.Error(), tt.reason) {
				t.Errorf("错误 %q 应包含 %q", err.Error(), tt.reason)
			}
		})
	}
}

// TestRenderTopicMarkdown 验证选题文档把质量头放在方向之前：
// 可行性已被钳制过，读者得先看到样本量才知道钳制为何发生。
func TestRenderTopicMarkdown(t *testing.T) {
	report := TopicReport{Ideas: []TopicIdea{
		{Direction: "方向甲", Why: "理由甲", Feasibility: FeasibilityMedium,
			Risks: []string{"风险甲"}, NextSteps: []string{"样本不足，先扫够样本或试水再定"}},
	}}
	q := QualityReport{TotalRaw: 12, ValidEntries: 8, Sparse: true,
		Issues: []string{"有效条目不足 15 条，数据稀疏"}}

	md := RenderTopicMarkdown(report, q)

	for _, want := range []string{"[数据稀疏]", "方向甲", "样本不足", "有效条目不足 15 条"} {
		if !containsSubstr(md, want) {
			t.Errorf("选题文档应包含 %q", want)
		}
	}
	iQuality := indexOf(md, "数据质量")
	iIdea := indexOf(md, "方向甲")
	if iQuality < 0 || iIdea < 0 || iQuality > iIdea {
		t.Errorf("质量头必须在方向之前（quality=%d idea=%d）", iQuality, iIdea)
	}
}

// TestAppendVerificationTag 验证「待拆文验证」标记统一追加。
func TestAppendVerificationTag(t *testing.T) {
	ideas := []TopicIdea{
		{Direction: "方向1", Why: "理由1"},
		{Direction: "方向2", Why: "理由2（待拆文验证）"}, // 已有标记
		{Direction: "方向3", Why: "理由3  "},          // 末尾空格
	}

	result := AppendVerificationTag(ideas)

	const tag = "（待拆文验证）"

	for i, idea := range result {
		if !containsSubstr(idea.Why, tag) {
			t.Errorf("ideas[%d].Why = %q, want to contain %q", i, idea.Why, tag)
		}
	}

	// 已有标记的不应该重复追加
	if result[1].Why != "理由2（待拆文验证）" {
		t.Errorf("ideas[1].Why = %q, want no duplicate tag", result[1].Why)
	}
}
