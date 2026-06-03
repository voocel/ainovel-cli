package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host/persona"
	"github.com/voocel/ainovel-cli/internal/host/reminder"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// agentToRole 把 subagent name 归一为 ModelSet 认得的 role 名。
// architect_short / architect_long 都共用同一个 architect role 配置。
// 跟 host.agentRoleName 同义，因为 build 与 host 互不依赖故各持一份。
func agentToRole(name string) string {
	if strings.HasPrefix(name, "architect_") {
		return "architect"
	}
	// 竞稿写手 writer_<slug> 的 cost 归属到 writer role，否则会按 agent 全名当成独立 role 算错。
	if strings.HasPrefix(name, "writer_") {
		return "writer"
	}
	return name
}

// subagentMaxRetries 给所有 SubAgentConfig 与 Coordinator 统一的 LLM retry 上限。
// 退避策略：指数 1s/2s/4s/8s/16s（受 maxDelay 上限约束），优先服从 server Retry-After。
// 配合 ToolsAreIdempotent=true 让 stream-idle / 503 / 短暂网络抖动这类 retryable
// 错误能在 subagent 层就近重试，而不是把整个 subagent 抛回 coordinator 重派发。
// 项目铁律一保证写类工具走 checkpoint+digest 幂等，重试是安全的。
const subagentMaxRetries = 5

// UsageRecorder 是 BuildCoordinator 可选的用量回调；签名与 OnMessage 一致，
// 每条 agent 消息都会调一次，由 Host 层负责聚合。nil 表示不追踪。
type UsageRecorder func(agentName string, msg agentcore.AgentMessage)

// BuildCoordinator 组装 Coordinator Agent 及其 SubAgent。
// 返回 Agent、AskUserTool、WriterRestorePack 以及 Coordinator 的 ContextEngine
// 引用——Host 层 /model 切换时需要直接调 SetContextWindow + SetReserveTokens
// 联动新模型的窗口（writer/architect/editor 走 ContextManagerFactory 自动重建，
// 不需要 ref；只有常驻的 coordinator 需要）。
// Host 层通过 Agent.Subscribe 获取事件流,不再需要 emit 回调。
func BuildCoordinator(
	cfg bootstrap.Config,
	store *store.Store,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	recordUsage UsageRecorder,
) (*agentcore.Agent, *tools.AskUserTool, *ctxpack.WriterRestorePack, *corecontext.ContextEngine) {
	// 共享工具
	rulesOpts := rules.DefaultOptions(bundle.RulesFS)
	contextTool := tools.NewContextTool(store, bundle.References, cfg.Style, rulesOpts)
	readChapter := tools.NewReadChapterTool(store)
	askUser := tools.NewAskUserTool()

	architectTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveFoundationTool(store),
	}
	writerTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewPlanChapterTool(store),
		tools.NewDraftChapterTool(store),
		tools.NewEditChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCommitChapterTool(store).WithRules(rulesOpts),
	}
	editorTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewSaveReviewTool(store),
		tools.NewSaveArcSummaryTool(store),
		tools.NewSaveVolumeSummaryTool(store),
	}

	// Provider failover 只记日志,不通知宿主
	reportFailover := func(ev bootstrap.FailoverEvent) {
		slog.Warn("provider 切换",
			"module", "agent",
			"role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err,
		)
	}

	architectModel := models.ForRoleWithFailover("architect", reportFailover)
	writerModel := models.ForRoleWithFailover("writer", reportFailover)
	editorModel := models.ForRoleWithFailover("editor", reportFailover)
	coordinatorModel := models.ForRoleWithFailover("coordinator", reportFailover)

	// Coordinator 的 ContextManager 在 Agent 构造时一次性生成，按启动模型解析。
	// 运行中 /model 切换到更小窗口的模型时，建议用户显式配置 context_window 兜底。
	_, coordinatorModelName, _ := models.CurrentSelection("coordinator")
	coordinatorContextWindow, coordinatorSource := cfg.ResolveContextWindow(coordinatorModelName)
	// Writer 的 ContextManager 由工厂每次调用重建，窗口随模型 swap 动态跟随（见下方工厂）。
	_, writerModelName, _ := models.CurrentSelection("writer")
	writerContextWindow, writerSource := cfg.ResolveContextWindow(writerModelName)
	bootstrap.LogContextWindowChoice("coordinator", coordinatorModelName, coordinatorContextWindow, coordinatorSource)
	bootstrap.LogContextWindowChoice("writer", writerModelName, writerContextWindow, writerSource)

	// modelLookup 写入 session 时给每条 assistant 消息附 _meta:{provider,model}，
	// 让 replay 不再依赖"当前 ModelSet"来反推历史 cost，运行中切换模型也能精确算。
	modelLookup := func(agentName string) (string, string) {
		role := agentToRole(agentName)
		provider, name, _ := models.CurrentSelection(role)
		return provider, name
	}
	baseOnMsg := store.Sessions.SubAgentLogger(modelLookup)
	onMsg := func(agentName, task string, msg agentcore.AgentMessage) {
		baseOnMsg(agentName, task, msg)
		if recordUsage != nil {
			recordUsage(agentName, msg)
		}
	}
	baseCoordinatorLog := store.Sessions.CoordinatorLogger(modelLookup)
	coordinatorOnMessage := func(msg agentcore.AgentMessage) {
		baseCoordinatorLog(msg)
		if recordUsage != nil {
			recordUsage("coordinator", msg)
		}
	}

	architectStopGuardFactory := func(_, _ string) agentcore.StopGuard {
		return reminder.NewArchitectStopGuard(store)
	}
	architectShort := subagent.Config{
		Name:               "architect_short",
		Description:        "短篇规划师：为单卷、单冲突、高密度故事生成紧凑设定与扁平大纲",
		Model:              architectModel,
		SystemPrompt:       bundle.Prompts.ArchitectShort,
		Tools:              architectTools,
		MaxTurns:           12,
		MaxRetries:         subagentMaxRetries,
		ToolsAreIdempotent: true,
		OnMessage:          onMsg,
		StopAfterToolResult: func(toolName string, result json.RawMessage) bool {
			r := decodeSaveFoundationResult(toolName, result)
			return r.Type == "outline" && r.FoundationReady
		},
		StopGuardFactory: architectStopGuardFactory,
	}
	architectLong := subagent.Config{
		Name:               "architect_long",
		Description:        "长篇规划师：为连载型、可持续升级的故事生成分层设定与卷弧大纲",
		Model:              architectModel,
		SystemPrompt:       bundle.Prompts.ArchitectLong,
		Tools:              architectTools,
		MaxTurns:           20,
		MaxRetries:         subagentMaxRetries,
		ToolsAreIdempotent: true,
		OnMessage:          onMsg,
		StopAfterToolResult: func(toolName string, result json.RawMessage) bool {
			r := decodeSaveFoundationResult(toolName, result)
			switch r.Type {
			case "update_compass", "expand_arc", "complete_book":
				return true
			default:
				return false
			}
		},
		StopGuardFactory: architectStopGuardFactory,
	}

	writerPrompt := bundle.Prompts.Writer
	if style, ok := bundle.Styles[cfg.Style]; ok {
		writerPrompt += "\n\n" + style
	}

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(store)

	writer := subagent.Config{
		Name:               "writer",
		Description:        "创作者：自主完成一章的构思、写作、自审和提交",
		Model:              writerModel,
		SystemPrompt:       writerPrompt,
		Tools:              writerTools,
		MaxTurns:           30,
		MaxRetries:         subagentMaxRetries,
		ToolsAreIdempotent: true,
		StopAfterTools:     []string{"commit_chapter"},
		OnMessage:          onMsg,
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return reminder.NewWriterStopGuard(store)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			// 每次 subagent(writer) 调用都会重建，从当前 runModel 读取最新模型名。
			// /model 切换 writer 后下一章自动用新窗口。
			window, _ := cfg.ResolveContextWindow(bootstrap.ModelName(model))
			return newContextManager(contextManagerConfig{
				Model:            model,
				ContextWindow:    window,
				ReserveTokens:    bootstrap.CompactReserveTokens(window),
				KeepRecentTokens: 20000,
				Agent:            "writer",
				ToolMicrocompact: &corecontext.ToolResultMicrocompactConfig{
					IdleThreshold: 5 * time.Minute,
				},
				ExtraStrategies: []corecontext.Strategy{
					ctxpack.NewStoreSummaryCompact(ctxpack.StoreSummaryCompactConfig{
						Store:            store,
						KeepRecentTokens: 20000,
					}),
				},
				Summary: &corecontext.FullSummaryConfig{
					PostSummaryHooks:    []corecontext.PostSummaryHook{restore.Hook()},
					SystemPrompt:        ctxpack.WriterSummarySystemPrompt,
					SummaryPrompt:       ctxpack.WriterSummaryPrompt,
					UpdateSummaryPrompt: ctxpack.WriterUpdateSummaryPrompt,
					TurnPrefixPrompt:    ctxpack.WriterTurnPrefixPrompt,
				},
			})
		},
	}

	editor := subagent.Config{
		Name:               "editor",
		Description:        "审阅者：阅读原文，从结构和审美两个层面发现问题",
		Model:              editorModel,
		SystemPrompt:       bundle.Prompts.Editor,
		Tools:              editorTools,
		MaxTurns:           20,
		MaxRetries:         subagentMaxRetries,
		ToolsAreIdempotent: true,
		OnMessage:          onMsg,
		StopAfterTools:     []string{"save_review", "save_arc_summary", "save_volume_summary"},
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return reminder.NewEditorStopGuard(store)
		},
	}

	// ---- 多人格竞稿装配 ----
	contestCfg := cfg.WritingContest.Normalize()
	var contestSubagents []subagent.Config
	if contestCfg.Enabled() {
		styleGen := func(ctx context.Context, author string) (string, error) {
			return generatePersonaStyle(ctx, writerModel, author)
		}
		// EnsurePersonas 串行调 N 次 LLM，给整体加超时避免冷启动让 host.New 挂起；
		// 超时由 persona.Generator 内部兜底为通用文风，不阻断流程。
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		personas, perr := persona.New(store, styleGen).EnsurePersonas(ctx, contestCfg.Personas)
		if perr != nil {
			slog.Warn("persona 生成异常，按已得结果继续", "module", "agent", "err", perr)
		}
		for _, p := range personas {
			pSlug := p.Slug
			personaTools := []agentcore.Tool{
				contextTool,
				readChapter,
				tools.NewPlanChapterTool(store),
				tools.NewDraftPersonaTool(store, pSlug),
				tools.NewCheckConsistencyTool(store),
				tools.NewCommitChapterTool(store).WithRules(rulesOpts),
			}
			personaPrompt := writerPrompt + "\n\n## 你的写作人格\n" + p.StyleBlock
			contestSubagents = append(contestSubagents, subagent.Config{
				Name:               "writer_" + pSlug,
				Description:        fmt.Sprintf("竞稿写手（人格：%s）", p.Author),
				Model:              writerModel,
				SystemPrompt:       personaPrompt,
				Tools:              personaTools,
				MaxTurns:           30,
				MaxRetries:         subagentMaxRetries,
				ToolsAreIdempotent: true,
				OnMessage:          onMsg,
				// 注意：不设 StopAfterTools。
				// 候选阶段需要在 draft_persona 后停止（由 CandidateStopGuard 保证）；
				// 润色阶段需要推进到 commit_chapter（由 WriterStopGuard 保证）。
				// 若固定 StopAfterTools:commit_chapter，候选阶段则无法在 draft_persona 干净停止。
				// StopGuardFactory 通过检测 task 文本中是否含"润色"来切换两阶段语义。
				StopGuardFactory: func(_, task string) agentcore.StopGuard {
					// task 含"润色" → 中选 writer 走润色+提交，要求 commit。
					// 否则为候选阶段 → 要求写候选草稿。
					if strings.Contains(task, "润色") {
						return reminder.NewWriterStopGuard(store)
					}
					return reminder.NewCandidateStopGuard(store)
				},
			})
		}

		// Judge：固定复用 editor 模型。
		// ModelSet 没有 ForRef 入口，自定义 judge 模型属后续增强，暂不实现（YAGNI）。
		// 仅在确有 >=2 份候选 persona 时才注册 judge：store 异常导致 personas 为空
		// 时不注册无用的 judge（无候选可裁）。
		if len(personas) >= 2 {
			if contestCfg.Judge != nil {
				slog.Warn("writing_contest.judge 模型配置暂不生效，当前复用 editor 模型", "module", "agent")
			}
			// judge 读候选稿需要 persona 的 slug 列表，从已生成的 personas 提取。
			judgeSlugs := make([]string, 0, len(personas))
			for _, p := range personas {
				judgeSlugs = append(judgeSlugs, p.Slug)
			}
			contestSubagents = append(contestSubagents, subagent.Config{
				Name:         "judge",
				Description:  "选优裁判：对比多份候选稿，选优并给修改意见",
				Model:        editorModel,
				SystemPrompt: bundle.Prompts.Judge,
				// read_candidates 一次性读本章所有候选稿；readChapter 保留供 judge 读已提交终稿做连贯性参考。
				Tools:              []agentcore.Tool{contextTool, readChapter, tools.NewReadCandidatesTool(store, judgeSlugs), tools.NewSaveVerdictTool(store)},
				MaxTurns:           15,
				MaxRetries:         subagentMaxRetries,
				ToolsAreIdempotent: true,
				OnMessage:          onMsg,
				StopAfterTools:     []string{"save_verdict"},
				StopGuardFactory: func(_, _ string) agentcore.StopGuard {
					return reminder.NewJudgeStopGuard(store)
				},
			})
		}
	}

	allSubagents := []subagent.Config{architectShort, architectLong, writer, editor}
	allSubagents = append(allSubagents, contestSubagents...)
	subagentTool := subagent.New(allSubagents...)

	coordinatorEngine := newContextManager(contextManagerConfig{
		Model:            coordinatorModel,
		ContextWindow:    coordinatorContextWindow,
		ReserveTokens:    bootstrap.CompactReserveTokens(coordinatorContextWindow),
		KeepRecentTokens: 30000,
		Agent:            "coordinator",
		CommitOnProject:  true,
	})

	agent := agentcore.NewAgent(
		agentcore.WithModel(coordinatorModel),
		agentcore.WithSystemPrompt(bundle.Prompts.Coordinator),
		agentcore.WithTools(subagentTool, contextTool),
		agentcore.WithMaxTurns(100_000),
		agentcore.WithOnMessage(coordinatorOnMessage),
		agentcore.WithToolsAreIdempotent(true),
		// subagent 是流程主通道；真实错误应显式返回给 Host，而不是在单次 run 内永久禁用工具。
		agentcore.WithMaxToolErrors(0),
		agentcore.WithMaxRetries(subagentMaxRetries),
		agentcore.WithContextManager(coordinatorEngine),
		agentcore.WithStopGuard(reminder.NewStopGuard(store, nil)),
	)
	return agent, askUser, restore, coordinatorEngine
}

type saveFoundationResult struct {
	Type            string `json:"type"`
	FoundationReady bool   `json:"foundation_ready"`
}

func decodeSaveFoundationResult(toolName string, result json.RawMessage) saveFoundationResult {
	if toolName != "save_foundation" {
		return saveFoundationResult{}
	}
	var r saveFoundationResult
	_ = json.Unmarshal(result, &r)
	return r
}

// generatePersonaStyle 让 LLM 依作者名生成一段文风 prompt 片段。
// 失败由调用方（persona.Generator）兜底，这里只负责一次模型调用。
// 调用模式对齐 internal/host/imp/foundation.go:41。
func generatePersonaStyle(ctx context.Context, model agentcore.ChatModel, author string) (string, error) {
	prompt := fmt.Sprintf(
		"请用 150 字以内，描述网文作者「%s」的写作风格特征，用于指导另一个 AI 模仿其文风。"+
			"覆盖：句式节奏、用词偏好、叙事视角、情绪渲染、擅长题材。直接输出风格描述，不要前缀。",
		author,
	)
	resp, err := model.Generate(ctx, []agentcore.Message{agentcore.UserMsg(prompt)}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("model returned nil response")
	}
	return strings.TrimSpace(resp.Message.TextContent()), nil
}
