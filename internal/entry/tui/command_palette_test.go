package tui

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/skills"
)

// TestSkillPaletteItemsConvertsMeta 验证 store.List 输出被正确映射为菜单条目。
func TestSkillPaletteItemsConvertsMeta(t *testing.T) {
	dir := t.TempDir()
	store := skills.NewStore(dir)
	if err := store.Add(skills.SkillMeta{
		Name:        "cyberpunk-noir",
		Description: "赛博朋克 + 黑色侦探题材清单",
		Category:    "genres",
		Tags:        []string{"sci-fi"},
	}, "body content"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Add(skills.SkillMeta{
		Name:        "minimal-skill",
		Description: "最小用例",
	}, "body"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	items := skillPaletteItems(store)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %+v", len(items), items)
	}

	byName := map[string]commandPaletteItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	cs, ok := byName["cyberpunk-noir"]
	if !ok {
		t.Fatalf("missing cyberpunk-noir in %+v", items)
	}
	if cs.Kind != kindSkill || cs.Category != "genres" {
		t.Fatalf("cyberpunk-noir kind/category mismatch: %+v", cs)
	}
	if cs.Usage != "/cyberpunk-noir [补充要求]" {
		t.Fatalf("usage should mention skill inject format, got %q", cs.Usage)
	}
}

// TestCommandCompletionsIncludesSkillEntry 验证一级命令列表末尾始终追加
// "Skill ▸" 子菜单入口（即便 store 为 nil），让用户能发现 skill 路径。
func TestCommandCompletionsIncludesSkillEntry(t *testing.T) {
	// nil store：入口仍存在，但描述提示"为空"
	items := commandCompletions("", nil)
	last := items[len(items)-1]
	if last.Kind != kindSkillEntry {
		t.Fatalf("last item should be Skill entry, got %+v", last)
	}
	if last.Name != "Skill" {
		t.Fatalf("Skill entry name mismatch: %+v", last)
	}
	if last.Description == "" {
		t.Fatal("Skill entry should describe empty store")
	}

	// 有 skill 的 store：描述应包含数量
	dir := t.TempDir()
	store := skills.NewStore(dir)
	for _, n := range []string{"a", "b", "c"} {
		if err := store.Add(skills.SkillMeta{
			Name: n, Description: "x", Category: "misc",
		}, "body"); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	items = commandCompletions("", store)
	last = items[len(items)-1]
	if last.Kind != kindSkillEntry {
		t.Fatalf("last item should be Skill entry, got %+v", last)
	}
	if !containsStr(last.Description, "3") {
		t.Fatalf("description should mention skill count 3: %q", last.Description)
	}
}

// TestCommandCompletionsQuerySkillMatchesEntry 输入 "skill" 时一级菜单应让 Skill 入口浮顶，
// 因为名字精确匹配。
func TestCommandCompletionsQuerySkillMatchesEntry(t *testing.T) {
	items := commandCompletions("skill", nil)
	if len(items) == 0 {
		t.Fatal("query 'skill' should at least match Skill entry")
	}
	if items[0].Kind != kindSkillEntry {
		t.Fatalf("Skill entry should rank first for query 'skill', got %+v", items[0])
	}
}

// TestSkillCompletionsFiltersByQuery 二级子菜单按查询词过滤。
func TestSkillCompletionsFiltersByQuery(t *testing.T) {
	dir := t.TempDir()
	store := skills.NewStore(dir)
	_ = store.Add(skills.SkillMeta{Name: "cyberpunk-noir", Description: "赛博朋克", Category: "genres"}, "body")
	_ = store.Add(skills.SkillMeta{Name: "wuxia", Description: "武侠", Category: "genres"}, "body")

	all := skillCompletions("", store)
	if len(all) != 2 {
		t.Fatalf("expected 2 skills on empty query, got %d", len(all))
	}

	filtered := skillCompletions("cyb", store)
	if len(filtered) != 1 || filtered[0].Name != "cyberpunk-noir" {
		t.Fatalf("query 'cyb' should match cyberpunk-noir only, got %+v", filtered)
	}
}

// TestSkillCompletionsNilStore 二级子菜单 store 缺失时不 panic。
func TestSkillCompletionsNilStore(t *testing.T) {
	items := skillCompletions("any", nil)
	if len(items) != 0 {
		t.Fatalf("nil store should yield empty list, got %+v", items)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
