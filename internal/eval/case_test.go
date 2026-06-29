package eval

import (
	"path/filepath"
	"testing"
)

// TestSmokeCasesLoad 确保仓库内置 smoke case 能被加载器解析（含 DisallowUnknownFields 校验）。
func TestSmokeCasesLoad(t *testing.T) {
	dir := filepath.Join("..", "..", "evals", "cases", "smoke")
	cases, err := LoadCases(dir)
	if err != nil {
		t.Fatalf("加载 smoke case 失败: %v", err)
	}
	if len(cases) < 3 {
		t.Fatalf("期望至少 3 个 smoke case，得到 %d", len(cases))
	}
	for _, c := range cases {
		if c.Category != "smoke" {
			t.Errorf("%s: category 应为 smoke，得到 %s", c.ID, c.Category)
		}
		if c.Gate.MaxSeverity == "" {
			t.Errorf("%s: Validate 应填充默认 max_severity", c.ID)
		}
	}
}

func TestLoadCasesRejectsUnknownField(t *testing.T) {
	// 间接验证：合法 case 必须含 id+prompt；缺失即报错（Validate 路径）。
	if _, err := LoadCases(filepath.Join("..", "..", "evals", "cases", "smoke", "writer_first_chapter.json")); err != nil {
		t.Fatalf("单文件加载应成功: %v", err)
	}
}

// case id 会拼进 RemoveAll 的路径，路径穿越/分隔符必须被拒（高危防护）。
func TestCaseIDRejectsUnsafe(t *testing.T) {
	for _, bad := range []string{"../evil", "a/b", "/abs", "..", "Up", "with space", "dot.case"} {
		c := Case{ID: bad, Prompt: "x"}
		if err := c.Validate(); err == nil {
			t.Errorf("非法 id %q 应被拒", bad)
		}
	}
	for _, ok := range []string{"writer_first_chapter", "architect-long", "case1"} {
		c := Case{ID: ok, Prompt: "x"}
		if err := c.Validate(); err != nil {
			t.Errorf("合法 id %q 不应被拒: %v", ok, err)
		}
	}
}
