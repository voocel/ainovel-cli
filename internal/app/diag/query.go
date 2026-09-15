package diag

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

type Query struct {
	store         *store.Store
	version       string
	now           func() time.Time
	databaseIssue string
}

// WithDatabaseIssue preserves a classified startup failure without its private message.
func (q *Query) WithDatabaseIssue(code string) *Query {
	copy := *q
	copy.databaseIssue = databaseIssue(code)
	return &copy
}

func databaseIssue(code string) string {
	return allowed(code, "", "missing", "permission", "corrupt", "unsupported_schema", "open_failed", "path_unavailable")
}

func New(s *store.Store, version string, now func() time.Time) *Query {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if version == "" {
		version = "unknown"
	}
	return &Query{store: s, version: version, now: now}
}

func (q *Query) Inspect(ctx context.Context, request Request) (Report, error) {
	return q.inspect(ctx, request, false)
}

func (q *Query) inspect(ctx context.Context, request Request, share bool) (Report, error) {
	r := Report{
		Header:   Header{FormatVersion: FormatVersion, RulesVersion: RulesVersion, BuildVersion: q.version, Platform: runtime.GOOS + "/" + runtime.GOARCH, CapturedAt: q.now(), Scope: request},
		Findings: []Finding{}, Coverage: []Coverage{}, Operations: []Operation{}, Events: []Event{},
		Metrics: Metrics{States: map[model.OperationState]int{}},
	}
	r.Header.DatabaseIssue = q.databaseIssue
	r.Header.Selection = "paged_details"
	if share {
		r.Header.Selection = "issue_context_and_stream_tails"
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if request.EventAfter < 0 || (request.EventAfter != 0 && request.OperationID == "") || (request.After != "" && request.OperationID != "") {
		return r, fmt.Errorf("invalid diagnostic cursor: %w", model.ErrInvalid)
	}
	if request.ProjectID == "" {
		if request.RunID != "" || request.OperationID != "" || request.After != "" {
			return r, fmt.Errorf("diagnostic scope requires project: %w", model.ErrInvalid)
		}
		r.Summary.State = "environment"
		r.Header.Selection = "environment"
		r.Coverage = append(r.Coverage, Coverage{"environment", "partial", "仅采集构建版本和平台；未检查模型连通性或读取配置内容。"})
		status := "unavailable"
		if q.store != nil {
			status = "not_collected"
		}
		r.Coverage = append(r.Coverage, Coverage{"database", status, "未选择作品，不生成运行健康结论。"})
		return r, nil
	}
	if q.store == nil {
		return r, fmt.Errorf("diagnostic database is unavailable")
	}
	if share {
		// Validate first; a browsing cursor never selects the support evidence.
		request.After, request.EventAfter = "", 0
		r.Header.Scope = request
	}
	s, err := q.store.ReadDiagSnapshot(ctx, store.DiagRequest{ProjectID: request.ProjectID, RunID: request.RunID, OperationID: request.OperationID, AfterOperationID: request.After, AfterEventSequence: request.EventAfter, ForShare: share})
	if err != nil {
		return r, err
	}
	r.Header.CapturedAt, r.Header.Revision, r.Header.SchemaVersion = s.CapturedAt, s.Revision, s.SchemaVersion
	r.Header.RunEventBoundary = s.RunEventBoundary
	r.Summary.State = "no_run"
	if s.Run != nil {
		r.Header.Scope.RunID = s.Run.ID
		r.Summary.State, r.Summary.Reason = string(s.Run.State), s.Run.StateReason
		r.Summary.CreatedAt, r.Summary.UpdatedAt = &s.Run.CreatedAt, &s.Run.UpdatedAt
	}
	r.Summary.LastEventAt = s.LastEventAt
	r.Metrics = Metrics{Operations: s.OperationCount, States: s.Counts, Attempts: s.AttemptCount, RetriedOperations: s.RetriedOperationCount, Events: s.EventCount}
	r.Findings = findings(s.Counts[model.OperationFailed], s.ExpiredLeaseCount, s.ResultUnknownCount, s.MissingCompletionEventCount)
	r.NextOperationID, r.NextEventSequence = s.NextOperationID, s.NextEventSequence
	for _, op := range s.Operations {
		r.Operations = append(r.Operations, Operation{ID: op.ID, RunID: op.RunID, Kind: op.Kind, State: op.State, Attempt: op.Attempt, FailureCode: op.FailureCode, Error: op.Error, Executor: op.Executor, ConfigDigest: op.ConfigDigest, LeaseUntil: op.LeaseUntil, CreatedAt: op.CreatedAt, UpdatedAt: op.UpdatedAt, EventCount: op.EventCount, EventBoundary: op.EventBoundary, HasCompletionEvent: op.HasCompletionEvent})
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		for _, op := range r.Operations {
			source := ""
			switch f.Code {
			case "execution.failed":
				if op.State == model.OperationFailed {
					source = "operation.state"
				}
			case "execution.lease_expired":
				if op.State == model.OperationRunning && op.LeaseUntil != nil && !op.LeaseUntil.After(r.Header.CapturedAt) {
					source = "operation.lease"
				}
			case "execution.result_unknown", "execution.repeated_unknown_result":
				if op.FailureCode == model.FailureResultUnknown {
					source = "operation.failure_code"
				}
			case "observation.end_missing":
				if strings.HasPrefix(op.Executor, "llm.agent@") && !op.HasCompletionEvent && op.Attempt > 0 && (op.State == model.OperationSucceeded || op.State == model.OperationFailed || op.State == model.OperationCancelled || op.State == model.OperationStale) {
					source = "operation.event_boundary"
				}
			}
			if source != "" {
				f.Evidence = append(f.Evidence, Evidence{source, op.ID, op.Attempt, op.EventBoundary})
			}
		}
		if len(f.Evidence) < f.Count {
			r.Coverage = append(r.Coverage, Coverage{"findings", "partial", fmt.Sprintf("%s：本页附 %d/%d 个任务的证据引用，其余任务需翻页查看。", f.Code, len(f.Evidence), f.Count)})
		}
	}
	toolErrors := 0
	var toolEvidence []Evidence
	for _, event := range s.Events {
		e := Event{OperationID: event.OperationID, Sequence: event.Sequence, Attempt: event.Attempt, Kind: event.Kind, CreatedAt: event.CreatedAt, PayloadTruncated: event.PayloadTruncated}
		if request.OperationID != "" && !share {
			e.Text = string(event.Payload)
		}
		if len(event.Payload) > 0 || event.PayloadTruncated {
			if event.PayloadTruncated {
				r.Coverage = append(r.Coverage, Coverage{"events", "truncated", fmt.Sprintf("事件 #%d 的本地内容超过读取预算，仅展示前缀。", event.Sequence)})
			} else if event.Kind == "agent.run_ended" {
				e.Usage, err = decodeUsage(event.Payload)
				if err != nil {
					r.Coverage = append(r.Coverage, Coverage{"usage", "partial", fmt.Sprintf("事件 #%d 的用量摘要缺失或格式不支持。", event.Sequence)})
				}
			} else if event.Kind == "agent.message_committed" {
				var message struct {
					Role     string `json:"role"`
					Metadata struct {
						IsError bool `json:"is_error"`
					} `json:"metadata"`
				}
				if err := json.Unmarshal(event.Payload, &message); err != nil {
					r.Coverage = append(r.Coverage, Coverage{"tool_errors", "partial", fmt.Sprintf("事件 #%d 的消息格式无法解析。", event.Sequence)})
				} else if message.Role == "tool" && message.Metadata.IsError {
					toolErrors++
					toolEvidence = append(toolEvidence, Evidence{"event.tool_error", event.OperationID, event.Attempt, event.Sequence})
				}
			}
		}
		r.Events = append(r.Events, e)
	}
	if toolErrors > 0 {
		f := finding("execution.tool_error", toolErrors)
		f.OperationID = request.OperationID
		f.Evidence = toolEvidence
		r.Findings = append(r.Findings, f)
	}
	r.Coverage = append(r.Coverage, Coverage{"database", "complete", "运行、任务状态与事件截止序号来自同一次只读事务；每次翻页重新采集。"})
	r.Coverage = append(r.Coverage, Coverage{"statistics", "complete", "任务状态、attempt 和事件计数覆盖所选整个运行或任务，不受明细窗口影响。"})
	status := "complete"
	if len(r.Operations) < r.Metrics.Operations {
		status = "truncated"
	}
	detail := "本地任务明细每页最多 50 条；原始错误仅在选定任务后读取。"
	if share {
		detail = "按异常、运行中/重试、最近更新选取最多 200 个任务的上下文，与界面页码无关。"
	}
	r.Coverage = append(r.Coverage, Coverage{"operations", status, detail})
	status = "complete"
	if len(r.Events) < r.Metrics.Events {
		status = "truncated"
	}
	detail = "任务事件按序号分页；时间不表示跨任务因果关系。"
	if request.OperationID == "" || share {
		detail = "为选定任务分配尾部事件窗口，合计最多 200 条；最近事件时间取各流末条记录，不对全历史按时间排序。"
	}
	r.Coverage = append(r.Coverage, Coverage{"events", status, detail})
	r.Coverage = append(r.Coverage,
		Coverage{"tool_errors", "partial", "仅分析选定任务当前事件页中的工具错误；未读取、过大或旧格式消息不计为零。"},
		Coverage{"usage", "partial", "仅展示已读取的 agent.run_ended attempt 结束增量；恢复消息不重复累计，不计算不完整历史的整轮总量。"},
		Coverage{"models", "not_collected", "未采集实际模型；配置摘要仅表明执行配置是否相同。"},
		Coverage{"content_commits", "not_collected", "未查询内容提交时间；最近事件时间不等于最后创作进展。"})
	return r, nil
}
