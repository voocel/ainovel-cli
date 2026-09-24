package capability

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
)

func (r *Runtime) toolsFor(
	operation model.Operation,
	compiled prompt.Compiled,
	submit func(model.Proposal) (model.Proposal, error),
	submitVerdict func(model.ReviewVerdict) (model.ReviewVerdict, error),
) ([]agentcore.Tool, error) {
	tools := make([]agentcore.Tool, 0, len(compiled.Tools))
	for _, definition := range compiled.Tools {
		var schema map[string]any
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("decode tool schema %q: %w", definition.Name, err)
		}
		execute, err := r.toolExecutor(operation, compiled.WorkerProfile, definition.Name, submit, submitVerdict)
		if err != nil {
			return nil, err
		}
		tools = append(tools, agentcore.NewFuncTool(definition.Name, definition.Description, schema, execute))
	}
	return tools, nil
}

// toolExecutor 构造单个工具；worker 是提交提案时署名的 Worker Profile 标识。
func (r *Runtime) toolExecutor(
	operation model.Operation,
	worker, name string,
	submit func(model.Proposal) (model.Proposal, error),
	submitVerdict func(model.ReviewVerdict) (model.ReviewVerdict, error),
) (func(context.Context, json.RawMessage) (json.RawMessage, error), error) {
	switch name {
	case prompt.ToolAuthorityRead:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Kind     model.DocumentKind `json:"kind"`
				ID       string             `json:"id"`
				Revision model.Revision     `json:"revision"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			if args.Revision <= model.InitialRevision || args.Revision > operation.Snapshot.BaseRevision {
				return nil, fmt.Errorf("read revision %d is outside operation snapshot %d: %w", args.Revision, operation.Snapshot.BaseRevision, model.ErrInvalid)
			}
			ref := model.DocumentRef{Kind: args.Kind, ID: args.ID}
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
			return marshalWorkspaceArtifact(artifact)
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
			views := make([]workspaceArtifactView, 0, len(artifacts))
			for _, artifact := range artifacts {
				views = append(views, workspaceArtifactView{WorkspaceArtifact: artifact, Content: nil})
			}
			return json.Marshal(views)
		}, nil
	case prompt.ToolWorkspacePutChapter:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key     string                  `json:"key"`
				Chapter model.ManuscriptChapter `json:"chapter"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			artifact, err := r.workspace.PutChapter(ctx, operation.ID, args.Key, args.Chapter, nil, operation.Attempt, r.now())
			if err != nil {
				return nil, err
			}
			return marshalWorkspaceArtifact(artifact)
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
				ctx, operation.ID, args.Key, args.BlockID, args.Text, args.ExpectedVersion, operation.Attempt, r.now(),
			)
			if err != nil {
				return nil, err
			}
			return marshalWorkspaceArtifact(artifact)
		}, nil
	case prompt.ToolWorkspacePutCandidate:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key     string          `json:"key"`
				Content json.RawMessage `json:"content"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			artifact, err := r.store.PutWorkspaceArtifact(ctx, model.WorkspaceArtifact{
				OperationID: operation.ID, Key: args.Key, MediaType: "application/json",
				Content: args.Content, UpdatedAt: r.now(),
			}, nil, operation.Attempt)
			if err != nil {
				return nil, err
			}
			return marshalWorkspaceArtifact(artifact)
		}, nil
	case prompt.ToolWorkspacePutReview:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key      string                `json:"key"`
				Findings []model.ReviewFinding `json:"findings"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			content, err := json.Marshal(args.Findings)
			if err != nil {
				return nil, fmt.Errorf("encode review findings: %w", err)
			}
			artifact, err := r.store.PutWorkspaceArtifact(ctx, model.WorkspaceArtifact{
				OperationID: operation.ID, Key: args.Key,
				MediaType: model.ReviewArtifactMediaType, Content: content, UpdatedAt: r.now(),
			}, nil, operation.Attempt)
			if err != nil {
				return nil, err
			}
			return marshalWorkspaceArtifact(artifact)
		}, nil
	case prompt.ToolProposalSubmit:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Reason            string           `json:"reason"`
				Patches           []model.Patch    `json:"patches"`
				ConfirmCanon      []string         `json:"confirm_canon"`
				WorkspaceKey      string           `json:"workspace_key"`
				WorkspaceKeys     []string         `json:"workspace_keys"`
				WorkspaceVersion  *int64           `json:"workspace_version"`
				WorkspaceVersions map[string]int64 `json:"workspace_versions"`
				ReviewKey         string           `json:"review_key"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			if args.WorkspaceKey != "" && len(args.WorkspaceKeys) > 0 {
				return nil, fmt.Errorf("workspace_key and workspace_keys are mutually exclusive; single-chapter tasks use workspace_key only: %w", model.ErrInvalid)
			}
			workspaceKeys := args.WorkspaceKeys
			if args.WorkspaceKey != "" {
				workspaceKeys = append(workspaceKeys, args.WorkspaceKey)
			}
			versions := args.WorkspaceVersions
			if args.WorkspaceVersion != nil {
				if args.WorkspaceKey == "" || versions != nil {
					return nil, fmt.Errorf("workspace_version requires workspace_key and cannot be combined with workspace_versions: %w", model.ErrInvalid)
				}
				versions = map[string]int64{args.WorkspaceKey: *args.WorkspaceVersion}
			}
			if versions != nil {
				var err error
				args.Patches, err = r.materializeWorkspaceChapters(ctx, operation, workspaceKeys, versions, args.Patches)
				if err != nil {
					return nil, err
				}
			}
			patches, err := r.materializeCanon(ctx, operation, args.Patches, args.ConfirmCanon)
			if err != nil {
				return nil, err
			}
			args.Patches = patches
			if err := r.validateSubmissionArtifact(ctx, operation, workspaceKeys, args.ReviewKey, args.Patches); err != nil {
				return nil, err
			}
			if err := r.validatePlanTarget(ctx, operation, args.Patches); err != nil {
				return nil, err
			}
			proposal := model.Proposal{
				ID: operation.ID + "-proposal", OperationID: operation.ID,
				Target: operation.Target, BaseRevision: operation.Snapshot.BaseRevision,
				Author: model.Author{Kind: model.AuthorAI, ID: worker},
				Reason: args.Reason, Patches: args.Patches,
				ApprovalState: model.ApprovalPending, CreatedAt: r.now(),
			}
			if err := proposal.Validate(); err != nil {
				return nil, err
			}
			// 确定性结构校验（事实身份、old_value、依赖、D41）在工具边界当场反馈，收尾的
			// PrepareExecution 只兜底：否则模型看到"提交成功"，任务却在收尾失败且无从自纠。
			if err := r.changes.Validate(ctx, proposal); err != nil {
				return nil, err
			}
			proposal, err = submit(proposal)
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
				Status     string                        `json:"status"`
				ReviewKey  string                        `json:"review_key"`
				Intent     *model.IntentVerification     `json:"intent"`
				Directives []model.DirectiveVerification `json:"directives"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			// 宿主已知的事实由宿主盖入，不让模型复述再校验（D60）：Revision 与证据基线取自
			// 启动快照和任务输入，章节范围就是任务请求的范围，发现取自工作区审阅记录的当前版本。
			input, err := model.TaskInputAs[model.ReviewRangeInput](operation)
			if err != nil {
				return nil, err
			}
			findings, err := r.reviewFindings(ctx, operation, args.ReviewKey)
			if err != nil {
				return nil, err
			}
			verdict := model.ReviewVerdict{
				Status: args.Status, Revision: operation.Snapshot.BaseRevision,
				ChapterIDs: input.ChapterIDs, ReviewKey: args.ReviewKey, Basis: input.Basis.Normalize(),
				Intent: args.Intent, Directives: args.Directives, Findings: findings,
			}
			if err := model.ValidateReviewVerdictForOperation(operation, verdict); err != nil {
				return nil, err
			}
			verdict, err = submitVerdict(verdict)
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				Verdict model.ReviewVerdict `json:"verdict"`
			}{Verdict: verdict})
		}, nil
	default:
		return nil, fmt.Errorf("worker tool %q has no host implementation: %w", name, model.ErrInvalid)
	}
}

// reviewFindings 读取审阅记录当前版本的发现。同一执行内工作区只有本执行串行写入，
// 当前版本就是模型刚写完的那份；提交后被改由收尾时的证据检查拒绝。
func (r *Runtime) reviewFindings(ctx context.Context, operation model.Operation, key string) ([]model.ReviewFinding, error) {
	artifact, err := r.store.GetWorkspaceArtifact(ctx, operation.ID, key)
	if err != nil {
		return nil, fmt.Errorf("read review record %q (write it with workspace_put_review first): %w", key, err)
	}
	if artifact.MediaType != model.ReviewArtifactMediaType {
		return nil, fmt.Errorf("workspace artifact %q is not a review record: %w", key, model.ErrInvalid)
	}
	var findings []model.ReviewFinding
	if err := decodeToolArgs(artifact.Content, &findings); err != nil {
		return nil, fmt.Errorf("decode review record %q: %w", key, err)
	}
	return findings, nil
}

func decodeToolArgs(raw json.RawMessage, target any) error {
	if err := model.DecodeStrict(raw, target); err != nil {
		return fmt.Errorf("decode tool arguments: %w", err)
	}
	return nil
}

// The model sees JSON content; the storage model retains its byte-oriented contract.
type workspaceArtifactView struct {
	model.WorkspaceArtifact
	Content json.RawMessage `json:"content,omitempty"`
}

func marshalWorkspaceArtifact(artifact model.WorkspaceArtifact) (json.RawMessage, error) {
	return json.Marshal(workspaceArtifactView{WorkspaceArtifact: artifact, Content: json.RawMessage(artifact.Content)})
}
