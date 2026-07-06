package skills

import (
	"strings"
	"testing"
)

func TestParseSkill_FullFrontmatter(t *testing.T) {
	raw := `---
name: cyberpunk-noir
description: "赛博朋克 + 黑色侦探题材清单"
category: genres
tags: [sci-fi, cyberpunk, noir]
triggers:
  - 赛博朋克
  - cyberpunk
when: |
  用户想写赛博朋克题材时
  规划世界观与视觉锚点
do: |
  - 高密度都市
  - 霓虹与雨夜
priority: 70
---

# 标题

正文段落。
`
	meta, body, err := ParseSkill("/home/u/.ainovel/skills/genres/cyberpunk-noir.md", raw)
	if err != nil {
		t.Fatalf("ParseSkill error: %v", err)
	}
	if meta.Name != "cyberpunk-noir" {
		t.Errorf("Name = %q, want %q", meta.Name, "cyberpunk-noir")
	}
	if meta.Description != "赛博朋克 + 黑色侦探题材清单" {
		t.Errorf("Description = %q", meta.Description)
	}
	if meta.Category != "genres" {
		t.Errorf("Category = %q", meta.Category)
	}
	wantTags := []string{"sci-fi", "cyberpunk", "noir"}
	if len(meta.Tags) != len(wantTags) {
		t.Errorf("Tags = %v, want %v", meta.Tags, wantTags)
	}
	wantTriggers := []string{"赛博朋克", "cyberpunk"}
	if len(meta.Triggers) != len(wantTriggers) {
		t.Errorf("Triggers = %v, want %v", meta.Triggers, wantTriggers)
	}
	if !strings.Contains(meta.When, "用户想写赛博朋克题材时") {
		t.Errorf("When = %q", meta.When)
	}
	if !strings.Contains(meta.Do, "高密度都市") {
		t.Errorf("Do = %q", meta.Do)
	}
	if meta.Priority != 70 {
		t.Errorf("Priority = %d", meta.Priority)
	}
	if !strings.Contains(body, "# 标题") {
		t.Errorf("body = %q", body)
	}
}

func TestParseSkill_NoFrontmatter(t *testing.T) {
	raw := "# Just Markdown\n\nbody only"
	meta, body, err := ParseSkill("/tmp/foo.md", raw)
	if err != nil {
		t.Fatalf("expected no error for no-frontmatter file, got: %v", err)
	}
	if meta.Name != "" {
		t.Errorf("Name should be empty, got %q", meta.Name)
	}
	if meta.Priority != 50 {
		t.Errorf("default Priority = %d, want 50", meta.Priority)
	}
	if body != raw {
		t.Errorf("body should equal input")
	}
}

func TestParseSkill_EmptyFile(t *testing.T) {
	_, _, err := ParseSkill("/tmp/x.md", "")
	if err == nil {
		t.Errorf("expected error for empty file")
	}
}

func TestParseSkill_MissingName(t *testing.T) {
	raw := `---
description: "no name here"
---
body`
	_, _, err := ParseSkill("/tmp/x.md", raw)
	if err == nil {
		t.Errorf("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error should mention name, got: %v", err)
	}
}

func TestParseSkill_MissingDescription(t *testing.T) {
	raw := `---
name: foo
---
body`
	_, _, err := ParseSkill("/tmp/x.md", raw)
	if err == nil {
		t.Errorf("expected error for missing description")
	}
}

func TestParseSkill_MissingCloseDelim(t *testing.T) {
	raw := `---
name: foo
description: bar
body without close`
	_, _, err := ParseSkill("/tmp/x.md", raw)
	if err == nil {
		t.Errorf("expected error for missing close delim")
	}
}

func TestParseSkill_InvalidPriority(t *testing.T) {
	raw := `---
name: foo
description: bar
priority: not-a-number
---
body`
	_, _, err := ParseSkill("/tmp/x.md", raw)
	if err == nil {
		t.Errorf("expected error for non-integer priority")
	}
}

func TestParseSkill_InferCategoryFromPath(t *testing.T) {
	raw := `---
name: foo
description: bar
---
body`
	meta, _, err := ParseSkill("/home/u/.ainovel/skills/genres/foo.md", raw)
	if err != nil {
		t.Fatalf("ParseSkill error: %v", err)
	}
	if meta.Category != "genres" {
		t.Errorf("Category = %q, want %q (inferred from path)", meta.Category, "genres")
	}
}

func TestParseSkill_InlineArrayWithSpaces(t *testing.T) {
	raw := `---
name: foo
description: bar
tags: [a, b, "c d", "e"]
---
body`
	meta, _, err := ParseSkill("/tmp/x.md", raw)
	if err != nil {
		t.Fatalf("ParseSkill error: %v", err)
	}
	want := []string{"a", "b", "c d", "e"}
	if len(meta.Tags) != len(want) {
		t.Fatalf("Tags = %v, want %v", meta.Tags, want)
	}
	for i := range want {
		if meta.Tags[i] != want[i] {
			t.Errorf("Tags[%d] = %q, want %q", i, meta.Tags[i], want[i])
		}
	}
}

func TestParseSkill_QuotedDescription(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`name: foo
description: "包含: 冒号"
`, "包含: 冒号"},
		{`name: foo
description: '单引号包围'
`, "单引号包围"},
		{`name: foo
description: 裸值
`, "裸值"},
	}
	for i, c := range cases {
		raw := "---\n" + c.raw + "---\nbody"
		meta, _, err := ParseSkill("/tmp/x.md", raw)
		if err != nil {
			t.Errorf("case %d error: %v", i, err)
			continue
		}
		if meta.Description != c.want {
			t.Errorf("case %d Description = %q, want %q", i, meta.Description, c.want)
		}
	}
}

func TestIsValidName(t *testing.T) {
	cases := map[string]bool{
		"cyberpunk-noir": true,
		"abc123":         true,
		"a":              true,
		"":               false,
		"Abc":            false, // 大写不允许
		"abc_def":        false, // 下划线不允许
		"abc.def":        false,
		"中文":             false,
	}
	for input, want := range cases {
		if got := isValidName(input); got != want {
			t.Errorf("isValidName(%q) = %v, want %v", input, got, want)
		}
	}
}
