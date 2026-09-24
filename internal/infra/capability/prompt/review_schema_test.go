package prompt

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 审阅发现只经 workspace_put_review 写入，最终仍过 ReviewVerdict.Validate。
// 这一侧曾是裸 object，模型按猜测写成 note+directive_id，review:r5 连撞三次 submission_blocked。
func TestReviewFindingSchemaIsConstrained(t *testing.T) {
	definition, err := BuiltinCapability(model.OperationReviewRange)
	if err != nil {
		t.Fatalf("review capability: %v", err)
	}
	var items map[string]any
	for _, tool := range definition.Worker.Tools {
		if tool.Name != ToolWorkspacePutReview {
			continue
		}
		var parsed struct {
			Properties struct {
				Findings struct {
					Items map[string]any `json:"items"`
				} `json:"findings"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &parsed); err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
		items = parsed.Properties.Findings.Items
	}
	if len(items) == 0 {
		t.Fatalf("%s 的 findings 没有条目 schema：模型会按任意对象写", ToolWorkspacePutReview)
	}

	properties, _ := items["properties"].(map[string]any)
	severity, _ := properties["severity"].(map[string]any)
	enum, _ := severity["enum"].([]any)
	got := make([]string, 0, len(enum))
	for _, value := range enum {
		text, _ := value.(string)
		got = append(got, text)
	}
	// 枚举必须与领域取值一致，否则模型写得出 Validate 不认的 severity。
	if want := []string{string(model.FindingBlocking), string(model.FindingNote)}; !reflect.DeepEqual(got, want) {
		t.Errorf("severity 枚举 = %v, want %v", got, want)
	}
	// 只有 blocking 能链接核验项——这条约束校验器会拒，schema 必须先讲清楚。
	for _, field := range []string{"severity", "directive_id", "intent"} {
		constraint, _ := properties[field].(map[string]any)
		if description, _ := constraint["description"].(string); description == "" {
			t.Errorf("%s 缺少说明：模型只能靠猜，撞了规则也不知道改哪个字段", field)
		}
	}
	required, _ := items["required"].([]any)
	if len(required) != 3 {
		t.Errorf("必填字段 = %v，want chapter_id / severity / note", required)
	}
}

// authority_read 的 kind 曾是裸 string，模型就去猜 story_context / project_rules。
// 取值是 DocumentKind 的子集（哪几种可读是本工具的决定），所以只断言"有取值域"。
func TestAuthorityReadDeclaresReadableKinds(t *testing.T) {
	for _, kind := range []model.OperationKind{model.OperationDevelopPlan, model.OperationWriteChapter, model.OperationReviewRange} {
		definition, err := BuiltinCapability(kind)
		if err != nil {
			t.Fatalf("capability %s: %v", kind, err)
		}
		for _, tool := range definition.Worker.Tools {
			if tool.Name != ToolAuthorityRead {
				continue
			}
			var parsed struct {
				Properties map[string]struct {
					Enum        []string `json:"enum"`
					Description string   `json:"description"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(tool.InputSchema, &parsed); err != nil {
				t.Fatalf("%s schema: %v", kind, err)
			}
			if len(parsed.Properties["kind"].Enum) == 0 {
				t.Errorf("%s 的 authority_read.kind 没有取值域，模型只能猜", kind)
			}
			if parsed.Properties["revision"].Description == "" {
				t.Errorf("%s 的 authority_read.revision 没说明必填与上界", kind)
			}
		}
	}
}

// 校验器拒绝时必须说清改哪个字段，否则模型只会原样重试到 submission_blocked。
func TestReviewFindingRejectionNamesTheFieldToFix(t *testing.T) {
	verdict := model.ReviewVerdict{
		Revision: 2, ChapterIDs: []string{"ch-001"}, ReviewKey: "final_review_v1",
		Status: model.ReviewPass,
		Basis: model.EvidenceBasis{Documents: []model.DocumentBasis{{
			Ref: model.DocumentRef{Kind: model.DocumentManuscript, ID: "ch-001"}, Revision: 2}}},
		Findings: []model.ReviewFinding{{
			ChapterID: "ch-001", Severity: model.FindingNote, Note: "满足",
			DirectiveID: "intent-required-present", Intent: "required_present",
		}},
	}
	err := verdict.Validate()
	if err == nil {
		t.Fatal("note 发现链接核验项却通过了校验")
	}
	for _, want := range []string{"directive_id", "intent", string(model.FindingBlocking)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息没提到 %q，模型无法自纠：%v", want, err)
		}
	}
}
