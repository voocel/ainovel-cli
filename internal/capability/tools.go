package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func (r *Runtime) toolsFor(
	operation domain.Operation,
	schemas []prompt.ToolSchema,
	submit func(domain.Proposal) (domain.Proposal, error),
	submitVerdict func(domain.ReviewVerdict) (domain.ReviewVerdict, error),
) ([]agentcore.Tool, error) {
	tools := make([]agentcore.Tool, 0, len(schemas))
	for _, definition := range schemas {
		var schema map[string]any
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("decode tool schema %q: %w", definition.Name, err)
		}
		execute, err := r.toolExecutor(operation, definition.Name, submit, submitVerdict)
		if err != nil {
			return nil, err
		}
		tools = append(tools, agentcore.NewFuncTool(definition.Name, definition.Description, schema, execute))
	}
	return tools, nil
}

func (r *Runtime) toolExecutor(
	operation domain.Operation,
	name string,
	submit func(domain.Proposal) (domain.Proposal, error),
	submitVerdict func(domain.ReviewVerdict) (domain.ReviewVerdict, error),
) (func(context.Context, json.RawMessage) (json.RawMessage, error), error) {
	switch name {
	case prompt.ToolAuthorityRead:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Kind     domain.DocumentKind `json:"kind"`
				ID       string              `json:"id"`
				Revision domain.Revision     `json:"revision"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			if args.Revision <= domain.InitialRevision || args.Revision > operation.Snapshot.BaseRevision {
				return nil, fmt.Errorf("read revision %d is outside operation snapshot %d: %w", args.Revision, operation.Snapshot.BaseRevision, domain.ErrInvalid)
			}
			ref := domain.DocumentRef{Kind: args.Kind, ID: args.ID}
			document, err := r.store.GetDocument(ctx, operation.Target, ref, args.Revision)
			if err != nil {
				return nil, err
			}
			return json.Marshal(document)
		}, nil
	case prompt.ToolWorkspaceRead:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key string `json:"key"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			artifact, err := r.store.GetWorkspaceArtifact(ctx, operation.ID, args.Key)
			if err != nil {
				return nil, err
			}
			return json.Marshal(artifact)
		}, nil
	case prompt.ToolWorkspaceList:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct{}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			artifacts, err := r.store.ListWorkspaceArtifacts(ctx, operation.ID)
			if err != nil {
				return nil, err
			}
			return json.Marshal(artifacts)
		}, nil
	case prompt.ToolWorkspacePutChapter:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key             string                   `json:"key"`
				ExpectedVersion int64                    `json:"expected_version"`
				Chapter         domain.ManuscriptChapter `json:"chapter"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			artifact, err := r.workspace.PutChapter(ctx, operation.ID, args.Key, args.Chapter, args.ExpectedVersion, r.now())
			if err != nil {
				return nil, err
			}
			return json.Marshal(artifact)
		}, nil
	case prompt.ToolWorkspaceReplaceBlock:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key             string `json:"key"`
				BlockID         string `json:"block_id"`
				Text            string `json:"text"`
				ExpectedVersion int64  `json:"expected_version"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			artifact, err := r.workspace.ReplaceChapterBlock(
				ctx, operation.ID, args.Key, args.BlockID, args.Text, args.ExpectedVersion, r.now(),
			)
			if err != nil {
				return nil, err
			}
			return json.Marshal(artifact)
		}, nil
	case prompt.ToolWorkspacePutCandidate:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key             string          `json:"key"`
				ExpectedVersion int64           `json:"expected_version"`
				Content         json.RawMessage `json:"content"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			artifact, err := r.store.PutWorkspaceArtifact(ctx, domain.WorkspaceArtifact{
				OperationID: operation.ID, Key: args.Key, MediaType: "application/json",
				Content: args.Content, UpdatedAt: r.now(),
			}, args.ExpectedVersion)
			if err != nil {
				return nil, err
			}
			return json.Marshal(artifact)
		}, nil
	case prompt.ToolWorkspacePutReview:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key             string                 `json:"key"`
				ExpectedVersion int64                  `json:"expected_version"`
				Findings        []domain.ReviewFinding `json:"findings"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			content, err := json.Marshal(args.Findings)
			if err != nil {
				return nil, fmt.Errorf("encode review findings: %w", err)
			}
			artifact, err := r.store.PutWorkspaceArtifact(ctx, domain.WorkspaceArtifact{
				OperationID: operation.ID, Key: args.Key,
				MediaType: domain.ReviewArtifactMediaType, Content: content, UpdatedAt: r.now(),
			}, args.ExpectedVersion)
			if err != nil {
				return nil, err
			}
			return json.Marshal(artifact)
		}, nil
	case prompt.ToolProposalSubmit:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Reason        string         `json:"reason"`
				Patches       []domain.Patch `json:"patches"`
				WorkspaceKey  string         `json:"workspace_key"`
				WorkspaceKeys []string       `json:"workspace_keys"`
				ReviewKey     string         `json:"review_key"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			workspaceKeys := args.WorkspaceKeys
			if args.WorkspaceKey != "" {
				workspaceKeys = append(workspaceKeys, args.WorkspaceKey)
			}
			if err := r.validateSubmissionArtifact(ctx, operation, workspaceKeys, args.ReviewKey, args.Patches); err != nil {
				return nil, err
			}
			if err := r.validatePlanTarget(ctx, operation, args.Patches); err != nil {
				return nil, err
			}
			proposal := domain.Proposal{
				ID: operation.ID + "-proposal", OperationID: operation.ID,
				Target: operation.Target, BaseRevision: operation.Snapshot.BaseRevision,
				Author: domain.Author{Kind: domain.AuthorAI, ID: operation.Snapshot.WorkerProfileVersion},
				Reason: args.Reason, Patches: args.Patches,
				ApprovalState: domain.ApprovalPending, CreatedAt: r.now(),
			}
			if err := proposal.Validate(); err != nil {
				return nil, err
			}
			proposal, err := submit(proposal)
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				ProposalID string `json:"proposal_id"`
			}{ProposalID: proposal.ID})
		}, nil
	case prompt.ToolVerdictSubmit:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Status     string                         `json:"status"`
				ChapterIDs []string                       `json:"chapter_ids"`
				ReviewKey  string                         `json:"review_key"`
				Intent     *domain.IntentVerification     `json:"intent"`
				Directives []domain.DirectiveVerification `json:"directives"`
				Findings   []domain.ReviewFinding         `json:"findings"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			// 裁定的 Revision 由宿主绑定启动快照，模型不能宣称基线；范围必须
			// 与任务请求完全一致，防止漏审或越界裁定。
			verdict := domain.ReviewVerdict{
				Status: args.Status, Revision: operation.Snapshot.BaseRevision,
				ChapterIDs: args.ChapterIDs, ReviewKey: args.ReviewKey,
				Intent: args.Intent, Directives: args.Directives, Findings: args.Findings,
			}
			if err := domain.ValidateReviewVerdictForOperation(operation, verdict); err != nil {
				return nil, err
			}
			if err := r.validateReviewArtifact(ctx, operation, verdict); err != nil {
				return nil, err
			}
			verdict, err := submitVerdict(verdict)
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				Verdict domain.ReviewVerdict `json:"verdict"`
			}{Verdict: verdict})
		}, nil
	default:
		return nil, fmt.Errorf("worker tool %q has no host implementation: %w", name, domain.ErrInvalid)
	}
}

func (r *Runtime) validateReviewArtifact(
	ctx context.Context,
	operation domain.Operation,
	verdict domain.ReviewVerdict,
) error {
	artifact, err := r.store.GetWorkspaceArtifact(ctx, operation.ID, verdict.ReviewKey)
	if err != nil {
		return fmt.Errorf("read verdict review artifact %q: %w", verdict.ReviewKey, err)
	}
	if artifact.MediaType != domain.ReviewArtifactMediaType {
		return fmt.Errorf("workspace artifact %q is not a review record: %w", verdict.ReviewKey, domain.ErrInvalid)
	}
	var findings []domain.ReviewFinding
	if err := decodeToolArgs(artifact.Content, &findings); err != nil {
		return fmt.Errorf("decode verdict review artifact %q: %w", verdict.ReviewKey, err)
	}
	stored, err := json.Marshal(findings)
	if err != nil {
		return fmt.Errorf("encode stored review findings: %w", err)
	}
	submitted, err := json.Marshal(verdict.Findings)
	if err != nil {
		return fmt.Errorf("encode submitted review findings: %w", err)
	}
	if !bytes.Equal(stored, submitted) {
		return fmt.Errorf("verdict findings do not match review artifact %q: %w", verdict.ReviewKey, domain.ErrInvalid)
	}
	return nil
}

func decodeToolArgs(raw json.RawMessage, target any) error {
	if err := domain.DecodeStrict(raw, target); err != nil {
		return fmt.Errorf("decode tool arguments: %w", err)
	}
	return nil
}
