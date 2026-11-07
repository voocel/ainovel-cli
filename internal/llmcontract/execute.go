package llmcontract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/voocel/ainovel-cli/internal/llmretry"
	"github.com/voocel/litellm"
)

// FailureKind 区分不可由同一次结构化反馈修复的失败边界。
type FailureKind string

const (
	FailureRequest  FailureKind = "request"
	FailureProtocol FailureKind = "protocol"
	FailureLength   FailureKind = "length"
	FailureSafety   FailureKind = "safety"
	FailureContract FailureKind = "contract"
)

// Failure 保留失败类别和模型原始输出，供调用方决定日志、工件和 UI 表达。
type Failure struct {
	Kind     FailureKind
	Contract string
	Raw      string
	Err      error
}

func (e *Failure) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return e.Contract
	}
	if e.Contract == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Contract, e.Err)
}

func (e *Failure) Unwrap() error { return e.Err }

// Correction 描述一次模型可修复的输出错误。Attempt 是刚失败的调用序号。
type Correction struct {
	Attempt int
	Layer   string
	Mode    Mode
	Raw     string
	Err     error
}

// Hooks 只负责可观测性，不改变执行语义。
type Hooks struct {
	// Resolved 在请求发出前报告本次的协议意图（模式、来源、strict、身份）。
	// 若端点把 schema 挪进提示词（litellm.schema_prompt_fallback），后续的
	// Correction.Mode 会改报 prompt_contract：模式以端点的实际行为为准。
	Resolved     func(Resolution)
	RequestRetry func(llmretry.Event)
	Correction   func(Correction)
}

// ThinkingLevel 是本次结构化调用的推理强度覆盖；空值表示沿用请求模板的设置。
// 具体取值由端点裁决，llmcontract 只负责映射成 litellm.Thinking。
type ThinkingLevel string

const (
	ThinkingAuto   ThinkingLevel = ""
	ThinkingOff    ThinkingLevel = "off"
	ThinkingLow    ThinkingLevel = "low"
	ThinkingMedium ThinkingLevel = "medium"
	ThinkingHigh   ThinkingLevel = "high"
	ThinkingXHigh  ThinkingLevel = "xhigh"
	ThinkingMax    ThinkingLevel = "max"
)

// Option 在模型请求模板之上调整这一次结构化请求。只表达协议侧的覆盖，
// 不提供输出上限：生成预算由 provider/model 决定。
type Option func(*litellm.Request)

// WithThinking 用 level 覆盖模板的思考设置。ThinkingAuto 不覆盖；其余等级原样
// 透传给端点（由模型裁决合法取值）。level 应由调用方按模型能力解析后传入——
// 对不支持 thinking 的模型发任何等级（含 off）都是非法参数。
func WithThinking(level ThinkingLevel) Option {
	normalized := ThinkingLevel(strings.ToLower(strings.TrimSpace(string(level))))
	return func(req *litellm.Request) {
		switch normalized {
		case ThinkingAuto:
		case ThinkingOff:
			req.Thinking = &litellm.Thinking{Disabled: true}
		default:
			req.Thinking = &litellm.Thinking{Effort: string(normalized)}
		}
	}
}

// WithProviderOptions 把这批额外请求参数并入模板；同名键以 opts 为准。
func WithProviderOptions(opts litellm.ProviderOptions) Option {
	return func(req *litellm.Request) {
		if len(opts) == 0 {
			return
		}
		merged := make(litellm.ProviderOptions, len(req.ProviderOptions)+len(opts))
		maps.Copy(merged, req.ProviderOptions)
		maps.Copy(merged, opts)
		req.ProviderOptions = merged
	}
}

// Request 定义一次直接结构化返回。Contract 是结构的单一来源，Validate 只处理
// JSON Schema 无法表达的业务约束。
type Request[T any] struct {
	Contract     Contract
	SystemPrompt string
	Payload      string
	Options      []Option
	Validate     func(*T) error
	Agent        string
	Hooks        Hooks
}

const promptCorrection = "上面的输出不符合 JSON Schema。请根据错误修正，并只输出完整 JSON 对象，不要解释或 Markdown 围栏。"
const semanticCorrection = "上面的 JSON 结构合法但字段取值未通过业务校验。请根据错误修正，并重新输出完整 JSON 对象。"

// warningSchemaPromptFallback 是 litellm 在 adapter 把 JSON Schema 挪进提示词时
// 携带的 warning 代码（无 schema 字段的端点：DeepSeek/GLM/MiMo/MiniMax 等）。
// 出现它说明本次请求没有原生强制力，必须按 prompt contract 的行为自愈。
const warningSchemaPromptFallback = "litellm.schema_prompt_fallback"

// Execute 统一完成协议选择、提示词准备、请求重试、停止原因分类、Schema/DTO
// 解码和业务反馈自愈。prompt 模式的格式/Schema 错误以及两种模式的业务错误会
// 持续反馈给模型，直到成功或 context 结束；原生契约违约会立即暴露。
func Execute[T any](ctx context.Context, model Model, req Request[T]) (T, error) {
	var zero T
	if model.Core.Client == nil {
		return zero, &Failure{Kind: FailureProtocol, Contract: req.Contract.Name, Err: errors.New("模型未配置")}
	}

	format, resolution, err := Plan(model, req.Contract)
	if err != nil {
		return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Err: fmt.Errorf("准备输出契约: %w", err)}
	}
	systemPrompt, err := PreparePrompt(req.SystemPrompt, req.Contract, resolution)
	if err != nil {
		return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Err: fmt.Errorf("准备输出契约: %w", err)}
	}
	if req.Hooks.Resolved != nil {
		req.Hooks.Resolved(resolution)
	}

	// 请求模板决定模型身份与传输选项（Model、Thinking、ProviderOptions 等）；
	// 本次调用的消息、工具与输出格式由这里填。
	call := model.Core.Request
	call.Messages = []litellm.Message{
		litellm.System(systemPrompt),
		litellm.UserText(req.Payload),
	}
	call.Tools = nil
	call.ToolChoice = nil
	call.ResponseFormat = format
	for _, opt := range req.Options {
		if opt != nil {
			opt(&call)
		}
	}

	native := resolution.Mode == ModeNativeJSONSchema

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		resp, err := llmretry.Chat(ctx, model.Core.Client, llmretry.Config{
			Agent:   req.Agent,
			OnRetry: req.Hooks.RequestRetry,
		}, call)
		if err != nil {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			return zero, &Failure{Kind: requestFailureKind(err), Contract: req.Contract.Name, Err: err}
		}
		if resp == nil {
			return zero, &Failure{Kind: FailureProtocol, Contract: req.Contract.Name, Err: errors.New("模型返回空响应")}
		}
		if native && schemaPromptFallback(resp) {
			// adapter 把 schema 挪进了提示词：本次响应没有原生强制力，
			// 结构问题按 prompt contract 反馈重问，避免把端点能力差异误报为代码缺陷。
			native = false
			resolution.Mode = ModePromptContract
		}

		raw := resp.Text()
		if failure := stopFailure(resp.FinishReason); failure != nil {
			failure.Contract = req.Contract.Name
			failure.Raw = raw
			return zero, failure
		}

		body := strings.TrimSpace(raw)
		if native {
			if body == "" {
				return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Raw: raw, Err: errors.New("原生 schema 返回空内容")}
			}
		} else {
			body = ExtractJSONObject(raw)
		}

		layer := "schema"
		var cause error
		if body == "" {
			layer, cause = "decode", errors.New("输出中未找到 JSON 对象")
		} else if err := ValidateJSON(req.Contract.Schema, []byte(body)); err != nil {
			cause = err
		} else {
			var out T
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				// Schema 已通过而 DTO 无法解码，说明静态契约与 Go 类型不一致，
				// 继续要求模型重写无法修复代码缺陷。
				return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Raw: raw, Err: fmt.Errorf("schema 与 DTO 不一致: %w", err)}
			}
			if req.Validate == nil {
				return out, nil
			}
			if err := req.Validate(&out); err == nil {
				return out, nil
			} else {
				layer, cause = "semantic", err
			}
		}

		if native && layer != "semantic" {
			return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Raw: raw, Err: fmt.Errorf("原生 schema 契约违约: %w", cause)}
		}
		correction := Correction{Attempt: attempt, Layer: layer, Mode: resolution.Mode, Raw: raw, Err: cause}
		if req.Hooks.Correction != nil {
			req.Hooks.Correction(correction)
		}
		hint := promptCorrection
		if layer == "semantic" {
			hint = semanticCorrection
		}
		call.Messages = append(call.Messages,
			litellm.AssistantText(raw),
			litellm.UserText(hint+"\n错误："+cause.Error()),
		)
	}
}

// stopFailure 把终止原因映射为失败类别，与 agentcore.StopReason 的分类一致：
// 截断=length、拒答/过滤=safety、错误终止=error、意外工具调用=tool_use；
// stop/other 是正常结束，交由调用方解码。
func stopFailure(reason litellm.FinishReason) *Failure {
	switch reason {
	case litellm.FinishReasonLength:
		return &Failure{Kind: FailureLength, Err: errors.New("模型输出被长度截断(stop_reason=length)")}
	case litellm.FinishReasonSafety:
		return &Failure{Kind: FailureSafety, Err: errors.New("模型拒答或触发内容过滤(stop_reason=safety)")}
	case litellm.FinishReasonError:
		return &Failure{Kind: FailureProtocol, Err: errors.New("模型以错误状态结束(stop_reason=error)")}
	case litellm.FinishReasonToolCall:
		return &Failure{Kind: FailureProtocol, Err: errors.New("结构化调用意外返回工具调用(stop_reason=tool_use)")}
	}
	return nil
}

// requestFailureKind 按 litellm 的错误类型划分请求层失败：本地请求构造不合法
// （ErrorTypeValidation）属于协议缺陷；provider、网络、上下文溢出（ErrorTypeProvider /
// ErrorTypeNetwork / ErrorTypeContextOverflow 等）都是请求失败。Failure.Err 保留
// 原始 *litellm.Error，调用方可用 errors.As / litellm.ErrorTypeOf 继续细分。
func requestFailureKind(err error) FailureKind {
	if litellm.ErrorTypeOf(err) == litellm.ErrorTypeValidation {
		return FailureProtocol
	}
	return FailureRequest
}

// schemaPromptFallback 报告本次响应是否来自提示词回退的 schema 映射。
func schemaPromptFallback(resp *litellm.Response) bool {
	for _, warning := range resp.Warnings {
		if warning.Code == warningSchemaPromptFallback {
			return true
		}
	}
	return false
}

// ExtractJSONObject 返回文本中的第一个平衡 JSON 对象，字符串中的花括号不计入层级。
func ExtractJSONObject(raw string) string {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return ""
	}
	depth, inString, escaped := 0, false, false
	for i := start; i < len(raw); i++ {
		switch c := raw[i]; {
		case inString && escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && c == '{':
			depth++
		case !inString && c == '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return ""
}
