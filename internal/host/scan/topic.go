package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TopicIdea 是一条选题建议。
type TopicIdea struct {
	Direction   string   `json:"direction"`   // 方向
	Why         string   `json:"why"`         // 为什么能爆
	Feasibility string   `json:"feasibility"` // 可行性：高/中/低
	Risks       []string `json:"risks"`       // 风险
	NextSteps   []string `json:"next_steps"`  // 下一步行动
}

// TopicReport 是选题决策报告。
type TopicReport struct {
	Ideas []TopicIdea `json:"ideas"`
}

// validateTopic 机械校验选题产物：方向数量、必填字段、风险与下一步不得为空。
// 可行性等级不在这里校验——它由 ClampFeasibility 在校验通过后无条件钳制，
// 放进 Validate 会变成「回喂重问直到模型改口」，那正是我们不想要的谈判。
func validateTopic(r *TopicReport) error {
	if len(r.Ideas) < 2 {
		return fmt.Errorf("至少需要 2 个选题方向，当前 %d 个", len(r.Ideas))
	}
	for i, idea := range r.Ideas {
		if strings.TrimSpace(idea.Direction) == "" {
			return fmt.Errorf("方向[%d] 缺方向描述", i)
		}
		if strings.TrimSpace(idea.Why) == "" {
			return fmt.Errorf("方向 %q 缺「为什么能爆」", snippet(idea.Direction, 20))
		}
		if len(nonEmpty(idea.Risks)) == 0 {
			return fmt.Errorf("方向 %q 没给风险：每个选题都有代价，写不出来说明想得不够细", snippet(idea.Direction, 20))
		}
		if len(nonEmpty(idea.NextSteps)) == 0 {
			return fmt.Errorf("方向 %q 没给下一步动作：选题必须可执行", snippet(idea.Direction, 20))
		}
	}
	return nil
}

// DecideTopics 基于趋势分析产出选题决策，随后无条件施加两道 Go 侧硬规则：
// 可行性降级钳制与「待拆文验证」标记。模型不能通过措辞绕开。
func DecideTopics(ctx context.Context, m callModel, systemPrompt string, a *Analysis, q QualityReport, platform, rankName string, maxTokens int, prof callProfile) (*TopicReport, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "平台：%s\n榜单：%s\n有效条目：%d\n",
		fallback(platform, "未指明"), fallback(rankName, "未指明"), q.ValidEntries)
	if q.Sparse {
		sb.WriteString("数据质量：[数据稀疏] —— 样本不足，可行性不得给「高」\n")
	}
	sb.WriteString("\n以下是这份榜单的趋势分析：\n\n")
	data, _ := json.MarshalIndent(a, "", "  ")
	sb.Write(data)
	sb.WriteString("\n\n请据此给出 2-5 个可执行的选题方向。\n")

	out, err := callStructured[TopicReport](ctx, m, topicContract, systemPrompt, sb.String(), maxTokens, prof, validateTopic)
	if err != nil {
		return nil, err
	}

	// 两道硬规则按顺序施加，与模型的措辞无关。
	ideas := ClampFeasibility(out.Ideas, q.ValidEntries, q.Sparse, platform)
	ideas = AppendVerificationTag(ideas)
	report := &TopicReport{Ideas: ideas}

	for _, idea := range report.Ideas {
		prof.step(0, 0, "选题〈%s〉可行性 %s", snippet(idea.Direction, 24), idea.Feasibility)
	}
	return report, nil
}

// 可行性等级
const (
	FeasibilityHigh   = "高"
	FeasibilityMedium = "中"
	FeasibilityLow    = "低"
)

// 可行性降级钳制的样本阈值（与清洗规则一致）
const (
	feasibilityMinSamplesNormal = 15
	feasibilityMinSamplesSmall  = 10
)

// ClampFeasibility 可行性降级钳制：样本不足或数据稀疏时，强制把「高」降到「中」。
// 这是参考实现最容易被模型绕过的地方，必须代码执行。
func ClampFeasibility(ideas []TopicIdea, validEntries int, sparse bool, platform string) []TopicIdea {
	minSamples := feasibilityMinSamplesNormal
	if smallPlatforms[strings.ToLower(platform)] {
		minSamples = feasibilityMinSamplesSmall
	}

	// 样本不足或数据稀疏 → 钳制
	shouldClamp := sparse || validEntries < minSamples

	if !shouldClamp {
		return ideas
	}

	clamped := make([]TopicIdea, len(ideas))
	for i, idea := range ideas {
		clamped[i] = idea

		// 只钳制「高」→「中」
		if idea.Feasibility == FeasibilityHigh {
			clamped[i].Feasibility = FeasibilityMedium

			// 追加警告
			warning := "样本不足，先扫够样本或试水再定"
			if !containsWarning(clamped[i].NextSteps, warning) {
				clamped[i].NextSteps = append([]string{warning}, clamped[i].NextSteps...)
			}
		}
	}

	return clamped
}

// containsWarning 检查 NextSteps 是否已包含警告。
func containsWarning(steps []string, warning string) bool {
	for _, step := range steps {
		if strings.Contains(step, warning) {
			return true
		}
	}
	return false
}

// AppendVerificationTag 给「能爆的原因」统一追加「待拆文验证」标记。
func AppendVerificationTag(ideas []TopicIdea) []TopicIdea {
	const tag = "（待拆文验证）"
	tagged := make([]TopicIdea, len(ideas))
	for i, idea := range ideas {
		tagged[i] = idea
		if !strings.HasSuffix(strings.TrimSpace(idea.Why), tag) {
			tagged[i].Why = strings.TrimSpace(idea.Why) + " " + tag
		}
	}
	return tagged
}

// RenderTopicMarkdown 渲染选题决策 Markdown。
// 质量头在最前面：可行性等级已被钳制过，读者得先看到样本量才知道钳制为何发生。
func RenderTopicMarkdown(report TopicReport, q QualityReport) string {
	var sb strings.Builder
	sb.WriteString("# 选题决策\n\n")
	sb.WriteString(RenderQualityHeader(q))

	for i, idea := range report.Ideas {
		fmt.Fprintf(&sb, "## 方向 %d：%s\n\n", i+1, idea.Direction)
		fmt.Fprintf(&sb, "**为什么能爆**：%s\n\n", idea.Why)
		fmt.Fprintf(&sb, "**可行性**：%s\n\n", idea.Feasibility)

		if len(idea.Risks) > 0 {
			sb.WriteString("**风险**：\n\n")
			for _, risk := range idea.Risks {
				fmt.Fprintf(&sb, "- %s\n", risk)
			}
			sb.WriteString("\n")
		}

		if len(idea.NextSteps) > 0 {
			sb.WriteString("**下一步**：\n\n")
			for _, step := range idea.NextSteps {
				fmt.Fprintf(&sb, "- %s\n", step)
			}
			sb.WriteString("\n")
		}

		sb.WriteString("---\n\n")
	}

	return sb.String()
}
