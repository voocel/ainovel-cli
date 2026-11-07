// Package llmcontract 是直接结构化返回的统一契约与执行层：静态 Contract
// 是结构的单一来源，Execute 统一完成协议选择、提示词准备、请求重试、
// Schema/DTO 解码和反馈自愈。
// 协议在请求发出前确定；原生请求被拒或违约时原样暴露，禁止静默去掉 schema 重发。
package llmcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// Contract 是一次直接结构化返回的静态契约,紧邻各边界 DTO 定义。
type Contract struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Mode 是本次调用采用的结构化协议。
type Mode string

const (
	// ModeNativeJSONSchema 发送 response_format.json_schema，端点负责强制结构。
	ModeNativeJSONSchema Mode = "native_json_schema"
	// ModePromptContract 不发送 response_format：结构约束只来自提示词里的
	// schema 契约。Anthropic/Bedrock 没有 json_object 等价物（Messages API 会
	// 直接拒绝），所以 JSON 模式不是可移植的选择。
	ModePromptContract Mode = "prompt_contract"
)

// Source 是能力判断的依据来源。
type Source string

const (
	SourceConfig  Source = "config"  // 用户在 ModelConfig.json_schema 显式声明
	SourceAdapter Source = "adapter" // 未声明,交给 provider adapter 的 schema 映射
	SourceUnknown Source = "unknown" // 模型句柄不完整(无 client),无法判断
)

// Resolution 是请求发出前确定的协议选择结果,供调用方分支与日志。
type Resolution struct {
	Mode     Mode
	Source   Source
	Strict   bool // native 时是否携带 strict
	Provider string
	Model    string
}

// Model 是一次结构化调用所需的模型句柄：agentcore.Model 提供客户端与请求模板
// (Model、Thinking、ProviderOptions 等),Provider/ModelName 与 JSONSchemaOverride
// 是本次调用的身份与结构化协议覆盖。JSONSchemaOverride 为 nil 表示交给 provider
// adapter 的 schema 映射决定。
type Model struct {
	Core               agentcore.Model
	Provider           string
	ModelName          string
	JSONSchemaOverride *bool
}

// provider 返回 adapter 名:优先调用方声明的身份,其次 provider 自报。
func (m Model) provider() string {
	if m.Provider != "" {
		return m.Provider
	}
	if m.Core.Client != nil {
		return m.Core.Client.ProviderName()
	}
	return ""
}

// modelName 返回本次请求的模型名:优先调用方声明的身份,其次请求模板。
func (m Model) modelName() string {
	if m.ModelName != "" {
		return m.ModelName
	}
	return m.Core.Request.Model
}

// Resolve 每次调用现读模型句柄里的覆盖与身份(热切换后下一次调用即用新值):
// config 三态优先;未声明时交给 provider adapter 的 schema 映射,由 litellm 决定
// 是原生字段还是提示词回退。strict 只在 schema 满足 strict 子集时声明,避免把
// 结构问题变成 provider 侧校验错误。
func Resolve(m Model, c Contract) Resolution {
	res := Resolution{
		Mode:     ModeNativeJSONSchema,
		Source:   SourceAdapter,
		Strict:   ValidateStrictReady(c.Schema) == nil,
		Provider: m.provider(),
		Model:    m.modelName(),
	}
	if m.Core.Client == nil {
		res.Source = SourceUnknown
	}
	switch {
	case m.JSONSchemaOverride == nil:
		// 交给 adapter:litellm 为每个 provider 选择原生 schema 或提示词回退。
	case *m.JSONSchemaOverride:
		res.Source = SourceConfig
	default:
		res.Mode = ModePromptContract
		res.Source = SourceConfig
		res.Strict = false
	}
	return res
}

// Plan 解析协议并生成本次请求的 response_format:原生模式带命名 schema,
// prompt contract 模式不发 response_format(nil)。schema 无法序列化时返回错误,
// 请求不发。
func Plan(m Model, c Contract) (*litellm.ResponseFormat, Resolution, error) {
	res := Resolve(m, c)
	if res.Mode != ModeNativeJSONSchema {
		return nil, res, nil
	}
	format, err := litellm.NewResponseFormatJSONSchema(c.Name, c.Description, c.Schema)
	if err != nil {
		return nil, res, fmt.Errorf("llmcontract: %s response_format: %w", c.Name, err)
	}
	format.JSONSchema.Strict = &res.Strict
	return format, res, nil
}

// PreparePrompt 保持业务语义提示词只有一份：原生模式直接返回原文；prompt
// contract 模式从同一份 Schema 自动生成格式后缀。调用方不维护第二套模板，字段
// 变更也不会让提示词与 response_format 分叉。
func PreparePrompt(base string, c Contract, res Resolution) (string, error) {
	if res.Mode != ModePromptContract {
		return base, nil
	}
	schemaJSON, err := json.Marshal(c.Schema)
	if err != nil {
		return "", fmt.Errorf("llmcontract: marshal %s prompt schema: %w", c.Name, err)
	}
	contract := "## 输出契约\n\n" +
		"只输出一个符合下列 JSON Schema 的 JSON 对象，不要输出解释、Markdown 围栏或标签本身。\n\n" +
		"<output-json-schema>\n" + string(schemaJSON) + "\n</output-json-schema>"
	if strings.TrimSpace(base) == "" {
		return contract, nil
	}
	return strings.TrimSpace(base) + "\n\n" + contract, nil
}

// Nullable 把一个 schema 的 type 扩展为可空联合(["<t>","null"]),用于 strict
// 模式下"全字段 required、可选语义用 null"的表达。返回拷贝,不修改传入 map。
func Nullable(s map[string]any) map[string]any {
	out := maps.Clone(s)
	if t, ok := out["type"].(string); ok {
		out["type"] = []string{t, "null"}
	}
	switch values := out["enum"].(type) {
	case []string:
		enum := make([]any, 0, len(values)+1)
		for _, value := range values {
			enum = append(enum, value)
		}
		out["enum"] = append(enum, nil)
	case []any:
		enum := slices.Clone(values)
		for _, value := range enum {
			if value == nil {
				return out
			}
		}
		out["enum"] = append(enum, nil)
	}
	return out
}

// ValidateStrictReady 递归校验 schema 满足 OpenAI strict 子集的结构前提:
// 所有 object 的属性都必须列入 required(可选语义用 null 联合表达)。契约测试
// 用本函数前置断言(RFC §11.1),Resolve 也用它决定是否声明 strict;不把结构
// 问题留到运行时。
func ValidateStrictReady(s map[string]any) error {
	return validateStrictReady(s, "$")
}

func validateStrictReady(s map[string]any, path string) error {
	if typeIncludes(s["type"], "object") {
		props, _ := s["properties"].(map[string]any)
		required, _ := s["required"].([]string)
		for name, sub := range props {
			if !slices.Contains(required, name) {
				return fmt.Errorf("%s.%s 未列入 required(strict 要求全属性 required)", path, name)
			}
			if subMap, ok := sub.(map[string]any); ok {
				if err := validateStrictReady(subMap, path+"."+name); err != nil {
					return err
				}
			}
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		return validateStrictReady(items, path+"[]")
	}
	return nil
}

func typeIncludes(t any, want string) bool {
	switch v := t.(type) {
	case string:
		return v == want
	case []string:
		return slices.Contains(v, want)
	}
	return false
}

// Fingerprint 返回 schema 规范化 JSON 的 sha256 前 12 位 hex,用于日志关联;
// encoding/json 对 map 键排序,同一契约天然稳定。
func (c Contract) Fingerprint() string {
	data, err := json.Marshal(c.Schema)
	if err != nil {
		return "unmarshalable"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}
