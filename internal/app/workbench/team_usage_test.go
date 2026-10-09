package workbench

import (
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// 用时：正在执行的到此刻，其余到最后一条记录；同一角色后开始的尝试决定它的状态，只做
// 合规核对的尝试不换它的模型。
func TestTeamUsageSumsRolesAndTime(t *testing.T) {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Minute)
	usage := teamUsage([]store.AttemptUsage{
		{OperationID: "plan", Attempt: 1, Role: "architect", Model: "pro", State: model.OperationSucceeded, Current: true,
			Usage: model.Usage{Input: 100, Output: 10, CacheRead: 40, Cost: 0.2}, StartedAt: start, LastAt: start.Add(time.Minute)},
		{OperationID: "write", Attempt: 1, Role: "writer", Model: "flash", State: model.OperationRunning,
			Usage: model.Usage{Input: 50, Output: 5}, StartedAt: start.Add(2 * time.Minute), LastAt: start.Add(4 * time.Minute)},
		{OperationID: "write", Attempt: 2, Role: "writer", State: model.OperationRunning, Current: true,
			Usage: model.Usage{Input: 30, Output: 3}, StartedAt: start.Add(8 * time.Minute), LastAt: start.Add(9 * time.Minute)},
	}, now)
	if usage.Total != (model.Usage{Input: 180, Output: 18, CacheRead: 40, Cost: 0.2}) || usage.Worked != 5*time.Minute {
		t.Fatalf("total = %+v worked = %s", usage.Total, usage.Worked)
	}
	writer := usage.Roles["writer"]
	if writer.Usage != (model.Usage{Input: 80, Output: 8}) || writer.Worked != 4*time.Minute || writer.Model != "flash" || writer.State != model.OperationRunning {
		t.Fatalf("writer = %+v", writer)
	}
	if architect := usage.Roles["architect"]; architect.Worked != time.Minute || architect.Model != "pro" || len(usage.Roles) != 2 {
		t.Fatalf("roles = %+v", usage.Roles)
	}
}
