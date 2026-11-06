package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/retry"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/agents/guard"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// agentToRole 把 Worker 名归一为 ModelSet 认得的 role 名。
// architect_short / architect_long 都共用同一个 architect role 配置。
// 跟 host.agentRoleName 同义，因为 build 与 host 互不依赖故各持一份。
func agentToRole(name string) string {
	if strings.HasPrefix(name, "architect_") {
		return "architect"
	}
	return name
}

// promptCacheBase 从书目录派生稳定短哈希，作为提示词缓存身份前缀：同一本书
// 跨进程重启共享路由桶，且不向 provider 泄露本地路径。角色后缀由调用方拼接。
func promptCacheBase(bookDir string) string {
	sum := sha256.Sum256([]byte(bookDir))
	return "nvl-" + hex.EncodeToString(sum[:6])
}

// subagentMaxAttempts 是所有 Worker 单次模型调用的总尝试次数（首次 + 7 次重试）。
// 退避策略：指数退避（受 maxDelay 上限约束），优先服从 server Retry-After。
// 工具只在完整 Assistant 消息提交后启动，因此 stream-idle / 503 /
// 短暂网络抖动可以在 Worker 内安全重试，不会重放工具副作用。
const subagentMaxAttempts = 8

// workerCacheTTL 是显式缓存断点的 TTL；空 = 交给 provider 默认（Anthropic/Bedrock
// 是 5 分钟）。litellm 把 TTL 原样透传（anthropic 适配器拼进 cache_control.ttl），
// 而 OpenAI 的 prompt_cache_breakpoint 没有 ttl 字段，因此这里不写死 "5m"，
// 避免向不支持 TTL 的端点发出多余参数。
const workerCacheTTL = ""

// workerRetry 返回所有 Worker 共用的重试策略。
// 零值时长/倍率由 retry 包补默认值；MaxRetryAfter 兜住 Cloudflare 的
// origin 超时建议值（常在 2 分钟）。
func workerRetry() retry.Policy {
	return retry.Policy{MaxAttempts: subagentMaxAttempts, MaxRetryAfter: 2 * time.Minute}
}

// workerCache 返回显式缓存断点配置。
// litellm v1.9.6 的 Responses 适配器已支持在 tool result 上落断点
// （见 provider/openai/responses.go 的 promptCacheBreakpoint），
// 因此不再按 provider 区分；prompt_cache_key 路由亲和照旧由
// bootstrap.WithPromptCacheKey 写入。
func workerCache() *litellm.CacheControl {
	return &litellm.CacheControl{TTL: workerCacheTTL}
}

// UsageRecorder 是 BuildWorkers 可选的用量回调；每条落盘消息都会调一次，
// 由 Host 层负责聚合。task 是本次 run 的任务文本，作为会话身份，
// 供缓存链断裂检测按会话重置基线。nil 表示不追踪。
type UsageRecorder func(agentName, task string, msg agentcore.Message)

// ApplyThinking 把某具体角色的推理强度应用到 Worker（运行时 /model 调整用）。
// architect → 两个 architect_*；writer/editor → 对应 Worker。
// 空 level = 沿用模型/provider 默认。其它 role 名忽略。
type ApplyThinking func(role string, level bootstrap.ThinkingLevel)

// ParseThinkingLevel 把配置字符串转 bootstrap.ThinkingLevel。
// "" 合法（= 不覆盖/继承）；其余须是 off/minimal/low/medium/high/xhigh/max 之一，
// 否则返回 error（启动时降级当空并 warn，运行时把 error 回显给用户）。
func ParseThinkingLevel(s string) (bootstrap.ThinkingLevel, error) {
	return bootstrap.ParseThinkingLevel(s)
}

// ResolveThinkingForModel 返回该模型实际可用的推理强度，并与旧语义保持一致：
// ok=false 表示这个 level 对该模型没有意义（不支持思考的模型、或越级值），
// 调用方应跳过应用、沿用模型默认。
func ResolveThinkingForModel(m llmcontract.Model, level bootstrap.ThinkingLevel) (bootstrap.ThinkingLevel, bool) {
	if level == "" {
		return "", false
	}
	for _, a := range bootstrap.AvailableThinking(m) {
		if a == level {
			return level, true
		}
	}
	return "", false
}

// AvailableThinkingForModel 返回该模型可选的推理强度（TUI 快照用）。
func AvailableThinkingForModel(m llmcontract.Model) []bootstrap.ThinkingLevel {
	return bootstrap.AvailableThinking(m)
}

// thinkingTargets 返回某 role 实际受影响的 Worker 名；未知 role 返回 nil。
func thinkingTargets(role string) []string {
	switch role {
	case "architect":
		return []string{"architect_short", "architect_long"}
	case "writer", "editor":
		return []string{role}
	default:
		return nil
	}
}

// thinkingValue 取模型快照里的推理配置；null / 缺失视为不覆盖。
func thinkingValue(m llmcontract.Model) litellm.Thinking {
	if t := m.Core.Request.Thinking; t != nil {
		return *t
	}
	return litellm.Thinking{}
}

// roleThinking 解析某角色生效的推理强度；非法值降级为空（不覆盖）并 warn。
// 空值合法：表示沿用 provider 默认。
func roleThinking(cfg bootstrap.Config, role string) bootstrap.ThinkingLevel {
	level, err := bootstrap.ParseThinkingLevel(cfg.ResolveReasoningEffort(role))
	if err != nil {
		slog.Warn("忽略无效推理强度配置", "module", "agent", "role", role, "err", err)
		return ""
	}
	return level
}

// workerThinkingLevel 返回某角色启动时应写进 Worker 模型的推理强度：
// 原始配置意图经该角色当前模型的能力钳制，不可用则退回空（不覆盖）。
func workerThinkingLevel(cfg bootstrap.Config, models *bootstrap.ModelSet, role string) bootstrap.ThinkingLevel {
	if resolved, ok := ResolveThinkingForModel(models.ForRole(role), roleThinking(cfg, role)); ok {
		return resolved
	}
	return ""
}

// BuildWorkers 组装四个 Worker(architect_short/long、writer、editor)为可程序化
// 调用的 WorkerSet。Engine 直接调用其类型化入口，无 LLM 工具层
// (docs/engine-rfc.md §1)。
// 返回 WorkerSet、WriterRestorePack 与 ApplyThinking(运行时 /model 联动各角色推理强度)。
// onGuardBlock 可选(nil 安全):各 Worker StopGuard 的拦截/升级审计回调。
func BuildWorkers(
	cfg bootstrap.Config,
	store *store.Store,
	styleStats *tools.StyleStatsIndex,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	recordUsage UsageRecorder,
	onGuardBlock guard.BlockHook,
) (*WorkerSet, *ctxpack.WriterRestorePack, ApplyThinking) {
	// 共享工具
	contextTool := tools.NewContextTool(store, bundle.References, cfg.Style, styleStats)
	readChapter := tools.NewReadChapterTool(store)

	architectTools := []tools.Tool{
		contextTool,
		tools.NewSaveBookTool(store),
		tools.NewSaveFoundationTool(store),
		tools.NewReviseOutlineTool(store),
		tools.NewResolveOutlineFeedbackTool(store),
		tools.NewAuditFoundationTool(store),
	}
	architectLongTools := append([]tools.Tool(nil), architectTools...)
	architectLongTools = append(architectLongTools, tools.NewExpandNextArcTool(store))
	writerTools := []tools.Tool{
		contextTool,
		readChapter,
		tools.NewPlanChapterTool(store),
		tools.NewDraftChapterTool(store),
		tools.NewEditChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCommitChapterTool(store, styleStats),
	}
	editorTools := []tools.Tool{
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

	architectThinking := workerThinkingLevel(cfg, models, "architect")
	writerThinking := workerThinkingLevel(cfg, models, "writer")
	editorThinking := workerThinkingLevel(cfg, models, "editor")

	architectWindow, architectSource := models.ResolveContextWindow(architectModel.Provider, architectModel.ModelName)
	bootstrap.LogContextWindowChoice("architect", architectModel.ModelName, architectWindow, architectSource)
	writerWindow, writerSource := models.ResolveContextWindow(writerModel.Provider, writerModel.ModelName)
	bootstrap.LogContextWindowChoice("writer", writerModel.ModelName, writerWindow, writerSource)
	editorWindow, editorSource := models.ResolveContextWindow(editorModel.Provider, editorModel.ModelName)
	bootstrap.LogContextWindowChoice("editor", editorModel.ModelName, editorWindow, editorSource)

	// modelLookup 写入 session 时给每条 assistant 消息附 _meta:{provider,model}，
	// 让 replay 不再依赖"当前 ModelSet"来反推历史 cost，运行中切换模型也能精确算。
	modelLookup := func(agentName string) (string, string) {
		provider, name, _ := models.CurrentSelection(agentToRole(agentName))
		return provider, name
	}

	// 提示词缓存：一书一基、一角色一名。OpenAI 系用 prompt_cache_key 做路由亲和；
	// block 缓存端点用 cache_control 断点（system 地板 + 框架的滚动尖端）。
	cacheBase := promptCacheBase(store.Dir())
	cache := workerCache()

	// 压缩阈值：窗口减去压缩后必须留出的余量。CompactAt 在装配期固定；
	// 运行时把角色切到窗口不同的模型时，框架在 provider 报 context overflow
	// 后会先压缩再重试，仍能自愈。
	architectCompactAt := compactAt(architectWindow)
	writerCompactAt := compactAt(writerWindow)
	editorCompactAt := compactAt(editorWindow)

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(store)

	// 唯一组装路径:协议模板 {{VOICE}} 原位回填文风段,再追加风格预设。
	// eval 的 voice A/B 走同一函数,保证两臂等价(docs/voice-layer.md §3.2)。
	writerPrompt := assets.BuildWriterPrompt(bundle.Prompts.Writer, bundle.Voice, bundle.Styles[cfg.Style])

	architectStop := func(string) StopHook {
		return guard.NewArchitectStopGuard(store, onGuardBlock)
	}
	cfgs := map[string]agentcore.Config{
		"architect_short": {
			Model:     workerModel(architectModel, cacheBase+"-architect_short", architectThinking),
			System:    workerSystem(bundle.Prompts.ArchitectShort, cache),
			Tools:     terminateOn(tools.ToAgentcoreAll(architectTools), foundationReadyResult),
			MaxTurns:  15,
			Retry:     workerRetry(),
			Cache:     cache,
			Compactor: newRoleContextManager(architectContextProfile, store),
			CompactAt: architectCompactAt,
		},
		"architect_long": {
			Model:     workerModel(architectModel, cacheBase+"-architect_long", architectThinking),
			System:    workerSystem(bundle.Prompts.ArchitectLong, cache),
			Tools:     terminateOn(tools.ToAgentcoreAll(architectLongTools), architectLongShouldStopAfterToolResult),
			MaxTurns:  20,
			Retry:     workerRetry(),
			Cache:     cache,
			Compactor: newRoleContextManager(architectContextProfile, store),
			CompactAt: architectCompactAt,
		},
		"writer": {
			Model:    workerModel(writerModel, cacheBase+"-writer", writerThinking),
			System:   workerSystem(writerPrompt, cache),
			Tools:    terminateOn(tools.ToAgentcoreAll(writerTools), stopAfterToolSet("commit_chapter")),
			MaxTurns: 30,
			Retry:    workerRetry(),
			Cache:    cache,
			Compactor: newContextManager(contextManagerConfig{
				Store:   store,
				Restore: restore,
			}),
			CompactAt: writerCompactAt,
		},
		"editor": {
			Model:     workerModel(editorModel, cacheBase+"-editor", editorThinking),
			System:    workerSystem(bundle.Prompts.Editor, cache),
			Tools:     terminateOn(tools.ToAgentcoreAll(editorTools), stopAfterToolSet("save_review", "save_arc_summary", "save_volume_summary")),
			MaxTurns:  20,
			Retry:     workerRetry(),
			Cache:     cache,
			Compactor: newRoleContextManager(editorContextProfile, store),
			CompactAt: editorCompactAt,
		},
	}

	workers := NewWorkerSet(cfgs, []string{"architect_short", "architect_long", "writer", "editor"})
	workers.stopFactories["architect_short"] = architectStop
	workers.stopFactories["architect_long"] = architectStop
	workers.stopFactories["writer"] = func(string) StopHook {
		return guard.NewWriterStopGuard(store, onGuardBlock)
	}
	// 终态产物命中即停。终态退出仍会咨询 OnStop（契约测试
	// TestContract_TerminalToolExitConsultsStopGuard），任务感知的
	// NewEditorStopGuard 负责否决"被派生成摘要却只做了复核"的提前退出，
	// 所以 save_review 可以安全硬停。
	workers.stopFactories["editor"] = func(task string) StopHook {
		return guard.NewEditorStopGuard(store, task, onGuardBlock)
	}

	baseRecord := store.Sessions.SubAgentLogger(modelLookup)
	workers.record = func(agentName, task string, msg agentcore.Message) {
		baseRecord(agentName, task, msg)
		if recordUsage != nil {
			recordUsage(agentName, task, msg)
		}
	}

	// 运行时联动各角色推理强度（/model 调整用）。
	applyThinking := func(role string, level bootstrap.ThinkingLevel) {
		targets := thinkingTargets(role)
		if len(targets) == 0 {
			return
		}
		models.SetThinking(role, level)
		t := thinkingValue(models.ForRole(role))
		for _, name := range targets {
			workers.SetThinking(name, t)
		}
	}

	return workers, restore, applyThinking
}

// workerModel 把 ModelSet 的模型快照转成 Worker 的运行模型：写入该角色启动时的
// 推理强度（按模型能力钳制）与角色独占的提示词缓存路由键。
func workerModel(m llmcontract.Model, cacheKey string, level bootstrap.ThinkingLevel) agentcore.Model {
	m = bootstrap.ApplyThinking(m, level)
	if cacheKey != "" {
		m = bootstrap.WithPromptCacheKey(m, cacheKey)
	}
	return m.Core
}

// workerSystem 把提示词包成带缓存断点的 system 块（缓存地板）。
func workerSystem(prompt string, cache *litellm.CacheControl) []litellm.Block {
	return []litellm.Block{litellm.TextBlock{Text: prompt, Cache: cache}}
}

// terminateOn 用 Result.Terminate 表达旧的 StopAfterTools / StopAfterToolResult：
// 命中 pred 的工具调用结束后，本轮 run 走 OnStop 收尾。
func terminateOn(ts []agentcore.Tool, pred func(toolName string, result agentcore.Result) bool) []agentcore.Tool {
	out := make([]agentcore.Tool, len(ts))
	for i, t := range ts {
		inner := t
		run := t.Run
		name := t.Name
		if run != nil {
			inner.Run = func(ctx context.Context, args json.RawMessage) (agentcore.Result, error) {
				res, err := run(ctx, args)
				if err == nil && pred(name, res) {
					res.Terminate = true
				}
				return res, err
			}
		}
		out[i] = inner
	}
	return out
}

// stopAfterToolSet 返回"调用这些工具即终态"的判定。
func stopAfterToolSet(names ...string) func(toolName string, _ agentcore.Result) bool {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return func(toolName string, _ agentcore.Result) bool {
		_, ok := set[toolName]
		return ok
	}
}

type saveFoundationResult struct {
	Type            string `json:"type"`
	FoundationReady bool   `json:"foundation_ready"`
}

func decodeSaveFoundationResult(toolName string, result agentcore.Result) saveFoundationResult {
	if toolName != "save_foundation" {
		return saveFoundationResult{}
	}
	var r saveFoundationResult
	_ = json.Unmarshal([]byte(result.Text()), &r)
	return r
}

func architectLongShouldStopAfterToolResult(toolName string, result agentcore.Result) bool {
	if foundationReadyResult(toolName, result) {
		return true
	}
	if toolName == "expand_next_arc" {
		return true
	}
	r := decodeSaveFoundationResult(toolName, result)
	switch r.Type {
	case "complete_book":
		return true
	default:
		return false
	}
}

func foundationReadyResult(toolName string, result agentcore.Result) bool {
	if toolName != "audit_foundation" {
		return false
	}
	var r struct {
		FoundationReady bool `json:"foundation_ready"`
	}
	return json.Unmarshal([]byte(result.Text()), &r) == nil && r.FoundationReady
}
