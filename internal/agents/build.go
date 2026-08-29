package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/agents/guard"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
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
	return name
}

// promptCacheBase 从书目录派生稳定短哈希，作为提示词缓存身份前缀：同一本书
// 跨进程重启共享路由桶，且不向 provider 泄露本地路径。角色后缀由调用方拼接，
// subagent 每次 spawn 再追加 "#seq"（一次会话一个键）。
func promptCacheBase(bookDir string) string {
	sum := sha256.Sum256([]byte(bookDir))
	return "nvl-" + hex.EncodeToString(sum[:6])
}

// subagentMaxRetries 是所有 Worker 的 LLM retry 上限。
// 退避策略：指数退避（受 maxDelay 上限约束），优先服从 server Retry-After。
// 工具只在完整 Assistant 消息提交后启动，因此 stream-idle / 503 /
// 短暂网络抖动可以在 Worker 内安全重试，不会重放工具副作用。
const subagentMaxRetries = 7

// UsageRecorder 是 BuildWorkers 可选的用量回调；签名与 OnMessage 一致，
// 每条 agent 消息都会调一次，由 Host 层负责聚合。task 是本次 spawn 的任务文本
// 作为会话身份，供缓存链断裂检测按会话重置基线。
// nil 表示不追踪。
type UsageRecorder func(agentName, task string, msg agentcore.AgentMessage)

// ApplyThinking 把某具体角色的推理强度应用到 Worker（运行时 /model 调整用）。
// architect → 两个 architect_* 子代理；writer/editor → 对应子代理。
// 空 level = 沿用模型/provider 默认。其它 role 名忽略。
type ApplyThinking func(role string, level agentcore.ThinkingLevel)

// ParseThinkingLevel 把配置字符串转 agentcore.ThinkingLevel。
// "" 合法（= 不覆盖/继承）；其余须是 off/low/medium/high/xhigh/max 之一，
// 否则返回 error（启动时降级当空并 warn，运行时把 error 回显给用户）。
func ParseThinkingLevel(s string) (agentcore.ThinkingLevel, error) {
	lv := agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(s))
	switch lv {
	case "", agentcore.ThinkingOff, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax:
		return lv, nil
	default:
		return "", fmt.Errorf("无效推理强度 %q（可选：off/low/medium/high/xhigh/max）", s)
	}
}

func ResolveThinkingForModel(model agentcore.ChatModel, level agentcore.ThinkingLevel) (agentcore.ThinkingLevel, bool) {
	level = agentcore.NormalizeThinkingLevel(level)
	// 对不支持 thinking 的普通 chat 模型，显式 off 不是 no-op，而是非法参数。
	if cp, ok := model.(llm.CapabilityProvider); ok && cp.Capabilities().Thinking.Supported == llm.SupportNo {
		return agentcore.ThinkingAuto, level == agentcore.ThinkingAuto
	}
	return llm.ThinkingPolicyFor(model).Resolve(level)
}

func AvailableThinkingForModel(model agentcore.ChatModel) []agentcore.ThinkingLevel {
	if cp, ok := model.(llm.CapabilityProvider); ok && cp.Capabilities().Thinking.Supported == llm.SupportNo {
		return []agentcore.ThinkingLevel{agentcore.ThinkingAuto}
	}
	return llm.ThinkingPolicyFor(model).Available
}

// roleThinking 解析某角色生效的推理强度；非法值降级为空（不覆盖）并 warn。
func roleThinking(cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	lv, err := ParseThinkingLevel(cfg.ResolveReasoningEffort(role))
	if err != nil {
		slog.Warn("忽略无效推理强度配置", "module", "agent", "role", role, "err", err)
		return ""
	}
	return lv
}

func resolvedRoleThinking(model agentcore.ChatModel, cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	resolved, _ := ResolveThinkingForModel(model, roleThinking(cfg, role))
	return resolved
}

// BuildWorkers 组装三个 Worker(architect_short/long、writer、editor)为可程序化
// 调用的 subagent.Runner。Engine 直接调用其类型化入口，无 LLM 工具层
// (docs/engine-rfc.md §1)。
// 返回 Runner、WriterRestorePack 与 ApplyThinking(运行时 /model 联动各角色推理强度;
// writer/architect/editor 的 ContextManager 走工厂自动重建)。
// onGuardBlock 可选(nil 安全):各 Worker StopGuard 的拦截/升级审计回调。
func BuildWorkers(
	cfg bootstrap.Config,
	store *store.Store,
	styleStats *tools.StyleStatsIndex,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	recordUsage UsageRecorder,
	onGuardBlock guard.BlockHook,
) (*subagent.Runner, *ctxpack.WriterRestorePack, ApplyThinking) {
	// 共享工具
	contextTool := tools.NewContextTool(store, bundle.References, cfg.Style, styleStats)
	readChapter := tools.NewReadChapterTool(store)

	architectTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveBookTool(store),
		tools.NewSaveFoundationTool(store),
		tools.NewReviseOutlineTool(store),
		tools.NewResolveOutlineFeedbackTool(store),
		tools.NewAuditFoundationTool(store),
	}

	// 联网搜索工具：复用主 provider 链路（代理 → 上游 provider，触发服务端
	// web_search）。零额外配置，baseURL/apiKey/model 都从顶层推导。
	// 仅挂载到 architect：写作阶段（writer/editor）需要的是故事内一致性
	// 而非外部资料，挂上去反而引入幻觉风险。
	if pc, ok := cfg.Providers[cfg.Provider]; ok && pc.BaseURL != "" {
		architectTools = append(architectTools, tools.NewWebSearchTool(pc.BaseURL, pc.APIKey, cfg.ModelName))
	}

	// 素材库写工具：architect 收集到的命名/术语/视觉/设定素材落盘到 meta/materials.json，
	// 后续 novel_context.reference_pack.materials 自动注入。
	architectTools = append(architectTools,
		tools.NewSaveMaterialsTool(store),
		tools.NewListMaterialsTool(store),
		tools.NewRemoveMaterialTool(store),
	)
	writerTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewPlanChapterTool(store),
		tools.NewDraftChapterTool(store),
		tools.NewEditChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCommitChapterTool(store, styleStats),
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

	// Writer 的 ContextManager 由工厂每次调用重建，窗口随模型 swap 动态跟随（见下方工厂）。
	writerProvider, writerModelName, _ := models.CurrentSelection("writer")
	writerContextWindow, writerSource := cfg.ResolveContextWindow(writerProvider, writerModelName)
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
			recordUsage(agentName, task, msg)
		}
	}

	// 提示词缓存：一书一基、一角色一名、一会话一键（subagent spawn 追加 #seq）。
	// OpenAI 系用 prompt_cache_key 做路由亲和；Claude 系用 cache_control 滚动断点
	//（system 地板 + 末消息尖端）。provider 不支持时由 agentcore 按能力静默丢弃，
	// 多轮会话下读缓存收益恒为正，故不设开关。
	cacheBase := promptCacheBase(store.Dir())

	architectStopGuardFactory := func(_, _ string) agentcore.StopGuard {
		return guard.NewArchitectStopGuard(store, onGuardBlock)
	}
	architectThinking, _ := ResolveThinkingForModel(architectModel, roleThinking(cfg, "architect"))
	architectShort := subagent.Config{
		Name:             "architect_short",
		Description:      "短篇规划师：为单卷、单冲突、高密度故事生成紧凑设定与扁平大纲",
		Model:            architectModel,
		SystemPrompt:     bundle.Prompts.ArchitectShort,
		Tools:            architectTools,
		MaxTurns:         15,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    architectThinking,
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-architect_short",
		StopAfterToolResult: func(toolName string, result json.RawMessage) bool {
			return foundationReadyResult(toolName, result)
		},
		StopGuardFactory: architectStopGuardFactory,
	}
	architectLong := subagent.Config{
		Name:                "architect_long",
		Description:         "长篇规划师：为连载型、可持续升级的故事生成分层设定与卷弧大纲",
		Model:               architectModel,
		SystemPrompt:        bundle.Prompts.ArchitectLong,
		Tools:               architectTools,
		MaxTurns:            20,
		MaxRetries:          subagentMaxRetries,
		ThinkingLevel:       architectThinking,
		OnMessage:           onMsg,
		CacheLastMessage:    "ephemeral",
		PromptCacheKey:      cacheBase + "-architect_long",
		StopAfterToolResult: architectLongShouldStopAfterToolResult,
		StopGuardFactory:    architectStopGuardFactory,
	}

	// 唯一组装路径:协议模板 {{VOICE}} 原位回填文风段,再追加风格预设。
	// eval 的 voice A/B 走同一函数,保证两臂等价(docs/voice-layer.md §3.2)。
	writerPrompt := assets.BuildWriterPrompt(bundle.Prompts.Writer, bundle.Voice, bundle.Styles[cfg.Style])

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(store)

	writer := subagent.Config{
		Name:             "writer",
		Description:      "创作者：自主完成一章的构思、写作、自审和提交",
		Model:            writerModel,
		SystemPrompt:     writerPrompt,
		Tools:            writerTools,
		MaxTurns:         30,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    resolvedRoleThinking(writerModel, cfg, "writer"),
		StopAfterTools:   []string{"commit_chapter"},
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-writer",
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return guard.NewWriterStopGuard(store, onGuardBlock)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			// 每章按当前 writer 模型重建上下文管理器。
			window, _ := models.ResolveContextWindow(bootstrap.ModelProvider(model), bootstrap.ModelName(model))
			return newContextManager(contextManagerConfig{
				Model:         model,
				ContextWindow: window,
				ReserveTokens: bootstrap.CompactReserveTokens(window),
				Agent:         "writer",
				// 提交投影，避免后续轮次反复改写请求前缀。
				CommitProjected: true,
				ToolMicrocompact: &corecontext.ToolResultMicrocompactConfig{
					MinResultTokens: 200,
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
		Name:             "editor",
		Description:      "审阅者：阅读原文，从结构和审美两个层面发现问题",
		Model:            editorModel,
		SystemPrompt:     bundle.Prompts.Editor,
		Tools:            editorTools,
		MaxTurns:         20,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    resolvedRoleThinking(editorModel, cfg, "editor"),
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-editor",
		// 终态产物命中即停。终态退出仍会咨询 StopGuard（契约测试 TestContract_
		// TerminalToolExitConsultsStopGuard），任务感知的 NewEditorStopGuard 负责
		// 否决"被派生成摘要却只做了复核"的提前退出，所以 save_review 可以安全硬停。
		StopAfterToolResult: func(toolName string, _ json.RawMessage) bool {
			return toolName == "save_review" || toolName == "save_arc_summary" || toolName == "save_volume_summary"
		},
		StopGuardFactory: func(_, task string) agentcore.StopGuard {
			return guard.NewEditorStopGuard(store, task, onGuardBlock)
		},
	}

	runner := subagent.NewRunner(architectShort, architectLong, writer, editor)

	// 运行时联动各角色推理强度（/model 调整用）。
	applyThinking := func(role string, level agentcore.ThinkingLevel) {
		switch role {
		case "architect":
			level, _ = ResolveThinkingForModel(models.ForRole("architect"), level)
			runner.SetThinkingLevel("architect_short", level)
			runner.SetThinkingLevel("architect_long", level)
		case "writer", "editor":
			level, _ = ResolveThinkingForModel(models.ForRole(role), level)
			runner.SetThinkingLevel(role, level)
		}
	}

	return runner, restore, applyThinking
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

func architectLongShouldStopAfterToolResult(toolName string, result json.RawMessage) bool {
	if foundationReadyResult(toolName, result) {
		return true
	}
	r := decodeSaveFoundationResult(toolName, result)
	switch r.Type {
	case "expand_arc", "complete_book":
		return true
	default:
		return false
	}
}

func foundationReadyResult(toolName string, result json.RawMessage) bool {
	if toolName != "audit_foundation" {
		return false
	}
	var r struct {
		FoundationReady bool `json:"foundation_ready"`
	}
	return json.Unmarshal(result, &r) == nil && r.FoundationReady
}
