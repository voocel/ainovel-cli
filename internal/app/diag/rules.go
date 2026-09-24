package diag

import "fmt"

// Counts refer to the entire selected scope, not only the visible task page.
func findings(failed, expired, unknown, missing int) []Finding {
	result := []Finding{}
	for _, entry := range []struct {
		code  string
		count int
	}{
		{"execution.failed", failed}, {"execution.lease_expired", expired},
		{"execution.result_unknown", unknown}, {"observation.end_missing", missing},
	} {
		if entry.count > 0 {
			result = append(result, finding(entry.code, entry.count))
		}
	}
	if unknown > 1 {
		result = append(result, finding("execution.repeated_unknown_result", unknown))
	}
	return result
}

func finding(code string, count int) Finding {
	f := Finding{Code: code, Count: count, Severity: "warning", Certainty: "confirmed"}
	switch code {
	case "execution.failed":
		f.Severity = "error"
		f.Observed = fmt.Sprintf("%d 个任务的持久化状态为失败", count)
		f.Suggestion = "展开任务查看原始错误；失败状态本身不能确定根因。"
	case "execution.lease_expired":
		f.Observed = fmt.Sprintf("%d 个运行中任务的租约在采集时已过期", count)
		f.Suggestion = "核对执行器与恢复记录；租约过期不证明进程或外部任务已经停止。"
	case "execution.result_unknown":
		f.Observed = fmt.Sprintf("%d 个任务记录了外部结果未知", count)
		f.Suggestion = "先核对外部任务结果与恢复记录，不要据此重复提交外部任务。"
	case "execution.repeated_unknown_result":
		f.Certainty = "signal"
		f.Observed = fmt.Sprintf("观察范围内出现 %d 次外部结果未知", count)
		f.Suggestion = "比较执行器与事件；重复出现是调查信号，不代表具有同一根因。"
	case "observation.end_missing":
		f.Observed = fmt.Sprintf("%d 个已结束的 LLM 任务缺少当前 attempt 的结束摘要", count)
		f.Suggestion = "该部分用量与结束原因覆盖不完整；不能把缺失记录计为零。"
	case "execution.tool_error":
		f.Observed = fmt.Sprintf("所选范围记录了 %d 次工具错误", count)
		f.Suggestion = "证据按任务与 attempt 聚合；工具错误可能已在后续自纠，最终结果以任务状态为准。"
	default:
		return Finding{Code: "unknown", Count: count, Severity: "info", Certainty: "unknown"}
	}
	return f
}
