package tools

import (
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

var premiseHeadingAliases = map[string]string{
	"题材定位":    "题材定位",
	"题材和基调":   "题材和基调",
	"核心冲突":    "核心冲突",
	"主角目标":    "主角目标",
	"结局方向":    "终局方向",
	"终局方向":    "终局方向",
	"写作禁区":    "写作禁区",
	"差异化卖点":   "差异化卖点",
	"差异化钩子":   "差异化钩子",
	"核心兑现承诺":  "核心兑现承诺",
	"故事引擎":    "故事引擎",
	"关系/成长主线": "关系/成长主线",
	"升级路径":    "升级路径",
	"中段转折":    "中段转折",
	"中期转向":    "中段转折",
	"终局命题":    "终局命题",
	"短篇适配性":   "短篇适配性",
	"本作为什么适合短篇/单卷收束": "短篇适配性",
}

func parsePremiseSections(premise string) map[string]string {
	lines := strings.Split(premise, "\n")
	sections := make(map[string]string)
	var current string
	var body []string

	flush := func() {
		if current == "" {
			return
		}
		text := strings.TrimSpace(strings.Join(body, "\n"))
		if text != "" {
			sections[current] = text
		}
		body = body[:0]
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if heading, ok := canonicalPremiseHeading(trimmed); ok {
			flush()
			current = heading
			continue
		}
		if current != "" {
			body = append(body, line)
		}
	}
	flush()
	return sections
}

func canonicalPremiseHeading(line string) (string, bool) {
	if !strings.HasPrefix(line, "#") {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimLeft(line, "#"))
	if title == "" {
		return "", false
	}
	canonical, ok := premiseHeadingAliases[title]
	return canonical, ok
}

func premiseStructure(premise string, tier domain.PlanningTier) map[string]any {
	sections := parsePremiseSections(premise)
	required := requiredPremiseHeadings(tier)
	found := make([]string, 0, len(required))
	var missing []string
	for _, heading := range required {
		if _, ok := sections[heading]; ok {
			found = append(found, heading)
			continue
		}
		missing = append(missing, heading)
	}

	structure := map[string]any{
		"template_ready": len(missing) == 0,
		"found":          found,
		"missing":        missing,
	}
	if len(sections) > 0 {
		structure["section_count"] = len(sections)
	}
	return structure
}

func requiredPremiseHeadings(tier domain.PlanningTier) []string {
	common := []string{
		"题材和基调",
		"题材定位",
		"核心冲突",
		"主角目标",
		"终局方向",
		"写作禁区",
		"差异化卖点",
		"差异化钩子",
		"核心兑现承诺",
	}

	switch tier {
	case domain.PlanningTierLong:
		return append(common,
			"故事引擎",
			"关系/成长主线",
			"升级路径",
			"中段转折",
			"终局命题",
		)
	case domain.PlanningTierMid:
		return append(common,
			"故事引擎",
			"中段转折",
		)
	case domain.PlanningTierShort:
		return append(common,
			"短篇适配性",
		)
	default:
		return common
	}
}
