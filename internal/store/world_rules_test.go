package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestSaveAndLoadWorldRules(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	rules := []domain.WorldRule{
		{Category: "magic", Rule: "法术消耗精神力", Boundary: "精神力耗尽会昏迷"},
		{Category: "magic", Rule: "禁咒需要三人合力", Boundary: "单人强行施放会死亡"},
		{Category: "society", Rule: "贵族拥有领地裁判权", Boundary: "不得越权审判其他领地居民"},
	}

	if err := store.SaveWorldRules(rules); err != nil {
		t.Fatalf("SaveWorldRules: %v", err)
	}

	// 验证 JSON 文件存在
	if _, err := os.Stat(filepath.Join(dir, "world_rules.json")); err != nil {
		t.Fatalf("world_rules.json not created: %v", err)
	}
	// 验证 Markdown 文件存在
	if _, err := os.Stat(filepath.Join(dir, "world_rules.md")); err != nil {
		t.Fatalf("world_rules.md not created: %v", err)
	}

	loaded, err := store.LoadWorldRules()
	if err != nil {
		t.Fatalf("LoadWorldRules: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(loaded))
	}
	if loaded[0].Category != "magic" || loaded[0].Rule != "法术消耗精神力" {
		t.Errorf("first rule mismatch: %+v", loaded[0])
	}
	if loaded[2].Category != "society" {
		t.Errorf("third rule category mismatch: %+v", loaded[2])
	}
}

func TestLoadWorldRules_Empty(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	rules, err := store.LoadWorldRules()
	if err != nil {
		t.Fatalf("LoadWorldRules on empty dir: %v", err)
	}
	if rules != nil {
		t.Fatalf("expected nil for missing file, got %v", rules)
	}
}

func TestSaveWorldRules_Overwrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	v1 := []domain.WorldRule{{Category: "old", Rule: "旧规则", Boundary: "旧边界"}}
	if err := store.SaveWorldRules(v1); err != nil {
		t.Fatalf("SaveWorldRules v1: %v", err)
	}

	v2 := []domain.WorldRule{
		{Category: "new", Rule: "新规则A", Boundary: "新边界A"},
		{Category: "new", Rule: "新规则B", Boundary: "新边界B"},
	}
	if err := store.SaveWorldRules(v2); err != nil {
		t.Fatalf("SaveWorldRules v2: %v", err)
	}

	loaded, err := store.LoadWorldRules()
	if err != nil {
		t.Fatalf("LoadWorldRules: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 rules after overwrite, got %d", len(loaded))
	}
	if loaded[0].Category != "new" {
		t.Errorf("expected new category, got %s", loaded[0].Category)
	}
}

func TestRenderWorldRules(t *testing.T) {
	rules := []domain.WorldRule{
		{Category: "magic", Rule: "法术消耗精神力", Boundary: "精神力耗尽会昏迷"},
		{Category: "society", Rule: "贵族有裁判权", Boundary: ""},
		{Category: "magic", Rule: "禁咒需三人", Boundary: "单人施放会死"},
	}

	md := renderWorldRules(rules)

	// 验证标题
	if got := md[:len("# 世界观规则")]; got != "# 世界观规则" {
		t.Errorf("missing title, got: %s", got)
	}
	// 验证分组：magic 应该出现在 society 之前（按输入顺序）
	magicPos := indexOf(md, "## magic")
	societyPos := indexOf(md, "## society")
	if magicPos < 0 || societyPos < 0 {
		t.Fatalf("missing category headers in:\n%s", md)
	}
	if magicPos >= societyPos {
		t.Errorf("magic should appear before society")
	}
	// 验证边界渲染：有 boundary 的条目应包含"边界"字样
	if indexOf(md, "边界：精神力耗尽会昏迷") < 0 {
		t.Errorf("missing boundary in:\n%s", md)
	}
	// 验证无 boundary 的条目不输出边界行
	if indexOf(md, "边界：\n") >= 0 {
		t.Errorf("empty boundary should not be rendered")
	}
}

func TestRenderWorldRules_EmptyCategoryFallback(t *testing.T) {
	rules := []domain.WorldRule{
		{Category: "", Rule: "无分类规则", Boundary: "边界"},
	}
	md := renderWorldRules(rules)
	if indexOf(md, "## other") < 0 {
		t.Errorf("empty category should fall back to 'other', got:\n%s", md)
	}
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
