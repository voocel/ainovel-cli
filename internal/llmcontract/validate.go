package llmcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
)

// ValidateJSON 校验原始 JSON 是否满足直接返回契约使用的 JSON Schema 子集。
// 该子集覆盖 object/array/string/integer/number/boolean/null、required、enum 和
// additionalProperties。未声明 additionalProperties 时遵循 JSON Schema 默认语义，
// 不额外拒绝未知字段。
func ValidateJSON(schema map[string]any, raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("JSON 解析失败: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON 后存在额外值")
		}
		return fmt.Errorf("JSON 尾部非法: %w", err)
	}
	return validateValue(schema, value, "$")
}

func validateValue(schema map[string]any, value any, path string) error {
	types, err := schemaTypes(schema["type"])
	if err != nil {
		return fmt.Errorf("%s 契约非法: %w", path, err)
	}
	if value == nil && !slices.Contains(types, "null") {
		return fmt.Errorf("%s 必须是 %s，实际为 null", path, joinTypes(types))
	} else if value != nil {
		actual := valueType(value)
		if len(types) > 0 && !slices.Contains(types, actual) && !(actual == "integer" && slices.Contains(types, "number")) {
			return fmt.Errorf("%s 必须是 %s，实际为 %s", path, joinTypes(types), actual)
		}
	}

	if rawEnum, exists := schema["enum"]; exists {
		enum, err := enumValues(rawEnum)
		if err != nil {
			return fmt.Errorf("%s.enum 契约非法: %w", path, err)
		}
		if !enumContains(enum, value) {
			return fmt.Errorf("%s 必须是 %v 之一，实际为 %v", path, enum, value)
		}
	}
	if value == nil {
		return nil
	}

	switch typed := value.(type) {
	case map[string]any:
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s 契约缺少 properties", path)
		}
		required, err := requiredNames(schema["required"])
		if err != nil {
			return fmt.Errorf("%s.required 契约非法: %w", path, err)
		}
		for _, name := range required {
			if _, exists := typed[name]; !exists {
				return fmt.Errorf("%s.%s 是必填字段", path, name)
			}
		}
		if allowAdditional, declared := schema["additionalProperties"].(bool); declared && !allowAdditional {
			for name := range typed {
				if _, exists := properties[name]; !exists {
					return fmt.Errorf("%s.%s 未在契约中声明", path, name)
				}
			}
		}
		for name, child := range typed {
			childSchema, exists := properties[name]
			if !exists {
				continue
			}
			childMap, ok := childSchema.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s 契约不是对象", path, name)
			}
			if err := validateValue(childMap, child, path+"."+name); err != nil {
				return err
			}
		}
	case []any:
		itemSchema, ok := schema["items"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s 契约缺少 items", path)
		}
		for i, item := range typed {
			if err := validateValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaTypes(value any) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{typed}, nil
	case []string:
		return typed, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("type 联合必须只包含字符串")
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("type 必须是字符串或字符串数组")
	}
}

func requiredNames(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	if names, ok := stringSlice(value); ok {
		return names, nil
	}
	return nil, fmt.Errorf("必须是字符串数组")
}

func stringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}

func enumValues(value any) ([]any, error) {
	switch typed := value.(type) {
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out, nil
	case []any:
		for _, item := range typed {
			if item == nil {
				continue
			}
			if _, ok := item.(string); !ok {
				return nil, fmt.Errorf("只支持字符串和 null")
			}
		}
		return typed, nil
	default:
		return nil, fmt.Errorf("必须是数组")
	}
}

func enumContains(enum []any, value any) bool {
	for _, item := range enum {
		if item == nil && value == nil {
			return true
		}
		itemText, itemOK := item.(string)
		valueText, valueOK := value.(string)
		if itemOK && valueOK && itemText == valueText {
			return true
		}
	}
	return false
}

func valueType(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		if number, err := strconv.ParseFloat(string(typed), 64); err == nil && !math.IsInf(number, 0) && math.Trunc(number) == number {
			return "integer"
		}
		return "number"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func joinTypes(types []string) string {
	if len(types) == 0 {
		return "有效 JSON 值"
	}
	return fmt.Sprint(types)
}
