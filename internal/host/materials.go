package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// MaterialsCandidate 是 LLM 产出的素材候选条目。LLM 输出 JSON 时按这个结构。
// 用户筛选后，TUI 调 Host.MaterialsApprove 把选中的批量落盘。
type MaterialsCandidate struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Source   string `json:"source,omitempty"`
	Reason   string `json:"reason,omitempty"` // LLM 自述为什么这条值得入素材库
}

const materialsCollectorPrompt = `你是一个小说素材收集助手。

用户会给你一个小说创作需求（题材、风格、长度等）。你的任务：基于需求 + 自身知识 + 必要时调 web_search，**搜集 8-15 条具体可用的素材**，让用户挑选入库。

## 素材类型

每条素材按下列 category 归类：

- naming：命名表（人名/地名/组织名/技能名/作品名 候选清单，至少 5 个）
- terminology：术语表（流派/技术/官职/职业/招式 等术语释义）
- visual：视觉锚点（场景/服装/道具/色彩/光影 的具体描写素材）
- setting：设定资料（力量体系/历史/地理/经济 等世界规则）
- reference：参考资料（同类作品分析 / 真实事件参考 / 流派套路）

## 要求

- **具体**：title 一句话标题；content 是真实可用的素材内容（命名表给名字清单，不要给"取名的注意事项"）
- **多样**：每条素材聚焦一个角度，避免重复（同一题材不要重复出现"霓虹配色"两次）
- **量力**：8-15 条；少了不够挑，多了稀释质量
- **真实**：不确定的资料要标"（参考性内容）"，不要编造看似真实的人名/作品名

## 输出格式

直接输出一段 JSON，外层是 {"items": [...]}，items 是数组，每个元素含 category/title/content/source 字段。**不要任何额外文字、不要 ` + "```" + `json 围栏、不要 XML 标签**。

source 字段格式："web_search:query=xxx" / "builtin" / "skill:name"。

示例：
{"items":[{"category":"naming","title":"赛博朋克企业名候选","content":"Yorinaga-Kessler / Praxis Holdings / Helix Dynamics / Nightingale Cybernetics / Kuznetsov-Vol","source":"builtin"},{"category":"visual","title":"霓虹配色方案","content":"主色 #ff0080 配青蓝 #00d4ff；招牌用洋红+琥珀双色调；雨夜街道反光用紫色漫射。","source":"builtin"}]}`

const (
	materialsCollectorMaxTokens = 4096
	materialsCollectorTimeout   = 120 * time.Second
	materialsMinCandidates      = 1 // 用户至少要看到 1 条候选才算收集成功；少于阈值报错
)

// materialsCollect 单轮调用 LLM 收集素材候选。
//
// 设计：
//   - 单轮：MVP 不实现工具往返。LLM 若调 web_search 而不输出文本 → 报错让用户重试。
//     下一版加 mini loop（参考 cocreate 的实现）。
//   - 流式：onProgress 实时回传 thinking + 文本片段，TUI 可以渲染加载动画
//   - webSearch 允许传 nil（Provider 不支持时降级为纯知识模式）
//
// 返回 (candidates, rawResponse, err)：rawResponse 给调用方用于失败时 debug。
func materialsCollect(ctx context.Context, models *bootstrap.ModelSet, userPrompt string, onProgress func(kind, text string), webSearch *tools.WebSearchTool) ([]MaterialsCandidate, string, error) {
	userPrompt = strings.TrimSpace(userPrompt)
	if userPrompt == "" {
		return nil, "", fmt.Errorf("materials collect: empty user prompt")
	}

	model := models.ForRole("thinking")
	ctx, cancel := context.WithTimeout(ctx, materialsCollectorTimeout)
	defer cancel()

	msgs := []agentcore.Message{
		agentcore.SystemMsg(materialsCollectorPrompt),
		agentcore.UserMsg(userPrompt),
	}

	var toolSpecs []agentcore.ToolSpec
	if webSearch != nil {
		toolSpecs = []agentcore.ToolSpec{webSearchToolSpec()}
	}

	streamCh, err := model.GenerateStream(ctx, msgs, toolSpecs, agentcore.WithMaxTokens(materialsCollectorMaxTokens))
	if err != nil {
		return nil, "", fmt.Errorf("materials generate: %w", err)
	}

	var (
		text     strings.Builder
		thinking strings.Builder
		finalMsg agentcore.Message
	)
	for ev := range streamCh {
		switch ev.Type {
		case agentcore.StreamEventThinkingDelta:
			thinking.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressThinking, thinking.String())
			}
		case agentcore.StreamEventTextDelta:
			text.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressReply, text.String())
			}
		case agentcore.StreamEventDone:
			finalMsg = ev.Message
		case agentcore.StreamEventError:
			if ev.Err != nil {
				return nil, "", fmt.Errorf("materials generate: %w", ev.Err)
			}
			return nil, "", fmt.Errorf("materials generate failed")
		}
	}

	raw := text.String()
	if raw == "" && finalMsg.TextContent() != "" {
		raw = finalMsg.TextContent()
	}
	if strings.TrimSpace(raw) == "" {
		return nil, raw, fmt.Errorf("LLM 未输出文本（可能调用了 web_search 但 MVP 版未实现工具往返，请重试）")
	}

	candidates, err := parseMaterialsJSON(raw)
	if err != nil {
		return nil, raw, fmt.Errorf("parse materials json: %w", err)
	}
	if len(candidates) < materialsMinCandidates {
		return candidates, raw, fmt.Errorf("no candidates parsed from response")
	}
	return candidates, raw, nil
}

// parseMaterialsJSON 容忍 LLM 输出可能的杂质（前导思考文字、代码围栏）。
// 先整段 parse；失败则提取首尾大括号之间的子串再 parse。
func parseMaterialsJSON(raw string) ([]MaterialsCandidate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty response")
	}

	// 剥 ```json ... ``` 围栏（即便 prompt 明确禁止，仍偶尔出现）
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```")
		// 去掉可能的 "json" 标识符
		raw = strings.TrimPrefix(raw, "json")
		raw = strings.Trim(raw, "\n \t")
		if idx := strings.LastIndex(raw, "```"); idx >= 0 {
			raw = raw[:idx]
		}
		raw = strings.TrimSpace(raw)
	}

	// 直接 parse
	var envelope struct {
		Items []MaterialsCandidate `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Items != nil {
		return envelope.Items, nil
	}

	// 提取首尾大括号之间的内容再试
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON envelope found")
	}
	block := raw[start : end+1]
	if err := json.Unmarshal([]byte(block), &envelope); err != nil {
		return nil, fmt.Errorf("parse JSON block: %w", err)
	}
	return envelope.Items, nil
}
