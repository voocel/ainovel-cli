package prompt

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 工具 Schema 与领域结构体是两份手工维护的真理源。它们漂移一次的代价：
// ManuscriptChapter 有 depends_on 而 schema 没列，additionalProperties=false
// 直接硬拒——模型照抄读到的章节写回去，连撞 6 次。
//
// 这条检查纯靠反射比对 json tag，不需要任何人工登记的映射，因此不会自己失效。
// （取值域 enum 不适用：正确的 enum 常是领域取值的子集，推导不出来。）
func TestToolSchemaCoversDomainFields(t *testing.T) {
	schemas := map[string]map[string]any{}
	definitions, err := BuiltinCapabilities()
	if err != nil {
		t.Fatalf("built-in capabilities: %v", err)
	}
	for _, definition := range definitions {
		for _, tool := range definition.Worker.Tools {
			var parsed map[string]any
			if err := json.Unmarshal(tool.InputSchema, &parsed); err != nil {
				t.Fatalf("%s schema: %v", tool.Name, err)
			}
			schemas[tool.Name] = parsed
		}
	}

	// 每条给出：工具、到达该对象的 schema 路径、对应的领域类型。
	for _, c := range []struct {
		tool, path string
		typ        any
	}{
		{ToolWorkspacePutChapter, "chapter", model.ManuscriptChapter{}},
		{ToolWorkspacePutChapter, "chapter.blocks[]", model.ManuscriptBlock{}},
		{ToolWorkspacePutReview, "findings[]", model.ReviewFinding{}},
		{ToolVerdictSubmit, "directives[]", model.DirectiveVerification{}},
		{ToolVerdictSubmit, "intent", model.IntentVerification{}},
	} {
		t.Run(c.tool+"/"+c.path, func(t *testing.T) {
			properties, err := schemaProperties(schemas[c.tool], c.path)
			if err != nil {
				t.Fatalf("%s: %v", c.path, err)
			}
			domain := reflect.TypeOf(c.typ)
			var missing []string
			for i := 0; i < domain.NumField(); i++ {
				name := strings.Split(domain.Field(i).Tag.Get("json"), ",")[0]
				if name == "" || name == "-" {
					continue
				}
				if _, ok := properties[name]; !ok {
					missing = append(missing, name)
				}
			}
			if len(missing) > 0 {
				t.Errorf("%s 缺少 %s 的字段 %v；additionalProperties=false 会硬拒它们，模型照抄读到的形状就会连撞",
					c.path, domain.Name(), missing)
			}
		})
	}
}

// schemaProperties 沿 "a.b[]" 这样的路径取到目标对象的 properties。
func schemaProperties(schema map[string]any, path string) (map[string]any, error) {
	node := schema
	for _, segment := range strings.Split(path, ".") {
		properties, ok := node["properties"].(map[string]any)
		if !ok {
			return nil, errSchemaPath(path, segment, "上一级没有 properties")
		}
		name, isArray := strings.CutSuffix(segment, "[]")
		child, ok := properties[name].(map[string]any)
		if !ok {
			return nil, errSchemaPath(path, segment, "schema 里没有这个字段")
		}
		if isArray {
			if child, ok = child["items"].(map[string]any); !ok {
				return nil, errSchemaPath(path, segment, "数组没有 items")
			}
		}
		node = child
	}
	properties, ok := node["properties"].(map[string]any)
	if !ok {
		return nil, errSchemaPath(path, path, "目标不是带 properties 的对象")
	}
	return properties, nil
}

type schemaPathError struct{ path, segment, reason string }

func (e schemaPathError) Error() string {
	return "路径 " + e.path + " 在 " + e.segment + " 处断开：" + e.reason
}

func errSchemaPath(path, segment, reason string) error {
	return schemaPathError{path, segment, reason}
}
