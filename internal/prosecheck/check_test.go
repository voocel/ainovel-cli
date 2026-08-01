package prosecheck

import (
	"strings"
	"testing"
)

func TestCheckLeavesNaturalChapterAlone(t *testing.T) {
	text := `# 雨停以后

雨水顺着瓦当落进石槽。陈砚把湿斗笠挂在门边，先摸了摸灶沿，余温还在。

院门响了两声。卖豆腐的老周探进半张脸，问他昨夜有没有听见河滩上的动静。

陈砚没有回答。他从袖里取出一枚沾泥的铜扣，搁在桌上。老周认出扣背的记号，脸色变了，伸出去的手停在半空。

锅里的水开了。陈砚提壶冲茶，等老周自己坐下。`
	if got := Check(text); len(got) != 0 {
		t.Fatalf("自然文本不应误报: %+v", got)
	}
}

func TestCheckFindsHighConfidencePatternAndCapsEvidence(t *testing.T) {
	text := strings.Join([]string{
		"他不是害怕，而是在等门外的人先动。",
		"她不是迟疑，而是在听楼梯上的脚步。",
		"这不是退让，而是一次有意的试探。",
		"那不是雨声，而是碎石滚下屋脊。",
	}, "\n")
	finding := findRule(Check(text), "not_is_comparison")
	if finding == nil || finding.Severity != SeverityWarning || finding.Count != 4 {
		t.Fatalf("未正确检测矫正句: %+v", finding)
	}
	if len(finding.Evidence) != maxEvidence {
		t.Fatalf("evidence=%d, want %d", len(finding.Evidence), maxEvidence)
	}
}

func TestEndingRuleOnlyScansTailWindow(t *testing.T) {
	middleOnly := "没人知道，门后还藏着什么。\n" + strings.Repeat("院里雨声不断，众人围着火盆核对账册。\n", 80)
	if finding := findRule(Check(middleOnly), "trailer_ending"); finding != nil {
		t.Fatalf("正文中段不应触发章尾规则: %+v", finding)
	}

	withEnding := middleOnly + "\n谁也没想到，这才刚刚开始。"
	if finding := findRule(Check(withEnding), "trailer_ending"); finding == nil {
		t.Fatal("章尾预告体应被检测")
	}
}

func TestDensityRuleRequiresCountAndRate(t *testing.T) {
	sparse := strings.Repeat("风从窗缝进来，桌上的纸角动了动。", 120) + "仿佛一丝微微"
	if finding := findRule(Check(sparse), "cliche_density_tic"); finding != nil {
		t.Fatalf("低次数或低密度不应触发: %+v", finding)
	}

	dense := strings.Repeat("他仿佛看见一丝微光，动作微微一顿。\n", 6)
	finding := findRule(Check(dense), "cliche_density_tic")
	if finding == nil || finding.Count < 8 || !strings.Contains(finding.Metric, "/千字") {
		t.Fatalf("高密度套词应触发: %+v", finding)
	}
}

func TestDialogueDoesNotTriggerNarrativeTemplate(t *testing.T) {
	text := "“这不是借口，而是事实。”老周说完，把账本推了过去。"
	if finding := findRule(Check(text), "not_is_comparison"); finding != nil {
		t.Fatalf("对话中的自然辩驳不应按叙述模板上报: %+v", finding)
	}
}

func findRule(findings []Finding, rule string) *Finding {
	for i := range findings {
		if findings[i].Rule == rule {
			return &findings[i]
		}
	}
	return nil
}
