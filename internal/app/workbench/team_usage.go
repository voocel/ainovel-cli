package workbench

import (
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// TeamUsage 是创作团队在这本书上的累计用量与用时，从落盘的执行记录汇总：重启程序、
// 换窗口看到的都一样。用时算到快照时刻：进行中的尝试计到此刻，其余计到最后一条记录。
type TeamUsage struct {
	Total  model.Usage          `json:"total"`
	Worked time.Duration        `json:"worked"`
	Roles  map[string]RoleUsage `json:"roles,omitempty"` // 键是 Worker 的模型角色
}

// RoleUsage 是一个角色名下全部执行尝试的累计：Model 是它最近一次实际用的模型，State 是
// 它最近一项任务此刻的状态。任务里的语义合规核对计入该任务的角色。
type RoleUsage struct {
	Usage  model.Usage          `json:"usage"`
	Worked time.Duration        `json:"worked"`
	Model  string               `json:"model"`
	State  model.OperationState `json:"state"`
}

// teamUsage 按开始先后汇总各次尝试；同一角色后开始的尝试决定它的状态，有 Agent 运行的
// 尝试决定它的模型（合规核对用的是默认绑定，不代表角色）。
func teamUsage(attempts []store.AttemptUsage, now time.Time) TeamUsage {
	usage := TeamUsage{Roles: make(map[string]RoleUsage)}
	for _, attempt := range attempts {
		worked := attemptTime(attempt, now)
		usage.Total.Add(attempt.Usage)
		usage.Worked += worked
		role := usage.Roles[attempt.Role]
		role.Usage.Add(attempt.Usage)
		role.Worked += worked
		role.State = attempt.State
		if attempt.Model != "" {
			role.Model = attempt.Model
		}
		usage.Roles[attempt.Role] = role
	}
	return usage
}

// attemptTime 一次尝试的执行时长：正在执行的到此刻，其余到它最后一条执行记录（收尾、
// 合规核对，或进程中断前的最后一条消息——中断的尝试租约一过期就不再算到此刻）。
func attemptTime(attempt store.AttemptUsage, now time.Time) time.Duration {
	end := attempt.LastAt
	if attempt.Live {
		end = now
	}
	return end.Sub(attempt.StartedAt)
}
