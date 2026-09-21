package operation

import (
	"context"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// Store 定义任务执行需要的持久化契约；领取、续租及执行写入遵循同一归属围栏。
// 权威读写经注入的 change.Engine 完成，这里只列执行引擎自己调用的方法。
type Store interface {
	ClaimNextOperationForExecutor(
		ctx context.Context,
		workerID, executor string,
		leaseDuration time.Duration,
		now time.Time,
	) (model.Operation, error)
	ClaimNextOperationForExecutors(ctx context.Context, workerID string, executors []string, leaseDuration time.Duration, now time.Time) (model.Operation, error)
	ClaimOperationForExecutor(
		ctx context.Context,
		id, workerID, executor string,
		leaseDuration time.Duration,
		now time.Time,
	) (model.Operation, error)
	ConcludeOperation(ctx context.Context, id string, attempt int, to model.OperationState, message string, now time.Time) (model.Operation, error)
	CurrentRevision(ctx context.Context, target model.AuthorityTarget) (model.Revision, error)
	FailOperation(ctx context.Context, id string, attempt int, code model.FailureCode, message string, now time.Time) (model.Operation, error)
	GetChangeSet(ctx context.Context, id string) (model.ChangeSet, error)
	GetDerivedDocument(
		ctx context.Context,
		projectID string,
		revision model.Revision,
		kind, key string,
	) (model.DerivedDocument, error)
	GetProposalByOperation(ctx context.Context, operationID string) (model.Proposal, error)
	GetWorkspaceArtifact(ctx context.Context, operationID, key string) (model.WorkspaceArtifact, error)
	ListPlanNodes(ctx context.Context, target model.AuthorityTarget, at model.Revision) ([]model.PlanNode, error)
	RelocateProposal(ctx context.Context, proposal model.Proposal, attempt int, now time.Time) error
	RenewOperationLease(ctx context.Context, id, workerID string, attempt int, leaseDuration time.Duration, now time.Time) (model.Operation, error)
	SaveExecutionArtifacts(ctx context.Context, artifacts []model.Artifact, operationID string, attempt int) error
	SaveExecutionDerivedDocument(
		ctx context.Context, document model.DerivedDocument, operationID string, attempt int,
	) (model.DerivedDocument, error)
}
