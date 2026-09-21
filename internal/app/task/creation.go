package task

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// DefaultLease 是 Worker 租约的默认时长：心跳按其三分之一续租，进程退出后最多这么久
// 被回收。入口不传时统一取这里，不各自硬编码。
const DefaultLease = time.Minute

// ForCreation binds application execution settings without exposing their shape
// to the goal coordinator. A new binding is made for each drive invocation.
func (s *Manager) ForCreation(base StartOperationCommand) creation.Tasks {
	return creationTasks{manager: s, base: base}
}

type creationTasks struct {
	manager *Manager
	base    StartOperationCommand
}

func WorkCommand(base StartOperationCommand, work creation.WorkItem, at time.Time) (StartOperationCommand, error) {
	input, err := json.Marshal(work.Input)
	if err != nil {
		return StartOperationCommand{}, fmt.Errorf("encode %s task: %w", work.Kind, err)
	}
	base.OperationID, base.Kind, base.Input, base.CreatedAt = work.ID, work.Kind, input, at
	return base, nil
}

func (t creationTasks) Start(ctx context.Context, runID string, work creation.WorkItem, at time.Time) (model.Operation, error) {
	command, err := WorkCommand(t.base, work, at)
	if err != nil {
		return model.Operation{}, err
	}
	command.RunID = runID
	return t.manager.StartOperation(ctx, command)
}

func (t creationTasks) Restart(ctx context.Context, previousID, runID string, work creation.WorkItem, at time.Time) (model.Operation, error) {
	command, err := WorkCommand(t.base, work, at)
	if err != nil {
		return model.Operation{}, err
	}
	return t.manager.RestartOperation(ctx, RestartOperationCommand{
		FromOperationID: previousID, OperationID: work.ID, Input: command.Input,
		Packs: command.Packs, CreatorProfiles: command.CreatorProfiles,
		WorkerProfileID: command.WorkerProfileID, CoreProtocolVersion: command.CoreProtocolVersion,
		ConfigDigest: command.ConfigDigest, ApprovalPolicy: command.ApprovalPolicy,
		RunID: runID, CreatedAt: at,
	})
}

func (t creationTasks) Resume(ctx context.Context, id string, at time.Time) (model.Operation, error) {
	return t.manager.ResumeOperation(ctx, id, at)
}

func (t creationTasks) Run(ctx context.Context, id, workerID string, lease time.Duration, at time.Time) (model.Operation, error) {
	result, err := t.manager.RunOperation(ctx, id, workerID, lease, at)
	return result.Operation, err
}

func (t creationTasks) Failures(ctx context.Context, id string) (int, error) {
	return t.manager.store.CountOperationFailures(ctx, id)
}
