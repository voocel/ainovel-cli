package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helper：在 path 写一个最小合法 skill 文件
func writeSkillFile(t *testing.T, path, name, desc, category string, tags []string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: " + desc + "\ncategory: " + category + "\n"
	if len(tags) > 0 {
		content += "tags: ["
		for i, tag := range tags {
			if i > 0 {
				content += ", "
			}
			content += tag
		}
		content += "]\n"
	}
	content += "priority: 50\n---\n\nbody\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestStore_RefreshAndList(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "genres", "a.md"), "a", "alpha", "genres", []string{"x"})
	writeSkillFile(t, filepath.Join(root, "genres", "b.md"), "b", "beta", "genres", []string{"y"})
	writeSkillFile(t, filepath.Join(root, "styles", "c.md"), "c", "gamma", "styles", nil)

	s := NewStore(root)
	if err := s.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	all := s.List("")
	if len(all) != 3 {
		t.Errorf("List = %d items, want 3", len(all))
	}
	genres := s.List("genres")
	if len(genres) != 2 {
		t.Errorf("List(genres) = %d, want 2", len(genres))
	}
	cats := s.Categories()
	if len(cats) != 2 {
		t.Errorf("Categories = %v", cats)
	}
}

func TestStore_SearchTagHit(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "genres", "cyber.md"), "cyberpunk", "赛博朋克题材", "genres", []string{"cyberpunk", "sci-fi"})
	writeSkillFile(t, filepath.Join(root, "genres", "wuxia.md"), "wuxia", "武侠题材", "genres", []string{"wuxia", "ancient"})

	s := NewStore(root)
	_ = s.Refresh()

	hits := s.Search("cyberpunk", 5)
	if len(hits) != 1 {
		t.Fatalf("Search = %d hits, want 1", len(hits))
	}
	if hits[0].Name != "cyberpunk" {
		t.Errorf("Search[0].Name = %q, want cyberpunk", hits[0].Name)
	}
}

func TestStore_SearchTriggerHit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "genres", "x.md")
	content := "---\nname: x\ndescription: foo\ncategory: genres\ntriggers:\n  - 赛博朋克\n  - 仿生人\npriority: 50\n---\nbody\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewStore(root)
	_ = s.Refresh()

	hits := s.Search("我想写赛博朋克", 5)
	if len(hits) == 0 {
		t.Errorf("expected trigger hit, got 0")
	}
}

func TestStore_SearchPriorityTieBreaker(t *testing.T) {
	root := t.TempDir()
	// 两个 skill 都有 tag "x"，priority 高的应排前
	p1 := filepath.Join(root, "g", "a.md")
	c1 := "---\nname: a\ndescription: alpha\ncategory: g\ntags: [x]\npriority: 80\n---\nbody\n"
	p2 := filepath.Join(root, "g", "b.md")
	c2 := "---\nname: b\ndescription: beta\ncategory: g\ntags: [x]\npriority: 60\n---\nbody\n"
	os.MkdirAll(filepath.Dir(p1), 0o755)
	os.WriteFile(p1, []byte(c1), 0o644)
	os.WriteFile(p2, []byte(c2), 0o644)

	s := NewStore(root)
	_ = s.Refresh()

	hits := s.Search("x", 5)
	if len(hits) != 2 {
		t.Fatalf("Search = %d, want 2", len(hits))
	}
	if hits[0].Name != "a" {
		t.Errorf("Search[0].Name = %q, want a (higher priority)", hits[0].Name)
	}
}

func TestStore_Read(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "g", "foo.md")
	content := "---\nname: foo\ndescription: bar\ncategory: g\n---\n\n# Title\nbody\n"
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(content), 0o644)

	s := NewStore(root)
	_ = s.Refresh()

	body, err := s.Read("foo")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if body != content {
		t.Errorf("Read returned %q, want %q", body, content)
	}

	if _, err := s.Read("nonexistent"); err == nil {
		t.Errorf("Read nonexistent should error")
	}
}

func TestStore_AddAndRemove(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	_ = s.Refresh()

	meta := SkillMeta{
		Name:        "new-skill",
		Description: "新增 skill",
		Category:    "genres",
		Tags:        []string{"new"},
	}
	if err := s.Add(meta, "# 新增\nbody"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Add 后立即可 Search
	hits := s.Search("new", 5)
	if len(hits) != 1 || hits[0].Name != "new-skill" {
		t.Fatalf("Search after Add = %v, want [new-skill]", hits)
	}

	// 文件确实落盘
	path := filepath.Join(root, "genres", "new-skill.md")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}

	// 重复 Add 报错
	if err := s.Add(meta, "body"); err == nil {
		t.Errorf("Add duplicate should error")
	}

	// Remove
	if err := s.Remove("new-skill"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	hits = s.Search("new", 5)
	if len(hits) != 0 {
		t.Errorf("Search after Remove = %d, want 0", len(hits))
	}

	// Remove 不存在报错
	if err := s.Remove("nonexistent"); err == nil {
		t.Errorf("Remove nonexistent should error")
	}
}

func TestStore_AddInvalidName(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	_ = s.Refresh()

	cases := []string{"", "UPPER", "under_score", "中文", "dot.dot"}
	for _, name := range cases {
		meta := SkillMeta{Name: name, Description: "x", Category: "g"}
		if err := s.Add(meta, "body"); err == nil {
			t.Errorf("Add with name %q should error", name)
		}
	}
}

func TestStore_LazyReloadOnMtimeChange(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	_ = s.Refresh()

	// 初始空
	if hits := s.List(""); len(hits) != 0 {
		t.Fatalf("expected empty, got %d", len(hits))
	}

	// 写入新文件
	path := filepath.Join(root, "g", "x.md")
	os.MkdirAll(filepath.Dir(path), 0o755)
	content := "---\nname: x\ndescription: y\ncategory: g\n---\nbody\n"
	os.WriteFile(path, []byte(content), 0o644)

	// 推进 root 目录 mtime（maybeRefresh 检测的是 root 目录的 mtime，
	// 不是单个文件的 mtime——文件创建时父目录 mtime 通常会更新，但保险起见显式 Chtimes）
	newMtime := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(root, newMtime, newMtime); err != nil {
		t.Logf("Chtimes: %v (可能不受支持，跳过)", err)
	}

	// 再 List 应触发懒重载
	hits := s.List("")
	if len(hits) != 1 {
		t.Errorf("after mtime change, List = %d, want 1", len(hits))
	}
}

func TestStore_EmptyRoot(t *testing.T) {
	s := NewStore("") // 禁用
	if err := s.Refresh(); err != nil {
		t.Errorf("Refresh on empty root should not error, got %v", err)
	}
	if hits := s.Search("x", 5); len(hits) != 0 {
		t.Errorf("Search on empty root = %d, want 0", len(hits))
	}
	if cats := s.Categories(); len(cats) != 0 {
		t.Errorf("Categories on empty root = %v", cats)
	}
}

func TestRenderSkillFile_RoundTrip(t *testing.T) {
	meta := SkillMeta{
		Name:        "round-trip",
		Description: "包含: 冒号的描述",
		Category:    "genres",
		Tags:        []string{"a", "b c"},
		When:        "场景\n多行",
		Do:          "动作 1\n动作 2",
		Priority:    80,
	}
	body := "# 标题\n\n正文段落\n"
	content := renderSkillFile(meta, body)

	// 解析回来应一致
	parsed, parsedBody, err := ParseSkill("/tmp/x.md", content)
	if err != nil {
		t.Fatalf("round-trip ParseSkill: %v\ncontent:\n%s", err, content)
	}
	if parsed.Name != meta.Name {
		t.Errorf("Name %q != %q", parsed.Name, meta.Name)
	}
	if parsed.Description != meta.Description {
		t.Errorf("Description %q != %q", parsed.Description, meta.Description)
	}
	if parsed.Category != meta.Category {
		t.Errorf("Category %q != %q", parsed.Category, meta.Category)
	}
	if len(parsed.Tags) != len(meta.Tags) {
		t.Errorf("Tags len %d != %d", len(parsed.Tags), len(meta.Tags))
	}
	if parsed.Priority != meta.Priority {
		t.Errorf("Priority %d != %d", parsed.Priority, meta.Priority)
	}
	// ParseSkill 对 body 做 TrimSpace，所以末尾换行不保留——比较时去边白
	if strings.TrimSpace(parsedBody) != strings.TrimSpace(body) {
		t.Errorf("body round-trip mismatch:\n got: %q\nwant: %q", parsedBody, body)
	}
}
