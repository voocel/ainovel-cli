package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/narrative"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/workspace"
	"github.com/voocel/litellm"
)

func (r *Runtime) toolsFor(
	operation model.Operation,
	compiled prompt.Compiled,
	submit func(model.Proposal) (model.Proposal, error),
	submitVerdict func(model.ReviewVerdict) (model.ReviewVerdict, error),
) ([]agentcore.Tool, error) {
	story := r.storyLoader(operation)
	tools := make([]agentcore.Tool, 0, len(compiled.Tools))
	for _, definition := range compiled.Tools {
		var schema map[string]any
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("decode tool schema %q: %w", definition.Name, err)
		}
		execute, err := r.toolExecutor(operation, compiled.WorkerProfile, definition.Name, story, submit, submitVerdict)
		if err != nil {
			return nil, err
		}
		// 提交成功即收尾：结果先由循环落盘，不再请求模型。
		terminate := definition.Name == prompt.ToolProposalSubmit || definition.Name == prompt.ToolVerdictSubmit
		tools = append(tools, agentcore.Tool{
			Name: definition.Name, Description: definition.Description, Schema: schema,
			Run: func(ctx context.Context, args json.RawMessage) (agentcore.Result, error) {
				result, err := execute(ctx, args)
				if err != nil {
					return agentcore.Result{}, err
				}
				return agentcore.Result{Content: []litellm.Block{litellm.Text(string(result))}, Terminate: terminate}, nil
			},
		})
	}
	return tools, nil
}

// storyLoader 按任务冻结基线懒加载故事索引（D66），同一执行内的工具共用一份；
// 加载失败不缓存，下次调用重试。
func (r *Runtime) storyLoader(operation model.Operation) func(context.Context) (*narrative.Story, error) {
	var mu sync.Mutex
	var cached *narrative.Story
	return func(ctx context.Context) (*narrative.Story, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != nil {
			return cached, nil
		}
		story, err := r.loadStory(ctx, operation)
		if err != nil {
			return nil, err
		}
		cached = story
		return story, nil
	}
}

// loadStory 读冻结基线，从不读最新 Revision：之后的用户修改不会成为 old_value 的来源。
func (r *Runtime) loadStory(ctx context.Context, operation model.Operation) (*narrative.Story, error) {
	var content narrative.Content
	if operation.Snapshot.BaseRevision <= model.InitialRevision {
		return narrative.New(content), nil
	}
	var err error
	if content.Plan, err = baseDocuments[model.PlanNode](ctx, r.store, operation, model.DocumentPlan); err != nil {
		return nil, err
	}
	if content.Entities, err = baseDocuments[model.Entity](ctx, r.store, operation, model.DocumentEntity); err != nil {
		return nil, err
	}
	if content.Canon, err = baseDocuments[model.CanonFact](ctx, r.store, operation, model.DocumentCanon); err != nil {
		return nil, err
	}
	if content.Manuscript, err = baseDocuments[model.ManuscriptChapter](ctx, r.store, operation, model.DocumentManuscript); err != nil {
		return nil, err
	}
	return narrative.New(content), nil
}

func baseDocuments[T any](ctx context.Context, store runtimeStore, operation model.Operation, kind model.DocumentKind) ([]T, error) {
	documents, err := store.ListDocuments(ctx, operation.Target, kind, operation.Snapshot.BaseRevision)
	if err != nil {
		return nil, fmt.Errorf("read %s baseline: %w", kind, err)
	}
	values := make([]T, 0, len(documents))
	for _, document := range documents {
		var value T
		if err := json.Unmarshal(document.Content, &value); err != nil {
			return nil, fmt.Errorf("decode %s: %w", document.Document.Key(), err)
		}
		values = append(values, value)
	}
	return values, nil
}

type toolFunc = func(context.Context, json.RawMessage) (json.RawMessage, error)

// toolExecutor 构造单个工具；worker 是提交提案时署名的 Worker Profile 标识。返回给模型的
// 错误一律把内部 ID 换成故事标签（D66）。
func (r *Runtime) toolExecutor(
	operation model.Operation,
	worker, name string,
	story func(context.Context) (*narrative.Story, error),
	submit func(model.Proposal) (model.Proposal, error),
	submitVerdict func(model.ReviewVerdict) (model.ReviewVerdict, error),
) (toolFunc, error) {
	execute, err := r.executor(operation, worker, name, story, submit, submitVerdict)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		result, err := execute(ctx, raw)
		if err != nil {
			if base, loadErr := story(ctx); loadErr == nil {
				err = base.Humanize(err)
			}
		}
		return result, err
	}, nil
}

func (r *Runtime) executor(
	operation model.Operation,
	worker, name string,
	story func(context.Context) (*narrative.Story, error),
	submit func(model.Proposal) (model.Proposal, error),
	submitVerdict func(model.ReviewVerdict) (model.ReviewVerdict, error),
) (toolFunc, error) {
	switch name {
	case prompt.ToolAuthorityRead:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var query narrative.Query
			if err := decodeToolArgs(raw, &query); err != nil {
				return nil, err
			}
			base, err := story(ctx)
			if err != nil {
				return nil, err
			}
			view, err := base.Read(query)
			if err != nil {
				return nil, err
			}
			return json.Marshal(view)
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
				return nil, fmt.Errorf("read workspace %q (workspace_list lists existing keys): %w", args.Key, err)
			}
			content := json.RawMessage(artifact.Content)
			if artifact.MediaType == workspace.ChapterMediaType {
				if content, err = draftView(operation, artifact.Content); err != nil {
					return nil, err
				}
			}
			return json.Marshal(struct {
				workspaceReceipt
				Content json.RawMessage `json:"content"`
			}{receiptOf(artifact), content})
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
			receipts := make([]workspaceReceipt, 0, len(artifacts))
			for _, artifact := range artifacts {
				receipts = append(receipts, receiptOf(artifact))
			}
			return json.Marshal(receipts)
		}, nil
	case prompt.ToolWorkspacePutChapter:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key     string                 `json:"key"`
				Chapter narrative.ChapterDraft `json:"chapter"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			input, err := model.DecodeTaskInput(operation.Kind, operation.Input)
			if err != nil {
				return nil, err
			}
			base, err := story(ctx)
			if err != nil {
				return nil, err
			}
			// 章的身份由宿主按任务确定（D66）：模型只写标题与段落。
			chapter, err := base.Draft(input, args.Chapter)
			if err != nil {
				return nil, err
			}
			artifact, err := r.workspace.PutChapter(ctx, operation.ID, args.Key, chapter, nil, operation.Attempt, r.now())
			if err != nil {
				return nil, err
			}
			return json.Marshal(receiptOf(artifact))
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
			return json.Marshal(receiptOf(artifact))
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
			return json.Marshal(receiptOf(artifact))
		}, nil
	case prompt.ToolWorkspacePutReview:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Key      string              `json:"key"`
				Findings []narrative.Finding `json:"findings"`
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			input, err := model.TaskInputAs[model.ReviewRangeInput](operation)
			if err != nil {
				return nil, err
			}
			base, err := story(ctx)
			if err != nil {
				return nil, err
			}
			findings, err := base.Findings(input, args.Findings)
			if err != nil {
				return nil, err
			}
			content, err := json.Marshal(findings)
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
			return json.Marshal(receiptOf(artifact))
		}, nil
	case prompt.ToolProposalSubmit:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Reason            string           `json:"reason"`
				WorkspaceKey      string           `json:"workspace_key"`
				WorkspaceVersion  int64            `json:"workspace_version"`
				WorkspaceVersions map[string]int64 `json:"workspace_versions"`
				narrative.Submission
			}
			if err := decodeToolArgs(raw, &args); err != nil {
				return nil, err
			}
			versions := args.WorkspaceVersions
			if args.WorkspaceKey != "" {
				versions = map[string]int64{args.WorkspaceKey: args.WorkspaceVersion}
			}
			drafts, err := r.workspaceDrafts(ctx, operation, versions)
			if err != nil {
				return nil, err
			}
			input, err := model.DecodeTaskInput(operation.Kind, operation.Input)
			if err != nil {
				return nil, err
			}
			base, err := story(ctx)
			if err != nil {
				return nil, err
			}
			patches, err := base.Resolve(input, args.Submission, drafts)
			if err != nil {
				return nil, err
			}
			// 正文的故事依赖由宿主按事实变化写入（D40）。
			if patches, err = model.NormalizeSubmission(patches); err != nil {
				return nil, err
			}
			proposal := model.Proposal{
				ID: operation.ID + "-proposal", OperationID: operation.ID,
				Target: operation.Target, BaseRevision: operation.Snapshot.BaseRevision,
				Author: model.Author{Kind: model.AuthorAI, ID: worker},
				Reason: args.Reason, Patches: patches,
				ApprovalState: model.ApprovalPending, CreatedAt: r.now(),
			}
			// 确定性校验（结构、事实、任务提交契约，D41/D63/D64）在工具边界当场反馈，收尾的
			// PrepareExecution 执行同一份：否则模型看到"提交成功"，任务却在收尾失败且无从自纠。
			// 报错可能指向本次新建的文档，用应用后的故事翻译。
			if err := proposal.Validate(); err != nil {
				return nil, err
			}
			if err := r.changes.Validate(ctx, proposal); err != nil {
				if projected, applyErr := base.Apply(patches); applyErr == nil {
					return nil, projected.Humanize(err)
				}
				return nil, err
			}
			if _, err := submit(proposal); err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				Status string `json:"status"`
			}{"submitted"})
		}, nil
	case prompt.ToolVerdictSubmit:
		return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var args struct {
				Status    string                   `json:"status"`
				ReviewKey string                   `json:"review_key"`
				Checks    []model.RequirementCheck `json:"checks"`
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
				Checks: args.Checks, Findings: findings,
			}
			if err := model.ValidateReviewVerdictForOperation(operation, verdict); err != nil {
				return nil, err
			}
			verdict, err = submitVerdict(verdict)
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct {
				Status string `json:"status"`
			}{verdict.Status})
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

// workspaceReceipt 是工作区写入的回执：提交只需要键与版本。
type workspaceReceipt struct {
	Key     string `json:"key"`
	Version int64  `json:"version"`
}

func receiptOf(artifact model.WorkspaceArtifact) workspaceReceipt {
	return workspaceReceipt{Key: artifact.Key, Version: artifact.Version}
}

// draftView 把工作稿读回成模型写入时的形状：章号只在一次改写多章时有意义。
func draftView(operation model.Operation, content []byte) (json.RawMessage, error) {
	var chapter model.ManuscriptChapter
	if err := json.Unmarshal(content, &chapter); err != nil {
		return nil, fmt.Errorf("decode workspace chapter: %w", err)
	}
	draft := narrative.ChapterDraft{Title: chapter.Title, Blocks: chapter.Blocks}
	if operation.Kind == model.OperationRewriteAffected {
		draft.Number = chapter.Number
	}
	return json.Marshal(draft)
}
