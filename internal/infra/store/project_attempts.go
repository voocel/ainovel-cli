package store

import (
	"context"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// AttemptUsage 是一本书里一次执行尝试的落盘汇总（工作台「创作团队」）：用量是这次尝试各条
// 执行记录自带用量之和（模型消息与合规核对），起止是它第一条与最后一条执行记录。
type AttemptUsage struct {
	OperationID string
	Attempt     int
	State       model.OperationState // 任务此刻的状态
	Live        bool                 // 正在执行：任务此刻的尝试，租约未过期（进程中断留下的不算）
	Role        string               // 任务的角色，取自它的 agent.run_started
	Model       string               // 这次尝试 Agent 实际用的模型；只做合规核对的尝试为空
	Usage       model.Usage
	StartedAt   time.Time
	LastAt      time.Time
}

// ProjectAttempts 按开始先后列出一本书全部执行尝试的用量与起止。主查询只读事件的覆盖
// 索引；角色与模型取自 agent.run_started 的载荷，按组各查一次。崩溃恢复后裁决已保存提案
// 的尝试只有合规核对、没有 agent.run_started，角色因此按任务取。
func (s *Store) ProjectAttempts(ctx context.Context, projectID string, now time.Time) ([]AttemptUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.operation_id, e.attempt, o.state,
			e.attempt = o.attempt AND o.state = 'running' AND o.lease_until_unix_ms > ?,
			COALESCE((SELECT json_extract(CAST(r.payload AS TEXT), '$.role') FROM operation_events r
				WHERE r.operation_id = e.operation_id AND r.kind = 'agent.run_started' LIMIT 1), ''),
			COALESCE((SELECT json_extract(CAST(r.payload AS TEXT), '$.model') FROM operation_events r
				WHERE r.operation_id = e.operation_id AND r.attempt = e.attempt AND r.kind = 'agent.run_started'), ''),
			SUM(e.input_tokens), SUM(e.output_tokens), SUM(e.cache_read_tokens), SUM(e.cost),
			MIN(e.created_at_unix_ms), MAX(e.created_at_unix_ms)
		FROM operations o JOIN operation_events e ON e.operation_id = o.id
		WHERE o.target_kind = ? AND o.target_id = ? AND e.kind IN ('agent.run_started', 'agent.message_committed', 'agent.run_ended',
			'semantic.compliance_checked', 'semantic.compliance_failed')
		GROUP BY e.operation_id, e.attempt
		ORDER BY MIN(e.created_at_unix_ms), e.operation_id, e.attempt`, now.UnixMilli(), model.AuthorityProject, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project attempts: %w", err)
	}
	defer rows.Close()
	var attempts []AttemptUsage
	for rows.Next() {
		var a AttemptUsage
		var state string
		var started, last int64
		if err := rows.Scan(&a.OperationID, &a.Attempt, &state, &a.Live, &a.Role, &a.Model,
			&a.Usage.Input, &a.Usage.Output, &a.Usage.CacheRead, &a.Usage.Cost, &started, &last); err != nil {
			return nil, fmt.Errorf("scan project attempt: %w", err)
		}
		a.State = model.OperationState(state)
		a.StartedAt, a.LastAt = time.UnixMilli(started).UTC(), time.UnixMilli(last).UTC()
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project attempts: %w", err)
	}
	return attempts, nil
}
