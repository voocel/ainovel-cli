package diag

import "fmt"

// PlanActions 根据高置信 Finding 生成可执行动作。
// 只有 Confidence==high && AutoLevel==safe 的 Finding 才会产出 Action。
func PlanActions(findings []Finding) []Action {
	var actions []Action
	seen := make(map[string]struct{})

	for _, f := range findings {
		if f.Confidence != ConfHigh || f.AutoLevel != AutoSafe {
			continue
		}
		if _, ok := seen[f.Rule]; ok {
			continue
		}
		seen[f.Rule] = struct{}{}

		actions = append(actions, planRule(f)...)
	}
	return actions
}

func planRule(f Finding) []Action {
	key := findingFingerprint(f)

	switch f.Rule {
	case "PhaseFlowMismatch":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEmitNotice, Severity: f.Severity, Summary: f.Title, Message: f.Title, Fingerprint: key},
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "Trạng thái机异常修复", Message: "Trạng thái机异常：" + f.Evidence + "。Vui lòng先Kiểm tra并修正 progress 的 phase/flow Trạng thái，再Tiếp tục运行。", Fingerprint: key},
		}
	case "OutlineExhausted":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "Đại cương耗尽处理", Message: "Đã hoàn thànhChương数达到已规划上限。Vui lòng优先调用 Architect Mở rộng下一弧或追加Mới卷，再Tiếp tục viết。", Fingerprint: key},
		}
	case "OrphanedSteer":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "消费未处理的用户干预", Message: "存在未消费的用户干预指令，Vui lòng优先处理 pending steer 后再Tiếp tụcHiện tại任务。", Fingerprint: key},
		}
	default:
		return nil
	}
}

func findingFingerprint(f Finding) string {
	return fmt.Sprintf("%s|%s|%s|%s", f.Rule, f.Target, f.Title, f.Evidence)
}
