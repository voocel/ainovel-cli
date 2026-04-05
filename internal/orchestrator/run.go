package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/domain"
)

// emitFn 是可选的 UIEvent 发射回调，用于向 UI 转发结构化事件。
type emitFn func(UIEvent)

// deltaFn 是可选的流式 token 回调，用于向 TUI 转发 LLM 生成的文字。
type deltaFn func(delta string)

// clearFn 是可选的流式缓冲清空回调，在新一轮 LLM 输出开始时触发。
type clearFn func()

type contextProgress struct {
	Agent           string
	Tokens          int
	ContextWindow   int
	Percent         float64
	Scope           string
	Strategy        string
	ActiveMessages  int
	SummaryMessages int
	CompactedCount  int
	KeptCount       int
}

// parseSubAgentRetry 从 EventToolExecUpdate 中提取 SubAgent 转发的重试信息。
func parseSubAgentRetry(ev agentcore.Event) (string, bool) {
	if ev.Progress == nil || ev.Progress.Kind != agentcore.ProgressRetry {
		return "", false
	}
	msg := truncateLog(ev.Progress.Message, 80)
	return fmt.Sprintf("%s 重试 (%d/%d): %s", ev.Progress.Agent, ev.Progress.Attempt, ev.Progress.MaxRetries, msg), true
}

// parseStreamDelta 从 EventToolExecUpdate 中提取流式 delta 文本。
func parseStreamDelta(ev agentcore.Event) (string, bool) {
	if ev.Progress == nil || ev.Progress.Kind != agentcore.ProgressToolDelta || ev.Progress.Delta == "" {
		return "", false
	}
	return ev.Progress.Delta, true
}

// parseThinkingDelta 从 EventToolExecUpdate 中提取思考文本。
func parseThinkingDelta(ev agentcore.Event) (string, bool) {
	if ev.Progress == nil || ev.Progress.Kind != agentcore.ProgressThinking || ev.Progress.Thinking == "" {
		return "", false
	}
	return ev.Progress.Thinking, true
}

// parseProgressSummary 从 EventToolExecUpdate 中提取可读摘要。
func parseProgressSummary(ev agentcore.Event) string {
	if ev.Progress == nil {
		return "progress"
	}
	if summary := summarizeStructuredProgress(ev.Progress); summary != "" || ev.Progress.Kind == agentcore.ProgressThinking {
		return summary
	}
	return "progress"
}

func summarizeStructuredProgress(progress *agentcore.ProgressPayload) string {
	if progress == nil {
		return ""
	}
	switch progress.Kind {
	case agentcore.ProgressThinking:
		return ""
	case agentcore.ProgressToolStart:
		if progress.Tool != "" {
			return fmt.Sprintf("%s → %s", progress.Agent, toolDisplayName(progress.Tool, progress.Args))
		}
	case agentcore.ProgressToolError:
		if progress.Tool != "" {
			toolName := toolDisplayName(progress.Tool, progress.Args)
			if progress.Message != "" {
				return fmt.Sprintf("%s → %s (error: %s)", progress.Agent, toolName, truncateLog(progress.Message, 120))
			}
			return fmt.Sprintf("%s → %s (error)", progress.Agent, toolName)
		}
	case agentcore.ProgressTurnCounter:
		if progress.Agent != "" && progress.Turn > 0 {
			return fmt.Sprintf("%s turn %d", progress.Agent, progress.Turn)
		}
	case agentcore.ProgressSummary:
		if progress.Summary != "" {
			return progress.Summary
		}
	}
	return ""
}

func toolDisplayName(tool string, args json.RawMessage) string {
	switch tool {
	case "save_foundation":
		if typ := foundationTypeFromArgs(args); typ != "" {
			return fmt.Sprintf("%s[%s]", tool, typ)
		}
	}
	return tool
}

func parseToolProgress(ev agentcore.Event) (toolProgress, bool) {
	if ev.Progress == nil {
		return toolProgress{}, false
	}
	switch ev.Progress.Kind {
	case agentcore.ProgressToolStart:
		if ev.Progress.Tool == "" {
			return toolProgress{}, false
		}
		return toolProgress{
			Agent:   ev.Progress.Agent,
			Tool:    ev.Progress.Tool,
			Message: ev.Progress.Message,
		}, true
	case agentcore.ProgressToolError:
		if ev.Progress.Tool == "" {
			return toolProgress{}, false
		}
		return toolProgress{
			Agent:   ev.Progress.Agent,
			Tool:    ev.Progress.Tool,
			Error:   true,
			Message: ev.Progress.Message,
		}, true
	default:
		return toolProgress{}, false
	}
}

func parseContextProgress(ev agentcore.Event) (contextProgress, bool) {
	if ev.Progress == nil || ev.Progress.Kind != agentcore.ProgressContext {
		return contextProgress{}, false
	}
	var payload struct {
		Tokens          int     `json:"tokens"`
		ContextWindow   int     `json:"context_window"`
		Percent         float64 `json:"percent"`
		Scope           string  `json:"scope"`
		Strategy        string  `json:"strategy"`
		ActiveMessages  int     `json:"active_messages"`
		SummaryMessages int     `json:"summary_messages"`
		CompactedCount  int     `json:"compacted_count"`
		KeptCount       int     `json:"kept_count"`
	}
	if len(ev.Progress.Meta) > 0 {
		if err := json.Unmarshal(ev.Progress.Meta, &payload); err != nil {
			return contextProgress{}, false
		}
	}
	return contextProgress{
		Agent:           ev.Progress.Agent,
		Tokens:          payload.Tokens,
		ContextWindow:   payload.ContextWindow,
		Percent:         payload.Percent,
		Scope:           payload.Scope,
		Strategy:        payload.Strategy,
		ActiveMessages:  payload.ActiveMessages,
		SummaryMessages: payload.SummaryMessages,
		CompactedCount:  payload.CompactedCount,
		KeptCount:       payload.KeptCount,
	}, true
}

func foundationTypeFromArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var payload struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Type)
}

func foundationTypeFromResult(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var payload struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Type)
}

func foundationStepLabel(typ string) string {
	switch typ {
	case "premise":
		return "故事前提"
	case "outline":
		return "章节大纲"
	case "layered_outline":
		return "卷弧大纲"
	case "characters":
		return "角色档案"
	case "world_rules":
		return "世界规则"
	case "expand_arc":
		return "弧章节展开"
	case "append_volume":
		return "下一卷规划"
	case "update_compass":
		return "故事指南针"
	default:
		return "基础设定"
	}
}

func toolStartPreview(tool string, args json.RawMessage) string {
	switch tool {
	case "save_foundation":
		return foundationPreview(args)
	default:
		return ""
	}
}

func foundationPreview(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var payload struct {
		Type    string          `json:"type"`
		Volume  int             `json:"volume"`
		Arc     int             `json:"arc"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return ""
	}

	label := foundationStepLabel(payload.Type)
	switch payload.Type {
	case "layered_outline":
		if n := jsonArrayLen(payload.Content); n > 0 {
			return fmt.Sprintf("正在保存%s，当前包含 %d 卷。", label, n)
		}
	case "outline", "characters", "world_rules":
		if n := jsonArrayLen(payload.Content); n > 0 {
			return fmt.Sprintf("正在保存%s，当前包含 %d 项。", label, n)
		}
	case "expand_arc":
		if n := jsonArrayLen(payload.Content); n > 0 && payload.Volume > 0 && payload.Arc > 0 {
			return fmt.Sprintf("正在保存%s：第 %d 卷第 %d 弧，共 %d 章。", label, payload.Volume, payload.Arc, n)
		}
		if payload.Volume > 0 && payload.Arc > 0 {
			return fmt.Sprintf("正在保存%s：第 %d 卷第 %d 弧。", label, payload.Volume, payload.Arc)
		}
	case "append_volume":
		if idx := jsonObjectIntField(payload.Content, "index"); idx > 0 {
			return fmt.Sprintf("正在保存%s：第 %d 卷。", label, idx)
		}
	case "update_compass":
		if direction := jsonObjectStringField(payload.Content, "ending_direction"); direction != "" {
			return fmt.Sprintf("正在保存%s：%s", label, truncateLog(direction, 48))
		}
	}

	if label == "" {
		return ""
	}
	return fmt.Sprintf("正在保存%s。", label)
}

func foundationResultSummary(result json.RawMessage) string {
	if len(result) == 0 {
		return "save_foundation.done"
	}
	var payload struct {
		Type      string   `json:"type"`
		Remaining []string `json:"remaining"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return "save_foundation.done"
	}

	name := toolDisplayName("save_foundation", mustMarshalJSON(map[string]any{"type": payload.Type}))
	if len(payload.Remaining) == 0 {
		return name + ".done"
	}
	return fmt.Sprintf("%s.done (remaining: %s)", name, strings.Join(payload.Remaining, ", "))
}

func jsonArrayLen(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0
	}
	return len(arr)
}

func jsonObjectIntField(raw json.RawMessage, field string) int {
	if len(raw) == 0 {
		return 0
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0
	}
	value, ok := obj[field]
	if !ok {
		return 0
	}
	switch n := value.(type) {
	case float64:
		return int(n)
	default:
		return 0
	}
}

func jsonObjectStringField(raw json.RawMessage, field string) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	value, ok := obj[field]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func mustMarshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// extractLoadingSummary 从 novel_context 的返回 JSON 中提取 _loading_summary 字段。
func extractLoadingSummary(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var data struct {
		Summary string `json:"_loading_summary"`
	}
	if err := json.Unmarshal(result, &data); err != nil {
		return ""
	}
	return data.Summary
}

// logSubAgentResult 从 subagent 结果中提取 usage 和 error，分别记录结构化日志。
func logSubAgentResult(result json.RawMessage, emit emitFn) {
	if len(result) == 0 {
		slog.Debug("subagent 返回空结果", "module", "tool")
		return
	}
	var data struct {
		Output string `json:"output"`
		Error  string `json:"error"`
		Usage  struct {
			Input      int     `json:"input"`
			Output     int     `json:"output"`
			CacheRead  int     `json:"cache_read"`
			CacheWrite int     `json:"cache_write"`
			Cost       float64 `json:"cost"`
			Turns      int     `json:"turns"`
			Tools      int     `json:"tools"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(result, &data); err != nil {
		slog.Debug("subagent 结果解析失败", "module", "tool", "raw", truncateLog(string(result), 200))
		return
	}

	u := data.Usage
	slog.Info("subagent usage", "module", "tool",
		"input", u.Input, "output", u.Output,
		"cache_read", u.CacheRead, "turns", u.Turns, "tools", u.Tools)

	if data.Error != "" {
		if isUserCanceledText(data.Error) {
			slog.Info("subagent 已取消",
				"module", "tool",
				"raw_error", data.Error,
				"raw_result", string(result))
			return
		}
		slog.Error("subagent 错误", "module", "tool", "err", data.Error)
		if emit != nil {
			emit(UIEvent{Time: time.Now(), Category: "ERROR",
				Summary: "subagent: " + truncateLog(data.Error, 80), Level: "error"})
		}
		return
	}

	slog.Debug("subagent 完成", "module", "tool", "output", truncateLog(data.Output, 200))
	if emit != nil {
		emit(UIEvent{Time: time.Now(), Category: "TOOL", Summary: "subagent.done", Level: "info"})
	}
}

func extractToolErrorText(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(result, &plain); err == nil {
		return plain
	}
	var obj struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(result, &obj); err == nil {
		switch {
		case obj.Error != "":
			return obj.Error
		case obj.Message != "":
			return obj.Message
		case obj.Detail != "":
			return obj.Detail
		}
	}
	return truncateLog(string(result), 160)
}

func isUserCanceledText(text string) bool {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return false
	}
	return strings.Contains(text, "context canceled") ||
		strings.Contains(text, "request interrupted by user")
}

func agentLabel(name string) string {
	switch name {
	case "architect_short", "architect_mid", "architect_long":
		return "Architect 规划中"
	case "writer":
		return "Writer 创作中"
	case "editor":
		return "Editor 审阅中"
	default:
		return name
	}
}

func truncateLog(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// contextRewriteCallback 创建上下文重写的可观测回调，用于 slog、运行时边界记录和 TUI 事件。
func contextRewriteCallback(agent string, emit emitFn, appendBoundary func(domain.RuntimeQueueItem)) func(corecontext.RewriteEvent) {
	return func(ev corecontext.RewriteEvent) {
		boundaryKind := runtimeContextBoundaryKind(ev)
		attrs := []any{
			"module", "context",
			"agent", agent,
			"kind", boundaryKind,
			"reason", ev.Reason,
			"strategy", ev.Strategy,
			"committed", ev.Committed,
			"tokens_before", ev.TokensBefore,
			"tokens_after", ev.TokensAfter,
		}
		if info := ev.Info; info != nil {
			attrs = append(attrs,
				"msgs_before", info.MessagesBefore,
				"msgs_after", info.MessagesAfter,
				"compacted", info.CompactedCount,
				"kept", info.KeptCount,
				"split_turn", info.IsSplitTurn,
				"incremental", info.IsIncremental,
				"summary_runes", info.SummaryLen,
				"duration_ms", info.Duration.Milliseconds(),
			)
		}
		slog.Warn("上下文重写", attrs...)

		if appendBoundary != nil {
			boundary := domain.RuntimeContextBoundary{
				Agent:        agent,
				Kind:         boundaryKind,
				Reason:       ev.Reason,
				Strategy:     ev.Strategy,
				Committed:    ev.Committed,
				TokensBefore: ev.TokensBefore,
				TokensAfter:  ev.TokensAfter,
			}
			if info := ev.Info; info != nil {
				boundary.MessagesBefore = info.MessagesBefore
				boundary.MessagesAfter = info.MessagesAfter
				boundary.CompactedCount = info.CompactedCount
				boundary.KeptCount = info.KeptCount
				boundary.SplitTurn = info.IsSplitTurn
				boundary.Incremental = info.IsIncremental
				boundary.SummaryRunes = info.SummaryLen
			}
			appendBoundary(domain.RuntimeQueueItem{
				Time:     time.Now(),
				Kind:     domain.RuntimeQueueContextEdge,
				Priority: domain.RuntimePriorityControl,
				Agent:    agent,
				Category: boundaryKind,
				Summary:  formatContextRewriteSummary(agent, ev, boundaryKind),
				Payload:  boundary,
			})
		}

		if emit == nil {
			return
		}
		summary := formatContextRewriteSummary(agent, ev, boundaryKind)
		emit(UIEvent{Time: time.Now(), Category: "COMPACT", Summary: summary, Level: "warn"})
	}
}

func runtimeContextBoundaryKind(ev corecontext.RewriteEvent) string {
	switch {
	case ev.Reason == "overflow":
		return "recovered"
	case ev.Committed:
		return "compacted"
	default:
		return "projected"
	}
}

func formatContextRewriteSummary(agent string, ev corecontext.RewriteEvent, boundaryKind string) string {
	ratio := 0
	if ev.TokensBefore > 0 {
		ratio = ev.TokensAfter * 100 / ev.TokensBefore
	}
	label := "临时投影"
	switch boundaryKind {
	case "compacted":
		label = "已提交压缩"
	case "recovered":
		label = "恢复压缩"
	}
	summary := fmt.Sprintf("%s %s: %s %d→%d tok (%d%%)",
		agent, label, ev.Strategy, ev.TokensBefore, ev.TokensAfter, ratio)
	if info := ev.Info; info != nil {
		summary += fmt.Sprintf(" %d→%d msgs 摘要%d字 耗时%s",
			info.MessagesBefore, info.MessagesAfter,
			info.SummaryLen, info.Duration.Round(time.Millisecond))
		if info.IsSplitTurn {
			summary += " [split]"
		}
		if info.IsIncremental {
			summary += " [增量]"
		}
	}
	return summary
}
