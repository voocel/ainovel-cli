package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// setupRewriteHost 构造一个有 foundation + 已完成章节的 host，用于 rewrite 测试。
func setupRewriteHost(t *testing.T) (*Host, string) {
	t.Helper()
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("# 测试书\n赛博朋克短篇"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "首章", CoreEvent: "..."}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveCompass(domain.StoryCompass{EndingDirection: "终结"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init("测试书", 10); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveFinalChapter(1, "正文内容"); err != nil {
		t.Fatal(err)
	}

	h := &Host{store: st}
	return h, dir
}

func TestRewriteFoundation_ArchivesAllArtifacts(t *testing.T) {
	h, dir := setupRewriteHost(t)

	backup, err := h.RewriteFoundation()
	if err != nil {
		t.Fatalf("重写失败：%v", err)
	}
	if !strings.Contains(backup, "rewrite-backup-") {
		t.Errorf("备份路径名不正确：%s", backup)
	}

	// 原 foundation 文件应被移走
	for _, p := range []string{"premise.md", "outline.json", "outline.md", "characters.json"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("原文件 %s 应被移走：err=%v", p, err)
		}
	}
	// 备份目录里应有这些文件
	for _, p := range []string{"premise.md", "outline.json", "compass.json", "progress.json", "chapters/01.md"} {
		if _, err := os.Stat(filepath.Join(backup, p)); err != nil {
			t.Errorf("备份目录应有 %s：%v", p, err)
		}
	}
}

func TestRewriteFoundation_LeavesSessionsAndMaterials(t *testing.T) {
	h, dir := setupRewriteHost(t)

	// 写一个 materials.json 和 sessions/ 下的文件
	if err := os.WriteFile(filepath.Join(dir, "meta", "materials.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "meta", "sessions", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "sessions", "agents", "architect.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := h.RewriteFoundation(); err != nil {
		t.Fatalf("重写失败：%v", err)
	}

	// materials.json 应保留（与大纲解耦）
	if _, err := os.Stat(filepath.Join(dir, "meta", "materials.json")); err != nil {
		t.Errorf("materials.json 应被保留：%v", err)
	}
	// sessions 也应保留
	if _, err := os.Stat(filepath.Join(dir, "meta", "sessions", "agents", "architect.jsonl")); err != nil {
		t.Errorf("sessions 应被保留：%v", err)
	}
}

func TestRewriteFoundation_RejectsEmptyProject(t *testing.T) {
	// 没有 foundation 的项目也能重写（幂等），不应报错。
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st}

	backup, err := h.RewriteFoundation()
	if err != nil {
		t.Fatalf("空项目重写应成功：%v", err)
	}
	// 备份目录会被创建（即使为空）
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("备份目录应被创建：%v", err)
	}
	// chapters/ 目录被重建
	if _, err := os.Stat(filepath.Join(dir, "chapters")); err != nil {
		t.Errorf("chapters 目录应被重建：%v", err)
	}
}
