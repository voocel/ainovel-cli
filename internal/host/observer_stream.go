package host

import (
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// handleSubagentDelta 分流 subagent 的文本与工具调用参数：
// - DeltaText 直接作为 markdown 流出
// - DeltaToolCall 只对已知的长内容工具（如 draft_chapter.content）抽取字段流出；其他工具的参数 JSON 全部丢弃
func (o *observer) handleSubagentDelta(p *agentcore.ProgressPayload) {
	if p.DeltaKind != agentcore.DeltaToolCall {
		o.emitStreamDelta(p.Delta, false)
		return
	}
	if p.Tool == "" {
		return // 工具名未就绪，下一个 delta 再试
	}

	// 流式识别到工具名时提前发 TOOL 进行中事件，让 spinner 覆盖整段 LLM 生成期间
	// （否则 draft_chapter 这类工具的"进行中"只在真实 Execute 的几十毫秒里显示）。
	// 真正的 ProgressToolStart 到来时识别到 toolStarts 已有记录，只会补齐 summary。
	o.ensureSubagentToolStarted(p.Agent, p.Tool)
	o.updateToolCallSummaryFromDelta(p.Agent, p.Tool, p.Delta)

	cur, ok := o.streamExtractors[p.Agent]
	// 同工具调用 args 已闭合（顶层 } 命中）后，仍可能收到 trailing delta：
	// 某些 provider（实测）会把单次 args 拆成多个 chunk，
	// 最末一个 chunk 在 `}` 之后还跟着空白或重复字符。此时若按"工具名匹配 +
	// Done 即重建"处理，新 extractor 又会 emit 一次 ✻ header 并把尾段 token
	// 当作新 args 解析。这些 delta 是冗余尾巴，丢弃即可。
	if ok && cur.tool == p.Tool && cur.ext.Done() {
		return
	}
	// 工具名变了或还没建过：新建。
	if !ok || cur.tool != p.Tool {
		ext := newToolExtractor(p.Tool)
		if ext == nil {
			delete(o.streamExtractors, p.Agent)
			return
		}
		cur = &agentExtractor{tool: p.Tool, ext: ext}
		o.streamExtractors[p.Agent] = cur
	}
	if emitted := cur.ext.Feed(p.Delta); emitted != "" {
		if !cur.emittedAny {
			cur.emittedAny = true
			// streamClear 让 extractor 的 ✻ header 落在新 round 起点，配合
			// renderStreamContent 的 HasPrefix("✻") 检查走 renderAgentBlock 高亮
			// 路径；用 ensureStreamParagraphBreak 只插空行不开 round，✻ 仍会被
			// 前面的 thinking/正文包住，落到 renderChapterBlock 用默认色画掉。
			o.streamClear()
			// streamClear 防御性清空了 streamExtractors。当前 cur 还要继续 Feed
			// 本工具调用后续的 delta，必须立刻把它重新登记回去；否则下一段 delta
			// 来时会新建 extractor，从 args 中段开始解析（在嵌套对象的 `{` 处
			// 才进入 psBeforeKey），把 timeline_events.time / foreshadow_updates.id
			// 等当成顶层字段，TUI 上重复出现 ✻ header。
			o.streamExtractors[p.Agent] = cur
		}
		o.emitStreamDelta(emitted, false)
	}
}

func (o *observer) handleCoordinatorToolDelta(ev agentcore.Event) {
	msg, ok := ev.Message.(agentcore.Message)
	if !ok {
		return
	}
	call, ok := latestToolCall(msg)
	if !ok || call.Name == "" {
		return
	}
	if call.Name == "subagent" {
		o.ensureCoordinatorDispatchStarted(call)
		o.updateCoordinatorDispatchSummaryFromDelta(ev.Delta)
		return
	}
	o.ensureCoordinatorToolStarted(call.Name)
	o.updateToolCallSummaryFromDelta("coordinator", call.Name, ev.Delta)
}

func latestToolCall(msg agentcore.Message) (agentcore.ToolCall, bool) {
	calls := msg.ToolCalls()
	if len(calls) == 0 {
		return agentcore.ToolCall{}, false
	}
	return calls[len(calls)-1], true
}

func (o *observer) emitStreamDelta(delta string, thinking bool) {
	if delta == "" {
		return
	}
	if thinking != o.streamThinking {
		o.emitD(utils.ThinkingSep)
		o.streamThinking = thinking
	}
	o.emitD(delta)
	o.streamHasContent = true
	o.streamLastByte = delta[len(delta)-1]
}

// ensureSubagentToolStarted 在流式识别到 tool_call 首次出现时，提前为该 agent
// 登记一次进行中的 TOOL 调用，使事件流的 spinner 覆盖"LLM 流式生成 tool_call
// 参数"这一段时间（通常占调用总耗时的 99%）。args 此时尚不完整，暂以纯工具名
// 为 summary；等真正的 ProgressToolStart 到来时会补齐带参数的 summary。
func (o *observer) ensureSubagentToolStarted(agent, tool string) {
	if agent == "" || tool == "" {
		return
	}
	if _, ok := o.toolStarts[agent]; ok {
		return // 已有进行中调用，幂等
	}
	o.resetStreamArgLabel(agent, tool)
	id := nextEventID()
	o.toolStarts[agent] = &activeCall{
		id:      id,
		start:   time.Now(),
		summary: tool, // 先用纯工具名，ProgressToolStart 到来时可能更新为 tool(第N章)
		depth:   1,
	}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  tool,
		Level:    "info",
		Depth:    1,
	})
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
	})
	o.emitFallbackStreamHeader(tool)
}

func (o *observer) resetStreamArgLabel(agent, tool string) {
	key := streamArgKey(agent, tool)
	delete(o.streamArgPrefixes, key)
	delete(o.streamArgLabels, key)
}

// emitFallbackStreamHeader 给未配置 extractor 的工具补一行 ✻ 标题到流面板。
// 三条路径都要调用以保证一致：
//  1. ensureSubagentToolStarted —— subagent 流式 tool args（DeltaToolCall）
//  2. handleToolUpdate ProgressToolStart —— subagent 非流式 tool args
//  3. handleToolStart —— coordinator 自身工具
//
// 缺任何一条，同一个工具就会"writer 调有 ✻、coordinator 调没 ✻"或反过来。
func (o *observer) emitFallbackStreamHeader(tool string) {
	if _, has := toolDisplays[tool]; has {
		return // 有 extractor，header 由 extractor 自行输出
	}
	o.streamClear()
	o.emitStreamDelta(streamHeaderFallback(tool)+"\n", false)
}

// streamHeaderFallback 为未配置 extractor 的工具生成流式 header 文本，
// 让用户即使对轻量读取类工具也能看到"在调用什么"。
//
// 前缀 "✻ " 是约定的"agent 调度块"标记 — TUI 的 renderStreamContent 见到这个
// 前缀会走 renderAgentBlock 路径渲染（图标 + 高亮 label + 分隔线），
// 否则会落到正文块路径用终端默认色，header 看起来就是普通正文不醒目。
func streamHeaderFallback(tool string) string {
	label := tool
	switch tool {
	case "ask_user":
		label = "向用户提问"
	}
	return "✻ " + label
}

// streamClear 通知 TUI 开启新一轮 streamRound，同时重置与段落分隔相关的状态。
// 逻辑上新 round 是"空 stream"，否则下一次首个 extractor emit 会误补前导空行。
//
// streamThinking 必须一并重置：emitStreamDelta 用 streamThinking 跨调用追踪
// 上一段是不是思考。新 round 内还没输出过任何内容，下一次 emit(thinking=false)
// 不应该再插入 ThinkingSep。否则 fallback header（如 ✻ 读章节）会被 \x02
// 抢先占头，renderStreamContent 的 HasPrefix("✻") 失配，整段落到正文路径
// 再被 ThinkingSep 切分为思考段，title 颜色被画成思考色。
func (o *observer) streamClear() {
	o.emitC()
	o.streamHasContent = false
	o.streamLastByte = 0
	o.streamThinking = false
	// 上一轮的 subagent 结束前 ProgressToolEnd 已 delete，这里防御性清空。
	if len(o.streamExtractors) > 0 {
		o.streamExtractors = make(map[string]*agentExtractor)
	}
}
