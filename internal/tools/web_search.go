package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
)

// WebSearchTool 通过代理触发 Anthropic 服务端 web_search 工具。
//
// 实现思路（方案 E）：本工具是一个客户端 Tool，但内部不发 Tavily/DuckDuckGo，
// 而是构造一个带 tools:[{type:"web_search_20250305"}] 的 Anthropic /v1/messages
// 请求打到代理，由代理路由到的上游（各 provider 等）在服务端执行
// 真实搜索，再把模型基于结果生成的总结 + 提取出的链接列表返回给
// architect/coordinator。
//
// ★ 路由不确定性：代理按 priority/weight 自己调度，ainovel-cli 这边无法假设
//   一定走某个特定 provider。如果路由到的上游不支持 web_search_20250305，本工具返回空结果
//   + hint（而非 error），让 architect 优雅降级到"无外部资料"继续工作，避免
//   死循环重试。
//
// 选这条路而非 litellm 的 extra_body 透传，是因为 litellm v1.8.5 的 anthropic
// provider 用强类型 anthropicRequest 序列化，ProviderOptions 只识别 metadata
// 两个 key，extra_body.tools 会被静默丢弃或直接报 "unsupported provider option"。
//
// 配置由 build.go 从 cfg.Providers[主provider] + cfg.ModelName 推导，零额外配置。
type WebSearchTool struct {
	baseURL     string        // 代理入口，如 http://localhost:23000（不带 /v1）
	apiKey      string        // 代理用户 key（x-api-key 头）
	model       string        // 代理路由用模型名（代理会按自己的 weight/priority 重定向到真实 provider）
	httpClient  *http.Client
	maxResults  int // 提取的链接上限，避免响应过大
	maxAttempts int // 总尝试次数（首次 + 重试），覆盖暂时性超时/5xx/429
}

func NewWebSearchTool(baseURL, apiKey, model string) *WebSearchTool {
	return &WebSearchTool{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		httpClient: &http.Client{
			// 单次 HTTP 超时。实测代理→上游的 web_search 中位数 28s、
			// P95 ~38s（见 /tmp/ws_stress_result.json，28 样本）；30s 会切断约
			// 43% 的真实请求，导致 LLM 看到"工具执行失败: deadline exceeded"
			// 后转述成"上游服务超时"。给 60s 留 ~50% 余量，再叠加下面的重试。
			Timeout: 60 * time.Second,
		},
		maxResults:  8,
		maxAttempts: 3, // 首次 + 至多 2 次重试，覆盖暂时性 5xx/超时/网络抖动
	}
}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "联网搜索：获取外部资料、流派套路、时事常识、参考资料等。返回模型基于搜索结果生成的总结和原始链接。注意：能否真正联网取决于代理当前路由到的上游是否支持服务端 web_search；不支持时返回空结果与 hint，调用方应基于已有知识继续工作。"
}
func (t *WebSearchTool) Label() string { return "联网搜索" }

// 纯读工具（不改 store 状态），可被并发调度。
func (t *WebSearchTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *WebSearchTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *WebSearchTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("query", schema.String("搜索关键词（中英文均可，过长会被模型自行精炼）")).Required(),
		schema.Property("max_results", schema.Int("返回链接上限，默认 8（已截取最相关的前 N 条）")),
	)
}

func (t *WebSearchTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if a.MaxResults > 0 {
		t.maxResults = a.MaxResults
	}

	payload := map[string]any{
		"model":      t.model,
		"max_tokens": 2048,
		// 服务端搜索工具：上游 provider 把它映射成内部 web_search_prime。
		// max_uses 限制单次请求内模型自主发起的搜索次数，避免它把请求
		// 拖成长链路。5 是经验值（之前 curl 测试中模型用了 4 次）。
		"tools": []map[string]any{
			{
				"type":     "web_search_20250305",
				"name":     "web_search",
				"max_uses": 5,
			},
		},
		"messages": []map[string]any{
			{
				"role": "user",
				"content": fmt.Sprintf(
					"请用 web_search 工具联网搜索：%s\n\n"+
						"要求：\n"+
						"1. 优先搜索 2025-2026 年的资料；\n"+
						"2. 阅读搜索结果后，给出一份精炼的中文总结（聚焦事实，不要空话），不超过 500 字；\n"+
						"3. 总结后另起一行，列出参考链接（标题 + URL），最多 %d 条。\n\n"+
						"如果搜索失败或无结果，直接说明，不要编造。",
					a.Query, t.maxResults,
				),
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	// ctx 预算检查：父 ctx（如 cocreate 300s 总预算）剩余时间不足以完成一次完整调用时，
	// 直接降级，避免拖死整个对话——否则父 ctx 超时会切断正在跑的 HTTP 请求，
	// 错误冒泡到 LLM 那里被转述成"上游服务超时"，体验更差。
	// 阈值用 httpClient.Timeout（60s）：剩余不到单次超时就不发，宁可早降级不冒险。
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < t.httpClient.Timeout {
			slog.Warn("web_search 跳过:ctx 剩余预算不足",
				"module", "tool", "query", a.Query,
				"remaining", remaining.Round(time.Second),
				"need", t.httpClient.Timeout)
			return json.Marshal(map[string]any{
				"query":   a.Query,
				"summary": "",
				"links":   []map[string]string{},
				"count":   0,
				"hint": fmt.Sprintf(
					"本次对话剩余时间不足（剩 %s，需 %s），跳过联网搜索。"+
						"请基于已有知识继续工作，不要重试本工具。",
					remaining.Round(time.Second), t.httpClient.Timeout,
				),
			})
		}
	}

	// 重试循环：覆盖暂时性网络抖动 / 偶发 5xx / 单次超时。
	// 4xx（除 429）不重试——通常是请求本身问题（上游不支持、参数错误），
	// 重试也是浪费。注意：architect 收到 error 会触发 subagentMaxRetries 死循环，
	// 所以最终失败仍走降级 hint 路径，绝不让 error 冒泡。
	url := t.baseURL + "/v1/messages"
	var lastHTTP int
	var lastErr error
	var lastBody []byte
	for attempt := 1; attempt <= t.maxAttempts; attempt++ {
		httpCode, respBody, callErr := t.doOnce(ctx, url, body)
		lastHTTP, lastBody, lastErr = httpCode, respBody, callErr

		// 成功路径
		if callErr == nil && httpCode >= 200 && httpCode < 300 {
			summary, links := parseAnthropicSearchResponse(respBody, t.maxResults)
			result := map[string]any{
				"query":   a.Query,
				"summary": summary,
				"links":   links,
				"count":   len(links),
			}
			if summary == "" && len(links) == 0 {
				slog.Warn("web_search 返回空结果", "module", "tool",
					"query", a.Query, "status", httpCode, "attempt", attempt)
				result["hint"] = "搜索未返回可用内容（可能是网络问题或上游限流）。可换关键词重试，或基于已有知识继续。"
			}
			if attempt > 1 {
				slog.Info("web_search 重试后成功", "module", "tool",
					"query", a.Query, "attempt", attempt)
			}
			return json.Marshal(result)
		}

		// 失败：判断是否可重试
		retryable := isRetryableWebSearchErr(callErr, httpCode)
		slog.Warn("web_search 调用失败",
			"module", "tool", "query", a.Query,
			"attempt", attempt, "max_attempts", t.maxAttempts,
			"http", httpCode, "retryable", retryable, "err", callErr)
		if !retryable || attempt == t.maxAttempts {
			break
		}
		// 简易线性退避：第 1 次重试前睡 1s，第 2 次前睡 2s。不在 ctx 取消时傻等。
		backoff := time.Duration(attempt) * time.Second
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			break
		case <-time.After(backoff):
		}
	}

	// 全部失败：降级返回 hint（绝不冒泡 error，避免 architect 死循环重试）。
	snippet := string(lastBody)
	if len(snippet) > 500 {
		snippet = snippet[:500] + "...(truncated)"
	}
	slog.Warn("web_search 全部重试失败，降级返回空结果",
		"module", "tool", "query", a.Query,
		"last_http", lastHTTP, "last_err", lastErr, "snippet", snippet)
	hint := buildFallbackHint(lastHTTP, lastErr)
	return json.Marshal(map[string]any{
		"query":   a.Query,
		"summary": "",
		"links":   []map[string]string{},
		"count":   0,
		"hint":    hint,
	})
}

// doOnce 发一次 HTTP 请求,返回 (http_code, body, err)。
// 抽出来让 Execute 的重试循环更清爽。
func (t *WebSearchTool) doOnce(ctx context.Context, url string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", t.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("web_search request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, data, nil
}

// isRetryableWebSearchErr 判断错误是否值得重试。
// 可重试：网络错误（含超时）、HTTP 5xx、429。
// 不可重试：HTTP 4xx（除 429）—— 通常是请求本身问题。
// 注意：HTTP 200 但 callErr != nil（如读 body 中途断开）也算可重试。
func isRetryableWebSearchErr(callErr error, httpCode int) bool {
	if callErr != nil {
		return true
	}
	if httpCode >= 500 || httpCode == 429 {
		return true
	}
	return false
}

// buildFallbackHint 根据失败原因生成给 LLM 看的 hint 文本。
// 始终告诉 LLM"不要重试本工具",避免它在 mini loop 里反复调。
func buildFallbackHint(httpCode int, lastErr error) string {
	if lastErr != nil {
		// 多半是 timeout / connection reset / context canceled
		return fmt.Sprintf(
			"web_search 调用出错：%s。本次跳过联网搜索，"+
				"请基于已有知识继续工作，不要重试本工具。", lastErr.Error(),
		)
	}
	return fmt.Sprintf(
		"上游返回 HTTP %d（可能路由到的 provider 不支持 web_search_20250305 服务端工具）。"+
			"本次跳过联网搜索，请基于已有知识继续工作，不要重试本工具。",
		httpCode,
	)
}

// parseAnthropicSearchResponse 从 Anthropic /v1/messages 响应里提取
// 模型总结（text 块）和参考链接（tool_result.content 中正则提取的 URL）。
//
// 上游 provider 返回的 tool_result.content 是 Python repr 形式的字符串（' 而非 "），
// 不能直接 JSON parse；用正则提取 link/title 字段最稳健。
func parseAnthropicSearchResponse(data []byte, maxResults int) (string, []map[string]string) {
	var parsed struct {
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text,omitempty"`
			Content json.RawMessage `json:"content,omitempty"` // tool_result.content 可能是 string 或 array
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", nil
	}

	var summaryParts []string
	for _, block := range parsed.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			summaryParts = append(summaryParts, block.Text)
		}
	}
	summary := strings.Join(summaryParts, "\n\n")

	// 链接提取：从原始响应里抓所有 https?:// 链接 + 邻近的 title。
	// 优先从 tool_result.content 区域抓，但简单起见全域抓取然后去重。
	links := extractLinks(string(data), maxResults)

	return summary, links
}

// linkRe 匹配 http(s) 链接。仅抓标准化 URL 字符（含中文路径），不含引号/空格。
var linkRe = regexp.MustCompile(`https?://[^\s'"<>\]\\]+`)

// titleRe 在原始文本中抓 'title': '...' 形式（Python repr）或 "title": "..."（标准 JSON）。
var titleRe = regexp.MustCompile(`(?:["']title["']\s*:\s*["'])([^"']{1,200})["']`)

func extractLinks(raw string, maxResults int) []map[string]string {
	if maxResults <= 0 {
		maxResults = 8
	}

	urls := linkRe.FindAllString(raw, -1)
	if len(urls) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	titles := titleRe.FindAllStringSubmatch(raw, -1)

	out := make([]map[string]string, 0, maxResults)
	titleIdx := 0
	for _, u := range urls {
		// 去重 + 跳过明显的非资源链接（如 schema.org 之类的本体）
		if seen[u] || isNoiseURL(u) {
			continue
		}
		seen[u] = true
		item := map[string]string{"link": u}
		if titleIdx < len(titles) {
			item["title"] = titles[titleIdx][1]
			titleIdx++
		}
		out = append(out, item)
		if len(out) >= maxResults {
			break
		}
	}
	return out
}

// isNoiseURL 过滤掉搜索结果中常见的"伪结果"链接（schema、tracker、API 文档等）。
func isNoiseURL(u string) bool {
	for _, skip := range []string{
		"schema.org",
		"openid",
		"/api/",
		"google.com/sorry",
		"google.com/url",
		"web.archive.org",
	} {
		if strings.Contains(u, skip) {
			return true
		}
	}
	return false
}
