// Package llmcontract 是直接结构化返回的统一契约与执行层
// (docs/structured-output-refactor.md §5)：静态 Contract 是结构的单一来源，
// Execute 统一完成能力选择、提示词准备、请求重试、Schema/DTO 解码和反馈自愈。
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
	"github.com/voocel/agentcore/llm"
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
	ModeNativeJSONSchema Mode = "native_json_schema"
	ModePromptContract   Mode = "prompt_contract"
)

// Source 是能力判断的依据来源。
type Source string

const (
	SourceConfig  Source = "config"  // 用户在 ModelConfig.json_schema 显式声明
	SourceAdapter Source = "adapter" // provider adapter 的模型级能力表
	SourceUnknown Source = "unknown" // 无声明且能力未知,保守走 prompt contract
)

// Resolution 是请求发出前确定的协议选择结果,供调用方分支与日志。
type Resolution struct {
	Mode     Mode
	Source   Source
	Strict   bool // native 时是否携带 strict
	Provider string
	Model    string
}

// jsonSchemaOverrider 由携带 config 三态覆盖的模型包装器实现
// (bootstrap.SwappableModel 及透传它的包装层)。
type jsonSchemaOverrider interface {
	JSONSchemaOverride() *bool
}

type modelInfoProvider interface {
	Info() llm.ModelInfo
}

// ModelFacts 是一次能力解析所需的同一时刻快照。热切换包装器实现该接口，
// 避免 Resolve 分别读取能力、配置覆盖和模型身份时混入两次切换之间的状态。
type ModelFacts struct {
	Capabilities       llm.Capabilities
	Info               llm.ModelInfo
	JSONSchemaOverride *bool
}

type modelFactsProvider interface {
	StructuredOutputFacts() ModelFacts
}

// Resolve 每次调用现读当前模型事实(热切换后下一次调用即用新值):
// config 三态优先,其次 adapter 模型级能力,未知一律 prompt contract。
func Resolve(model any) Resolution {
	res := Resolution{Mode: ModePromptContract, Source: SourceUnknown}

	var caps llm.Capabilities
	var info llm.ModelInfo
	var override *bool
	if fp, ok := model.(modelFactsProvider); ok {
		facts := fp.StructuredOutputFacts()
		caps, info, override = facts.Capabilities, facts.Info, facts.JSONSchemaOverride
	} else {
		if cp, ok := model.(llm.CapabilityProvider); ok {
			caps = cp.Capabilities()
		}
		if ip, ok := model.(modelInfoProvider); ok {
			info = ip.Info()
		}
		if o, ok := model.(jsonSchemaOverrider); ok {
			override = o.JSONSchemaOverride()
		}
	}
	res.Provider, res.Model = caps.Provider, caps.Model
	if res.Provider == "" {
		res.Provider = info.Provider
	}
	if res.Model == "" {
		res.Model = info.Name
	}

	if override != nil {
		res.Source = SourceConfig
		if *override {
			res.Mode = ModeNativeJSONSchema
			// 用户声明 endpoint 遵守 Structured Outputs 契约即默认 strict;
			// adapter 明确说不支持 strict 时才只发 schema 不发 strict。
			res.Strict = caps.Structured.Strict != llm.SupportNo
		}
		return res
	}

	switch caps.Structured.JSONSchema {
	case llm.SupportYes:
		res.Mode = ModeNativeJSONSchema
		res.Source = SourceAdapter
		res.Strict = caps.Structured.Strict == llm.SupportYes
	case llm.SupportNo:
		res.Source = SourceAdapter
	}
	return res
}

// Plan 解析协议并在原生模式下生成调用选项;prompt contract 模式返回 nil opts。
func Plan(model any, c Contract) ([]agentcore.CallOption, Resolution) {
	res := Resolve(model)
	if res.Mode != ModeNativeJSONSchema {
		return nil, res
	}
	return []agentcore.CallOption{
		agentcore.WithJSONSchema(c.Name, c.Description, c.Schema, res.Strict),
	}, res
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
// 所有 object 的属性都必须列入 required(可选语义用 null 联合表达)。litellm
// 在请求期做同样校验并自动补 additionalProperties:false;契约测试用本函数
// 前置断言(RFC §11.1),不把结构问题留到运行时。
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
