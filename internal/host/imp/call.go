package imp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// import 专用 typed-call 内核（RFC §13）。小而专用，不建通用 LLM 工作流框架。
// 三类失败分离：请求层瞬时错误重试；输出层 JSON/校验失败带反馈重问；容量错误（长度截断）
// 不原样重试也不占用语义重试，交调用方决定失败或前缀打捞。
const (
	callMaxSemanticAttempts = 3
	callMaxRequestRetries   = 7
	callMaxRetryDelay       = 60 * time.Second
)

const callRetryHint = "上面的输出不是合法 JSON 或缺少必填字段。只输出一个符合约定 schema 的 JSON 对象，不要任何解释文字，不要 Markdown 代码围栏。"

// callModel 是内核对模型的最小依赖，便于测试注入 mock。
type callModel interface {
	Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error)
}

// errTruncated 表示模型因长度停止（容量错误）。携带原始文本供调用方决定失败或前缀打捞（§9.5）。
type errTruncated struct {
	Raw string
}

func (e *errTruncated) Error() string { return "模型输出被长度截断（stop=length）" }

// errSemantic 表示输出层语义失败（JSON/校验多次仍非法），携带最后一次原始响应，
// 供 runner 统一落 failures/ 失败工件（§14.2），所有语义函数共用。
type errSemantic struct {
	Raw string
	Err error
}

func (e *errSemantic) Error() string { return e.Err.Error() }
func (e *errSemantic) Unwrap() error { return e.Err }

func assistantMsg(text string) agentcore.Message {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(text)}}
}

// callProfile 承载一次结构化调用的能力相关选项（thinking），由 Host 探测的 ModelRuntime 派生；
// 零值表示不发 thinking、走 prompt-only（与无能力信息时等价）。
//
// 结构化输出不发 response_format：litellm 的能力表是 provider 级，而 response_format 支持是
// 模型级事实——经聚合网关按 provider 能力发 json_object 会对不支持的模型直接 HTTP 400
// （实测 openrouter 上 tencent/hy3 只支持 json_schema，json_object 被上游 Novita 拒绝）。
// 管线本就靠 prompt 契约 + extractJSONObject + validate 反馈重问兜底，json_object 只是
// 锦上添花的约束，不值得为它引入整类硬失败。
//
// TODO(json-schema)：RFC §13.2 第 1 级——与仓库其它模型调用点（arbiter 裁定、engine 语义工具等）
// 统一改造为 JSON Schema 约束输出时，在此增加 schema 位并按「模型级」能力核验后组装 callOptions。
type callProfile struct {
	thinking agentcore.ThinkingLevel
	// notify 可选：把请求退避重试/校验重问回显给界面；nil 时静默（§14.1）。
	// retryAt 非零 = 下次重试的截止时刻，UI 据此渲染逐秒倒计时（事件只带截止点，剩余时间渲染时算）。
	notify func(msg string, retryAt time.Time)
	// progress 可选：回显长时阶段的内部推进（切分第 N/M 块、区间摘要 N/M）；nil 时静默。
	// 切分/综合在函数内部逐块/逐区间调用模型，单块可达数分钟，没有它面板整段静默像卡死（§14.1）。
	progress func(current, total int, msg string)
	// log 可选：导入专属日志（logs/import.log）；nil 回退默认 logger。
	log *slog.Logger
}

func (p callProfile) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

// step 回显一条普通进度（长时阶段的内部推进）。
func (p callProfile) step(current, total int, format string, args ...any) {
	if p.progress != nil {
		p.progress(current, total, fmt.Sprintf(format, args...))
	}
}

// say 回显一条长时调用状态。重试可能静默数分钟（指数退避累计 2 分钟以上），
// 不回显用户会误以为卡死。
func (p callProfile) say(format string, args ...any) {
	p.sayRetry(time.Time{}, format, args...)
}

// sayRetry 回显一条带重试截止时刻的状态，供 UI 倒计时。
func (p callProfile) sayRetry(retryAt time.Time, format string, args ...any) {
	if p.notify != nil {
		p.notify(fmt.Sprintf(format, args...), retryAt)
	}
}

// snippet 把多行文本压成单行短摘要供界面回显：合并空白、截到 max 个 rune。
func snippet(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// briefErr 把错误压成单行短文本供界面回显（完整错误链仍走日志与失败工件）。
// 适配器结构化事实放前面：截断时优先保住"哪类错、什么状态码"，网关 message 可牺牲。
func briefErr(err error) string {
	s := err.Error()
	if d := modelErrDetail(err); d != "" {
		s = d + "：" + s
	}
	return snippet(s, 100)
}

// errTypeLabels 把 litellm 错误分类翻成一眼可读的中文短标签。
var errTypeLabels = map[litellm.ErrorType]string{
	litellm.ErrorTypeAuth:            "鉴权失败",
	litellm.ErrorTypeRateLimit:       "限流",
	litellm.ErrorTypeNetwork:         "网络错误",
	litellm.ErrorTypeValidation:      "请求参数非法",
	litellm.ErrorTypeProvider:        "上游服务错误",
	litellm.ErrorTypeTimeout:         "超时",
	litellm.ErrorTypeQuota:           "配额不足",
	litellm.ErrorTypeModel:           "模型不可用",
	litellm.ErrorTypeInternal:        "内部错误",
	litellm.ErrorTypeContextOverflow: "上下文超限",
	litellm.ErrorTypeOverloaded:      "上游过载",
	litellm.ErrorTypeContentFilter:   "内容过滤拦截",
}

// modelErrDetail 从错误链提取适配器的结构化事实（错误分类、HTTP 状态、provider、模型）。
// 网关的 message 常常只有一句空泛的 "Provider returned error"，单靠它无法判断是配置错、
// 上游故障还是限流；这些事实 litellm 一直带着，只是不进 Error() 文案。agentcore 适配器的
// Unwrap 明确允许知道 litellm 的调用方 errors.As 取原始错误。非模型调用错误返回空串。
func modelErrDetail(err error) string {
	var le *litellm.LiteLLMError
	if !errors.As(err, &le) {
		return ""
	}
	parts := make([]string, 0, 4)
	if label := errTypeLabels[le.Type]; label != "" {
		parts = append(parts, label)
	}
	if le.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", le.StatusCode))
	}
	if le.Provider != "" {
		parts = append(parts, le.Provider)
	}
	if le.Model != "" {
		parts = append(parts, le.Model)
	}
	return strings.Join(parts, "，")
}

// callOptions 组装本次调用的 CallOption：始终带输出上限；按能力可选 thinking。
// thinking 仅在非 Auto 时发送——对不支持 thinking 的模型发任何等级（含 off）都是非法参数（与 arbiter 同策略）。
func (p callProfile) callOptions(maxTokens int) []agentcore.CallOption {
	opts := []agentcore.CallOption{agentcore.WithMaxTokens(maxTokens)}
	if p.thinking != agentcore.ThinkingAuto {
		opts = append(opts, agentcore.WithThinking(p.thinking))
	}
	return opts
}

// callStructured 发起一次结构化调用：system + payload → 解析进 T → validate。
// 校验失败带反馈重问（最多 callMaxSemanticAttempts）；长度截断返回 *errTruncated。
// prof 决定是否发 thinking / JSON Object 约束；无论 provider 是否约束输出，都走同一套 extractJSONObject + validate 兜底。
func callStructured[T any](ctx context.Context, m callModel, systemPrompt, payload string, maxTokens int, prof callProfile, validate func(*T) error) (T, error) {
	var zero T
	if m == nil {
		return zero, fmt.Errorf("imp: model 未配置")
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(payload),
	}
	opts := prof.callOptions(maxTokens)
	var lastErr error
	var lastRaw string
	attempts := 0
	for attempt := 1; attempt <= callMaxSemanticAttempts; attempt++ {
		attempts = attempt
		resp, err := generateWithRetry(ctx, m, prof, messages, opts...)
		if err != nil {
			if d := modelErrDetail(err); d != "" {
				return zero, fmt.Errorf("imp: 模型调用失败（%s）：%w", d, err)
			}
			return zero, fmt.Errorf("imp: 模型调用失败：%w", err)
		}
		if resp == nil {
			lastErr = fmt.Errorf("模型返回空响应")
			break
		}
		raw := resp.Message.TextContent()
		lastRaw = raw
		if resp.Message.StopReason == agentcore.StopReasonLength {
			return zero, &errTruncated{Raw: raw}
		}
		out, verr := parseStructured[T](raw, validate)
		if verr == nil {
			return *out, nil
		}
		lastErr = verr
		messages = append(messages, assistantMsg(raw),
			agentcore.UserMsg(callRetryHint+"\n错误："+verr.Error()))
		if ctx.Err() != nil {
			break
		}
		if attempt < callMaxSemanticAttempts {
			prof.say("输出校验未通过（%s），带错误反馈重问（尝试 %d/%d）", briefErr(verr), attempt+1, callMaxSemanticAttempts)
		}
		prof.logger().Warn("imp 结构化输出重试", "attempt", attempt, "err", verr)
	}
	// 用户取消不是语义失败：不落 failures/ 工件、不报「N 次尝试」误导排查方向。
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	return zero, &errSemantic{Raw: lastRaw,
		Err: fmt.Errorf("imp: 结构化输出失败（%d 次尝试）：%w", attempts, lastErr)}
}

// parseStructured 从原始文本截取 JSON 对象、解析进 T 并 validate。
func parseStructured[T any](raw string, validate func(*T) error) (*T, error) {
	s := extractJSONObject(raw)
	if s == "" {
		return nil, fmt.Errorf("输出中未找到 JSON 对象")
	}
	var out T
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("解析 JSON：%w", err)
	}
	if validate != nil {
		if err := validate(&out); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// generateWithRetry 只重试适配器标记 retryable 的瞬时错误，遵守 Retry-After/指数退避。
// 鉴权/权限/模型不支持等终止错误立即返回（§13.3）。每次退避回显一条带截止时刻的状态，UI 倒计时。
func generateWithRetry(ctx context.Context, m callModel, prof callProfile, messages []agentcore.Message, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= callMaxRequestRetries; attempt++ {
		resp, err := m.Generate(ctx, messages, nil, opts...)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || !isRetryable(err) || attempt == callMaxRequestRetries {
			return nil, err
		}
		delay := retryDelay(err, attempt)
		prof.sayRetry(time.Now().Add(delay), "模型请求失败（%s），重试第 %d/%d 次", briefErr(err), attempt+1, callMaxRequestRetries)
		// 面板回显截断到单行，完整错误链只有日志能承载（重试后成功的请求不会落失败工件）。
		prof.logger().Warn("imp 模型请求重试", "attempt", attempt+1, "max", callMaxRequestRetries, "delay", delay, "err", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func isRetryable(err error) bool {
	var retryable agentcore.RetryableError
	return errors.As(err, &retryable) && retryable.Retryable()
}

func retryDelay(err error, attempt int) time.Duration {
	var hinter agentcore.RetryHinter
	if errors.As(err, &hinter) {
		if d := hinter.RetryAfter(); d > 0 {
			if d > callMaxRetryDelay {
				return callMaxRetryDelay
			}
			return d
		}
	}
	d := time.Duration(1<<attempt) * time.Second
	if d > callMaxRetryDelay {
		return callMaxRetryDelay
	}
	return d
}

// extractJSONObject 截取首个平衡的 JSON 对象（容忍围栏与前后缀文本）。
func extractJSONObject(raw string) string {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, escape := 0, false, false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		if inStr {
			switch {
			case escape:
				escape = false
			case c == '\\':
				escape = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return ""
}
